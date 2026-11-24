package sarvam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"classrec/internal/sarvam/sarvamtest"
)

const testKey = "sk-test-key-123"

func newTestClient(srv *sarvamtest.Server) *Client {
	c := New(testKey)
	c.BaseURL = srv.URL
	c.PollEvery = time.Millisecond
	c.Retry = RetryPolicy{Max: 3, Base: time.Millisecond, MaxDelay: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	return c
}

func writeFiles(t *testing.T, names ...string) []BatchFile {
	t.Helper()
	var files []BatchFile
	for _, n := range names {
		p := filepath.Join(t.TempDir(), n)
		if err := os.WriteFile(p, []byte("audio-bytes-"+n), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, BatchFile{Name: n, Path: p})
	}
	return files
}

// One job carries every chunk; each result must come back under its own input name,
// and the api key must go to Sarvam but never to the presigned storage URLs.
func TestRunBatchRoutesResultsToTheirInputs(t *testing.T) {
	srv := sarvamtest.New(testKey)
	defer srv.Close()
	srv.PollsRun = 2

	var saved []BatchState
	got, err := newTestClient(srv).RunBatch(context.Background(),
		JobParams{Model: "saaras:v4", Mode: "codemix", WithTimestamps: true},
		writeFiles(t, "chunk_000.ogg", "chunk_001.ogg"), BatchState{},
		func(s BatchState) error { saved = append(saved, s); return nil }, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(string(got["chunk_000.ogg"].Raw), "alpha chunk_000.ogg") ||
		!strings.Contains(string(got["chunk_001.ogg"].Raw), "alpha chunk_001.ogg") {
		t.Fatalf("results not matched to inputs: %v", got)
	}
	if len(saved) != 2 || saved[0].Started || !saved[1].Started || saved[1].JobID == "" {
		t.Fatalf("job id must be persisted before starting, then marked started: %+v", saved)
	}
	if len(srv.Params) != 1 || !strings.Contains(string(srv.Params[0]), `"with_timestamps":true`) {
		t.Fatalf("job parameters not sent as expected: %s", srv.Params)
	}
	if len(srv.Bad) != 0 {
		t.Fatalf("protocol violations: %v", srv.Bad)
	}
}

func TestRetriesTransientErrorsAndNeverLeaksSecrets(t *testing.T) {
	srv := sarvamtest.New(testKey)
	defer srv.Close()
	c := newTestClient(srv)

	srv.Fail503("/speech-to-text/job/v1", 2)
	if _, err := c.InitJob(context.Background(), JobParams{Model: "saaras:v4"}); err != nil {
		t.Fatalf("two 503s then success should be absorbed by retries: %v", err)
	}

	srv.Fail503("/status", 100)
	_, err := c.Status(context.Background(), "job-1")
	if err == nil {
		t.Fatal("expected failure after retries are exhausted")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the api key: %v", err)
	}

	srv.Close()
	err = c.PutFile(context.Background(), srv.URL+"/blob/j/f?sig=SECRETSIG", writeFiles(t, "a.ogg")[0].Path)
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatalf("transport error must not echo the signed URL: %v", err)
	}
}
