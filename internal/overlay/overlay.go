// Package overlay is the always-on-top pill and "I didn't get this" button shown while a class records.
// It never takes focus, so Zoom's and Meet's keyboard shortcuts keep working after a click.
package overlay

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
