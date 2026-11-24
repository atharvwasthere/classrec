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

// parseChunk reads one Sarvam batch output file. Timestamps are chunk-level: parallel arrays of
// text, start and end, in seconds from the start of that audio file. The live API names the text
// array "words" (each entry is a phrase of ~15s); the docs call it "chunks", so both are read.
func parseChunk(raw []byte) ([]Segment, error) {
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

// merge shifts each chunk's segments by that chunk's start offset in the full recording.
func merge(parts [][]Segment, offsets []float64) []Segment {
	var all []Segment
	for i, part := range parts {
		for _, s := range part {
			all = append(all, Segment{Start: s.Start + offsets[i], End: s.End + offsets[i], Text: s.Text})
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].Start < all[b].Start })
	return all
}

// MergeChunks parses each chunk's Sarvam output and places its segments on the whole
// recording's timeline: offsets[i] is where chunk i starts.
func MergeChunks(raws [][]byte, offsets []float64) ([]Segment, error) {
	parts := make([][]Segment, len(raws))
	for i, raw := range raws {
		var err error
		if parts[i], err = parseChunk(raw); err != nil {
			return nil, fmt.Errorf("chunk %d: %w", i, err)
		}
	}
	return merge(parts, offsets), nil
}

// formatTimestamp renders seconds as hh:mm:ss, rounding down.
func formatTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int64(sec)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s%3600/60, s%60)
}

func line(s Segment) string {
	// a newline would split one segment over two lines; that is the only edit made to the text
	return "[" + formatTimestamp(s.Start) + "] " + strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s.Text)
}

// Markdown renders the transcript: YAML front matter (date, source file, duration), then one
// "[hh:mm:ss] text" line per segment. date is YYYY-MM-DD, resolved by the caller.
func Markdown(sourcePath, date string, durationSec float64, segs []Segment) string {
	src, _ := json.Marshal(baseName(sourcePath)) // a JSON string is a valid YAML scalar
	var b strings.Builder
	fmt.Fprintf(&b, "---\ndate: %s\nsource: %s\nduration: %s\n---\n\n", date, src, formatTimestamp(durationSec))
	for _, s := range segs {
		b.WriteString(line(s))
		b.WriteByte('\n')
	}
	return b.String()
}

// baseName splits on both separators so a Windows path works no matter which OS runs this.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

var zoomName = regexp.MustCompile(`GMT(\d{8}-\d{6})`)

// ZoomDate reads the recording's local calendar date from a Zoom file name (GMT20251228-133527_...).
func ZoomDate(path string) (string, bool) {
	m := zoomName.FindStringSubmatch(baseName(path))
	if m == nil {
		return "", false
	}
	t, err := time.ParseInLocation("20060102-150405", m[1], time.UTC)
	if err != nil {
		return "", false
	}
	return t.In(time.Local).Format("2006-01-02"), true
}

// around returns segments starting from mark-before to mark+after seconds, inclusive.
func around(segs []Segment, mark, before, after float64) []Segment {
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
		fmt.Fprintf(&b, "## Mark %d at [%s]\n\n", i+1, formatTimestamp(mark))
		window := around(segs, mark, before, after)
		if len(window) == 0 {
			b.WriteString("(no transcript lines in this window)\n")
		}
		for _, s := range window {
			b.WriteString(line(s))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}
