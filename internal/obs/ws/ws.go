// Package ws is the small part of RFC 6455 WebSocket that obs-websocket needs: a client that dials
// ws:// URLs, a server-side Accept for test fakes, and whole text messages in both directions.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	acceptGUID     = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxMessageSize = 16 << 20

	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	finalFrameBit = 0x80
	maskBit       = 0x80

	// closeNormal is the close code for an orderly goodbye.
	closeNormal = 1000
)

// CloseError is returned by ReadMessage once the peer closes the connection.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("websocket closed: %d %s", e.Code, e.Reason)
}

// Conn is one WebSocket connection. ReadMessage must be called from one goroutine;
// WriteText and Close are safe from any goroutine.
type Conn struct {
	netConn  net.Conn
	reader   *bufio.Reader
	isClient bool // clients must mask what they send; servers must not

	writeMu sync.Mutex
}

// Dial opens a WebSocket connection to a ws:// URL.
func Dial(ctx context.Context, rawURL string) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("only ws:// URLs are supported, not %q", rawURL)
	}
	var dialer net.Dialer
	netConn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	conn, err := clientHandshake(ctx, netConn, u)
	if err != nil {
		netConn.Close()
		return nil, err
	}
	return conn, nil
}

func clientHandshake(ctx context.Context, netConn net.Conn, u *url.URL) (*Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		netConn.SetDeadline(deadline)
		defer netConn.SetDeadline(time.Time{})
	}
	key := newKey()
	path := u.RequestURI()
	request := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(netConn, request); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(netConn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("websocket handshake: server answered %s", response.Status)
	}
	if response.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		return nil, errors.New("websocket handshake: wrong Sec-WebSocket-Accept")
	}
	return &Conn{netConn: netConn, reader: reader, isClient: true}, nil
}

// Accept upgrades an HTTP request to a WebSocket connection on the server side.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	isUpgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	key := r.Header.Get("Sec-WebSocket-Key")
	if !isUpgrade || key == "" {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade request")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer cannot be hijacked")
	}
	netConn, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := io.WriteString(netConn, response); err != nil {
		netConn.Close()
		return nil, err
	}
	return &Conn{netConn: netConn, reader: buffered.Reader, isClient: false}, nil
}

// ReadMessage returns the next whole text or binary message. It answers pings itself.
// After the peer closes, it returns a *CloseError.
func (c *Conn) ReadMessage() ([]byte, error) {
	var message []byte
	for {
		f, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch f.opcode {
		case opPing:
			if err := c.writeFrame(opPong, f.payload); err != nil {
				return nil, err
			}
		case opPong:
			// We never send pings, so a pong needs no answer.
		case opClose:
			return nil, c.answerClose(f.payload)
		case opText, opBinary, opContinuation:
			message = append(message, f.payload...)
			if len(message) > maxMessageSize {
				return nil, errors.New("websocket message too large")
			}
			if f.final {
				return message, nil
			}
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %d", f.opcode)
		}
	}
}

// WriteText sends one text message.
func (c *Conn) WriteText(message []byte) error {
	return c.writeFrame(opText, message)
}

// Close sends a normal close and shuts the connection.
func (c *Conn) Close() error {
	return c.CloseWithCode(closeNormal, "")
}

// CloseWithCode sends a close frame with code and reason, then shuts the connection.
func (c *Conn) CloseWithCode(code int, reason string) error {
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	payload = append(payload, reason...)
	c.writeFrame(opClose, payload) // the peer may already be gone
	return c.netConn.Close()
}

// Abort drops the connection without a close frame, the way a crashed peer does.
func (c *Conn) Abort() error {
	return c.netConn.Close()
}

// answerClose echoes the peer's close code, shuts the connection and reports why it closed.
func (c *Conn) answerClose(payload []byte) error {
	closeErr := &CloseError{Code: 1005} // 1005: no status code was given
	if len(payload) >= 2 {
		closeErr.Code = int(binary.BigEndian.Uint16(payload))
		closeErr.Reason = string(payload[2:])
	}
	c.writeFrame(opClose, payload[:min(2, len(payload))])
	c.netConn.Close()
	return closeErr
}

type frame struct {
	final   bool
	opcode  byte
	payload []byte
}

func (c *Conn) readFrame() (frame, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.reader, header[:]); err != nil {
		return frame{}, err
	}
	f := frame{
		final:  header[0]&finalFrameBit != 0,
		opcode: header[0] & 0x0F,
	}
	masked := header[1]&maskBit != 0
	length, err := c.readPayloadLength(header[1] & 0x7F)
	if err != nil {
		return frame{}, err
	}
	if length > maxMessageSize {
		return frame{}, errors.New("websocket frame too large")
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, mask[:]); err != nil {
			return frame{}, err
		}
	}
	f.payload = make([]byte, length)
	if _, err := io.ReadFull(c.reader, f.payload); err != nil {
		return frame{}, err
	}
	if masked {
		applyMask(f.payload, mask)
	}
	return f, nil
}

// readPayloadLength decodes the 7-bit length, or the 16- or 64-bit length that follows it.
func (c *Conn) readPayloadLength(short byte) (uint64, error) {
	switch short {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(extended[:])), nil
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(extended[:]), nil
	default:
		return uint64(short), nil
	}
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	header := []byte{finalFrameBit | opcode}
	lengthMaskBit := byte(0)
	if c.isClient {
		lengthMaskBit = maskBit
	}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, lengthMaskBit|byte(length))
	case length <= 0xFFFF:
		header = append(header, lengthMaskBit|126)
		header = binary.BigEndian.AppendUint16(header, uint16(length))
	default:
		header = append(header, lengthMaskBit|127)
		header = binary.BigEndian.AppendUint64(header, uint64(length))
	}

	body := payload
	if c.isClient {
		var mask [4]byte
		rand.Read(mask[:])
		header = append(header, mask[:]...)
		body = make([]byte, length)
		copy(body, payload)
		applyMask(body, mask)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.netConn.Write(header); err != nil {
		return err
	}
	_, err := c.netConn.Write(body)
	return err
}

func applyMask(data []byte, mask [4]byte) {
	for i := range data {
		data[i] ^= mask[i%4]
	}
}

func newKey() string {
	var key [16]byte
	rand.Read(key[:])
	return base64.StdEncoding.EncodeToString(key[:])
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}
