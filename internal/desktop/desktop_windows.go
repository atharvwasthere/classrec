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
)

// EnumWindows reports each window through a callback. Windows never frees a callback, so there is
// exactly one for the process, and it appends into enumFound under enumMu.
var (
	enumMu       sync.Mutex
	enumFound    []windows.HWND
	enumCallback = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		enumFound = append(enumFound, hwnd)
		const continueEnumerating = 1
		return continueEnumerating
	})
)

var procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

// PrimaryDisplay is the primary display's size in true pixels. It relies on classrec's manifest
// making the process DPI aware; without it Windows would report the scaled size (1536x864 for a
// 1920x1080 screen at 125%). It returns 0, 0 if Windows will not say.
func PrimaryDisplay() (width, height int) {
	const smCXScreen, smCYScreen = 0, 1
	w, _, _ := procGetSystemMetrics.Call(smCXScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYScreen)
	return int(w), int(h)
}

// MicReadable checks that Windows' record of which apps use the mic can be read; detection needs it.
func MicReadable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, micKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return err
	}
	defer key.Close()
	_, err = key.ReadSubKeyNames(-1)
	return err
}

// Snapshot observes all titled top-level windows and the mic entries of interest.
func Snapshot(now time.Time) detect.Sample {
	return detect.Sample{
		Time:    now,
		Windows: describeWindows(listTopLevelWindows()),
		Mic:     micNow(),
	}
}

func listTopLevelWindows() []windows.HWND {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = enumFound[:0]
	windows.EnumWindows(enumCallback, nil)
	found := make([]windows.HWND, len(enumFound))
	copy(found, enumFound)
	return found
}

func describeWindows(hwnds []windows.HWND) []detect.Window {
	foreground := windows.GetForegroundWindow()
	exeByProcess := map[uint32]string{}
	var described []detect.Window
	for _, hwnd := range hwnds {
		title := windowText(hwnd)
		if title == "" {
			continue
		}
		exe := ownerExe(hwnd, exeByProcess)
		visible := windows.IsWindowVisible(hwnd)
		if !shouldLog(visible, exe) {
			continue
		}
		described = append(described, detect.Window{
			Exe:        exe,
			Title:      title,
			Class:      className(hwnd),
			Visible:    visible,
			Minimized:  isMinimized(hwnd),
			Cloaked:    isCloaked(hwnd),
			Foreground: hwnd == foreground,
		})
	}
	return described
}

// shouldLog drops hidden windows of other apps, which are noise; hidden Zoom and Edge windows may still tell us something.
func shouldLog(visible bool, exe string) bool {
	if visible {
		return true
	}
	return isClassApp(exe)
}

// ownerExe returns the full exe path of the window's process, looking each process up once per snapshot.
func ownerExe(hwnd windows.HWND, exeByProcess map[uint32]string) string {
	var pid uint32
	windows.GetWindowThreadProcessId(hwnd, &pid)
	exe, known := exeByProcess[pid]
	if !known {
		exe = exePath(pid)
		exeByProcess[pid] = exe
	}
	return exe
}

// isClassApp reports whether a path (with \ or / separators, or # as in the registry) is Zoom or Edge.
func isClassApp(path string) bool {
	lastSeparator := strings.LastIndexAny(path, `\/#`)
	fileName := strings.ToLower(path[lastSeparator+1:])
	return fileName == "zoom.exe" || fileName == "msedge.exe"
}

func windowText(hwnd windows.HWND) string {
	length, _, _ := procGetWindowTextLen.Call(uintptr(hwnd))
	if length == 0 {
		return ""
	}
	buf := make([]uint16, length+1)
	procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

func className(hwnd windows.HWND) string {
	buf := make([]uint16, 256)
	copied, err := windows.GetClassName(hwnd, &buf[0], int32(len(buf)))
	if err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:copied])
}

func isMinimized(hwnd windows.HWND) bool {
	iconic, _, _ := procIsIconic.Call(uintptr(hwnd))
	return iconic != 0
}

// isCloaked is true for windows Windows hides without un-showing them, e.g. on another virtual desktop.
func isCloaked(hwnd windows.HWND) bool {
	var cloaked uint32
	windows.DwmGetWindowAttribute(hwnd, windows.DWMWA_CLOAKED, unsafe.Pointer(&cloaked), uint32(unsafe.Sizeof(cloaked)))
	return cloaked != 0
}

func exePath(pid uint32) string {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(process, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// micNow returns the Zoom and Edge entries, plus any other app that has the mic open.
func micNow() []detect.MicUse {
	appsKey, err := registry.OpenKey(registry.CURRENT_USER, micKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer appsKey.Close()
	appNames, err := appsKey.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var entries []detect.MicUse
	for _, appName := range appNames {
		entry, ok := readMicEntry(appsKey, appName)
		if !ok {
			continue
		}
		if entry.InUse() || isClassApp(appName) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func readMicEntry(appsKey registry.Key, appName string) (detect.MicUse, bool) {
	appKey, err := registry.OpenKey(appsKey, appName, registry.QUERY_VALUE)
	if err != nil {
		return detect.MicUse{}, false
	}
	defer appKey.Close()
	start, _, _ := appKey.GetIntegerValue("LastUsedTimeStart")
	stop, _, _ := appKey.GetIntegerValue("LastUsedTimeStop")
	return detect.MicUse{Path: appName, Start: int64(start), Stop: int64(stop)}, true
}
