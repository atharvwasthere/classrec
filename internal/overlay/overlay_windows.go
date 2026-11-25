package overlay

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"classrec/internal/fsx"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	gdi32                   = windows.NewLazySystemDLL("gdi32.dll")
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

const (
	wmDestroy         = 0x0002
	wmPaint           = 0x000F
	wmEraseBkgnd      = 0x0014
	wmMouseActivate   = 0x0021
	wmTimer           = 0x0113
	wmMouseMove       = 0x0200
	wmLButtonDown     = 0x0201
	wmLButtonUp       = 0x0202
	wmCaptureChanged  = 0x0215
	wmDpiChanged      = 0x02E0
	wmAppShow         = 0x8000 + 1
	wmAppHide         = 0x8000 + 2
	wmAppRefresh      = 0x8000 + 3
	wmAppClose        = 0x8000 + 4
	maNoActivate      = 3
	wsPopup           = 0x80000000
	wsExTopmost       = 0x00000008
	wsExToolWindow    = 0x00000080
	wsExLayered       = 0x00080000
	wsExNoActivate    = 0x08000000
	lwaColorKey       = 1
	lwaAlpha          = 2
	swHide            = 0
	swShowNoActivate  = 4
	swpNoSize         = 0x0001
	swpNoMove         = 0x0002
	swpNoZOrder       = 0x0004
	swpNoActivate     = 0x0010
	dtCenter          = 0x0001
	dtVCenter         = 0x0004
	dtWordBreak       = 0x0010
	dtSingleLine      = 0x0020
	dtCalcRect        = 0x0400
	dtNoPrefix        = 0x0800
	monitorDefaultNul = 0
	monitorDefaultNea = 2
	idcArrow          = 32512
	srcCopy           = 0x00CC0020
	flashTimer        = 1
	flashMillis       = 2000
	dpiPerMonitorV2   = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (HANDLE)-4
)

// Colors are COLORREF, 0x00BBGGRR. Key is painted where the window should be see-through and click-through.
const (
	colKey      = 0x00FF00FF
	colPill     = 0x00242120
	colDot      = 0x003539E5
	colAmber    = 0x0000B3FF
	colRed      = 0x002828C6
	colButton   = 0x00C06515
	colPressed  = 0x00A1470D
	colSaved    = 0x00327D2E
	colWhite    = 0x00FFFFFF
	colInk      = 0x001A1A1A
	windowAlpha = 240
)

// Overlay is a running overlay window. Its methods are safe from any goroutine.
type Overlay struct {
	cfg  Config
	hwnd uintptr
	done chan struct{}

	mu    sync.Mutex
	level Level
	text  string

	// UI-thread state
	dpi                 uint32
	pillFont, btnFont   uintptr
	pill, button        rect
	pressed, dragging   bool
	dragCursor, dragWin point
	flash               string // "" or the text shown on the button for 2 s
	flashOK             bool
}

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }

type wndClassEx struct {
	Size, Style                      uint32
	WndProc                          uintptr
	ClsExtra, WndExtra               int32
	Instance, Icon, Cursor, Bg, Menu uintptr
	ClassName                        *uint16
	IconSm                           uintptr
}

type msg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
	Private        uint32
}

