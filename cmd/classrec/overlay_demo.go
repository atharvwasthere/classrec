package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"classrec/internal/marks"
	"classrec/internal/overlay"
)

// demoStates are the overlay's looks, as overlay-demo names them.
var demoStates = []struct {
	name  string
	level overlay.Level
	text  string
	saved bool
}{
	{"green", overlay.Normal, "", false},
	{"amber", overlay.Amber, chargerText, false},
	{"red", overlay.Red, notHearing, false},
	{"saved", overlay.Normal, "", true},
	{"offer", overlay.Offer, "Zoom class detected", false},
}

// runOverlayDemo writes a PNG of every state, in both themes, next to classrec, then shows the
// live overlay in one state so its look, dragging and focus behaviour can be checked.
func runOverlayDemo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("overlay-demo", flag.ContinueOnError)
	dir := fs.String("dir", filepath.Join(exeDir(), "overlay-demo"), "")
	state := fs.String("state", "green", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var level overlay.Level
	var text string
	found := false
	for _, s := range demoStates {
		for _, theme := range []string{"dark", "light"} {
			png := filepath.Join(exeDir(), fmt.Sprintf("overlay-%s-%s.png", s.name, theme))
			if err := overlay.RenderPNG(png, s.level, s.text, s.saved, theme == "dark", 1.5); err != nil {
				return err
			}
		}
		if s.name == *state {
			level, text, found = s.level, s.text, true
		}
	}
	if !found {
		return fmt.Errorf("--state must be green, amber, red, saved or offer, not %q", *state)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", filepath.Join(exeDir(), "overlay-*.png"))

	marksPath := filepath.Join(*dir, "marks.json")
	o, err := overlay.New(overlay.Config{
		SettingsPath: settingsPath(),
		OnMark:       func() error { return marks.AppendTime(marksPath, time.Now()) },
	})
	if err != nil {
		return err
	}
	defer o.Close()
	o.SetStatus(level, text)
	o.Show()
	fmt.Fprintf(os.Stderr, "per-monitor-v2 DPI aware: %v\nbutton clicks go to %s\nCtrl+C to close\n", overlay.DPIAwareness(), marksPath)
	<-ctx.Done()
	return nil
}
