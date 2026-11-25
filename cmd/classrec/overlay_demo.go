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

// runOverlayDemo shows the overlay as if a class were recording, so its look, dragging and focus behaviour can be checked.
func runOverlayDemo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("overlay-demo", flag.ContinueOnError)
	dir := fs.String("dir", filepath.Join(exeDir(), "overlay-demo"), "")
	status := fs.String("status", "normal", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	level, text := overlay.Normal, ""
	switch *status {
	case "normal":
	case "amber":
		level, text = overlay.Amber, "Plug in charger"
	case "red":
		level, text = overlay.Red, "Not hearing the class - call Adi"
	default:
		return fmt.Errorf("--status must be normal, amber or red, not %q", *status)
	}

	marksPath := filepath.Join(*dir, "marks.json")
	start := time.Now()
	o, err := overlay.New(overlay.Config{
		SettingsPath: settingsPath(),
		OnMark:       func() error { return marks.Append(marksPath, time.Since(start).Seconds()) },
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
