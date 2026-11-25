package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"classrec/internal/desktop"
	"classrec/internal/obs"
	"classrec/internal/recorder"
)

// runInstall writes the classrec OBS profile. OBS must be closed: it lists profiles when it starts
// and rewrites the current profile's files when it exits.
func runInstall(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	root := fs.String("root", defaultRoot, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if recorder.OBSRunning() {
		return errors.New("close OBS first: it rewrites its profile files when it exits")
	}
	w, h := desktop.PrimaryDisplay()
	if w <= 0 || h <= 0 {
		return errors.New("could not read the display size")
	}
	outW, outH := outputSize(w, h)
	dir, err := obs.WriteProfile(obsProfilesDir(), obs.ProfileSettings{
		Name: recorder.ProfileName, RecordDir: filepath.Join(*root, "_staging"), Encoder: recorder.Encoder,
		BaseWidth: w, BaseHeight: h, OutputWidth: outW, OutputHeight: outH,
		FPS: recorder.FPS, BitrateKbps: recorder.BitrateKbps, KeyframeSecs: recorder.KeyframeSecs,
	})
	if err != nil {
		return fmt.Errorf("writing the obs profile: %w", err)
	}
	fmt.Fprintf(os.Stderr, "obs profile %q written to %s (canvas %dx%d, recording %dx%d at %d fps)\n",
		recorder.ProfileName, dir, w, h, outW, outH, recorder.FPS)
	return nil
}

// outputSize is the recorded size for a canvas: 1280x720 for any 16:9 screen, else 720 high at the
// screen's own shape, so the picture is scaled but never stretched.
func outputSize(baseW, baseH int) (w, h int) {
	aspect := float64(baseW) / float64(baseH)
	if math.Abs(aspect-16.0/9.0)/(16.0/9.0) < 0.02 {
		return recorder.OutputWidth, recorder.OutputHeight
	}
	return int(math.Round(aspect*recorder.OutputHeight/2)) * 2, recorder.OutputHeight
}
