package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"classrec/internal/manifest"
	"classrec/internal/sarvam"
	"classrec/internal/sarvam/sarvamtest"
)

type fixture struct {
	dir, video string
	srv        *sarvamtest.Server
	cfg        Config
}

func (f *fixture) pipeline(t *testing.T) *Pipeline {
	t.Helper()
	c := sarvam.NewWith("test-key", sarvam.Options{BaseURL: f.srv.URL, PollEvery: time.Millisecond,
		Retry: sarvam.RetryPolicy{Max: 2, Base: time.Millisecond, MaxDelay: time.Millisecond,
			Sleep: func(context.Context, time.Duration) error { return nil }}})
	p, err := New(f.cfg, c, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newFixture builds a 35s video that splits into two chunks.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	needFFmpeg(t)
	dir := t.TempDir()
	srv := sarvamtest.New("test-key")
	t.Cleanup(srv.Close)
	video := makeVideo(t, dir, 35)
	return &fixture{dir: dir, video: video, srv: srv, cfg: Config{
		Video: video, Out: filepath.Join(dir, "out"), CacheDir: filepath.Join(dir, "cache"),
		ChunkMaxSec: 20, SilenceWindowSec: 5, Date: "2026-01-02",
		Params: sarvam.JobParams{Model: "saaras:v4", Mode: "codemix", WithTimestamps: true},
	}}
}

func TestSecondRunMakesNoAPICalls(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.pipeline(t).Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := f.srv.APICalls()
	if first == 0 {
		t.Fatal("first run should have called the API")
	}

	if err := f.pipeline(t).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.APICalls(); got != first {
		t.Fatalf("second run made %d API calls", got-first)
	}

	// raw output deleted: the stage reruns, but the hash cache answers it
	if err := os.RemoveAll(filepath.Join(f.cfg.Out, "raw")); err != nil {
		t.Fatal(err)
	}
	if err := f.pipeline(t).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.APICalls(); got != first {
		t.Fatalf("cache-hit run made %d API calls", got-first)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Out, "raw", "chunk_001.json")); err != nil {
		t.Fatalf("raw response not restored from cache: %v", err)
	}
}

func TestRerunAfterPartialFailureOnlyResendsMissingChunks(t *testing.T) {
	f := newFixture(t)
	f.srv.FailFile = "chunk_001.ogg"

	err := f.pipeline(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "chunk_001.ogg") {
		t.Fatalf("want a failure naming chunk_001.ogg, got %v", err)
	}
	m, _ := manifest.Load(filepath.Join(f.cfg.Out, "manifest.json"))
	if s := m.Stages["transcribe"]; s.Status != manifest.Failed || !strings.Contains(s.Error, "chunk_001.ogg") {
		t.Fatalf("manifest should record why transcribe failed: %+v", s)
	}

	f.srv.FailFile = ""
	if err := f.pipeline(t).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"chunk_000.ogg", "chunk_001.ogg", "chunk_001.ogg"}
	if strings.Join(f.srv.Uploaded, ",") != strings.Join(want, ",") {
		t.Fatalf("uploads %v, want %v (chunk_000 was already paid for)", f.srv.Uploaded, want)
	}
}
