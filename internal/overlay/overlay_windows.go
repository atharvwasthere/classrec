package overlay

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowClassName = "classrec.overlay"
	normalPillText  = "Recording class"

	flashTimerID  = 1
	flashMillis   = 2000
	flashSaved    = "Saved ✓"
	flashNotSaved = "Not saved"
)

// buttonFlash is the short-lived message on the button after a click.
type buttonFlash struct {
	Text  string // "" when no flash is showing
	Saved bool
}

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
	pillFont       uintptr
	buttonFont     uintptr
	pill           rect
	button         rect
	buttonPressed  bool
	dragging       bool
	dragStartMouse point
	dragStartPos   point
	flash          buttonFlash
}

// current is the one overlay; a window procedure has no closure to find its Go object.
var current *Overlay

// New starts the overlay's UI thread with the window created and hidden.
func New(cfg Config) (*Overlay, error) {
	if current != nil {
		return nil, errors.New("overlay already running")
	}
	o := &Overlay{
		cfg:  cfg,
		done: make(chan struct{}),
		text: normalPillText,
	}
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

// SetStatus changes the pill; Normal ignores text and shows "Recording class".
func (o *Overlay) SetStatus(level Level, text string) {
	if level == Normal {
		text = normalPillText
	}
	o.statusMu.Lock()
	o.level = level
	o.text = text
	o.statusMu.Unlock()
	o.post(wmAppRefresh)
}

// Close destroys the window and waits for the UI thread to end.
func (o *Overlay) Close() {
	o.post(wmAppClose)
	<-o.done
	current = nil
}

func (o *Overlay) post(message uintptr) {
	procPostMessageW.Call(o.hwnd, message, 0, 0)
}

func (o *Overlay) status() (Level, string) {
	o.statusMu.Lock()
	defer o.statusMu.Unlock()
	return o.level, o.text
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

	class := wndClassEx{
		WndProc:   windows.NewCallback(wndProc),
		Instance:  uintptr(instance),
		Cursor:    arrowCursor,
		ClassName: className,
	}
	class.Size = uint32(unsafe.Sizeof(class))
	registered, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if registered == 0 {
		return err
	}

	savedPos, hasSavedPos := loadPosition(o.cfg.SettingsPath)
	exStyle := uintptr(wsExLayered | wsExTopmost | wsExToolWindow | wsExNoActivate)
	hwnd, _, err := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(className)), 0, wsPopup,
		uintptr(savedPos.X), uintptr(savedPos.Y), 1, 1, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		return err
	}
	o.hwnd = hwnd

	// Pixels painted colorKey are see-through and let clicks pass to the window below.
	procSetLayeredAttrs.Call(hwnd, colorKey, windowAlpha, lwaColorKey|lwaAlpha)
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	o.setDPI(uint32(dpi))
	if !hasSavedPos || !isOnAnyMonitor(savedPos) {
		o.moveToDefaultPosition()
	}
	return nil
}

func runMessageLoop() {
	var m msg
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		quitOrError := int32(result) <= 0
		if quitOrError {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// wndProc routes each window message to its handler.
func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	o := current
	isOurWindow := o != nil && o.hwnd == hwnd // false while CreateWindowEx is still running
	if !isOurWindow {
		return defaultWindowProc(hwnd, message, wParam, lParam)
	}

	switch message {
	case wmMouseActivate:
		// The heart of the overlay: a click never takes focus from Zoom or Edge.
		return maNoActivate
	case wmEraseBkgnd:
		const backgroundHandledByPaint = 1
		return backgroundHandledByPaint
	case wmPaint:
		o.paint()
	case wmLButtonDown:
		o.onLeftButtonDown(pointFromLParam(lParam))
	case wmMouseMove:
		o.onMouseMove()
	case wmLButtonUp:
		o.onLeftButtonUp(pointFromLParam(lParam))
	case wmCaptureChanged:
		o.onCaptureLost()
	case wmTimer:
		o.onTimer(wParam)
	case wmDpiChanged:
		newDPI := uint32(wParam & 0xFFFF)
		o.setDPI(newDPI)
	case wmAppShow:
		o.showWithoutActivating()
	case wmAppHide:
		procShowWindow.Call(hwnd, swHide)
	case wmAppRefresh:
		o.layout()
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
	case wmDestroy:
		o.onDestroy()
	default:
		return defaultWindowProc(hwnd, message, wParam, lParam)
	}
	return 0
}

func defaultWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	result, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return result
}

