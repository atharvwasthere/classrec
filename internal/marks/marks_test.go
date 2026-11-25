package marks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
