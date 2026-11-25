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

// DPIAwareness is false outside Windows.
func DPIAwareness() bool { return false }
