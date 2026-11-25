// Package overlay is the always-on-top pill and "I didn't get this" button shown while a class records.
// It never takes focus, so Zoom's and Meet's keyboard shortcuts keep working after a click.
package overlay

import (
	"encoding/json"
	"os"

	"classrec/internal/fsx"
)

// Level is the pill's state.
type Level int

const (
	Normal Level = iota
	Amber
	Red
)

// Config wires the overlay to its surroundings.
type Config struct {
	SettingsPath string       // where the dragged position is remembered
	OnMark       func() error // called on the UI thread when the button is clicked
}

type point struct{ X, Y int32 }

// loadPosition returns the saved top-left in physical pixels; ok is false if there is none.
func loadPosition(path string) (point, bool) {
	var s struct{ Overlay *point }
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Overlay == nil {
		return point{}, false
	}
	return *s.Overlay, true
}

// savePosition rewrites only the "overlay" key; settings.json also holds the agent's mode.
func savePosition(path string, p point) error {
	s := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			return err // a hand-edited file with a typo is left alone, not overwritten
		}
	}
	pos, err := json.Marshal(p)
	if err != nil {
		return err
	}
	s["overlay"] = pos
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, b)
}
