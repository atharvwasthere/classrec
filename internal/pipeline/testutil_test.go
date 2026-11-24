package pipeline

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
}

func ffmpegOK(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append([]string{"-y", "-v", "error"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// makeVideo writes a small mp4 whose audio is a tone with 2s of silence every 10s.
func makeVideo(t *testing.T, dir string, seconds int) string {
	t.Helper()
	path := filepath.Join(dir, "class.mp4")
	d := strconv.Itoa(seconds)
	ffmpegOK(t,
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=5:duration="+d,
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+d,
		"-af", "volume=enable='between(mod(t,10),8,10)':volume=0",
		"-c:v", "mpeg4", "-c:a", "aac", "-shortest", path)
	return path
}
