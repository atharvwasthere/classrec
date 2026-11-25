package marks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Promise: a mark lands at the right second of the joined recording, even when OBS restarted mid-class.
func TestToSecondsAcrossPieces(t *testing.T) {
	start := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return start.Add(time.Duration(sec) * time.Second) }
	starts := []time.Time{at(0), at(620)} // piece 1 lasts 600 s, OBS restarts, piece 2 from 620 s
	durations := []float64{600, 900}
	got := ToSeconds([]time.Time{at(-5), at(100), at(610), at(700), at(5000)}, starts, durations)
	want := []float64{0, 100, 600, 680, 1500} // before, in piece 1, in the gap, in piece 2, after
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestAppendTimeKeepsWallClockTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "class", "marks.json")
	ist := time.FixedZone("IST", 5*3600+1800)
	first := time.Date(2026, 11, 25, 14, 20, 5, 123456789, ist)
	second := first.Add(40 * time.Minute)
	for _, at := range []time.Time{first, second} {
		if err := AppendTime(path, at); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if want := `["2026-11-25T14:20:05.123+05:30","2026-11-25T15:00:05.123+05:30"]`; string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
	got, err := ReadTimes(path)
	if err != nil || len(got) != 2 || !got[1].Equal(second.Round(time.Millisecond)) {
		t.Fatalf("got %v, err %v", got, err)
	}
}

func TestAppendTimeKeepsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marks.json")
	os.WriteFile(path, []byte("[12.3]"), 0o644) // seconds, not times
	if err := AppendTime(path, time.Now()); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(path); string(b) != "[12.3]" {
		t.Fatalf("file was overwritten: %s", b)
	}
}
