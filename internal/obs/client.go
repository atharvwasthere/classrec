// Package obs is a client for the parts of obs-websocket v5 classrec uses: recording control,
// output stats and input audio levels.
package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"classrec/internal/ws"
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
