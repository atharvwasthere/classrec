package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"classrec/internal/media"
)

func TestExtractVerifyAndReuse(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	video := makeVideo(t, dir, 4)

	p, err := New(Config{Video: video, Out: filepath.Join(dir, "out")}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.extract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.M.AudioSHA256 == "" || p.M.DurationSec < 3 {
		t.Fatalf("manifest not filled: %+v", p.M)
	}
	if !p.extractVerify() {
		t.Fatal("verify should pass for untouched audio")
	}

	// a reusable file with the right duration is adopted byte for byte
	reuse := filepath.Join(dir, "pre.ogg")
	if err := media.ExtractAudio(context.Background(), video, reuse); err != nil {
		t.Fatal(err)
	}
	want, _ := media.SHA256File(reuse)
	p2, _ := New(Config{Video: video, Out: filepath.Join(dir, "out2"), ReuseAudio: reuse}, quiet)
	if err := p2.extract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p2.M.AudioSHA256 != want {
		t.Fatal("reused audio was not adopted")
	}

	// a reusable file of the wrong length is ignored and re-extracted
	short := filepath.Join(dir, "short.ogg")
	ffmpegOK(t, "-f", "lavfi", "-i", "sine=duration=1", "-c:a", "libopus", "-f", "ogg", short)
	shortSum, _ := media.SHA256File(short)
	p3, _ := New(Config{Video: video, Out: filepath.Join(dir, "out3"), ReuseAudio: short}, quiet)
	if err := p3.extract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p3.M.AudioSHA256 == shortSum {
		t.Fatal("mismatched audio must not be adopted")
	}
}
