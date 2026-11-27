package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"classrec/internal/fsx"
	"classrec/internal/media"
)

// defaultSilenceDB is the class-audio level below which the class counts as silent, until
// `classrec calibrate` has measured real classes.
const defaultSilenceDB = -50

// settings are the choices kept in settings.json next to classrec. Every key is optional; the
// overlay's position and the mode live there too.
type settings struct {
	SilenceDB     *float64 `json:"silence_db"`     // set by classrec calibrate
	RecordingsDir string   `json:"recordings_dir"` // where classes are filed
	HelpContact   string   `json:"help_contact"`   // who the card says to call when only someone else can fix it
}

// readSettings reads settings.json; a missing or unreadable file means every default.
func readSettings() settings {
	var s settings
	if content, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(content, &s) // a broken file means the defaults, as if it were missing
	}
	return s
}

// silenceDB is the calibrated threshold from settings.json, else the default.
func silenceDB() float64 {
	if s := readSettings(); s.SilenceDB != nil {
		return *s.SilenceDB
	}
	return defaultSilenceDB
}

// recordingsDir is where classes are filed: settings.json's recordings_dir, else the Videos folder.
func recordingsDir() string {
	if s := readSettings(); s.RecordingsDir != "" {
		return s.RecordingsDir
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "Videos", "Class Recordings")
}

// setSetting writes one key of settings.json and keeps the others (mode, overlay position).
// A file it cannot read is left alone rather than overwritten.
func setSetting(key string, value any) error {
	s := map[string]json.RawMessage{}
	if content, err := os.ReadFile(settingsPath()); err == nil {
		if err := json.Unmarshal(content, &s); err != nil {
			return fmt.Errorf("%s is not valid JSON; fix it by hand: %w", settingsPath(), err)
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s[key] = encoded
	content, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(settingsPath(), content)
}

// runCalibrate sets the silence threshold from real class recordings: 15 dB below the quietest
// recording's mean level, kept between -70 and -35 dBFS.
func runCalibrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: classrec calibrate <recording.mkv> [more recordings]")
	}
	quietest := 0.0
	for _, path := range fs.Args() {
		mean, err := media.MeanVolume(ctx, path)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		fmt.Fprintf(os.Stderr, "%s: mean %.1f dB\n", filepath.Base(path), mean)
		quietest = min(quietest, mean)
	}
	threshold := max(-70, min(-35, quietest-15))
	if err := setSetting("silence_db", threshold); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "silence threshold set to %.1f dB in %s\n", threshold, settingsPath())
	return nil
}
