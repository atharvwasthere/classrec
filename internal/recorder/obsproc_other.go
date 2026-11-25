//go:build !windows

package recorder

import "errors"

// DefaultOBSBin has no meaning off Windows.
const DefaultOBSBin = ""

// OBSRunning cannot tell off Windows, and says yes so nothing tries to launch it.
func OBSRunning() bool { return true }

// LauncherFor cannot start OBS off Windows.
func LauncherFor(string) func() error {
	return func() error { return errors.New("starting obs is only supported on windows") }
}
