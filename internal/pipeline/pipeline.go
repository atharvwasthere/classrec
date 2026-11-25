package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"classrec/internal/manifest"
	"classrec/internal/sarvam"
)

// Config is one run of the pipeline over one video.
type Config struct {
	Video string
	Out   string
	// ReuseAudio is an already-extracted audio file adopted instead of re-extracting, if its duration matches the video.
	ReuseAudio string
	// MarksPath is an optional JSON array of seconds; each gets a transcript window in marks.md.
	MarksPath string
	// Date (YYYY-MM-DD) overrides the recording date; otherwise it comes from the file name or the container.
	Date string
	// StopAfter ends the run after the named stage, e.g. "chunk" for an offline dry run.
	StopAfter string

	ChunkMaxSec      float64
	SilenceWindowSec float64
	SilenceNoiseDB   float64
	SilenceMinSec    float64

	Params sarvam.JobParams
	// CacheDir holds raw Sarvam responses keyed by chunk audio hash, shared across output dirs.
	CacheDir string
}

func (c *Config) applyDefaults() {
	if c.ChunkMaxSec == 0 {
		c.ChunkMaxSec = 3600
	}
	if c.SilenceWindowSec == 0 {
		c.SilenceWindowSec = 120
	}
	if c.SilenceNoiseDB == 0 {
		c.SilenceNoiseDB = -35
	}
	if c.SilenceMinSec == 0 {
		c.SilenceMinSec = 0.4
	}
}

// Pipeline turns a video into a transcript, one resumable stage at a time.
type Pipeline struct {
	cfg   Config
	m     *manifest.Manifest
	batch Batcher
	logf  func(string, ...any)
	date  string // resolved recording date, set by Run
}

// New prepares a run. batch may be nil when every transcript is already cached.
func New(cfg Config, batch Batcher, logf func(string, ...any)) (*Pipeline, error) {
	cfg.applyDefaults()
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return nil, err
	}
	m, err := manifest.Load(filepath.Join(cfg.Out, "manifest.json"))
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(cfg.Video)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(cfg.Video)
	if err != nil {
		return nil, err
	}
	src := manifest.Source{Path: abs, Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano()}
	if m.Adopt(src) {
		logf("source video changed since the last run, starting over")
	}
	if err := m.Save(); err != nil {
		return nil, err
	}
	return &Pipeline{cfg: cfg, m: m, batch: batch, logf: logf}, nil
}

func (p *Pipeline) stages() []Stage {
	stages := []Stage{
		{Name: "extract", Verify: p.extractVerify, Run: p.extract},
		{Name: "chunk", Verify: p.chunkVerify, Run: p.chunkStage},
		{Name: "transcribe", Verify: p.transcribeVerify, Run: p.transcribe},
		{Name: "merge", Verify: p.mergeVerify, Run: p.merge},
		{Name: "write", Verify: p.writeVerify, Run: p.write},
		{Name: "cost", Verify: p.costVerify, Run: p.cost},
	}
	if p.cfg.MarksPath != "" {
		stages = append(stages, Stage{Name: "marks", Verify: func() bool { return false }, Run: p.marks})
	}
	for i, s := range stages {
		if s.Name == p.cfg.StopAfter {
			return stages[:i+1]
		}
	}
	return stages
}

// Run runs every stage not yet done, stopping after Config.StopAfter if set.
func (p *Pipeline) Run(ctx context.Context) error {
	stages := p.stages()
	if slices.ContainsFunc(stages, func(s Stage) bool { return s.Name == "write" }) {
		// fail before anything is sent to Sarvam, not after paying for it
		var err error
		if p.date, err = p.resolveDate(ctx); err != nil {
			return err
		}
	}
	if err := execute(ctx, p.m, stages, p.logf); err != nil {
		return fmt.Errorf("%w (re-run to resume from this stage)", err)
	}
	return nil
}

func (p *Pipeline) path(parts ...string) string {
	return filepath.Join(append([]string{p.cfg.Out}, parts...)...)
}
