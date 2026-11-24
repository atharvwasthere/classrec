package pipeline

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestChunkStageCutsInSilenceAndCoversAudio(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	video := makeVideo(t, dir, 35) // silent at 8-10, 18-20, 28-30 s

	p, err := New(Config{Video: video, Out: filepath.Join(dir, "out"), ChunkMaxSec: 20, SilenceWindowSec: 5}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.extract(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.chunkStage(ctx); err != nil {
		t.Fatal(err)
	}

	cs := p.M.Chunks
	if len(cs) != 2 {
		t.Fatalf("want 2 chunks, got %+v", cs)
	}
	if math.Abs(cs[0].DurationSec-19) > 0.6 {
		t.Fatalf("first chunk should end in the 18-20s silence, got %.2fs", cs[0].DurationSec)
	}
	if math.Abs(cs[1].StartSec-cs[0].DurationSec) > 0.6 {
		t.Fatalf("second chunk starts at %.2f, first ends at %.2f", cs[1].StartSec, cs[0].DurationSec)
	}
	if sum := cs[0].DurationSec + cs[1].DurationSec; math.Abs(sum-p.M.DurationSec) > 1 {
		t.Fatalf("chunks total %.2fs, audio is %.2fs", sum, p.M.DurationSec)
	}
	if !p.chunkVerify() {
		t.Fatal("verify should pass for untouched chunks")
	}
	if err := os.WriteFile(p.chunkPath(cs[1]), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p.chunkVerify() {
		t.Fatal("verify must fail once a chunk changes")
	}
}
