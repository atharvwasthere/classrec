package main

import (
	"testing"
	"time"

	"classrec/internal/overlay"
)

// Promise: the pill turns red after a minute of not hearing the class, amber on battery or while
// OBS drops frames, and back to normal when things recover.
func TestPillWarnings(t *testing.T) {
	start := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return start.Add(time.Duration(sec) * time.Second) }
	w := newWarnings(start)
	for _, step := range []struct {
		s         classSignals
		wantLevel overlay.Level
		wantText  string
	}{
		{classSignals{now: at(10), audible: true}, overlay.Normal, ""},
		{classSignals{now: at(20), audible: true, onBattery: true}, overlay.Amber, chargerText},
		{classSignals{now: at(30), audible: true}, overlay.Normal, ""},
		{classSignals{now: at(60)}, overlay.Normal, ""},                       // 30 s quiet: a pause, not a problem
		{classSignals{now: at(90), onBattery: true}, overlay.Red, notHearing}, // 60 s quiet beats the charger
		{classSignals{now: at(100), audible: true, skippedFrames: 40}, overlay.Amber, struggleText},
		{classSignals{now: at(130), audible: true, skippedFrames: 40}, overlay.Amber, struggleText}, // still within the minute
		{classSignals{now: at(170), audible: true, skippedFrames: 40}, overlay.Normal, ""},
		{classSignals{now: at(180), audible: true, skippedFrames: 3}, overlay.Normal, ""}, // new piece, counter restarted
	} {
		level, text, _ := w.update(step.s)
		if level != step.wantLevel || text != step.wantText {
			t.Errorf("at %s: got %v %q, want %v %q", step.s.now.Sub(start), level, text, step.wantLevel, step.wantText)
		}
	}
}
