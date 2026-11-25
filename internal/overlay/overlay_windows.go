package overlay

import (
	"errors"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	windowClassName = "classrec.overlay"
	normalText      = "Recording class"

	flashTimerID  = 1
	flashTickMs   = 40
	flashHold     = 1500 * time.Millisecond // shown fully, then
	flashFade     = 500 * time.Millisecond  // faded out
	flashSaved    = "Saved ✓"
	flashNotSaved = "Not saved"
)

// Overlay is a running overlay window. Its exported methods are safe from any goroutine;
// everything else runs on the overlay's own UI thread.
type Overlay struct {
	cfg  Config
	hwnd uintptr
	done chan struct{}

	statusMu sync.Mutex
	level    Level
	text     string

	// UI-thread state
	dpi            uint32
	dark           bool
	backdrop       bool
	collapsed      bool
	expandedAt     point // where the card was before it collapsed
	hot, pressed   part
	tracking       bool // asked Windows for WM_MOUSELEAVE
	dragging       bool
	dragStartMouse point
	dragStartPos   point
	flash          string
	flashOK        bool
	flashAt        time.Time
}

// current is the one overlay; a window procedure has no closure to find its Go object.
var current *Overlay

// New starts the overlay's UI thread with the window created and hidden.
func New(cfg Config) (*Overlay, error) {
	if current != nil {
		return nil, errors.New("overlay already running")
	}
	o := &Overlay{cfg: cfg, done: make(chan struct{}), text: normalText}
	current = o
	created := make(chan error, 1)
	go o.runUIThread(created)
	if err := <-created; err != nil {
		current = nil
		return nil, err
	}
	return o, nil
}

// Show displays the overlay without activating it.
func (o *Overlay) Show() { o.post(wmAppShow) }

// Hide hides the overlay.
func (o *Overlay) Hide() { o.post(wmAppHide) }

// SetStatus changes the status line; Normal ignores text and shows "Recording class".
func (o *Overlay) SetStatus(level Level, text string) {
	if level == Normal {
		text = normalText
	}
	o.statusMu.Lock()
	o.level, o.text = level, text
	o.statusMu.Unlock()
	o.post(wmAppRefresh)
}

// Close destroys the window and waits for the UI thread to end.
func (o *Overlay) Close() {
	o.post(wmAppClose)
	<-o.done
	current = nil
}

func (o *Overlay) post(message uintptr) { procPostMessageW.Call(o.hwnd, message, 0, 0) }

// scene is what to draw now.
func (o *Overlay) scene() scene {
	o.statusMu.Lock()
	s := scene{level: o.level, text: o.text}
	o.statusMu.Unlock()
	s.dark, s.backdrop, s.collapsed, s.hot, s.pressed = o.dark, o.backdrop, o.collapsed, o.hot, o.pressed
	if o.flash != "" {
		s.flash, s.flashOK = o.flash, o.flashOK
		s.flashAlpha = 1 - float32(max(0, time.Since(o.flashAt)-flashHold))/float32(flashFade)
	}
	return s
}

// runUIThread creates the window and runs its message loop. A window belongs to the OS thread
// that created it, so this goroutine stays locked to its thread for its whole life.
func (o *Overlay) runUIThread(created chan<- error) {
	runtime.LockOSThread()
	defer close(o.done)
	if err := o.createWindow(); err != nil {
		created <- err
		return
	}
	created <- nil
	runMessageLoop()
}

func (o *Overlay) createWindow() error {
	var instance windows.Handle
	windows.GetModuleHandleEx(0, nil, &instance)
	className, _ := windows.UTF16PtrFromString(windowClassName)
	arrowCursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	class := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: uintptr(instance), Cursor: arrowCursor, ClassName: className}
	class.Size = uint32(unsafe.Sizeof(class))
	if registered, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); registered == 0 {
		return err
	}

	savedPos, hasSavedPos := loadPosition(o.cfg.SettingsPath)
	exStyle := uintptr(wsExTopmost | wsExToolWindow | wsExNoActivate)
	hwnd, _, err := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(className)), 0, wsPopup,
		uintptr(savedPos.X), uintptr(savedPos.Y), 1, 1, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		return err
	}
	o.hwnd = hwnd
	o.applyTheme()
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	o.setDPI(uint32(dpi))
	if !hasSavedPos || !isOnAnyMonitor(savedPos) {
		o.moveToDefaultPosition()
	}
	return nil
}

