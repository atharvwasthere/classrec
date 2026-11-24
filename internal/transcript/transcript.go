// Package transcript turns Sarvam's per-chunk responses into one timestamped class transcript.
package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// ParseChunk reads one Sarvam batch output file. Timestamps are chunk-level: parallel arrays of
// text, start and end, in seconds from the start of that audio file. The live API names the text
// array "words" (each entry is a phrase of ~15s); the docs call it "chunks", so both are read.
func ParseChunk(raw []byte) ([]Segment, error) {
	var r struct {
		Timestamps *struct {
			Words  []string  `json:"words"`
			Chunks []string  `json:"chunks"`
			Start  []float64 `json:"start_time_seconds"`
			End    []float64 `json:"end_time_seconds"`
		} `json:"timestamps"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	ts := r.Timestamps
	if ts == nil {
		return nil, errors.New("response has no timestamps (was the job created with with_timestamps?)")
	}
	texts := ts.Words
	if len(texts) == 0 {
		texts = ts.Chunks
	}
	if len(texts) != len(ts.Start) || len(texts) != len(ts.End) {
		return nil, fmt.Errorf("timestamps are misaligned: %d texts, %d starts, %d ends", len(texts), len(ts.Start), len(ts.End))
	}
	segs := make([]Segment, len(texts))
	for i := range segs {
		segs[i] = Segment{Start: ts.Start[i], End: ts.End[i], Text: texts[i]}
	}
	return segs, nil
}

// Merge shifts each chunk's segments by that chunk's start offset in the full recording.
func Merge(parts [][]Segment, offsets []float64) []Segment {
	var all []Segment
	for i, part := range parts {
		for _, s := range part {
			all = append(all, Segment{Start: s.Start + offsets[i], End: s.End + offsets[i], Text: s.Text})
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].Start < all[b].Start })
	return all
}

// FormatTimestamp renders seconds as hh:mm:ss, rounding down.
func FormatTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int64(sec)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s%3600/60, s%60)
}

func Line(s Segment) string {
	// a newline would split one segment over two lines; that is the only edit made to the text
	return "[" + FormatTimestamp(s.Start) + "] " + strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s.Text)
}

type Meta struct {
	Date        string
	Source      string
	DurationSec float64
}

func Markdown(m Meta, segs []Segment) string {
	src, _ := json.Marshal(m.Source) // a JSON string is a valid YAML scalar
	var b strings.Builder
	fmt.Fprintf(&b, "---\ndate: %s\nsource: %s\nduration: %s\n---\n\n", m.Date, src, FormatTimestamp(m.DurationSec))
	for _, s := range segs {
		b.WriteString(Line(s))
		b.WriteByte('\n')
	}
	return b.String()
}

var zoomName = regexp.MustCompile(`GMT(\d{4})(\d{2})(\d{2})-\d{6}`)

// RecordingDate reads the date from a Zoom file name (GMT20251228-133527_...), else uses fallback.
func RecordingDate(fileName string, fallback time.Time) string {
	if m := zoomName.FindStringSubmatch(fileName); m != nil {
		return m[1] + "-" + m[2] + "-" + m[3]
	}
	return fallback.Format("2006-01-02")
}

// Around returns segments starting from mark-before to mark+after seconds, inclusive.
func Around(segs []Segment, mark, before, after float64) []Segment {
	var out []Segment
	for _, s := range segs {
		if s.Start >= mark-before && s.Start <= mark+after {
			out = append(out, s)
		}
	}
	return out
}

func MarksMarkdown(marks []float64, segs []Segment, before, after float64) string {
	var b strings.Builder
	for i, mark := range marks {
		fmt.Fprintf(&b, "## Mark %d at [%s]\n\n", i+1, FormatTimestamp(mark))
		window := Around(segs, mark, before, after)
		if len(window) == 0 {
			b.WriteString("(no transcript lines in this window)\n")
		}
		for _, s := range window {
			b.WriteString(Line(s))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}
