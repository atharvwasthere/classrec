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

	scenes       map[string]bool
	sceneOrder   []string
	inputs       map[string]*Input
	inputOrder   []string
	currentScene string
}

// New starts a fake OBS that requires password.
func New(password string) *Server {
	s := &Server{Password: password, scenes: map[string]bool{}, inputs: map[string]*Input{}}
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
	switch requestType {
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
		return true, success, nil
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
		return true, success, nil
	case "StopRecord":
		if !s.recording {
			return false, codeOutputNotRunning, nil
		}
		s.recording = false
		path := fmt.Sprintf(`C:\Users\Admin\Class Recordings\_staging\recording-%d.mkv`, s.recordings)
		return true, success, map[string]any{"outputPath": path}
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