// applyTheme follows the system's light or dark mode and asks Windows 11 for rounded corners and
// acrylic. Where acrylic is not available the card is painted solid.
func (o *Overlay) applyTheme() {
	o.dark = systemUsesDarkMode()
	dark, corner, acrylic := boolToDWORD(o.dark), uint32(dwmwcpRound), uint32(dwmsbtTransientWindow)
	setDWM(o.hwnd, dwmwaUseImmersiveDarkMode, dark)
	setDWM(o.hwnd, dwmwaCornerPreference, corner)
	margins := [4]int32{-1, -1, -1, -1} // the whole window is "frame", so the backdrop shows through
	extended, _, _ := procDwmExtendFrame.Call(o.hwnd, uintptr(unsafe.Pointer(&margins)))
	o.backdrop = extended == 0 && setDWM(o.hwnd, dwmwaSystemBackdropType, acrylic)
}

func setDWM(hwnd uintptr, attribute, value uint32) bool {
	hr, _, _ := procDwmSetWindowAttr.Call(hwnd, uintptr(attribute), uintptr(unsafe.Pointer(&value)), 4)
	return hr == 0
}

func boolToDWORD(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// systemUsesDarkMode reads the "app mode" from Settings › Personalisation › Colours.
func systemUsesDarkMode() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	light, _, err := key.GetIntegerValue("AppsUseLightTheme")
	return err == nil && light == 0
}

func runMessageLoop() {
	var m msg
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// wndProc routes each window message to its handler.
func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	o := current
	if o == nil || o.hwnd != hwnd { // also true while CreateWindowEx is still running
		return defaultWindowProc(hwnd, message, wParam, lParam)
	}
	switch message {
	case wmMouseActivate:
		return maNoActivate // the heart of the overlay: a click never takes focus from Zoom or Edge
	case wmEraseBkgnd:
		return 1 // paint draws every pixel
	case wmPaint:
		o.paint()
	case wmLButtonDown:
		o.onLeftButtonDown(pointFromLParam(lParam))
	case wmMouseMove:
		o.onMouseMove(pointFromLParam(lParam))
	case wmMouseLeave:
		o.tracking = false
		o.setHot(partNone)
	case wmLButtonUp:
		o.onLeftButtonUp(pointFromLParam(lParam))
	case wmCaptureChanged:
		o.pressed, o.dragging = partNone, false
		o.invalidate()
	case wmTimer:
		o.onFlashTick()
	case wmDpiChanged:
		o.setDPI(uint32(wParam & 0xFFFF))
	case wmSettingChange:
		o.applyTheme()
		o.invalidate()
	case wmAppShow:
		procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		procShowWindow.Call(hwnd, swShowNoActivate)
	case wmAppHide:
		procShowWindow.Call(hwnd, swHide)
	case wmAppRefresh:
		o.invalidate()
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
	case wmDestroy:
		procPostQuitMessage.Call(0)
	default:
		return defaultWindowProc(hwnd, message, wParam, lParam)
	}
	return 0
}

func defaultWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	result, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return result
}

// logical turns a client point in monitor pixels into logical pixels.
func (o *Overlay) logical(p point) point {
	return point{X: p.X * 96 / int32(o.dpi), Y: p.Y * 96 / int32(o.dpi)}
}

// onLeftButtonDown presses the button or "–", expands a collapsed tab, or starts a drag.
func (o *Overlay) onLeftButtonDown(p point) {
	procSetCapture.Call(o.hwnd)
	if o.collapsed {
		return // expands on release
	}
	if target := o.scene().partAt(o.logical(p)); target != partNone {
		o.pressed = target
		o.invalidate()
		return
	}
	o.dragging = true
	o.dragStartMouse = cursorPosition()
	o.dragStartPos = o.windowPosition()
}

func (o *Overlay) onMouseMove(p point) {
	if !o.tracking {
		track := trackMouseEvent{Flags: tmeLeave, Window: o.hwnd}
		track.Size = uint32(unsafe.Sizeof(track))
		procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&track)))
		o.tracking = true
	}
	if o.dragging {
		mouse := cursorPosition()
		o.moveTo(point{X: o.dragStartPos.X + mouse.X - o.dragStartMouse.X, Y: o.dragStartPos.Y + mouse.Y - o.dragStartMouse.Y})
		return
	}
	o.setHot(o.scene().partAt(o.logical(p)))
}

