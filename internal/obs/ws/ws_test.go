package ws

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// echoServer echoes every message back, and closes with code 4009 when it receives "bye".
func echoServer(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			return
		}
		for {
			message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if string(message) == "bye" {
				conn.CloseWithCode(4009, "auth failed")
				return
			}
			conn.WriteText(message)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestEchoAllLengthEncodings(t *testing.T) {
	conn, err := Dial(context.Background(), echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, size := range []int{0, 5, 125, 126, 65535, 65536, 200000} {
		sent := bytes.Repeat([]byte("x"), size)
		if err := conn.WriteText(sent); err != nil {
			t.Fatal(err)
		}
		got, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, sent) {
			t.Fatalf("size %d: echoed %d bytes", size, len(got))
		}
	}
}

func TestCloseCodeReachesClient(t *testing.T) {
	conn, err := Dial(context.Background(), echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	conn.WriteText([]byte("bye"))
	_, err = conn.ReadMessage()
	var closeErr *CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != 4009 || closeErr.Reason != "auth failed" {
		t.Fatalf("got %v", err)
	}
}

func TestDialRefusesNonWebSocketServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")); err == nil {
		t.Fatal("want error")
	}
}
