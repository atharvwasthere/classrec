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
	return NewWith(testKey, Options{BaseURL: srv.URL, PollEvery: time.Millisecond,
		Retry: RetryPolicy{Max: 3, Base: time.Millisecond, MaxDelay: time.Millisecond,
			Sleep: func(context.Context, time.Duration) error { return nil }}})
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

var testParams = JobParams{Model: "saaras:v4", Mode: "codemix", WithTimestamps: true}

// One job carries every chunk; each result must come back under its own input name,
// and the api key must go to Sarvam but never to the presigned storage URLs.
func TestRunBatchRoutesResultsToTheirInputs(t *testing.T) {
	srv := sarvamtest.New(testKey)
	defer srv.Close()
	srv.PollsRun = 2

	var saved []*BatchState
	got, err := newTestClient(srv).RunBatch(context.Background(), BatchRequest{
		Params: testParams, Files: writeFiles(t, "chunk_000.ogg", "chunk_001.ogg"),
		Save: func(s *BatchState) error { saved = append(saved, s); return nil }, Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(string(got["chunk_000.ogg"].Raw), "alpha chunk_000.ogg") ||
		!strings.Contains(string(got["chunk_001.ogg"].Raw), "alpha chunk_001.ogg") {
		t.Fatalf("results not matched to inputs: %v", got)
	}
	if len(saved) != 3 || saved[0].Started || !saved[1].Started || saved[1].JobID == "" || saved[2] != nil {
		t.Fatalf("job id persisted before starting, then marked started, then cleared when over: %+v", saved)
	}
	if len(srv.Params) != 1 || !strings.Contains(string(srv.Params[0]), `"with_timestamps":true`) {
		t.Fatalf("job parameters not sent as expected: %s", srv.Params)
	}
	if len(srv.Bad) != 0 {
		t.Fatalf("protocol violations: %v", srv.Bad)
	}
}

// A crash after the job was created must resume that job, not create (and pay for) a second one;
// state made for other files or other parameters must be ignored.
func TestRunBatchResumesOnlyMatchingState(t *testing.T) {
	srv := sarvamtest.New(testKey)
	defer srv.Close()
	c := newTestClient(srv)
	ctx := context.Background()
	files := writeFiles(t, "chunk_000.ogg")
	run := func(resume *BatchState) {
		t.Helper()
		_, err := c.RunBatch(ctx, BatchRequest{Params: testParams, Files: files, Resume: resume,
			Save: func(*BatchState) error { return nil }, Logf: func(string, ...any) {}})
		if err != nil {
			t.Fatal(err)
		}
	}

	var created *BatchState
	_, _ = c.RunBatch(ctx, BatchRequest{Params: testParams, Files: files,
		Save: func(s *BatchState) error {
			if created == nil {
				created = s
			}
			return nil
		}, Logf: func(string, ...any) {}})
	if len(srv.Params) != 1 || created == nil {
		t.Fatalf("setup: want one job and its saved state, got %d jobs, state %v", len(srv.Params), created)
	}

	run(created)
	if len(srv.Params) != 1 {
		t.Fatalf("matching state must resume job %s, but %d jobs exist", created.JobID, len(srv.Params))
	}

	other := *created
	other.Files = []string{"chunk_009.ogg"}
	run(&other)
	other = *created
	other.Params = "deadbeef"
	run(&other)
	if len(srv.Params) != 3 {
		t.Fatalf("state for other files or parameters must start a new job, got %d jobs", len(srv.Params))
	}
}

func TestRetriesTransientErrorsAndNeverLeaksSecrets(t *testing.T) {
	srv := sarvamtest.New(testKey)
	defer srv.Close()
	c := newTestClient(srv)

	srv.Fail503("/speech-to-text/job/v1", 2)
	if _, err := c.initJob(context.Background(), JobParams{Model: "saaras:v4"}); err != nil {
		t.Fatalf("two 503s then success should be absorbed by retries: %v", err)
	}

	srv.Fail503("/status", 100)
	_, err := c.status(context.Background(), "job-1")
	if err == nil {
		t.Fatal("expected failure after retries are exhausted")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the api key: %v", err)
	}

	srv.Close()
	err = c.putFile(context.Background(), srv.URL+"/blob/j/f?sig=SECRETSIG", writeFiles(t, "a.ogg")[0].Path)
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatalf("transport error must not echo the signed URL: %v", err)
	}
}
