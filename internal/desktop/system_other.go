//go:build !windows

package desktop

import "time"

// OnBattery is unknown outside Windows.
func OnBattery() bool { return false }

// KeepAwake does nothing outside Windows.
func KeepAwake(bool) bool { return false }

// FreeBytes is unknown outside Windows.
func FreeBytes(string) (uint64, error) { return 0, nil }

// SingleInstance does nothing outside Windows.
func SingleInstance(string, time.Duration) error { return nil }

// HideConsole does nothing outside Windows.
func HideConsole() {}
