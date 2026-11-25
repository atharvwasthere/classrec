//go:build !windows

package desktop

// OnBattery is unknown outside Windows.
func OnBattery() bool { return false }

// KeepAwake does nothing outside Windows.
func KeepAwake(bool) bool { return false }

// FreeBytes is unknown outside Windows.
func FreeBytes(string) (uint64, error) { return 0, nil }

// SingleInstance does nothing outside Windows.
func SingleInstance(string) error { return nil }
