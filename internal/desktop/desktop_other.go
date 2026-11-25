//go:build !windows

package desktop

import (
	"errors"
	"time"

	"classrec/internal/detect"
)

// Snapshot sees nothing outside Windows; it exists so the agent builds and tests on any OS.
func Snapshot(now time.Time) detect.Sample { return detect.Sample{Time: now} }

// MicReadable fails outside Windows.
func MicReadable() error { return errors.New("the mic record is a Windows registry key") }

// PrimaryDisplay is unknown outside Windows.
func PrimaryDisplay() (width, height int) { return 0, 0 }
