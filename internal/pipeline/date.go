package pipeline

import (
	"context"
	"fmt"
	"time"

	"classrec/internal/media"
	"classrec/internal/transcript"
)

const dateNotFound = "cannot tell when this was recorded: the file name is not a Zoom name and the video has no creation_time; pass --date YYYY-MM-DD"

// resolveDate picks the recording date: an explicit --date, else the Zoom file name, else the
// container's creation_time. It never falls back to the file's modified time (that is the download date).
func (p *Pipeline) resolveDate(ctx context.Context) (string, error) {
	if d := p.cfg.Date; d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return "", fmt.Errorf("--date %q must look like 2026-02-08", d)
		}
		return d, nil
	}
	if d, ok := transcript.ZoomDate(p.cfg.Video); ok {
		return d, nil
	}
	if d, ok := media.CreationDate(ctx, p.cfg.Video); ok {
		return d, nil
	}
	return "", fmt.Errorf("%s", dateNotFound)
}