type paintStruct struct {
	Hdc                uintptr
	Erase              int32
	Paint              rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// current is the one overlay; a window procedure has no closure to find its Go object.
var current *Overlay

// New starts the overlay's UI thread with the window created and hidden.
func New(cfg Config) (*Overlay, error) {
	if current != nil {
		return nil, errors.New("overlay already running")
	}
	o := &Overlay{cfg: cfg, done: make(chan struct{}), text: "Recording class"}
	current = o
	ready := make(chan error, 1)
	go o.loop(ready)
	if err := <-ready; err != nil {
		current = nil
		return nil, err
	}
	return o, nil
}

// Show displays the overlay without activating it.
func (o *Overlay) Show() { o.post(wmAppShow) }

// Hide hides the overlay.
func (o *Overlay) Hide() { o.post(wmAppHide) }

// SetStatus changes the pill; Normal ignores text and shows "Recording class".
func (o *Overlay) SetStatus(l Level, text string) {
	o.mu.Lock()
	o.level, o.text = l, text
	if l == Normal {
		o.text = "Recording class"
	}
	o.mu.Unlock()
	o.post(wmAppRefresh)
}

// Close destroys the window and waits for the UI thread to end.
func (o *Overlay) Close() {
	o.post(wmAppClose)
	<-o.done
	current = nil
}

func (o *Overlay) post(m uintptr) { procPostMessageW.Call(o.hwnd, m, 0, 0) }

func (o *Overlay) loop(ready chan<- error) {
	runtime.LockOSThread()
	defer close(o.done)

	var inst windows.Handle
	windows.GetModuleHandleEx(0, nil, &inst)
	class, _ := windows.UTF16PtrFromString("classrec.overlay")
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: uintptr(inst), Cursor: cursor, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		ready <- err
		return
	}
	pos, saved := loadPosition(o.cfg.SettingsPath)
	hwnd, _, err := procCreateWindowExW.Call(wsExLayered|wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(class)), 0, wsPopup, uintptr(pos.X), uintptr(pos.Y), 1, 1, 0, 0, uintptr(inst), 0)
	if hwnd == 0 {
		ready <- err
		return
	}
	o.hwnd = hwnd
	procSetLayeredAttrs.Call(hwnd, colKey, windowAlpha, lwaColorKey|lwaAlpha)
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	o.setDPI(uint32(dpi))
	if !saved || !onScreen(pos) {
		o.moveToDefault()
	}
	ready <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	o := current
	if o == nil || o.hwnd != hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
		return r
	}
	switch message {
	case wmMouseActivate:
		return maNoActivate
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		o.paint()
		return 0
	case wmLButtonDown:
		p := point{int32(int16(lParam)), int32(int16(lParam >> 16))}
		if o.button.contains(p) {
			o.pressed = true
			o.invalidate()
		} else {
			o.dragging = true
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&o.dragCursor)))
			o.dragWin = o.windowPos()
		}
		procSetCapture.Call(hwnd)
		return 0
	case wmMouseMove:
		if o.dragging {
			var c point
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&c)))
			x, y := o.dragWin.X+c.X-o.dragCursor.X, o.dragWin.Y+c.Y-o.dragCursor.Y
			procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
		}
		return 0
	case wmLButtonUp:
		p := point{int32(int16(lParam)), int32(int16(lParam >> 16))}
		wasPressed, wasDragging := o.pressed, o.dragging
		procReleaseCapture.Call() // sends WM_CAPTURECHANGED, which clears both flags
		if wasPressed && o.button.contains(p) {
			o.mark()
		}
		if wasDragging {
			savePosition(o.cfg.SettingsPath, o.windowPos())
		}
		return 0
	case wmCaptureChanged:
		if o.pressed {
			o.invalidate()
		}
		o.pressed, o.dragging = false, false
		return 0
	case wmTimer:
		if wParam == flashTimer {
			procKillTimer.Call(hwnd, flashTimer)
			o.flash = ""
			o.invalidate()
		}
		return 0
	case wmDpiChanged:
		// Keep the top-left where it is; layout recomputes the size, so the suggested rect isn't needed.
		o.setDPI(uint32(wParam & 0xFFFF))
		return 0
	case wmAppShow:
		procSetWindowPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate) // HWND_TOPMOST
		procShowWindow.Call(hwnd, swShowNoActivate)
		return 0
	case wmAppHide:
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmAppRefresh:
		o.layout()
		return 0
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procDeleteObject.Call(o.pillFont)
		procDeleteObject.Call(o.btnFont)
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func (o *Overlay) mark() {
	o.flash, o.flashOK = "Saved ✓", true
	if o.cfg.OnMark != nil {
		if err := o.cfg.OnMark(); err != nil {
			o.flash, o.flashOK = "Not saved", false
		}
	}
	procSetTimer.Call(o.hwnd, flashTimer, flashMillis, 0)
	o.invalidate()
}

