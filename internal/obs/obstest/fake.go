// Package obstest is an in-memory obs-websocket v5 server for tests. It answers only the requests
// classrec sends, and can push input levels and drop connections to imitate OBS crashing.
package obstest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"classrec/internal/obs/ws"
)

const (
	salt      = "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI="
	challenge = "+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY="

	codeSuccess          = 100
	codeUnknownRequest   = 204
	codeNotReady         = 207
	codeOutputRunning    = 500
	codeOutputNotRunning = 501
	codeNotFound         = 600
	codeExists           = 601
)

// Input is an input the fake holds, as created by CreateInput.
type Input struct {
	Scene    string
	Kind     string
	Settings map[string]any
}

// Profile is a profile as `classrec install` would leave it.
type Profile struct {
	Params map[string]string // "category/name" -> value
	Video  map[string]any    // GetVideoSettings fields
}

// Server is a fake OBS.
type Server struct {
	srv      *httptest.Server
	password string

	mu         sync.Mutex
	conns      []*ws.Conn
	requests   []string // request types received, in order
	failing    map[string]bool
	notReady   int // StartRecords to answer "not ready", as a freshly started OBS does
	recording  bool
	recordings int
	recordDir  string
	recordPath string // file of the recording in progress

	profiles  map[string]Profile
	profile   string
	muted     map[string]bool
	scenes    map[string]bool
	scene     string
	inputs    map[string]*Input
	itemIDs   map[string]int            // "scene/input" -> scene item id
	transform map[string]map[string]any // "scene/input" -> transform
	skipped   [2]int                    // render, output
}

// New starts a fake OBS that requires password; "" turns authentication off. It has one
// profile, "Untitled", recording 1920x1080 at 30 fps in simple mode.
func New(password string) *Server {
	s := &Server{
		password: password,
		failing:  map[string]bool{},
		profiles: map[string]Profile{"Untitled": {Params: map[string]string{"Output/Mode": "Simple"},
			Video: map[string]any{"fpsNumerator": 30, "fpsDenominator": 1, "baseWidth": 1920, "baseHeight": 1080, "outputWidth": 1920, "outputHeight": 1080}}},
		profile: "Untitled",
		muted:   map[string]bool{}, scenes: map[string]bool{}, inputs: map[string]*Input{},
		itemIDs: map[string]int{}, transform: map[string]map[string]any{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the ws:// address to connect to.
func (s *Server) URL() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") }

// Close stops the server and drops every connection.
func (s *Server) Close() {
	s.DropConnections()
	s.srv.Close()
}

// AddProfile adds or replaces a profile, as if its files had been written while OBS was closed.
func (s *Server) AddProfile(name string, p Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[name] = p
}

// FailRequests makes every later request of these types fail, as when OBS refuses them.
func (s *Server) FailRequests(types ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range types {
		s.failing[t] = true
	}
}

// StartingUp makes the next n StartRecords answer "not ready", as OBS does for a few seconds
// after it starts.
func (s *Server) StartingUp(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notReady = n
}

// DropConnections closes every connection without a close frame, as when the network hiccups.
// OBS keeps recording.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.conns {
		conn.Abort()
	}
	s.conns = nil
}

// Kill imitates obs64.exe dying: connections drop and the recording stops, but the file it was
// writing stays on disk. A later connection is a relaunched OBS.
func (s *Server) Kill() {
	s.mu.Lock()
	s.recording = false
	s.mu.Unlock()
	s.DropConnections()
}

// SetSkippedFrames sets the counters GetStats reports.
func (s *Server) SetSkippedFrames(render, output int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = [2]int{render, output}
}

// State is a snapshot of what the fake has been told.
type State struct {
	Recording  bool
	Profile    string
	RecordDir  string
	Scene      string
	Inputs     map[string]Input
	Muted      map[string]bool
	Transforms map[string]map[string]any // "scene/input" -> transform
	Requests   []string
}

// State returns a copy of the fake's state.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Recording: s.recording, Profile: s.profile, RecordDir: s.recordDir, Scene: s.scene,
		Inputs: map[string]Input{}, Muted: map[string]bool{}, Transforms: map[string]map[string]any{},
		Requests: append([]string(nil), s.requests...)}
	for k, v := range s.inputs {
		st.Inputs[k] = *v
	}
	for k, v := range s.muted {
		st.Muted[k] = v
	}
	for k, v := range s.transform {
		st.Transforms[k] = v
	}
	return st
}

