package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Mode is what the agent does after a class. Only modeFull runs the Sarvam pipeline.
type Mode string

const (
	modeRecord Mode = "record"
	modeFull   Mode = "full"
)

// modeChoice is the mode in effect and where it came from, for the startup log.
type modeChoice struct {
	Mode   Mode
	Source string
}

func settingsPath() string {
	return filepath.Join(exeDir(), "settings.json")
}

// agentMode picks the mode from the --mode flag, else settings.json, else record.
// A wrong flag is an error, since someone typed it on purpose. Anything unreadable or unknown
// in the file means record, so Sarvam is never called by accident.
func agentMode(flagValue, settingsFile string) (modeChoice, error) {
	if flagValue != "" {
		return modeFromFlag(flagValue)
	}
	return modeFromSettings(settingsFile), nil
}

func modeFromFlag(flagValue string) (modeChoice, error) {
	mode := Mode(flagValue)
	if !isKnownMode(mode) {
		return modeChoice{}, fmt.Errorf("--mode must be %s or %s, not %q", modeRecord, modeFull, flagValue)
	}
	return modeChoice{Mode: mode, Source: "--mode flag"}, nil
}

func modeFromSettings(settingsFile string) modeChoice {
	content, err := os.ReadFile(settingsFile)
	if err != nil {
		return recordByDefault("no " + settingsFile)
	}

	var settings struct {
		Mode *string `json:"mode"`
	}
	if err := json.Unmarshal(content, &settings); err != nil {
		return recordByDefault(settingsFile + " is not valid JSON")
	}
	if settings.Mode == nil {
		return recordByDefault("no mode in " + settingsFile)
	}

	mode := Mode(*settings.Mode)
	if !isKnownMode(mode) {
		return recordByDefault(fmt.Sprintf("unknown mode %q in %s", *settings.Mode, settingsFile))
	}
	return modeChoice{Mode: mode, Source: settingsFile}
}

func recordByDefault(reason string) modeChoice {
	return modeChoice{Mode: modeRecord, Source: "default, " + reason}
}

func isKnownMode(mode Mode) bool {
	return mode == modeRecord || mode == modeFull
}
