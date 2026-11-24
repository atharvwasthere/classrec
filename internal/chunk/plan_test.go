package chunk

import (
	"math"
	"testing"

	"classrec/internal/media"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPlanShortInputIsOneChunk(t *testing.T) {
	got := Plan(300, 3600, 120, nil)
	if len(got) != 1 || got[0].Start != 0 || got[0].Duration != 300 {
		t.Fatalf("got %v", got)
	}
}

func TestPlanHardCutWhenNoSilence(t *testing.T) {
	got := Plan(7500, 3600, 120, nil)
	want := []Span{{0, 3600}, {3600, 3600}, {7200, 300}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if !near(got[i].Start, want[i].Start) || !near(got[i].Duration, want[i].Duration) {
			t.Fatalf("span %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestPlanCutsInTheLongestSilenceInTheWindow(t *testing.T) {
	silences := []media.Silence{
		{Start: 3000, End: 3010},   // outside the window
		{Start: 3500, End: 3501},   // short
		{Start: 3540, End: 3546},   // longest in [3480,3600]
		{Start: 3700, End: 3710},   // past the limit, must not be used
		{Start: 3598, End: 3604.5}, // straddles the limit, only [3598,3600] counts
	}
	got := Plan(7000, 3600, 120, silences)
	if !near(got[0].Duration, 3543) {
		t.Fatalf("first cut should be the midpoint of 3540-3546, got %v", got[0])
	}
}

func TestPlanChunksNeverExceedMaxAndCoverEverything(t *testing.T) {
	silences := []media.Silence{{Start: 3560, End: 3570}, {Start: 7100, End: 7110}}
	total := 10800.0 // a 3 hour class
	spans := Plan(total, 3600, 120, silences)
	end := 0.0
	for i, s := range spans {
		if !near(s.Start, end) {
			t.Fatalf("span %d starts at %v, previous ended at %v", i, s.Start, end)
		}
		if s.Duration > 3600+1e-9 || s.Duration <= 0 {
			t.Fatalf("span %d has duration %v", i, s.Duration)
		}
		end = s.Start + s.Duration
	}
	if !near(end, total) {
		t.Fatalf("spans end at %v, want %v", end, total)
	}
}
