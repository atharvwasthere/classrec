package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"classrec/internal/manifest"
)

type Config struct {
	Video string
	Out   string
	// ReuseAudio is an already-extracted audio file adopted instead of re-extracting, if its duration matches the video.
	ReuseAudio string
}

type Pipeline struct {
	Cfg  Config
	M    *manifest.Manifest
	Logf func(string, ...any)
}

func New(cfg Config, logf func(string, ...any)) (*Pipeline, error) {
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
	if m.SourceChanged(src) {
		logf("source video changed since the last run, starting over")
		m.Reset(src)
	}
	m.Source = src
	if err := m.Save(); err != nil {
		return nil, err
	}
	return &Pipeline{Cfg: cfg, M: m, Logf: logf}, nil
}

func (p *Pipeline) Stages() []Stage {
	return []Stage{
		{Name: "extract", Verify: p.extractVerify, Run: p.extract},
	}
}

func (p *Pipeline) Run(ctx context.Context) error {
	if err := Execute(ctx, p.M, p.Stages(), p.Logf); err != nil {
		return fmt.Errorf("%w (re-run to resume from this stage)", err)
	}
	return nil
}

func (p *Pipeline) path(parts ...string) string {
	return filepath.Join(append([]string{p.Cfg.Out}, parts...)...)
}
