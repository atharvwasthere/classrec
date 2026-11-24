package pipeline

import (
	"context"
	"fmt"

	"classrec/internal/manifest"
)

type Stage struct {
	Name string
	// Verify confirms a stage the manifest calls done still has its outputs. Nil means trust the manifest.
	Verify func() bool
	Run    func(ctx context.Context) error
}

// Execute runs stages in order, skipping completed ones. Once a stage actually
// runs, every later stage reruns too, since its inputs may have changed.
func Execute(ctx context.Context, m *manifest.Manifest, stages []Stage, logf func(string, ...any)) error {
	rerun := false
	for _, s := range stages {
		if !rerun && m.IsDone(s.Name) && (s.Verify == nil || s.Verify()) {
			logf("[%s] done, skipping", s.Name)
			continue
		}
		rerun = true
		logf("[%s] running", s.Name)
		if err := m.Begin(s.Name); err != nil {
			return err
		}
		runErr := s.Run(ctx)
		if err := m.Finish(s.Name, runErr); err != nil {
			return err
		}
		if runErr != nil {
			return fmt.Errorf("stage %s: %w", s.Name, runErr)
		}
	}
	return nil
}
