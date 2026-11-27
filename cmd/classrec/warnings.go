package main

import (
	"image"
	"image/color"
	"time"

	"classrec/internal/overlay"
)

// What the pill says when something needs the student's attention.
const (
	chargerText  = "Plug in charger"
	notHearing   = "No class audio" // followed by who to ask, see askFor
	noVideo      = "No class video"
	struggleText = "Recording is struggling"
	silenceLimit = 60 * time.Second
	// struggleFrames dropped in a minute, a tenth of 15 fps, is when the video visibly stutters.
	// Meet in Edge costs OBS about 2% of frames on this laptop, which no one sees (2026-11-27).
	struggleFrames = 90
)

// classSignals is what the agent sees about a class in progress at one moment.
type classSignals struct {
	now           time.Time
	audible       bool // the class app is louder than the calibrated silence threshold
	onBattery     bool
	skippedFrames int  // OBS's dropped output frames so far in this piece
	videoBlank    bool // OBS's picture of the class window has shown nothing for a minute
}

// warnings turns a class's signals into the pill's state. Not hearing the class is red, since
// a silent recording is worthless; no picture, running on battery or a stuttering video is amber.
type warnings struct {
	askFor      string // " — call <help contact>", or " — ask for help" with none
	lastAudible time.Time
	lastSkipped int       // OBS's count at the last update
	minuteStart time.Time // frames are counted a minute at a time
	minuteDrops int       // frames OBS dropped so far this minute
	struggling  bool      // last minute's drops reached struggleFrames
}

// blankVideo reports whether OBS's frame of the class window shows no picture: OBS could not render
// it (err), or every pixel is one colour. A real Zoom or Edge window always has toolbars and text,
// so a white slide in it is not blank; a capture that lost the window is.
func blankVideo(frame image.Image, err error) bool {
	if err != nil || frame == nil || frame.Bounds().Empty() {
		return true
	}
	b := frame.Bounds()
	first := frame.At(b.Min.X, b.Min.Y)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !sameColour(frame.At(x, y), first) {
				return false
			}
		}
	}
	return true
}

// sameColour allows the few levels of noise a video encoder adds to a flat colour.
func sameColour(a, b color.Color) bool {
	const tolerance = 8 << 8 // of 0xffff per channel
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	near := func(p, q uint32) bool { return max(p, q)-min(p, q) <= tolerance }
	return near(ar, br) && near(ag, bg) && near(ab, bb)
}

// newWarnings starts counting at start. helpContact is who to call about a problem only someone
// else can fix; with none the card says to ask for help.
func newWarnings(start time.Time, helpContact string) *warnings {
	return &warnings{askFor: askFor(helpContact), lastAudible: start, minuteStart: start}
}

// askFor is the end of a warning that needs someone else: " — call <help contact>", or " — ask for help".
func askFor(helpContact string) string {
	if helpContact == "" {
		return " — ask for help"
	}
	return " — call " + helpContact
}

// update returns the pill's level and text, and, when a minute has just been counted, how many
// frames OBS dropped in it (0 otherwise).
func (w *warnings) update(s classSignals) (level overlay.Level, text string, droppedLastMinute int) {
	if s.audible {
		w.lastAudible = s.now
	}
	if s.skippedFrames >= w.lastSkipped {
		w.minuteDrops += s.skippedFrames - w.lastSkipped
	} else {
		w.minuteDrops += s.skippedFrames // a new piece starts counting at 0 again
	}
	w.lastSkipped = s.skippedFrames
	if s.now.Sub(w.minuteStart) >= time.Minute {
		droppedLastMinute = w.minuteDrops
		w.struggling = w.minuteDrops >= struggleFrames
		w.minuteStart, w.minuteDrops = s.now, 0
	}

	switch {
	case s.now.Sub(w.lastAudible) >= silenceLimit:
		return overlay.Red, notHearing + w.askFor, droppedLastMinute
	case s.videoBlank:
		return overlay.Amber, noVideo + w.askFor, droppedLastMinute
	case s.onBattery:
		return overlay.Amber, chargerText, droppedLastMinute
	case w.struggling:
		return overlay.Amber, struggleText, droppedLastMinute
	}
	return overlay.Normal, "", droppedLastMinute
}
