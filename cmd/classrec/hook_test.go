package main

import (
	"context"
	"testing"
	"time"

	"classrec/internal/recorder"
)

// Promise: mode record, the default, never calls Sarvam; mode full transcribes every filed class.
func TestOnlyFullModeTranscribes(t *testing.T) {
	filed := recorder.Class{Dir: t.TempDir(), Files: []string{"part-01.mkv"}}
	for _, tt := range []struct {
		mode  Mode
		class recorder.Class
		want  bool
	}{
		{modeRecord, filed, false},
		{modeFull, filed, true},
		{modeFull, recorder.Class{Dir: filed.Dir, Pending: 1}, false}, // a piece is still in staging
	} {
		called := make(chan string, 1)
		process := func(_ context.Context, dir string, _ func(string, ...any)) error { called <- dir; return nil }
		afterClass(context.Background(), tt.mode, tt.class, t.Logf, process)
		select {
		case <-called:
			if !tt.want {
				t.Errorf("mode %s with %+v started the pipeline", tt.mode, tt.class)
			}
		case <-time.After(200 * time.Millisecond):
			if tt.want {
				t.Errorf("mode %s with %+v did not start the pipeline", tt.mode, tt.class)
			}
		}
	}
}
