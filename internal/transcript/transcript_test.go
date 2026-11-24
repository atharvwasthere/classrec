package transcript

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMergeAddsChunkOffsetsAndKeepsOrder(t *testing.T) {
	c0 := []Segment{{0, 5, "a"}, {3590.5, 3598, "b"}}
	c1 := []Segment{{0.25, 4, "c"}, {7, 9, "d"}}
	got := Merge([][]Segment{c0, c1}, []float64{0, 3599.5})
	want := []Segment{{0, 5, "a"}, {3590.5, 3598, "b"}, {3599.75, 3603.5, "c"}, {3606.5, 3608.5, "d"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestFormatTimestampRoundsDown(t *testing.T) {
	cases := map[float64]string{
		0: "00:00:00", 59.99: "00:00:59", 60: "00:01:00", 3599.99: "00:59:59",
		3600: "01:00:00", 7500: "02:05:00", 36000: "10:00:00", -3: "00:00:00",
	}
	for in, want := range cases {
		if got := FormatTimestamp(in); got != want {
			t.Errorf("FormatTimestamp(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestMarkdownKeepsTextVerbatimOneLinePerSegment(t *testing.T) {
	segs := []Segment{{3.2, 6, "शनि की दशा"}, {3601, 3605, "next guru\nline"}}
	got := Markdown(Meta{Date: "2025-12-28", Source: "GMT20251228-133527_Recording.mp4", DurationSec: 7500}, segs)
	want := "---\ndate: 2025-12-28\nsource: \"GMT20251228-133527_Recording.mp4\"\nduration: 02:05:00\n---\n\n" +
		"[00:00:03] शनि की दशा\n[01:00:01] next guru line\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRecordingDate(t *testing.T) {
	fallback := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := RecordingDate("GMT20251228-133527_Recording_640x360.mp4", fallback); got != "2025-12-28" {
		t.Fatal(got)
	}
	if got := RecordingDate("lecture.mp4", fallback); got != "2026-01-02" {
		t.Fatal(got)
	}
}

func TestMarksWindowIsMinus120ToPlus180AndClampsAtStart(t *testing.T) {
	segs := []Segment{{0, 1, "s0"}, {879, 880, "before"}, {880, 881, "edge-in"}, {1180, 1181, "edge-out"}, {1181, 1182, "after"}}
	got := Around(segs, 1000, 120, 180)
	if len(got) != 2 || got[0].Text != "edge-in" || got[1].Text != "edge-out" {
		t.Fatalf("window boundaries wrong: %v", got)
	}
	if early := Around(segs, 60, 120, 180); len(early) != 1 || early[0].Text != "s0" {
		t.Fatalf("a mark near the start must not drop the opening lines: %v", early)
	}
}

func TestParseChunkRejectsShapesItCannotTrust(t *testing.T) {
	ok := `{"timestamps":{"chunks":["x","y"],"start_time_seconds":[0,2],"end_time_seconds":[2,4]}}`
	segs, err := ParseChunk([]byte(ok))
	if err != nil || len(segs) != 2 || segs[1] != (Segment{2, 4, "y"}) {
		t.Fatalf("valid response: %v %v", segs, err)
	}
	for name, raw := range map[string]string{
		"no timestamps": `{"transcript":"x"}`,
		"misaligned":    `{"timestamps":{"chunks":["x","y"],"start_time_seconds":[0],"end_time_seconds":[2,4]}}`,
	} {
		if _, err := ParseChunk([]byte(raw)); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: expected an error", name)
		}
	}
}
