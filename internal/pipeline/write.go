package pipeline

import (
	"context"
	"os"
	"time"

	"classrec/internal/fsx"
	"classrec/internal/transcript"
)

func (p *Pipeline) transcriptFile() string { return p.path("transcript.md") }

func (p *Pipeline) writeVerify() bool {
	_, err := os.Stat(p.transcriptFile())
	return err == nil
}

func (p *Pipeline) write(context.Context) error {
	segs, err := p.loadSegments()
	if err != nil {
		return err
	}
	md := transcript.Markdown(p.m.Source.Path, time.Unix(0, p.m.Source.MtimeNS), p.m.DurationSec, segs)
	return fsx.WriteFileAtomic(p.transcriptFile(), []byte(md))
}