// SendLevel pushes an InputVolumeMeters event giving input a mono level of mul (a linear multiplier).
func (s *Server) SendLevel(inputName string, mul float64) {
	data := map[string]any{
		"eventType":   "InputVolumeMeters",
		"eventIntent": 1 << 16,
		"eventData": map[string]any{"inputs": []any{map[string]any{
			"inputName": inputName, "inputLevelsMul": [][]float64{{mul, mul, mul}},
		}}},
	}
	s.mu.Lock()
	conns := append([]*ws.Conn(nil), s.conns...)
	s.mu.Unlock()
	for _, conn := range conns {
		send(conn, 5, data)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Accept(w, r)
	if err != nil || !s.handshake(conn) {
		return
	}
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			Op   int `json:"op"`
			Data struct {
				RequestType string          `json:"requestType"`
				RequestID   string          `json:"requestId"`
				RequestData json.RawMessage `json:"requestData"`
			} `json:"d"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Op != 6 {
			continue
		}
		s.answer(conn, m.Data.RequestType, m.Data.RequestID, m.Data.RequestData)
	}
}

// handshake sends Hello, checks Identify, and answers Identified or closes with 4009.
func (s *Server) handshake(conn *ws.Conn) bool {
	helloData := map[string]any{"obsWebSocketVersion": "5.5.0", "rpcVersion": 1}
	if s.password != "" {
		helloData["authentication"] = map[string]string{"challenge": challenge, "salt": salt}
	}
	send(conn, 0, helloData)
	raw, err := conn.ReadMessage()
	if err != nil {
		return false
	}
	var identify struct {
		Op   int `json:"op"`
		Data struct {
			Authentication string `json:"authentication"`
		} `json:"d"`
	}
	if json.Unmarshal(raw, &identify) != nil || identify.Op != 1 {
		conn.CloseWithCode(4000, "expected identify")
		return false
	}
	if s.password != "" && identify.Data.Authentication != expectedAuth(s.password) {
		conn.CloseWithCode(4009, "Authentication failed.")
		return false
	}
	send(conn, 2, map[string]any{"negotiatedRpcVersion": 1})
	return true
}

// request is the data of any request classrec sends; each handler reads the fields it needs.
type request struct {
	Scene      string         `json:"sceneName"`
	Source     string         `json:"sourceName"`
	Input      string         `json:"inputName"`
	Kind       string         `json:"inputKind"`
	Settings   map[string]any `json:"inputSettings"`
	Muted      bool           `json:"inputMuted"`
	ItemID     int            `json:"sceneItemId"`
	Transform  map[string]any `json:"sceneItemTransform"`
	Profile    string         `json:"profileName"`
	Category   string         `json:"parameterCategory"`
	Name       string         `json:"parameterName"`
	RecordDir  string         `json:"recordDirectory"`
	unparsable bool
}

// reply is a handler's answer: a status code and optional response data.
type reply struct {
	code int
	data map[string]any
}

func ok(data map[string]any) reply { return reply{codeSuccess, data} }

var handlers = map[string]func(*Server, request) reply{
	"StartRecord": (*Server).startRecord,
	"StopRecord":  (*Server).stopRecord,
	"GetRecordStatus": func(s *Server, _ request) reply {
		return ok(map[string]any{"outputActive": s.recording, "outputDuration": 0})
	},
	"GetStats": func(s *Server, _ request) reply {
		return ok(map[string]any{"renderSkippedFrames": s.skipped[0], "renderTotalFrames": 1000,
			"outputSkippedFrames": s.skipped[1], "outputTotalFrames": 1000, "activeFps": 15.0, "cpuUsage": 7.0})
	},
	"GetRecordDirectory": func(s *Server, _ request) reply { return ok(map[string]any{"recordDirectory": s.recordDir}) },
	"SetRecordDirectory": func(s *Server, q request) reply { s.recordDir = q.RecordDir; return ok(nil) },
	"GetProfileList": func(s *Server, _ request) reply {
		var names []string
		for name := range s.profiles {
			names = append(names, name)
		}
		return ok(map[string]any{"currentProfileName": s.profile, "profiles": names})
	},
	"SetCurrentProfile": func(s *Server, q request) reply {
		if _, found := s.profiles[q.Profile]; !found {
			return reply{codeNotFound, nil}
		}
		s.profile = q.Profile
		return ok(nil)
	},
	"GetProfileParameter": func(s *Server, q request) reply {
		value, set := s.profiles[s.profile].Params[q.Category+"/"+q.Name]
		if !set {
			return ok(map[string]any{"parameterValue": nil})
		}
		return ok(map[string]any{"parameterValue": value})
	},
	"GetVideoSettings": func(s *Server, _ request) reply { return ok(s.profiles[s.profile].Video) },
	"GetSpecialInputs": func(*Server, request) reply {
		return ok(map[string]any{"desktop1": "Desktop Audio", "mic1": "Mic/Aux"})
	},
	"SetInputMute":           func(s *Server, q request) reply { s.muted[q.Input] = q.Muted; return ok(nil) },
	"CreateScene":            (*Server).createScene,
	"CreateInput":            (*Server).createInput,
	"SetInputSettings":       (*Server).setInputSettings,
	"SetCurrentProgramScene": (*Server).setScene,
	"GetSceneItemId":         (*Server).sceneItemID,
	"SetSceneItemTransform":  (*Server).setTransform,
}

func (s *Server) answer(conn *ws.Conn, requestType, requestID string, requestData json.RawMessage) {
	var q request
	if len(requestData) > 0 && json.Unmarshal(requestData, &q) != nil {
		q.unparsable = true
	}
	s.mu.Lock()
	s.requests = append(s.requests, requestType)
	handler, known := handlers[requestType]
	var rep reply
	switch {
	case s.failing[requestType]:
		rep = reply{codeNotFound, nil}
	case !known || q.unparsable:
		rep = reply{codeUnknownRequest, nil}
	default:
		rep = handler(s, q)
	}
	s.mu.Unlock()

	response := map[string]any{
		"requestType":   requestType,
		"requestId":     requestID,
		"requestStatus": map[string]any{"result": rep.code == codeSuccess, "code": rep.code},
	}
	if rep.data != nil {
		response["responseData"] = rep.data
	}
	send(conn, 7, response)
}

func (s *Server) startRecord(request) reply {
	if s.notReady > 0 {
		s.notReady--
		return reply{codeNotReady, nil}
	}
	if s.recording {
		return reply{codeOutputRunning, nil}
	}
	s.recording = true
	s.recordings++
	name := fmt.Sprintf("recording-%d.mkv", s.recordings)
	s.recordPath = filepath.Join(s.recordDir, name)
	if _, err := os.Stat(s.recordDir); s.recordDir != "" && err == nil {
		_ = os.WriteFile(s.recordPath, []byte("fake mkv "+name), 0o644) // OBS creates the file as it starts
	}
	return ok(nil)
}

func (s *Server) stopRecord(request) reply {
	if !s.recording {
		return reply{codeOutputNotRunning, nil}
	}
	s.recording = false
	return ok(map[string]any{"outputPath": s.recordPath})
}

func (s *Server) createScene(q request) reply {
	if s.scenes[q.Scene] {
		return reply{codeExists, nil}
	}
	s.scenes[q.Scene] = true
	return ok(nil)
}

func (s *Server) createInput(q request) reply {
	if !s.scenes[q.Scene] {
		return reply{codeNotFound, nil}
	}
	if _, found := s.inputs[q.Input]; found {
		return reply{codeExists, nil}
	}
	s.inputs[q.Input] = &Input{Scene: q.Scene, Kind: q.Kind, Settings: q.Settings}
	s.itemIDs[q.Scene+"/"+q.Input] = len(s.itemIDs) + 1
	return ok(map[string]any{"sceneItemId": len(s.itemIDs)})
}

func (s *Server) setInputSettings(q request) reply {
	in, found := s.inputs[q.Input]
	if !found {
		return reply{codeNotFound, nil}
	}
	for k, v := range q.Settings {
		in.Settings[k] = v
	}
	return ok(nil)
}

func (s *Server) setScene(q request) reply {
	if !s.scenes[q.Scene] {
		return reply{codeNotFound, nil}
	}
	s.scene = q.Scene
	return ok(nil)
}

func (s *Server) sceneItemID(q request) reply {
	id, found := s.itemIDs[q.Scene+"/"+q.Source]
	if !found {
		return reply{codeNotFound, nil}
	}
	return ok(map[string]any{"sceneItemId": id})
}

func (s *Server) setTransform(q request) reply {
	for key, id := range s.itemIDs {
		if id == q.ItemID && strings.HasPrefix(key, q.Scene+"/") {
			s.transform[key] = q.Transform
			return ok(nil)
		}
	}
	return reply{codeNotFound, nil}
}

// expectedAuth is written out separately from the client's, so a bug in one doesn't hide in both.
func expectedAuth(password string) string {
	secret := sha256.Sum256([]byte(password + salt))
	secretB64 := base64.StdEncoding.EncodeToString(secret[:])
	response := sha256.Sum256([]byte(secretB64 + challenge))
	return base64.StdEncoding.EncodeToString(response[:])
}

func send(conn *ws.Conn, op int, data any) {
	encoded, _ := json.Marshal(map[string]any{"op": op, "d": data})
	_ = conn.WriteText(encoded) // a dropped connection is what some tests want
}
