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

	"classrec/internal/ws"
)

const (
	salt      = "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI="
	challenge = "+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY="

	codeOutputRunning         = 500
	codeOutputNotRunning      = 501
	codeUnknownRequest        = 204
	codeResourceNotFound      = 600
	codeResourceAlreadyExists = 601
)

// Input is an input the fake holds, as created by CreateInput.
type Input struct {
	Scene    string
	Kind     string
	Settings map[string]any
}

// Server is a fake OBS. Set Password to "" to run without authentication.
type Server struct {
	srv      *httptest.Server
	Password string

	mu            sync.Mutex
	conns         []*ws.Conn
	requests      []string // request types received, in order
	recording     bool
	recordings    int
	renderSkipped int
	outputSkipped int

	recordDir     string
	recordPath    string // file of the recording in progress
	profiles      []string
	profile       string
	params        map[string]map[string]string // profile -> "category/name" -> value
	video         map[string]any
	muted         map[string]bool
	specialInputs map[string]string

	monitorW, monitorH int
	failing            map[string]bool           // request types that always fail
	itemIDs            map[string]int            // "scene/input" -> scene item id
	transforms         map[string]map[string]any // "scene/input" -> scene item transform
	nextItemID         int

	scenes       map[string]bool
	sceneOrder   []string
	inputs       map[string]*Input
	inputOrder   []string
	currentScene string
}

// New starts a fake OBS that requires password.
func New(password string) *Server {
	s := &Server{
		Password: password, scenes: map[string]bool{}, inputs: map[string]*Input{},
		profiles: []string{"Untitled"}, profile: "Untitled",
		params:        map[string]map[string]string{"Untitled": {}},
		video:         map[string]any{"fpsNumerator": 30, "fpsDenominator": 1, "baseWidth": 1920, "baseHeight": 1080, "outputWidth": 1920, "outputHeight": 1080},
		muted:         map[string]bool{},
		specialInputs: map[string]string{"desktop1": "Desktop Audio", "mic1": "Mic/Aux"},
		monitorW:      1920, monitorH: 1080,
		failing:    map[string]bool{},
		itemIDs:    map[string]int{},
		transforms: map[string]map[string]any{},
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

// DropConnections closes every open connection without a close frame, as when OBS crashes.
// Recording state survives, so a test can also check a "relaunched" OBS.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.conns {
		conn.Abort()
	}
	s.conns = nil
}

// Requests returns the request types received so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Recording reports whether a recording is running.
func (s *Server) Recording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recording
}

// SetMonitor sets the size of the primary display GetMonitorList reports.
func (s *Server) SetMonitor(width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.monitorW, s.monitorH = width, height
}

// FailRequests makes every later request of these types fail, as when OBS refuses them.
func (s *Server) FailRequests(types ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range types {
		s.failing[t] = true
	}
}

// SceneItemTransform returns what was set on the input's scene item, nil if nothing was.
func (s *Server) SceneItemTransform(scene, input string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	for k, v := range s.transforms[scene+"/"+input] {
		out[k] = v
	}
	return out
}

// Kill imitates obs64.exe dying: every connection drops and the recording is lost,
// but the file it was writing stays on disk.
func (s *Server) Kill() {
	s.mu.Lock()
	s.recording = false
	s.mu.Unlock()
	s.DropConnections()
}

// IsMuted reports whether the input is muted.
func (s *Server) IsMuted(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.muted[name]
}

// Param is a profile.ini value of the named profile.
func (s *Server) Param(profile, category, name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.params[profile][category+"/"+name]
	return v, ok
}

// Profile is the current profile's name.
func (s *Server) Profile() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

// Video is the current video settings.
func (s *Server) Video() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	for k, v := range s.video {
		out[k] = v
	}
	return out
}

// RecordDirectory is the folder the fake records into.
func (s *Server) RecordDirectory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordDir
}

// CurrentScene is the scene OBS is set to record.
func (s *Server) CurrentScene() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentScene
}

// Input returns a copy of the named input's state.
func (s *Server) Input(name string) (Input, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.inputs[name]
	if !ok {
		return Input{}, false
	}
	copied := *in
	copied.Settings = map[string]any{}
	for k, v := range in.Settings {
		copied.Settings[k] = v
	}
	return copied, true
}

// SetSkippedFrames sets the counters GetStats reports.
func (s *Server) SetSkippedFrames(render, output int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renderSkipped = render
	s.outputSkipped = output
}