// px converts device-independent pixels to physical pixels at the window's DPI.
func (o *Overlay) px(dip int32) int32 { return dip * int32(o.dpi) / 96 }

func (o *Overlay) setDPI(dpi uint32) {
	if dpi == 0 {
		dpi = 96
	}
	o.dpi = dpi
	procDeleteObject.Call(o.pillFont)
	procDeleteObject.Call(o.btnFont)
	o.pillFont = o.font(15, 600)
	o.btnFont = o.font(18, 700)
	o.layout()
}

func (o *Overlay) font(pt, weight int32) uintptr {
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	h := -pt * int32(o.dpi) / 72
	f, _, _ := procCreateFontW.Call(uintptr(h), 0, 0, 0, uintptr(weight), 0, 0, 0, 1 /* DEFAULT_CHARSET */, 0, 0,
		5 /* CLEARTYPE_QUALITY */, 0, uintptr(unsafe.Pointer(face)))
	return f
}

// Layout in DIPs: a 280-wide pill that grows to fit its text, an 8 gap, then a 64-high button.
func (o *Overlay) layout() {
	w, pad := o.px(280), o.px(16)
	text := o.pillText()
	tr := rect{0, 0, w - pad*2 - o.px(22), 0}
	dc, _, _ := procGetDC.Call(o.hwnd)
	old, _, _ := procSelectObject.Call(dc, o.pillFont)
	drawText(dc, text, &tr, dtWordBreak|dtCalcRect|dtNoPrefix)
	procSelectObject.Call(dc, old)
	procReleaseDC.Call(o.hwnd, dc)

	pillH := max(o.px(44), tr.Bottom+o.px(20))
	o.pill = rect{0, 0, w, pillH}
	o.button = rect{0, pillH + o.px(8), w, pillH + o.px(8) + o.px(64)}
	procSetWindowPos.Call(o.hwnd, 0, 0, 0, uintptr(w), uintptr(o.button.Bottom), swpNoMove|swpNoZOrder|swpNoActivate)
	o.invalidate()
}

func (o *Overlay) pillText() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.text
}

func (o *Overlay) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))

	w, h := o.pill.Right, o.button.Bottom
	mem, _, _ := procCreateCompatibleDC.Call(hdc)
	bmp, _, _ := procCreateCompatibleBmp.Call(hdc, uintptr(w), uintptr(h))
	oldBmp, _, _ := procSelectObject.Call(mem, bmp)
	defer func() {
		procSelectObject.Call(mem, oldBmp)
		procDeleteObject.Call(bmp)
		procDeleteDC.Call(mem)
	}()

	fillRect(mem, rect{0, 0, w, h}, colKey)
	procSetBkMode.Call(mem, 1) // TRANSPARENT

	o.mu.Lock()
	level, text := o.level, o.text
	o.mu.Unlock()
	bg, ink, dot := uintptr(colPill), uintptr(colWhite), uintptr(colDot)
	switch level {
	case Amber:
		bg, ink, dot = colAmber, colInk, colInk
	case Red:
		bg, ink, dot = colRed, colWhite, colWhite
	}
	radius := o.px(22)
	fillRound(mem, o.pill, radius, bg)
	pad, d := o.px(16), o.px(12)
	cy := (o.pill.Top + o.pill.Bottom) / 2
	fillEllipse(mem, rect{pad, cy - d/2, pad + d, cy + d/2}, dot)
	tr := rect{pad + d + o.px(10), o.pill.Top, o.pill.Right - pad, o.pill.Bottom}
	o.drawCentered(mem, o.pillFont, text, tr, ink, false)

	btn, label := uintptr(colButton), "I didn't get this"
	switch {
	case o.flash != "" && o.flashOK:
		btn, label = colSaved, o.flash
	case o.flash != "":
		btn, label = colRed, o.flash
	case o.pressed:
		btn = colPressed
	}
	fillRound(mem, o.button, o.px(16), btn)
	o.drawCentered(mem, o.btnFont, label, o.button, colWhite, true)

	procBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, srcCopy)
}

