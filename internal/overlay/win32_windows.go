package overlay

import "golang.org/x/sys/windows"

// Win32 functions the overlay calls that golang.org/x/sys/windows does not wrap.
var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procSetCapture          = user32.NewProc("SetCapture")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procTrackMouseEvent     = user32.NewProc("TrackMouseEvent")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetThreadDpiContext = user32.NewProc("GetThreadDpiAwarenessContext")
	procAreDpiContextsEqual = user32.NewProc("AreDpiAwarenessContextsEqual")

	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procDeleteDC           = gdi32.NewProc("DeleteDC")

	procDwmSetWindowAttr = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrame   = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

// Window messages.
const (
	wmDestroy        = 0x0002
	wmPaint          = 0x000F
	wmEraseBkgnd     = 0x0014
	wmSettingChange  = 0x001A
	wmMouseActivate  = 0x0021
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmCaptureChanged = 0x0215
	wmMouseLeave     = 0x02A3
	wmDpiChanged     = 0x02E0
	wmApp            = 0x8000
)

// Messages other goroutines post to the UI thread.
const (
	wmAppShow    = wmApp + 1
	wmAppHide    = wmApp + 2
	wmAppRefresh = wmApp + 3
	wmAppClose   = wmApp + 4
)

// Window styles and flags.
const (
	maNoActivate = 3

	wsPopup        = 0x80000000
	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExNoActivate = 0x08000000

	swHide           = 0
	swShowNoActivate = 4

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
	hwndTopmost   = ^uintptr(0) // (HWND)-1

	monitorDefaultToNull    = 0
	monitorDefaultToNearest = 2

	idcArrow = 32512
	tmeLeave = 2
	srcCopy  = 0x00CC0020

	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the handle value -4.
	dpiAwarenessPerMonitorV2 = ^uintptr(3)

	// DWM window attributes (Windows 11; older Windows refuses them and the card stays plain).
	dwmwaUseImmersiveDarkMode = 20
	dwmwaCornerPreference     = 33
	dwmwaSystemBackdropType   = 38
	dwmwcpRound               = 2
	dwmsbtTransientWindow     = 3 // acrylic
)

type rect struct {
	Left, Top, Right, Bottom int32
}

func (r rect) width() int32  { return r.Right - r.Left }
func (r rect) height() int32 { return r.Bottom - r.Top }

func (r rect) contains(p point) bool {
	return p.X >= r.Left && p.X < r.Right && p.Y >= r.Top && p.Y < r.Bottom
}

// WNDCLASSEXW
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

// MSG
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   point
	Private uint32
}

// PAINTSTRUCT
type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

// MONITORINFO
type monitorInfo struct {
	Size     uint32
	Monitor  rect
	WorkArea rect
	Flags    uint32
}

// TRACKMOUSEEVENT
type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	Window    uintptr
	HoverTime uint32
}

// pointFromLParam unpacks the signed client coordinates of a mouse message.
func pointFromLParam(lParam uintptr) point {
	return point{X: int32(int16(lParam & 0xFFFF)), Y: int32(int16((lParam >> 16) & 0xFFFF))}
}
