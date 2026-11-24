package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"classrec/internal/transcript"
)

const (
	marksBeforeSec = 120
	marksAfterSec  = 180
)

func readMarks(path string) ([]float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var marks []float64
	if err := json.Unmarshal(b, &marks); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of seconds: %w", path, err)
	}
	for _, m := range marks {
		if m < 0 {
			return nil, fmt.Errorf("%s: mark %v is negative", path, m)
		}
	}
	return marks, nil
}

// marks is cheap and the marks file may change between runs, so it always reruns.
func (p *Pipeline) marks(context.Context) error {
	marks, err := readMarks(p.cfg.MarksPath)
	if err != nil {
		return err
	}
	segs, err := p.loadSegments()
	if err != nil {
		return err
	}
	return writeFileAtomic(p.path("marks.md"), []byte(transcript.MarksMarkdown(marks, segs, marksBeforeSec, marksAfterSec)))
}
