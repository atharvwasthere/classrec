package pipeline

import (
	"context"

	"classrec/internal/fsx"
	"classrec/internal/marks"
	"classrec/internal/transcript"
)

const (
	marksBeforeSec = 120
	marksAfterSec  = 180
)

// marks is cheap and the marks file may change between runs, so it always reruns.
func (p *Pipeline) marks(context.Context) error {
	ms, err := marks.Read(p.cfg.MarksPath)
	if err != nil {
		return err
	}
	segs, err := p.loadSegments()
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p.path("marks.md"), []byte(transcript.MarksMarkdown(ms, segs, marksBeforeSec, marksAfterSec)))
}