// drawCentered vertically centers text in r; wrapped text is measured first since DT_VCENTER needs DT_SINGLELINE.
func (o *Overlay) drawCentered(dc, font uintptr, text string, r rect, color uintptr, center bool) {
	old, _, _ := procSelectObject.Call(dc, font)
	defer procSelectObject.Call(dc, old)
	procSetTextColor.Call(dc, color)
	flags := uintptr(dtNoPrefix)
	if center {
		drawText(dc, text, &r, flags|dtCenter|dtVCenter|dtSingleLine)
		return
	}
	m := rect{r.Left, 0, r.Right, 0}
	drawText(dc, text, &m, flags|dtWordBreak|dtCalcRect)
	top := r.Top + (r.Bottom-r.Top-m.Bottom)/2
	r.Top, r.Bottom = top, top+m.Bottom
	drawText(dc, text, &r, flags|dtWordBreak)
}

func drawText(dc uintptr, s string, r *rect, flags uintptr) {
	u, _ := windows.UTF16FromString(s)
	procDrawTextW.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(r)), flags)
}

func fillRect(dc uintptr, r rect, color uintptr) {
	b, _, _ := procCreateSolidBrush.Call(color)
	procFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), b)
	procDeleteObject.Call(b)
}

func fillRound(dc uintptr, r rect, radius int32, color uintptr) {
	rgn, _, _ := procCreateRoundRectRgn.Call(uintptr(r.Left), uintptr(r.Top), uintptr(r.Right+1), uintptr(r.Bottom+1), uintptr(radius*2), uintptr(radius*2))
	fillRgn(dc, rgn, color)
}

func fillEllipse(dc uintptr, r rect, color uintptr) {
	rgn, _, _ := procCreateEllipticRgn.Call(uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	fillRgn(dc, rgn, color)
}

func fillRgn(dc, rgn, color uintptr) {
	b, _, _ := procCreateSolidBrush.Call(color)
	procFillRgn.Call(dc, rgn, b)
	procDeleteObject.Call(b)
	procDeleteObject.Call(rgn)
}

func (o *Overlay) invalidate() { procInvalidateRect.Call(o.hwnd, 0, 0) }

func (r rect) contains(p point) bool {
	return p.X >= r.Left && p.X < r.Right && p.Y >= r.Top && p.Y < r.Bottom
}

func (o *Overlay) windowPos() point {
	var r rect
	procGetWindowRect.Call(o.hwnd, uintptr(unsafe.Pointer(&r)))
	return point{r.Left, r.Top}
}

// moveToDefault puts the overlay at the right edge of its monitor's work area, vertically centered,
// clear of Zoom's and Meet's bottom control bars.
func (o *Overlay) moveToDefault() {
	mon, _, _ := procMonitorFromWindow.Call(o.hwnd, monitorDefaultNea)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	w, h := o.pill.Right, o.button.Bottom
	x := mi.Work.Right - w - o.px(16)
	y := mi.Work.Top + (mi.Work.Bottom-mi.Work.Top-h)/2
	procSetWindowPos.Call(o.hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

func onScreen(p point) bool {
	m, _, _ := procMonitorFromPoint.Call(uintptr(uint32(p.X))|uintptr(uint32(p.Y))<<32, monitorDefaultNul)
	return m != 0
}

// DPIAwareness reports whether the process runs per-monitor v2, i.e. whether the embedded manifest took effect.
func DPIAwareness() bool {
	ctx, _, _ := procGetThreadDpiContext.Call()
	eq, _, _ := procAreDpiContextsEqual.Call(ctx, dpiPerMonitorV2)
	return eq != 0
}

type settings struct {
	Overlay *point `json:"overlay,omitempty"`
}

// loadPosition returns the saved top-left in physical pixels; ok is false if there is none.
func loadPosition(path string) (point, bool) {
	var s settings
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Overlay == nil {
		return point{}, false
	}
	return *s.Overlay, true
}

func savePosition(path string, p point) {
	var s settings
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	s.Overlay = &p
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		fsx.WriteFileAtomic(path, b)
	}
}
