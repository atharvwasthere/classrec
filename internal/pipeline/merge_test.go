package pipeline

import (
	"context"
	"math"
	"strings"
	"testing"
)

// The fake returns segments at 0s and 5s in every chunk, so the second chunk's must land at its real offset.
func TestMergeShiftsLaterChunksByTheirRealStart(t *testing.T) {
	f := newFixture(t)
	p := f.pipeline(t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	segs, err := p.loadSegments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 4 {
		t.Fatalf("want 4 segments, got %v", segs)
	}
	off := p.m.Chunks[1].StartSec
	if off < 15 || math.Abs(segs[2].Start-off) > 1e-9 || math.Abs(segs[3].Start-(off+5)) > 1e-9 {
		t.Fatalf("chunk 1 starts at %.3fs but its segments start at %.3f and %.3f", off, segs[2].Start, segs[3].Start)
	}
	if !strings.Contains(segs[2].Text, "chunk_001") {
		t.Fatalf("segment came from the wrong chunk: %v", segs[2])
	}
}
