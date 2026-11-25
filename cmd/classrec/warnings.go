package main

import (
	"time"

	"classrec/internal/overlay"
)

// What the pill says when something needs her attention.
const (
	chargerText   = "Plug in charger"
	notHearing    = "No class audio — call Adi"
	struggleText  = "Recording is struggling"
	silenceLimit  = 60 * time.Second
	struggleHolds = time.Minute // how long the amber stays after OBS last dropped frames
)

// classSignals is what the agent sees about a class in progress at one moment.
type classSignals struct {
	now           time.Time
	audible       bool // the class app is louder than the calibrated silence threshold
	onBattery     bool
	skippedFrames int // OBS's dropped output frames so far in this piece
}

// warnings turns a class's signals into the pill's state. Not hearing the class is red, since
// a silent recording is worthless; running on battery or OBS dropping frames is amber.
type warnings struct {
	lastAudible   time.Time
	lastSkipped   int
	lastStruggled time.Time
}

func newWarnings(start time.Time) *warnings { return &warnings{lastAudible: start} }

// update returns the pill's level and text, and whether OBS dropped frames since the last update.
func (w *warnings) update(s classSignals) (overlay.Level, string, bool) {
	if s.audible {
		w.lastAudible = s.now
	}
	dropped := s.skippedFrames > w.lastSkipped
	if dropped {
		w.lastStruggled = s.now
	}
	w.lastSkipped = s.skippedFrames // a new piece starts counting at 0 again

	switch {
	case s.now.Sub(w.lastAudible) >= silenceLimit:
		return overlay.Red, notHearing, dropped
	case s.onBattery:
		return overlay.Amber, chargerText, dropped
	case !w.lastStruggled.IsZero() && s.now.Sub(w.lastStruggled) < struggleHolds:
		return overlay.Amber, struggleText, dropped
	}
	return overlay.Normal, "", dropped
}