// onLeftButtonDown presses the button, or starts dragging the overlay when the press is on the pill.
func (o *Overlay) onLeftButtonDown(p point) {
	if o.button.contains(p) {
		o.buttonPressed = true
		o.invalidate()
	} else {
		o.dragging = true
		o.dragStartMouse = cursorPosition()
		o.dragStartPos = o.windowPosition()
	}
	procSetCapture.Call(o.hwnd)
}

func (o *Overlay) onMouseMove() {
	if !o.dragging {
		return
	}
	mouse := cursorPosition()
	x := o.dragStartPos.X + mouse.X - o.dragStartMouse.X
	y := o.dragStartPos.Y + mouse.Y - o.dragStartMouse.Y
	o.moveTo(point{X: x, Y: y})
}

func (o *Overlay) onLeftButtonUp(p point) {
	wasPressed := o.buttonPressed
	wasDragging := o.dragging
	procReleaseCapture.Call() // triggers onCaptureLost, which clears both flags

	releasedOnButton := wasPressed && o.button.contains(p)
	if releasedOnButton {
		o.markMoment()
	}
	if wasDragging {
		savePosition(o.cfg.SettingsPath, o.windowPosition())
	}
}

func (o *Overlay) onCaptureLost() {
	if o.buttonPressed {
		o.invalidate()
	}
	o.buttonPressed = false
	o.dragging = false
}

func (o *Overlay) onTimer(timerID uintptr) {
	if timerID != flashTimerID {
		return
	}
	procKillTimer.Call(o.hwnd, flashTimerID)
	o.flash = buttonFlash{}
	o.invalidate()
}

func (o *Overlay) onDestroy() {
	procDeleteObject.Call(o.pillFont)
	procDeleteObject.Call(o.buttonFont)
	procPostQuitMessage.Call(0)
}

// markMoment records "I didn't get this" and flashes the result on the button for 2 s.
func (o *Overlay) markMoment() {
	o.flash = buttonFlash{Text: flashSaved, Saved: true}
	if o.cfg.OnMark != nil {
		if err := o.cfg.OnMark(); err != nil {
			o.flash = buttonFlash{Text: flashNotSaved, Saved: false}
		}
	}
	procSetTimer.Call(o.hwnd, flashTimerID, flashMillis, 0)
	o.invalidate()
}

func (o *Overlay) showWithoutActivating() {
	procSetWindowPos.Call(o.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	procShowWindow.Call(o.hwnd, swShowNoActivate)
}

func (o *Overlay) moveTo(p point) {
	procSetWindowPos.Call(o.hwnd, 0, uintptr(p.X), uintptr(p.Y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

func (o *Overlay) resizeTo(width, height int32) {
	procSetWindowPos.Call(o.hwnd, 0, 0, 0, uintptr(width), uintptr(height), swpNoMove|swpNoZOrder|swpNoActivate)
}

func (o *Overlay) windowPosition() point {
	var r rect
	procGetWindowRect.Call(o.hwnd, uintptr(unsafe.Pointer(&r)))
	return point{X: r.Left, Y: r.Top}
}

// moveToDefaultPosition puts the overlay at the right edge of its monitor's work area, vertically
// centered, clear of Zoom's and Meet's bottom control bars.
func (o *Overlay) moveToDefaultPosition() {
	monitor, _, _ := procMonitorFromWindow.Call(o.hwnd, monitorDefaultToNearest)
	info := monitorInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))

	work := info.WorkArea
	width := o.pill.width()
	height := o.button.Bottom
	x := work.Right - width - o.px(edgeMargin)
	y := work.Top + (work.height()-height)/2
	o.moveTo(point{X: x, Y: y})
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
