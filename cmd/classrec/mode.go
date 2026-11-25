package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Modes of the agent. Only full runs the Sarvam pipeline after a class.
const (
	modeRecord = "record"
	modeFull   = "full"
)

func settingsPath() string { return filepath.Join(exeDir(), "settings.json") }

// agentMode picks the mode from the --mode flag, else settings.json, else record.
// Anything unreadable or unknown in the file means record, so Sarvam is never called by accident.
// source says where the mode came from, for the startup log.
func agentMode(flagVal, settingsFile string) (mode, source string, err error) {
	switch flagVal {
	case modeRecord, modeFull:
		return flagVal, "--mode flag", nil
	case "":
	default:
		return "", "", fmt.Errorf("--mode must be %s or %s, not %q", modeRecord, modeFull, flagVal)
	}
	b, err := os.ReadFile(settingsFile)
	if err != nil {
		return modeRecord, "default, no " + settingsFile, nil
	}
	var s struct{ Mode *string }
	if err := json.Unmarshal(b, &s); err != nil {
		return modeRecord, "default, " + settingsFile + " is not valid JSON", nil
	}
	switch {
	case s.Mode == nil:
		return modeRecord, "default, no mode in " + settingsFile, nil
	case *s.Mode == modeRecord || *s.Mode == modeFull:
		return *s.Mode, settingsFile, nil
	}
	return modeRecord, fmt.Sprintf("default, unknown mode %q in %s", *s.Mode, settingsFile), nil
}
