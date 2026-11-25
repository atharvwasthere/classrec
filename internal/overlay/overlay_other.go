//go:build !windows

package overlay

import "errors"

// Overlay exists outside Windows only so callers build; New always fails.
type Overlay struct{}

func New(Config) (*Overlay, error)       { return nil, errors.New("the overlay needs Windows") }
func (*Overlay) Show()                   {}
func (*Overlay) Hide()                   {}
func (*Overlay) SetStatus(Level, string) {}
func (*Overlay) Close()                  {}
func DPIAwareness() bool                 { return false }
