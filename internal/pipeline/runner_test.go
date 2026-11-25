package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"classrec/internal/manifest"
)

func quiet(string, ...any) {}

func TestResumeSkipsDoneAndRetriesFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	calls := map[string]int{}
	failC := true
	count := func(name string) func(context.Context) error {
		return func(context.Context) error { calls[name]++; return nil }
	}
	stages := func() []Stage {
		return []Stage{
			{Name: "a", Run: count("a")},
			{Name: "b", Run: count("b")},
			{Name: "c", Run: func(context.Context) error {
				calls["c"]++
				if failC {
					return errors.New("boom")
				}
				return nil
			}},
		}
	}

	m1, _ := manifest.Load(path)
	if err := execute(context.Background(), m1, stages(), quiet); err == nil {
		t.Fatal("expected stage c to fail")
	}

	m2, err := manifest.Load(path) // a fresh process reading the saved manifest
	if err != nil {
		t.Fatal(err)
	}
	if s := m2.Stages["c"]; s.Status != manifest.Failed || s.Error != "boom" {
		t.Fatalf("c should be failed with its error, got %+v", s)
	}
	if !m2.IsDone("a") || !m2.IsDone("b") {
		t.Fatal("a and b should be done")
	}

	failC = false
	if err := execute(context.Background(), m2, stages(), quiet); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 || calls["b"] != 1 || calls["c"] != 2 {
		t.Fatalf("a and b must not rerun, c retried once: %v", calls)
	}
	if s := m2.Stages["c"]; s.Status != manifest.Done || s.Error != "" {
		t.Fatalf("c should now be done with the error cleared, got %+v", s)
	}
}

func TestFailedVerifyRerunsThatStageAndEverythingAfter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	calls := map[string]int{}
	outputsOK := true
	stages := func() []Stage {
		run := func(n string) func(context.Context) error {
			return func(context.Context) error { calls[n]++; return nil }
		}
		return []Stage{
			{Name: "a", Run: run("a")},
			{Name: "b", Verify: func() bool { return outputsOK }, Run: run("b")},
			{Name: "c", Run: run("c")},
		}
	}
	m, _ := manifest.Load(path)
	if err := execute(context.Background(), m, stages(), quiet); err != nil {
		t.Fatal(err)
	}

	outputsOK = false
	if err := execute(context.Background(), m, stages(), quiet); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 || calls["b"] != 2 || calls["c"] != 2 {
		t.Fatalf("expected a once, b and c twice: %v", calls)
	}
}
