package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setIST(t *testing.T) {
	t.Helper()
	old := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	t.Cleanup(func() { time.Local = old })
}

func TestResolveDateChain(t *testing.T) {
	needFFmpeg(t)
	setIST(t)
	dir := t.TempDir()
	plain := makeVideo(t, dir, 2)

	zoom := filepath.Join(dir, "GMT20251228-133527_Recording.mp4")
	if err := os.Rename(plain, zoom); err != nil {
		t.Fatal(err)
	}
	tagged := filepath.Join(dir, "BNN DAY 42.mp4")
	ffmpegOK(t, "-i", zoom, "-c", "copy", "-metadata", "creation_time=2026-02-08T13:50:10Z", tagged)
	bare := filepath.Join(dir, "lecture.mp4")
	ffmpegOK(t, "-i", zoom, "-c", "copy", "-map_metadata", "-1", bare)
	// make the modified time say something that must never be used
	old := time.Date(2001, 3, 4, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(bare, old, old); err != nil {
		t.Fatal(err)
	}

	resolve := func(video, date string) (string, error) {
		p, err := New(Config{Video: video, Out: filepath.Join(dir, "out-"+filepath.Base(video)), Date: date}, nil, quiet)
		if err != nil {
			t.Fatal(err)
		}
		return p.resolveDate(context.Background())
	}

	if d, err := resolve(zoom, ""); err != nil || d != "2025-12-28" {
		t.Fatalf("zoom file name: %q %v", d, err)
	}
	if d, err := resolve(tagged, ""); err != nil || d != "2026-02-08" {
		t.Fatalf("container creation_time: %q %v", d, err)
	}
	if d, err := resolve(zoom, "2024-05-06"); err != nil || d != "2024-05-06" {
		t.Fatalf("an explicit --date wins: %q %v", d, err)
	}
	if _, err := resolve(bare, ""); err == nil || !strings.Contains(err.Error(), "--date") {
		t.Fatalf("with no source of truth the error must ask for --date (never use the 2001 mtime): %v", err)
	}
	if _, err := resolve(bare, "08.02.2026"); err == nil {
		t.Fatal("a --date that is not YYYY-MM-DD must be rejected")
	}
}

// Failing after the paid transcription would waste money, so a missing date must stop the run first.
func TestMissingDateStopsBeforeAnyAPICall(t *testing.T) {
	f := newFixture(t)
	f.cfg.Date = ""
	err := f.pipeline(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--date") {
		t.Fatalf("expected a --date error, got %v", err)
	}
	if n := f.srv.APICalls(); n != 0 {
		t.Fatalf("made %d API calls before failing", n)
	}
}

func TestChangingTheDateRewritesTheTranscriptWithoutNewAPICalls(t *testing.T) {
	f := newFixture(t)
	if err := f.pipeline(t).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.srv.APICalls()
	transcript := filepath.Join(f.cfg.Out, "transcript.md")
	if b, _ := os.ReadFile(transcript); !strings.Contains(string(b), "date: 2026-01-02") {
		t.Fatalf("first transcript has the wrong date:\n%s", b)
	}

	f.cfg.Date = "2026-02-08"
	if err := f.pipeline(t).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(transcript); !strings.Contains(string(b), "date: 2026-02-08") {
		t.Fatalf("transcript was not rewritten with the new date:\n%s", b)
	}
	if f.srv.APICalls() != calls {
		t.Fatal("rewriting the front matter must not call the API")
	}
}