func (o *Overlay) onLeftButtonUp(p point) {
	pressed, dragging := o.pressed, o.dragging
	procReleaseCapture.Call() // clears pressed and dragging
	released := o.scene().partAt(o.logical(p))
	switch {
	case o.collapsed:
		o.setCollapsed(false)
	case dragging:
		_ = savePosition(o.cfg.SettingsPath, o.windowPosition()) // a lost position is only a nuisance
	case pressed == partButton && released == partButton:
		o.markMoment()
	case pressed == partCollapse && released == partCollapse:
		o.setCollapsed(true)
	}
}

func (o *Overlay) setHot(p part) {
	if o.hot != p {
		o.hot = p
		o.invalidate()
	}
}

// setCollapsed shrinks the card to a thin tab at the right edge of its screen, or restores it.
func (o *Overlay) setCollapsed(collapsed bool) {
	if collapsed {
		o.expandedAt = o.windowPosition()
	}
	o.collapsed, o.hot = collapsed, partNone
	o.resize()
	if !collapsed {
		o.moveTo(o.expandedAt)
		return
	}
	work := o.workArea()
	y := min(max(o.expandedAt.Y, work.Top), work.Bottom-o.px(tabH))
	o.moveTo(point{X: work.Right - o.px(tabW), Y: y})
}

// markMoment records "I didn't get this" and shows the result on the button, fading after 1.5 s.
func (o *Overlay) markMoment() {
	o.flash, o.flashOK, o.flashAt = flashSaved, true, time.Now()
	if o.cfg.OnMark != nil {
		if err := o.cfg.OnMark(); err != nil {
			o.flash, o.flashOK = flashNotSaved, false
		}
	}
	procSetTimer.Call(o.hwnd, flashTimerID, flashTickMs, 0)
	o.invalidate()
}

func (o *Overlay) onFlashTick() {
	if time.Since(o.flashAt) >= flashHold+flashFade {
		procKillTimer.Call(o.hwnd, flashTimerID)
		o.flash = ""
	}
	o.invalidate()
}

// px converts logical pixels to the window's monitor pixels.
func (o *Overlay) px(v int32) int32 { return v * int32(o.dpi) / 96 }

func (o *Overlay) setDPI(dpi uint32) {
	if dpi == 0 {
		dpi = 96
	}
	o.dpi = dpi
	o.resize()
}

func (o *Overlay) resize() {
	w, h := o.scene().size()
	procSetWindowPos.Call(o.hwnd, 0, 0, 0, uintptr(o.px(w)), uintptr(o.px(h)), swpNoMove|swpNoZOrder|swpNoActivate)
	o.invalidate()
}

func (o *Overlay) invalidate() { procInvalidateRect.Call(o.hwnd, 0, 0) }

func (o *Overlay) moveTo(p point) {
	procSetWindowPos.Call(o.hwnd, 0, uintptr(p.X), uintptr(p.Y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

func (o *Overlay) windowPosition() point {
	var r rect
	procGetWindowRect.Call(o.hwnd, uintptr(unsafe.Pointer(&r)))
	return point{X: r.Left, Y: r.Top}
}

func (o *Overlay) workArea() rect {
	monitor, _, _ := procMonitorFromWindow.Call(o.hwnd, monitorDefaultToNearest)
	info := monitorInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	return info.WorkArea
}

// moveToDefaultPosition puts the card at the right edge of its screen, halfway down, clear of
// Zoom's and Meet's bottom control bars.
func (o *Overlay) moveToDefaultPosition() {
	work := o.workArea()
	o.moveTo(point{X: work.Right - o.px(cardW) - o.px(edgeMargin), Y: work.Top + (work.height()-o.px(cardH))/2})
}

func isOnAnyMonitor(p point) bool {
	// MonitorFromPoint takes a POINT by value, packed into one 64-bit register.
	packed := uintptr(uint32(p.X)) | uintptr(uint32(p.Y))<<32
	monitor, _, _ := procMonitorFromPoint.Call(packed, monitorDefaultToNull)
	return monitor != 0
}

func cursorPosition() point {
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p
}

// DPIAwareness reports whether the process runs per-monitor v2, i.e. whether the embedded manifest took effect.
func DPIAwareness() bool {
	context, _, _ := procGetThreadDpiContext.Call()
	equal, _, _ := procAreDpiContextsEqual.Call(context, dpiAwarenessPerMonitorV2)
	return equal != 0
}
