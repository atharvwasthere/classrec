package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"time"

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
	name := filepath.Base(p.m.Source.Path)
	meta := transcript.Meta{
		Date:        transcript.RecordingDate(name, time.Unix(0, p.m.Source.MtimeNS)),
		Source:      name,
		DurationSec: p.m.DurationSec,
	}
	return writeFileAtomic(p.transcriptFile(), []byte(transcript.Markdown(meta, segs)))
}
