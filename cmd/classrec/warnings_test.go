package main

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"classrec/internal/overlay"
)

// Promise: the pill turns red after a minute of not hearing the class, amber when OBS records no
// picture, on battery or while OBS drops frames, and back to normal when things recover.
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
		{classSignals{now: at(190), audible: true, videoBlank: true, onBattery: true}, overlay.Amber, noVideo},
		{classSignals{now: at(200), audible: true}, overlay.Normal, ""},
	} {
		level, text, _ := w.update(step.s)
		if level != step.wantLevel || text != step.wantText {
			t.Errorf("at %s: got %v %q, want %v %q", step.s.now.Sub(start), level, text, step.wantLevel, step.wantText)
		}
	}
}

// Promise: a frame with no picture counts as blank; a real window, even one showing a white slide, does not.
func TestBlankVideo(t *testing.T) {
	flat := func(c color.Color) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 160, 90))
		draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
		return img
	}
	black, white := flat(color.Black), flat(color.White)
	noisyBlack := flat(color.Black)
	noisyBlack.Set(5, 5, color.RGBA{6, 4, 7, 255}) // encoder noise
	whiteSlide := flat(color.White)                // a slide filling the window, under Zoom's toolbar
	draw.Draw(whiteSlide, image.Rect(0, 80, 160, 90), image.NewUniform(color.RGBA{30, 30, 30, 255}), image.Point{}, draw.Src)
	for _, tt := range []struct {
		name  string
		frame image.Image
		err   error
		want  bool
	}{
		{"obs cannot render the window", nil, errors.New("code 702 Failed to render screenshot"), true},
		{"black", black, nil, true},
		{"white", white, nil, true},
		{"black with encoder noise", noisyBlack, nil, true},
		{"white slide in a window with a toolbar", whiteSlide, nil, false},
	} {
		if got := blankVideo(tt.frame, tt.err); got != tt.want {
			t.Errorf("%s: blank %v, want %v", tt.name, got, tt.want)
		}
	}
}
