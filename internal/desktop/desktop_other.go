//go:build !windows

package desktop

import (
	"time"

	"classrec/internal/detect"
)

// Snapshot sees nothing outside Windows; it exists so the agent builds and tests on any OS.
func Snapshot(now time.Time) detect.Sample { return detect.Sample{Time: now} }

// PrimaryDisplay is unknown outside Windows.
func PrimaryDisplay() (width, height int) { return 0, 0 }
