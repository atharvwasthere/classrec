// Package chunk decides where to split a long recording.
package chunk

import "classrec/internal/media"

type Span struct {
	Start, Duration float64
}

// Plan splits [0,total) into spans of at most maxLen seconds. Each cut goes in the
// longest silence that falls in the last `window` seconds before the limit, or is a
// hard cut at the limit when there is none.
func Plan(total, maxLen, window float64, silences []media.Silence) []Span {
	var spans []Span
	start := 0.0
	for total-start > maxLen {
		cut := cutPoint(start, start+maxLen, window, silences)
		spans = append(spans, Span{Start: start, Duration: cut - start})
		start = cut
	}
	return append(spans, Span{Start: start, Duration: total - start})
}

func cutPoint(start, limit, window float64, silences []media.Silence) float64 {
	lo := limit - window
	if lo < start {
		lo = start
	}
	best, bestLen := limit, 0.0
	for _, s := range silences {
		a, b := max(s.Start, lo), min(s.End, limit)
		if b-a > bestLen {
			best, bestLen = (a+b)/2, b-a
		}
	}
	return best
}
