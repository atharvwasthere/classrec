package media

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseSilences(t *testing.T) {
	stderr := `[silencedetect @ 0x1] silence_start: 10.5
[silencedetect @ 0x1] silence_end: 12.25 | silence_duration: 1.75
[silencedetect @ 0x1] silence_start: 30
[silencedetect @ 0x1] silence_end: 30.6 | silence_duration: 0.6
[silencedetect @ 0x1] silence_start: 99.9`
	want := []Silence{{10.5, 12.25}, {30, 30.6}}
	if got := parseSilences(stderr); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Stream-copying a mid-file ogg/opus chunk with short pages writes negative granule positions.
// ffprobe still reports a duration, but Sarvam rejected such files as "Audio duration is 0".
func TestCutIsValidWhereStreamCopyIsNot(t *testing.T) {
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.ogg")
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=duration=30",
		"-ac", "1", "-c:a", "libopus", "-b:a", "32k", "-f", "ogg", src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	broken := filepath.Join(dir, "broken.ogg")
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-ss", "10.3", "-i", src, "-t", "10",
		"-c", "copy", "-page_duration", "20000", "-f", "ogg", broken).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := ProbeClean(ctx, broken); err == nil {
		t.Fatal("ProbeClean must reject the stream-copied chunk")
	}

	good := filepath.Join(dir, "good.ogg")
	if err := Cut(ctx, src, good, 10.3, 10); err != nil {
		t.Fatal(err)
	}
	dur, err := ProbeClean(ctx, good)
	if err != nil || math.Abs(dur-10) > 0.05 {
		t.Fatalf("re-encoded cut should be valid and 10s long, got %v, %v", dur, err)
	}
}

func TestParseCreationDateRejectsPlaceholders(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	defer func() { time.Local = old }()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	if d, ok := parseCreationDate("2026-02-08T13:50:10.000000Z", now); !ok || d != "2026-02-08" {
		t.Fatalf("real creation_time: %q %v", d, ok)
	}
	if d, ok := parseCreationDate("2026-02-08T20:30:00Z", now); !ok || d != "2026-02-09" {
		t.Fatalf("20:30 UTC is 02:00 IST the next day, got %q %v", d, ok)
	}
	for _, bad := range []string{"", "not a date", "1904-01-01T00:00:00.000000Z", "1970-01-01T00:00:00Z", "2027-01-01T00:00:00Z"} {
		if d, ok := parseCreationDate(bad, now); ok {
			t.Errorf("%q must be rejected, got %q", bad, d)
		}
	}
}

// ffmpeg/ffprobe next to the binary win over PATH; on Windows they carry .exe.
func TestResolveInPrefersTheBinaryNextToTheExecutable(t *testing.T) {
	dir := t.TempDir()
	if got := resolveIn(dir, "ffmpeg", "windows"); got != "ffmpeg" {
		t.Fatalf("nothing next to the exe, expected the PATH name, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveIn(dir, "ffmpeg", "windows"); got != filepath.Join(dir, "ffmpeg.exe") {
		t.Fatalf("got %q", got)
	}
	if got := resolveIn(dir, "ffprobe", "windows"); got != "ffprobe" {
		t.Fatalf("ffprobe.exe is absent, expected the PATH name, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveIn(dir, "ffprobe", "linux"); got != filepath.Join(dir, "ffprobe") {
		t.Fatalf("no .exe suffix off Windows, got %q", got)
	}
	if got := resolveIn("", "ffmpeg", "windows"); got != "ffmpeg" {
		t.Fatalf("unknown exe dir must fall back to PATH, got %q", got)
	}
}
