// Package desktop reads the windows and microphone state that class detection runs on.
package desktop

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"classrec/internal/detect"
)

const micKey = `Software\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\microphone\NonPackaged`

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetWindowTextW   = user32.NewProc("GetWindowTextW")
	procGetWindowTextLen = user32.NewProc("GetWindowTextLengthW")
	procIsIconic         = user32.NewProc("IsIconic")

	// One callback for the process's life: Windows callbacks are never freed.
	enumCallback = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		enumFound = append(enumFound, hwnd)
		return 1
	})
	enumMu    sync.Mutex
	enumFound []windows.HWND
)

// Snapshot observes all titled top-level windows and the mic entries of interest.
func Snapshot(now time.Time) detect.Sample {
	return detect.Sample{Time: now, Windows: windowsNow(), Mic: micNow()}
}

func windowsNow() []detect.Window {
	enumMu.Lock()
	enumFound = enumFound[:0]
	windows.EnumWindows(enumCallback, nil)
	hwnds := append([]windows.HWND(nil), enumFound...)
	enumMu.Unlock()

	fg := windows.GetForegroundWindow()
	exes := map[uint32]string{}
	var out []detect.Window
	for _, h := range hwnds {
		title := windowText(h)
		if title == "" {
			continue
		}
		var pid uint32
		windows.GetWindowThreadProcessId(h, &pid)
		exe, ok := exes[pid]
		if !ok {
			exe = exePath(pid)
			exes[pid] = exe
		}
		visible := windows.IsWindowVisible(h)
		// Hidden windows of other apps are noise; Zoom and Edge hidden windows may still tell us something.
		if !visible && !isClassApp(exe) {
			continue
		}
		iconic, _, _ := procIsIconic.Call(uintptr(h))
		var cloaked uint32
		windows.DwmGetWindowAttribute(h, windows.DWMWA_CLOAKED, unsafe.Pointer(&cloaked), 4)
		out = append(out, detect.Window{
			Exe: exe, Title: title, Class: className(h),
			Visible: visible, Minimized: iconic != 0, Cloaked: cloaked != 0, Foreground: h == fg,
		})
	}
	return out
}

func isClassApp(exe string) bool {
	base := strings.ToLower(exe[strings.LastIndexAny(exe, `\/#`)+1:])
	return base == "zoom.exe" || base == "msedge.exe"
}

func windowText(h windows.HWND) string {
	n, _, _ := procGetWindowTextLen.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

func className(h windows.HWND) string {
	buf := make([]uint16, 256)
	n, err := windows.GetClassName(h, &buf[0], int32(len(buf)))
	if err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func exePath(pid uint32) string {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(p)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// micNow returns the Zoom and Edge entries, plus any other app that has the mic open.
func micNow() []detect.MicUse {
	k, err := registry.OpenKey(registry.CURRENT_USER, micKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var out []detect.MicUse
	for _, name := range names {
		sk, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		start, _, _ := sk.GetIntegerValue("LastUsedTimeStart")
		stop, _, _ := sk.GetIntegerValue("LastUsedTimeStop")
		sk.Close()
		m := detect.MicUse{Path: name, Start: int64(start), Stop: int64(stop)}
		if m.InUse() || isClassApp(name) {
			out = append(out, m)
		}
	}
	return out
}
