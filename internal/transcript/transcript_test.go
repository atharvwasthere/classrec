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
	got := merge([][]Segment{c0, c1}, []float64{0, 3599.5})
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
		if got := formatTimestamp(in); got != want {
			t.Errorf("formatTimestamp(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestMarkdownKeepsTextVerbatimOneLinePerSegment(t *testing.T) {
	segs := []Segment{{3.2, 6, "शनि की दशा"}, {3601, 3605, "next guru\nline"}}
	got := Markdown(`C:\Users\Admin\Class Recordings\GMT20251228-133527_Recording.mp4`, "2025-12-28", 7500, segs)
	want := "---\ndate: 2025-12-28\nsource: \"GMT20251228-133527_Recording.mp4\"\nduration: 02:05:00\n---\n\n" +
		"[00:00:03] शनि की दशा\n[01:00:01] next guru line\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Zoom names are GMT; the date written is the local calendar day, so a class at 04:00 IST on the 28th
// (22:30 GMT on the 27th) is dated the 28th. Windows paths must work on any OS running the code.
func TestZoomDateIsTheLocalDayAndReadsWindowsPaths(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	defer func() { time.Local = old }()

	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{`C:\Users\Admin\Class Recordings\GMT20251228-133527_Recording_640x360.mp4`, "2025-12-28", true},
		{"/Users/x/Class Recordings/GMT20251227-223000_Recording.mp4", "2025-12-28", true},
		{"lecture.mp4", "", false},
		{`C:\GMT20251228-133527\lecture.mp4`, "", false}, // only the file name counts, not a folder
	}
	for _, c := range cases {
		got, ok := ZoomDate(c.path)
		if got != c.want || ok != c.ok {
			t.Errorf("ZoomDate(%q) = %q, %v; want %q, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestMarksWindowIsMinus120ToPlus180AndClampsAtStart(t *testing.T) {
	segs := []Segment{{0, 1, "s0"}, {879, 880, "before"}, {880, 881, "edge-in"}, {1180, 1181, "edge-out"}, {1181, 1182, "after"}}
	got := around(segs, 1000, 120, 180)
	if len(got) != 2 || got[0].Text != "edge-in" || got[1].Text != "edge-out" {
		t.Fatalf("window boundaries wrong: %v", got)
	}
	if early := around(segs, 60, 120, 180); len(early) != 1 || early[0].Text != "s0" {
		t.Fatalf("a mark near the start must not drop the opening lines: %v", early)
	}
}

func TestParseChunkRejectsShapesItCannotTrust(t *testing.T) {
	// "words" is what the live API returns; "chunks" is what the docs show
	for key, raw := range map[string]string{
		"words":  `{"timestamps":{"words":["x","y"],"start_time_seconds":[0,2],"end_time_seconds":[2,4]}}`,
		"chunks": `{"timestamps":{"chunks":["x","y"],"start_time_seconds":[0,2],"end_time_seconds":[2,4]}}`,
	} {
		segs, err := parseChunk([]byte(raw))
		if err != nil || len(segs) != 2 || segs[1] != (Segment{2, 4, "y"}) {
			t.Fatalf("%s: %v %v", key, segs, err)
		}
	}
	for name, raw := range map[string]string{
		"no timestamps": `{"transcript":"x"}`,
		"misaligned":    `{"timestamps":{"chunks":["x","y"],"start_time_seconds":[0],"end_time_seconds":[2,4]}}`,
	} {
		if _, err := parseChunk([]byte(raw)); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: expected an error", name)
		}
	}
}
