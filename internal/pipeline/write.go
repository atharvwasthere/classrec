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
	name := filepath.Base(p.M.Source.Path)
	meta := transcript.Meta{
		Date:        transcript.RecordingDate(name, time.Unix(0, p.M.Source.MtimeNS)),
		Source:      name,
		DurationSec: p.M.DurationSec,
	}
	return writeFileAtomic(p.transcriptFile(), []byte(transcript.Markdown(meta, segs)))
}
