//go:build !windows

package main

// clickAt cannot click outside Windows; it reports no foreground change.
func clickAt(int32, int32) (before, after uintptr) { return 0, 0 }
