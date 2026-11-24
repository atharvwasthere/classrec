package pipeline

import (
	"context"
	"os"

	"classrec/internal/fsx"
	"classrec/internal/transcript"
)

func (p *Pipeline) transcriptFile() string { return p.path("transcript.md") }

func (p *Pipeline) writeVerify() bool {
	_, err := os.Stat(p.transcriptFile())
	return err == nil && p.m.Date == p.date
}

func (p *Pipeline) write(context.Context) error {
	segs, err := p.loadSegments()
	if err != nil {
		return err
	}
	md := transcript.Markdown(p.m.Source.Path, p.date, p.m.DurationSec, segs)
	if err := fsx.WriteFileAtomic(p.transcriptFile(), []byte(md)); err != nil {
		return err
	}
	p.m.Date = p.date
	return nil
}
