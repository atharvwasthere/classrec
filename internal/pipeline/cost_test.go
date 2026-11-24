package pipeline

import (
	"testing"

	"classrec/internal/manifest"
)

// The real class: 2h05m = 125 min -> ₹61.25 + 18% GST (₹11.025, rounded to paise) = ₹72.28.
func TestCostOfTheTwoHourFiveMinuteClass(t *testing.T) {
	api := &manifest.APITiming{}
	chunks := []manifest.Chunk{
		{DurationSec: 3600, API: api}, {DurationSec: 3600, API: api}, {DurationSec: 299.4, API: api},
	}
	billed, m := CostOf(chunks)
	if billed != 7500 {
		t.Fatalf("billed %d seconds, want 7500 (each chunk rounded up)", billed)
	}
	if m.BeforeGST != 61.25 || m.GST != 11.03 || m.Total != 72.28 {
		t.Fatalf("got %+v", m)
	}
}

func TestCostIgnoresChunksServedFromCache(t *testing.T) {
	chunks := []manifest.Chunk{{DurationSec: 600, API: &manifest.APITiming{}}, {DurationSec: 600}}
	if billed, _ := CostOf(chunks); billed != 600 {
		t.Fatalf("a cached chunk was never sent, got %d billed seconds", billed)
	}
}
