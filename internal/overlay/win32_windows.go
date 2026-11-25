package overlay

import "golang.org/x/sys/windows"

// Win32 functions the overlay calls that golang.org/x/sys/windows does not wrap.
var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

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
	procSetLayeredAttrs     = user32.NewProc("SetLayeredWindowAttributes")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procSetCapture          = user32.NewProc("SetCapture")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetTimer            = user32.NewProc("SetTimer")
	procKillTimer           = user32.NewProc("KillTimer")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procFillRect            = user32.NewProc("FillRect")
	procDrawTextW           = user32.NewProc("DrawTextW")
	procGetThreadDpiContext = user32.NewProc("GetThreadDpiAwarenessContext")
	procAreDpiContextsEqual = user32.NewProc("AreDpiAwarenessContextsEqual")

	procCreateFontW         = gdi32.NewProc("CreateFontW")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procCreateSolidBrush    = gdi32.NewProc("CreateSolidBrush")
	procCreateRoundRectRgn  = gdi32.NewProc("CreateRoundRectRgn")
	procCreateEllipticRgn   = gdi32.NewProc("CreateEllipticRgn")
	procFillRgn             = gdi32.NewProc("FillRgn")
	procSetTextColor        = gdi32.NewProc("SetTextColor")
	procSetBkMode           = gdi32.NewProc("SetBkMode")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	procBitBlt              = gdi32.NewProc("BitBlt")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
)

// Window messages.
const (
	wmDestroy        = 0x0002
	wmPaint          = 0x000F
	wmEraseBkgnd     = 0x0014
	wmMouseActivate  = 0x0021
	wmTimer          = 0x0113
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmCaptureChanged = 0x0215
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
	wsExLayered    = 0x00080000
	wsExNoActivate = 0x08000000

	lwaColorKey = 1
	lwaAlpha    = 2

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

	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the handle value -4.
	dpiAwarenessPerMonitorV2 = ^uintptr(3)
)

// Text and drawing flags.
const (
	dtCenter     = 0x0001
	dtVCenter    = 0x0004
	dtWordBreak  = 0x0010
	dtSingleLine = 0x0020
	dtCalcRect   = 0x0400
	dtNoPrefix   = 0x0800

	bkModeTransparent = 1
	srcCopy           = 0x00CC0020

	fontWeightSemibold = 600
	fontWeightBold     = 700
	defaultCharset     = 1
	clearTypeQuality   = 5
)

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

func (r rect) width() int32  { return r.Right - r.Left }
func (r rect) height() int32 { return r.Bottom - r.Top }

func (r rect) contains(p point) bool {
	insideX := p.X >= r.Left && p.X < r.Right
	insideY := p.Y >= r.Top && p.Y < r.Bottom
	return insideX && insideY
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

// pointFromLParam unpacks the signed client coordinates of a mouse message.
func pointFromLParam(lParam uintptr) point {
	x := int16(lParam & 0xFFFF)
	y := int16((lParam >> 16) & 0xFFFF)
	return point{X: int32(x), Y: int32(y)}
}