// SendLevel pushes an InputVolumeMeters event giving input a mono level of mul (a linear multiplier).
func (s *Server) SendLevel(inputName string, mul float64) {
	data := map[string]any{
		"eventType":   "InputVolumeMeters",
		"eventIntent": 1 << 16,
		"eventData": map[string]any{
			"inputs": []any{map[string]any{
				"inputName":      inputName,
				"inputLevelsMul": [][]float64{{mul, mul, mul}},
			}},
		},
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
	if err != nil {
		return
	}
	if !s.handshake(conn) {
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
	if s.Password != "" {
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
	if s.Password != "" && identify.Data.Authentication != expectedAuth(s.Password) {
		conn.CloseWithCode(4009, "Authentication failed.")
		return false
	}
	send(conn, 2, map[string]any{"negotiatedRpcVersion": 1})
	return true
}

func (s *Server) answer(conn *ws.Conn, requestType, requestID string, requestData json.RawMessage) {
	s.mu.Lock()
	s.requests = append(s.requests, requestType)
	ok, code, data := s.handleRequest(requestType, requestData)
	s.mu.Unlock()

	response := map[string]any{
		"requestType":   requestType,
		"requestId":     requestID,
		"requestStatus": map[string]any{"result": ok, "code": code},
	}
	if data != nil {
		response["responseData"] = data
	}
	send(conn, 7, response)
}

// handleRequest runs one request against the fake's state; s.mu is held.
func (s *Server) handleRequest(requestType string, requestData json.RawMessage) (ok bool, code int, data map[string]any) {
	const success = 100
	var in struct {
		SceneName     string         `json:"sceneName"`
		InputName     string         `json:"inputName"`
		InputKind     string         `json:"inputKind"`
		InputSettings map[string]any `json:"inputSettings"`
	}
	json.Unmarshal(requestData, &in) // absent or odd data leaves the zero value, like a missing field
	if s.failing[requestType] {
		return false, codeResourceNotFound, nil
	}
	switch requestType {
	case "GetMonitorList":
		return true, success, map[string]any{"monitors": []any{map[string]any{
			"monitorIndex": 0, "monitorName": "Fake display",
			"monitorWidth": s.monitorW, "monitorHeight": s.monitorH,
			"monitorPositionX": 0, "monitorPositionY": 0,
		}}}
	case "GetSceneItemId":
		var q struct {
			Scene  string `json:"sceneName"`
			Source string `json:"sourceName"`
		}
		json.Unmarshal(requestData, &q)
		id, ok := s.itemIDs[q.Scene+"/"+q.Source]
		if !ok {
			return false, codeResourceNotFound, nil
		}
		return true, success, map[string]any{"sceneItemId": id}
	case "SetSceneItemTransform":
		var q struct {
			Scene     string         `json:"sceneName"`
			ItemID    int            `json:"sceneItemId"`
			Transform map[string]any `json:"sceneItemTransform"`
		}
		json.Unmarshal(requestData, &q)
		for key, id := range s.itemIDs {
			if id == q.ItemID && strings.HasPrefix(key, q.Scene+"/") {
				if s.transforms[key] == nil {
					s.transforms[key] = map[string]any{}
				}
				for k, v := range q.Transform {
					s.transforms[key][k] = v
				}
				return true, success, nil
			}
		}
		return false, codeResourceNotFound, nil
	case "GetSceneList":
		scenes := []any{}
		for _, name := range s.sceneOrder {
			scenes = append(scenes, map[string]any{"sceneName": name})
		}
		return true, success, map[string]any{"scenes": scenes, "currentProgramSceneName": s.currentScene}
	case "GetInputList":
		inputs := []any{}
		for _, name := range s.inputOrder {
			inputs = append(inputs, map[string]any{"inputName": name, "inputKind": s.inputs[name].Kind})
		}
		return true, success, map[string]any{"inputs": inputs}
	case "CreateScene":
		if _, exists := s.scenes[in.SceneName]; exists {
			return false, codeResourceAlreadyExists, nil
		}
		s.scenes[in.SceneName] = true
		s.sceneOrder = append(s.sceneOrder, in.SceneName)
		return true, success, nil
	case "CreateInput":
		if !s.scenes[in.SceneName] {
			return false, codeResourceNotFound, nil
		}
		if _, exists := s.inputs[in.InputName]; exists {
			return false, codeResourceAlreadyExists, nil
		}
		s.inputs[in.InputName] = &Input{Scene: in.SceneName, Kind: in.InputKind, Settings: in.InputSettings}
		s.inputOrder = append(s.inputOrder, in.InputName)
		s.nextItemID++
		s.itemIDs[in.SceneName+"/"+in.InputName] = s.nextItemID
		return true, success, map[string]any{"sceneItemId": s.nextItemID}
	case "SetInputSettings":
		input, exists := s.inputs[in.InputName]
		if !exists {
			return false, codeResourceNotFound, nil
		}
		for k, v := range in.InputSettings {
			input.Settings[k] = v
		}
		return true, success, nil
	case "SetCurrentProgramScene":
		if !s.scenes[in.SceneName] {
			return false, codeResourceNotFound, nil
		}
		s.currentScene = in.SceneName
		return true, success, nil
	case "StartRecord":
		if s.recording {
			return false, codeOutputRunning, nil
		}
		s.recording = true
		s.recordings++
		name := fmt.Sprintf("recording-%d.mkv", s.recordings)
		s.recordPath = filepath.Join(s.recordDir, name)
		if s.recordDir == "" {
			s.recordPath = `C:\Users\Admin\Class Recordings\_staging\` + name
		} else if _, err := os.Stat(s.recordDir); err == nil {
			os.WriteFile(s.recordPath, []byte("fake mkv "+name), 0o644) // OBS creates the file as soon as it starts
		}
		return true, success, nil
	case "StopRecord":
		if !s.recording {
			return false, codeOutputNotRunning, nil
		}
		s.recording = false
		return true, success, map[string]any{"outputPath": s.recordPath}
	case "GetRecordDirectory":
		return true, success, map[string]any{"recordDirectory": s.recordDir}
	case "SetRecordDirectory":
		var d struct {
			Dir string `json:"recordDirectory"`
		}
		json.Unmarshal(requestData, &d)
		s.recordDir = d.Dir
		return true, success, nil
	case "GetProfileList":
		return true, success, map[string]any{"currentProfileName": s.profile, "profiles": s.profiles}
	case "CreateProfile", "SetCurrentProfile":
		var p struct {
			Name string `json:"profileName"`
		}
		json.Unmarshal(requestData, &p)
		known := s.params[p.Name] != nil
		if requestType == "CreateProfile" && known {
			return false, codeResourceAlreadyExists, nil
		}
		if requestType == "SetCurrentProfile" && !known {
			return false, codeResourceNotFound, nil
		}
		if !known {
			s.params[p.Name] = map[string]string{}
			s.profiles = append(s.profiles, p.Name)
		}
		s.profile = p.Name
		return true, success, nil
	case "SetProfileParameter", "GetProfileParameter":
		var p struct {
			Category string `json:"parameterCategory"`
			Name     string `json:"parameterName"`
			Value    string `json:"parameterValue"`
		}
		json.Unmarshal(requestData, &p)
		key := p.Category + "/" + p.Name
		if requestType == "SetProfileParameter" {
			s.params[s.profile][key] = p.Value
			return true, success, nil
		}
		value, set := s.params[s.profile][key]
		if !set {
			return true, success, map[string]any{"parameterValue": nil}
		}
		return true, success, map[string]any{"parameterValue": value}
	case "GetVideoSettings":
		return true, success, s.video
	case "SetVideoSettings":
		if s.recording {
			return false, codeOutputRunning, nil
		}
		var v map[string]any
		json.Unmarshal(requestData, &v)
		for k, val := range v {
			s.video[k] = val
		}
		return true, success, nil
	case "GetSpecialInputs":
		out := map[string]any{}
		for k, v := range s.specialInputs {
			out[k] = v
		}
		return true, success, out
	case "SetInputMute", "GetInputMute":
		var m struct {
			Name  string `json:"inputName"`
			Muted bool   `json:"inputMuted"`
		}
		json.Unmarshal(requestData, &m)
		if requestType == "SetInputMute" {
			s.muted[m.Name] = m.Muted
			return true, success, nil
		}
		return true, success, map[string]any{"inputMuted": s.muted[m.Name]}
	case "GetRecordStatus":
		return true, success, map[string]any{"outputActive": s.recording, "outputPaused": false, "outputDuration": 0}
	case "GetStats":
		return true, success, map[string]any{
			"renderSkippedFrames": s.renderSkipped, "renderTotalFrames": 1000,
			"outputSkippedFrames": s.outputSkipped, "outputTotalFrames": 1000,
			"activeFps": 15.0, "cpuUsage": 7.0,
		}
	default:
		return false, codeUnknownRequest, nil
	}
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
	conn.WriteText(encoded)
}
