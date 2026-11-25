package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procSendInput    = user32.NewProc("SendInput")
	procSetCursorPos = user32.NewProc("SetCursorPos")
	procGetCursorPos = user32.NewProc("GetCursorPos")
)

// INPUT holding a MOUSEINPUT, as laid out on 64-bit Windows.
type mouseInput struct {
	Type      uint32
	_         uint32
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	_         uint32
	ExtraInfo uintptr
}

// clickAt clicks the left mouse button at screen point x, y as a real click would, then puts the
// pointer back. It returns the foreground window before and after, which must not change.
func clickAt(x, y int32) (before, after windows.HWND) {
	var saved struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&saved)))
	defer procSetCursorPos.Call(uintptr(saved.X), uintptr(saved.Y))

	before = windows.GetForegroundWindow()
	procSetCursorPos.Call(uintptr(x), uintptr(y))
	const inputMouse, leftDown, leftUp = 0, 0x0002, 0x0004
	inputs := []mouseInput{{Type: inputMouse, Flags: leftDown}, {Type: inputMouse, Flags: leftUp}}
	procSendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	windows.SleepEx(500, false)
	return before, windows.GetForegroundWindow()
}
