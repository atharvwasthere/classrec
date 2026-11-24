package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"classrec/internal/chunk"
	"classrec/internal/manifest"
	"classrec/internal/media"
)

// cutTolerance is how far a cut chunk's real length may differ from its plan.
const cutTolerance = 0.5

func (p *Pipeline) chunkPath(c manifest.Chunk) string { return p.path(c.File) }

func (p *Pipeline) chunkVerify() bool {
	if len(p.M.Chunks) == 0 {
		return false
	}
	for _, c := range p.M.Chunks {
		sum, err := media.SHA256File(p.chunkPath(c))
		if err != nil || sum != c.SHA256 {
			return false
		}
	}
	return true
}

func (p *Pipeline) chunkStage(ctx context.Context) error {
	total := p.M.DurationSec
	var silences []media.Silence
	if total > p.Cfg.ChunkMaxSec {
		p.Logf("detecting silences to find cut points")
		var err error
		silences, err = media.DetectSilences(ctx, p.audioFile(), p.Cfg.SilenceNoiseDB, p.Cfg.SilenceMinSec)
		if err != nil {
			return err
		}
	}
	spans := chunk.Plan(total, p.Cfg.ChunkMaxSec, p.Cfg.SilenceWindowSec, silences)

	if err := os.RemoveAll(p.path("chunks")); err != nil {
		return err
	}
	if err := os.MkdirAll(p.path("chunks"), 0o755); err != nil {
		return err
	}
	chunks := make([]manifest.Chunk, 0, len(spans))
	for i, s := range spans {
		c := manifest.Chunk{Index: i, File: filepath.Join("chunks", fmt.Sprintf("chunk_%03d.ogg", i)), StartSec: s.Start}
		if err := media.Cut(ctx, p.audioFile(), p.chunkPath(c), s.Start, s.Duration); err != nil {
			return err
		}
		dur, err := media.Probe(ctx, p.chunkPath(c))
		if err != nil {
			return err
		}
		if math.Abs(dur-s.Duration) > cutTolerance {
			return fmt.Errorf("chunk %d is %.2fs long, planned %.2fs", i, dur, s.Duration)
		}
		sum, err := media.SHA256File(p.chunkPath(c))
		if err != nil {
			return err
		}
		c.DurationSec, c.SHA256 = dur, sum
		chunks = append(chunks, c)
		p.Logf("chunk %d: start %.1fs, %.1fs long", i, c.StartSec, c.DurationSec)
	}
	p.M.Chunks = chunks
	return nil
}
