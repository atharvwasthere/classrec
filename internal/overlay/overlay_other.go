//go:build !windows

package overlay

import "errors"

// Overlay exists outside Windows only so callers build; New always fails.
type Overlay struct{}

// New fails: the overlay needs Windows.
func New(Config) (*Overlay, error) { return nil, errors.New("the overlay needs Windows") }

// Show does nothing outside Windows.
func (*Overlay) Show() {}

// Hide does nothing outside Windows.
func (*Overlay) Hide() {}

// SetStatus does nothing outside Windows.
func (*Overlay) SetStatus(Level, string) {}

// Close does nothing outside Windows.
func (*Overlay) Close() {}

// RenderPNG fails: drawing needs Windows' GDI+.
func RenderPNG(string, Level, string, bool, float32) error {
	return errors.New("the overlay needs Windows")
}

// ButtonCenter is nowhere outside Windows.
func (*Overlay) ButtonCenter() (x, y int32) { return 0, 0 }

// DPIAwareness is false outside Windows.
func DPIAwareness() bool { return false }
