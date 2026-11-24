package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"

	"classrec/internal/chunk"
	"classrec/internal/manifest"
	"classrec/internal/media"
)

// cutTolerance is how far a cut chunk's real length may differ from its plan.
const cutTolerance = 0.5

func (p *Pipeline) chunkPath(c manifest.Chunk) string { return p.path(c.File) }

// chunkValid is true when the file is the one the manifest recorded and ffprobe finds nothing wrong with it.
func (p *Pipeline) chunkValid(ctx context.Context, c manifest.Chunk) bool {
	sum, err := media.SHA256File(p.chunkPath(c))
	if err != nil || sum != c.SHA256 {
		return false
	}
	_, err = media.ProbeClean(ctx, p.chunkPath(c))
	return err == nil
}

func (p *Pipeline) chunkVerify() bool {
	if len(p.m.Chunks) == 0 {
		return false
	}
	for _, c := range p.m.Chunks {
		if !p.chunkValid(context.Background(), c) {
			return false
		}
	}
	return true
}

// reusable returns the previous chunk i when it covers the same span and is still valid. Keeping it
// keeps its hash, so a chunk that was already transcribed is not sent (and paid for) again.
func (p *Pipeline) reusable(ctx context.Context, prev []manifest.Chunk, i int, s chunk.Span) (manifest.Chunk, bool) {
	if i >= len(prev) {
		return manifest.Chunk{}, false
	}
	c := prev[i]
	ok := math.Abs(c.StartSec-s.Start) < 0.01 && math.Abs(c.DurationSec-s.Duration) <= cutTolerance && p.chunkValid(ctx, c)
	return c, ok
}

func (p *Pipeline) chunkStage(ctx context.Context) error {
	total := p.m.DurationSec
	var silences []media.Silence
	if total > p.cfg.ChunkMaxSec {
		p.logf("detecting silences to find cut points")
		var err error
		silences, err = media.DetectSilences(ctx, p.audioFile(), p.cfg.SilenceNoiseDB, p.cfg.SilenceMinSec)
		if err != nil {
			return err
		}
	}
	spans := chunk.Plan(total, p.cfg.ChunkMaxSec, p.cfg.SilenceWindowSec, silences)

	if err := os.MkdirAll(p.path("chunks"), 0o755); err != nil {
		return err
	}
	prev := p.m.Chunks
	chunks := make([]manifest.Chunk, 0, len(spans))
	for i, s := range spans {
		if c, ok := p.reusable(ctx, prev, i, s); ok {
			p.logf("chunk %d: keeping existing valid file", i)
			chunks = append(chunks, c)
			continue
		}
		c := manifest.Chunk{Index: i, File: path.Join("chunks", fmt.Sprintf("chunk_%03d.ogg", i)), StartSec: s.Start}
		if err := media.Cut(ctx, p.audioFile(), p.chunkPath(c), s.Start, s.Duration); err != nil {
			return err
		}
		dur, err := media.ProbeClean(ctx, p.chunkPath(c))
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
		p.logf("chunk %d: start %.1fs, %.1fs long", i, c.StartSec, c.DurationSec)
	}
	p.m.Chunks = chunks
	return p.removeStaleChunks()
}

func (p *Pipeline) removeStaleChunks() error {
	keep := map[string]bool{}
	for _, c := range p.m.Chunks {
		keep[filepath.Base(c.File)] = true
	}
	entries, err := os.ReadDir(p.path("chunks"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			if err := os.Remove(p.path("chunks", e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
