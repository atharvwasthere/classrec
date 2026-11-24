package media

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSilences(t *testing.T) {
	stderr := `[silencedetect @ 0x1] silence_start: 10.5
[silencedetect @ 0x1] silence_end: 12.25 | silence_duration: 1.75
[silencedetect @ 0x1] silence_start: 30
[silencedetect @ 0x1] silence_end: 30.6 | silence_duration: 0.6
[silencedetect @ 0x1] silence_start: 99.9`
	want := []Silence{{10.5, 12.25}, {30, 30.6}}
	if got := ParseSilences(stderr); !reflect.DeepEqual(got, want) {
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
