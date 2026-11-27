// Package obs is a client for the parts of obs-websocket v5 classrec uses: recording control,
// output stats and input audio levels.
package obs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"classrec/internal/obs/ws"
)

// ErrAuthFailed means OBS rejected the password.
var ErrAuthFailed = errors.New("obs rejected the websocket password")

// RequestError is a request OBS received but refused, e.g. StartRecord while already recording.
type RequestError struct {
	RequestType string
	Code        int
	Comment     string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("obs %s failed: code %d %s", e.RequestType, e.Code, e.Comment)
}

// Stats is the part of GetStats that tells whether recording is healthy.
type Stats struct {
	RenderSkippedFrames int     `json:"renderSkippedFrames"`
	RenderTotalFrames   int     `json:"renderTotalFrames"`
	OutputSkippedFrames int     `json:"outputSkippedFrames"`
	OutputTotalFrames   int     `json:"outputTotalFrames"`
	ActiveFPS           float64 `json:"activeFps"`
	CPUUsage            float64 `json:"cpuUsage"`
}

// RecordStatus is the answer to GetRecordStatus.
type RecordStatus struct {
	Active     bool  `json:"outputActive"`
	Paused     bool  `json:"outputPaused"`
	DurationMS int64 `json:"outputDuration"`
}

// SourceScreenshot is a source's current frame as OBS renders it, width pixels wide. OBS refuses
// (code 702) when it has nothing to render, as when a window capture has lost its window.
func (c *Client) SourceScreenshot(ctx context.Context, source string, width int) (image.Image, error) {
	var response struct {
		ImageData string `json:"imageData"`
	}
	data := map[string]any{"sourceName": source, "imageFormat": "png", "imageWidth": width}
	if err := c.call(ctx, "GetSourceScreenshot", data, &response); err != nil {
		return nil, err
	}
	_, encoded, ok := strings.Cut(response.ImageData, ",")
	if !ok {
		return nil, errors.New("obs screenshot is not a data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(raw))
}

// InputLevel is the latest audio level OBS reported for one input.
type InputLevel struct {
	DB float64 // loudest channel's magnitude in dBFS; -Inf for silence
	At time.Time
}

// Client is one connection to OBS. Its methods are safe from any goroutine.
// When the connection drops, Done is closed and every method returns Err.
type Client struct {
	conn          *ws.Conn
	nextRequestID atomic.Uint64

	pendingMu sync.Mutex
	pending   map[string]chan requestResponse

	levelsMu sync.Mutex
	levels   map[string]InputLevel

	done    chan struct{}
	doneErr error // set before done is closed
}

// Connect dials OBS, authenticates with password (ignored if OBS has auth off) and subscribes to events.
func Connect(ctx context.Context, url, password string) (*Client, error) {
	conn, err := ws.Dial(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := identifyWith(conn, password); err != nil {
		conn.Close()
		return nil, err
	}
	c := &Client{
		conn:    conn,
		pending: map[string]chan requestResponse{},
		levels:  map[string]InputLevel{},
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// identifyWith runs the Hello → Identify → Identified handshake.
func identifyWith(conn *ws.Conn, password string) error {
	var h hello
	if err := readOp(conn, opHello, &h); err != nil {
		return fmt.Errorf("waiting for obs hello: %w", err)
	}
	id := identify{
		RPCVersion:         rpcVersion,
		EventSubscriptions: subscribeOutputs | subscribeInputVolumeMeters,
	}
	if h.Authentication != nil {
		id.Authentication = authResponse(password, h.Authentication.Salt, h.Authentication.Challenge)
	}
	if err := writeOp(conn, opIdentify, id); err != nil {
		return err
	}
	err := readOp(conn, opIdentified, nil)
	var closeErr *ws.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == closeAuthenticationFailed {
		return ErrAuthFailed
	}
	if err != nil {
		return fmt.Errorf("waiting for obs to accept identify: %w", err)
	}
	return nil
}

// StartRecord starts recording with OBS's configured output settings.
func (c *Client) StartRecord(ctx context.Context) error {
	return c.call(ctx, "StartRecord", nil, nil)
}

// StopRecord stops recording and returns the path of the file OBS wrote.
func (c *Client) StopRecord(ctx context.Context) (string, error) {
	var response struct {
		OutputPath string `json:"outputPath"`
	}
	err := c.call(ctx, "StopRecord", nil, &response)
	return response.OutputPath, err
}

// RecordStatus reports whether OBS is recording.
func (c *Client) RecordStatus(ctx context.Context) (RecordStatus, error) {
	var status RecordStatus
	err := c.call(ctx, "GetRecordStatus", nil, &status)
	return status, err
}

// Stats returns OBS's frame and CPU counters.
func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	err := c.call(ctx, "GetStats", nil, &stats)
	return stats, err
}

// CreateScene adds an empty scene.
func (c *Client) CreateScene(ctx context.Context, name string) error {
	return c.call(ctx, "CreateScene", map[string]any{"sceneName": name}, nil)
}

// CreateInput adds an input of the given kind to a scene, e.g. "window_capture".
func (c *Client) CreateInput(ctx context.Context, scene, name, kind string, settings map[string]any) error {
	data := map[string]any{
		"sceneName":        scene,
		"inputName":        name,
		"inputKind":        kind,
		"inputSettings":    settings,
		"sceneItemEnabled": true,
	}
	return c.call(ctx, "CreateInput", data, nil)
}

// SetInputSettings changes the given settings of an existing input and keeps the rest.
func (c *Client) SetInputSettings(ctx context.Context, name string, settings map[string]any) error {
	data := map[string]any{"inputName": name, "inputSettings": settings, "overlay": true}
	return c.call(ctx, "SetInputSettings", data, nil)
}

// SetCurrentScene makes the scene the one that gets recorded.
func (c *Client) SetCurrentScene(ctx context.Context, name string) error {
	return c.call(ctx, "SetCurrentProgramScene", map[string]any{"sceneName": name}, nil)
}

// SetRecordDirectory points OBS's recordings at dir.
func (c *Client) SetRecordDirectory(ctx context.Context, dir string) error {
	return c.call(ctx, "SetRecordDirectory", map[string]any{"recordDirectory": dir}, nil)
}

// RecordDirectory returns the folder OBS records into.
func (c *Client) RecordDirectory(ctx context.Context) (string, error) {
	var response struct {
		Directory string `json:"recordDirectory"`
	}
	err := c.call(ctx, "GetRecordDirectory", nil, &response)
	return response.Directory, err
}

// Profiles lists OBS's profile names and says which one is current.
func (c *Client) Profiles(ctx context.Context) (names []string, current string, err error) {
	var response struct {
		Current  string   `json:"currentProfileName"`
		Profiles []string `json:"profiles"`
	}
	err = c.call(ctx, "GetProfileList", nil, &response)
	return response.Profiles, response.Current, err
}

// SetCurrentProfile switches OBS to an existing profile.
func (c *Client) SetCurrentProfile(ctx context.Context, name string) error {
	return c.call(ctx, "SetCurrentProfile", map[string]any{"profileName": name}, nil)
}

// ProfileParameter reads one profile.ini value of the current profile; "" if it is not set.
func (c *Client) ProfileParameter(ctx context.Context, category, name string) (string, error) {
	var response struct {
		Value *string `json:"parameterValue"`
	}
	err := c.call(ctx, "GetProfileParameter", map[string]any{"parameterCategory": category, "parameterName": name}, &response)
	if response.Value == nil {
		return "", err
	}
	return *response.Value, err
}

// VideoSettings is the canvas, output size and frame rate of the current profile.
type VideoSettings struct {
	FPSNumerator   int `json:"fpsNumerator"`
	FPSDenominator int `json:"fpsDenominator"`
	BaseWidth      int `json:"baseWidth"`
	BaseHeight     int `json:"baseHeight"`
	OutputWidth    int `json:"outputWidth"`
	OutputHeight   int `json:"outputHeight"`
}

// VideoSettings reads the current video settings.
func (c *Client) VideoSettings(ctx context.Context) (VideoSettings, error) {
	var v VideoSettings
	err := c.call(ctx, "GetVideoSettings", nil, &v)
	return v, err
}

// SceneItemID finds the id of a source's item in a scene.
func (c *Client) SceneItemID(ctx context.Context, scene, source string) (int, error) {
	var response struct {
		ID int `json:"sceneItemId"`
	}
	err := c.call(ctx, "GetSceneItemId", map[string]any{"sceneName": scene, "sourceName": source}, &response)
	return response.ID, err
}

// SetSceneItemTransform changes the given fields of a scene item's transform, e.g. "boundsType".
func (c *Client) SetSceneItemTransform(ctx context.Context, scene string, itemID int, transform map[string]any) error {
	data := map[string]any{"sceneName": scene, "sceneItemId": itemID, "sceneItemTransform": transform}
	return c.call(ctx, "SetSceneItemTransform", data, nil)
}

// SetInputMute mutes or unmutes an input everywhere it is used.
func (c *Client) SetInputMute(ctx context.Context, name string, muted bool) error {
	return c.call(ctx, "SetInputMute", map[string]any{"inputName": name, "inputMuted": muted}, nil)
}

// GlobalAudioInputs returns the names of the global Desktop Audio and Mic/Aux inputs OBS has set up.
func (c *Client) GlobalAudioInputs(ctx context.Context) ([]string, error) {
	var response map[string]*string
	if err := c.call(ctx, "GetSpecialInputs", nil, &response); err != nil {
		return nil, err
	}
	var names []string
	for _, key := range []string{"desktop1", "desktop2", "mic1", "mic2", "mic3", "mic4"} {
		if name := response[key]; name != nil && *name != "" {
			names = append(names, *name)
		}
	}
	return names, nil
}

// InputLevel returns the latest level of the named input; ok is false until OBS has reported one.
func (c *Client) InputLevel(inputName string) (level InputLevel, ok bool) {
	c.levelsMu.Lock()
	defer c.levelsMu.Unlock()
	level, ok = c.levels[inputName]
	return level, ok
}

// Done is closed when the connection to OBS is gone, e.g. because OBS exited.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err says why the connection ended; nil while it is open.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.doneErr
	default:
		return nil
	}
}

// Close disconnects from OBS. It does not stop a recording.
func (c *Client) Close() error {
	return c.conn.Close()
}

// call sends one request and waits for its response, decoding responseData into result if non-nil.
func (c *Client) call(ctx context.Context, requestType string, data, result any) error {
	id := strconv.FormatUint(c.nextRequestID.Add(1), 10)
	responses := make(chan requestResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = responses
	c.pendingMu.Unlock()
	defer c.forgetRequest(id)

	err := writeOp(c.conn, opRequest, request{RequestType: requestType, RequestID: id, RequestData: data})
	if err != nil {
		return err
	}

	var response requestResponse
	select {
	case response = <-responses:
	case <-c.done:
		return c.doneErr
	case <-ctx.Done():
		return fmt.Errorf("obs %s: %w", requestType, ctx.Err())
	}

	if !response.RequestStatus.Result {
		return &RequestError{RequestType: requestType, Code: response.RequestStatus.Code, Comment: response.RequestStatus.Comment}
	}
	if result == nil || len(response.ResponseData) == 0 {
		return nil
	}
	return json.Unmarshal(response.ResponseData, result)
}

func (c *Client) forgetRequest(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// readLoop routes responses to their callers and keeps input levels current, until the connection ends.
func (c *Client) readLoop() {
	for {
		raw, err := c.conn.ReadMessage()
		if err != nil {
			c.doneErr = fmt.Errorf("obs connection lost: %w", err)
			close(c.done)
			return
		}
		var m message
		if err := json.Unmarshal(raw, &m); err != nil {
			continue // a message we can't parse is not worth dropping the connection over
		}
		switch m.Op {
		case opRequestResponse:
			c.deliverResponse(m.Data)
		case opEvent:
			c.handleEvent(m.Data)
		}
	}
}

func (c *Client) deliverResponse(data json.RawMessage) {
	var response requestResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return
	}
	c.pendingMu.Lock()
	waiting, ok := c.pending[response.RequestID]
	c.pendingMu.Unlock()
	if ok {
		waiting <- response
	}
}

func (c *Client) handleEvent(data json.RawMessage) {
	var e event
	if err := json.Unmarshal(data, &e); err != nil || e.EventType != "InputVolumeMeters" {
		return
	}
	var meters inputVolumeMeters
	if err := json.Unmarshal(e.EventData, &meters); err != nil {
		return
	}
	now := time.Now()
	c.levelsMu.Lock()
	defer c.levelsMu.Unlock()
	for _, input := range meters.Inputs {
		c.levels[input.InputName] = InputLevel{DB: loudestChannelDB(input.InputLevelsMul), At: now}
	}
}

// loudestChannelDB converts the highest channel magnitude to dBFS.
func loudestChannelDB(channels [][]float64) float64 {
	const magnitude = 0
	loudest := 0.0
	for _, channel := range channels {
		if len(channel) > magnitude {
			loudest = max(loudest, channel[magnitude])
		}
	}
	if loudest <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(loudest)
}

func writeOp(conn *ws.Conn, op int, data any) error {
	encodedData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(message{Op: op, Data: encodedData})
	if err != nil {
		return err
	}
	return conn.WriteText(encoded)
}

// readOp reads one message, checks its opcode, and decodes its data into into if non-nil.
func readOp(conn *ws.Conn, wantOp int, into any) error {
	raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	var m message
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.Op != wantOp {
		return fmt.Errorf("expected obs op %d, got %d", wantOp, m.Op)
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(m.Data, into)
}
