package overlay

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Layout in device-independent pixels (1/96 inch); px converts them for the window's monitor.
const (
	overlayWidth     = 280
	pillMinHeight    = 44
	pillTextPadding  = 10 // above and below the pill text when it wraps
	pillCornerRadius = 22
	sidePadding      = 16
	dotSize          = 12
	dotToTextGap     = 10
	pillToButtonGap  = 8
	buttonHeight     = 64
	buttonRadius     = 16
	edgeMargin       = 16 // between the overlay and the screen edge

	fontFace        = "Segoe UI"
	pillFontPoints  = 15
	buttonFontPoint = 18
	buttonLabel     = "I didn't get this"
)

// Colors are COLORREF values, 0x00BBGGRR.
const (
	colorKey     = 0x00FF00FF // painted where the window is see-through
	colorPill    = 0x00242120
	colorDot     = 0x003539E5
	colorAmber   = 0x0000B3FF
	colorRed     = 0x002828C6
	colorButton  = 0x00C06515
	colorPressed = 0x00A1470D
	colorSaved   = 0x00327D2E
	colorWhite   = 0x00FFFFFF
	colorInk     = 0x001A1A1A
	windowAlpha  = 240
)

type pillLook struct {
	Background uintptr
	Text       uintptr
	Dot        uintptr
}

type buttonLook struct {
	Background uintptr
	Label      string
}

func pillLookFor(level Level) pillLook {
	switch level {
	case Amber:
		return pillLook{Background: colorAmber, Text: colorInk, Dot: colorInk}
	case Red:
		return pillLook{Background: colorRed, Text: colorWhite, Dot: colorWhite}
	default:
		return pillLook{Background: colorPill, Text: colorWhite, Dot: colorDot}
	}
}

func (o *Overlay) currentButtonLook() buttonLook {
	if o.flash.Text != "" && o.flash.Saved {
		return buttonLook{Background: colorSaved, Label: o.flash.Text}
	}
	if o.flash.Text != "" {
		return buttonLook{Background: colorRed, Label: o.flash.Text}
	}
	if o.buttonPressed {
		return buttonLook{Background: colorPressed, Label: buttonLabel}
	}
	return buttonLook{Background: colorButton, Label: buttonLabel}
}

// px converts device-independent pixels to physical pixels at the window's DPI.
func (o *Overlay) px(dip int32) int32 {
	return dip * int32(o.dpi) / 96
}

// setDPI rebuilds the fonts and layout for a new monitor scale.
func (o *Overlay) setDPI(dpi uint32) {
	if dpi == 0 {
		dpi = 96
	}
	o.dpi = dpi
	procDeleteObject.Call(o.pillFont)
	procDeleteObject.Call(o.buttonFont)
	o.pillFont = o.createFont(pillFontPoints, fontWeightSemibold)
	o.buttonFont = o.createFont(buttonFontPoint, fontWeightBold)
	o.layout()
}

func (o *Overlay) createFont(points, weight int32) uintptr {
	face, _ := windows.UTF16PtrFromString(fontFace)
	heightPixels := -points * int32(o.dpi) / 72 // negative: match the character height, not the cell height
	font, _, _ := procCreateFontW.Call(uintptr(heightPixels), 0, 0, 0, uintptr(weight), 0, 0, 0,
		defaultCharset, 0, 0, clearTypeQuality, 0, uintptr(unsafe.Pointer(face)))
	return font
}

// layout sizes the pill to fit its text, puts the button under it, and resizes the window to match.
func (o *Overlay) layout() {
	width := o.px(overlayWidth)
	_, text := o.status()
	textHeight := o.measureTextHeight(o.pillFont, text, o.pillTextWidth())

	pillHeight := max(o.px(pillMinHeight), textHeight+2*o.px(pillTextPadding))
	o.pill = rect{Left: 0, Top: 0, Right: width, Bottom: pillHeight}

	buttonTop := pillHeight + o.px(pillToButtonGap)
	o.button = rect{Left: 0, Top: buttonTop, Right: width, Bottom: buttonTop + o.px(buttonHeight)}

	o.resizeTo(width, o.button.Bottom)
	o.invalidate()
}

func (o *Overlay) pillTextWidth() int32 {
	return o.px(overlayWidth) - 2*o.px(sidePadding) - o.px(dotSize) - o.px(dotToTextGap)
}

func (o *Overlay) measureTextHeight(font uintptr, text string, width int32) int32 {
	dc, _, _ := procGetDC.Call(o.hwnd)
	defer procReleaseDC.Call(o.hwnd, dc)
	previousFont, _, _ := procSelectObject.Call(dc, font)
	defer procSelectObject.Call(dc, previousFont)

	bounds := rect{Right: width}
	drawText(dc, text, &bounds, dtWordBreak|dtCalcRect|dtNoPrefix)
	return bounds.height()
}

// paint draws into an off-screen bitmap and copies it in one go, so the overlay never flickers.
func (o *Overlay) paint() {
	var ps paintStruct
	windowDC, _, _ := procBeginPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))

	width := o.pill.width()
	height := o.button.Bottom
	dc, _, _ := procCreateCompatibleDC.Call(windowDC)
	defer procDeleteDC.Call(dc)
	bitmap, _, _ := procCreateCompatibleBmp.Call(windowDC, uintptr(width), uintptr(height))
	defer procDeleteObject.Call(bitmap)
	previousBitmap, _, _ := procSelectObject.Call(dc, bitmap)
	defer procSelectObject.Call(dc, previousBitmap)

	fillRect(dc, rect{Right: width, Bottom: height}, colorKey)
	procSetBkMode.Call(dc, bkModeTransparent)
	o.paintPill(dc)
	o.paintButton(dc)

	procBitBlt.Call(windowDC, 0, 0, uintptr(width), uintptr(height), dc, 0, 0, srcCopy)
}

func (o *Overlay) paintPill(dc uintptr) {
	level, text := o.status()
	look := pillLookFor(level)
	fillRoundRect(dc, o.pill, o.px(pillCornerRadius), look.Background)

	dotLeft := o.px(sidePadding)
	dotDiameter := o.px(dotSize)
	centerY := (o.pill.Top + o.pill.Bottom) / 2
	dot := rect{Left: dotLeft, Top: centerY - dotDiameter/2, Right: dotLeft + dotDiameter, Bottom: centerY + dotDiameter/2}
	fillEllipse(dc, dot, look.Dot)

	textArea := rect{
		Left:   dot.Right + o.px(dotToTextGap),
		Top:    o.pill.Top,
		Right:  o.pill.Right - o.px(sidePadding),
		Bottom: o.pill.Bottom,
	}
	drawWrappedTextCenteredVertically(dc, o.pillFont, text, textArea, look.Text)
}

func (o *Overlay) paintButton(dc uintptr) {
	look := o.currentButtonLook()
	fillRoundRect(dc, o.button, o.px(buttonRadius), look.Background)
	drawSingleLineCentered(dc, o.buttonFont, look.Label, o.button, colorWhite)
}

func drawSingleLineCentered(dc, font uintptr, text string, area rect, color uintptr) {
	previousFont, _, _ := procSelectObject.Call(dc, font)
	defer procSelectObject.Call(dc, previousFont)
	procSetTextColor.Call(dc, color)
	drawText(dc, text, &area, dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
}

// drawWrappedTextCenteredVertically measures first, because DrawText only centers vertically on a single line.
func drawWrappedTextCenteredVertically(dc, font uintptr, text string, area rect, color uintptr) {
	previousFont, _, _ := procSelectObject.Call(dc, font)
	defer procSelectObject.Call(dc, previousFont)
	procSetTextColor.Call(dc, color)

	measured := rect{Left: area.Left, Right: area.Right}
	drawText(dc, text, &measured, dtWordBreak|dtCalcRect|dtNoPrefix)
	top := area.Top + (area.height()-measured.height())/2
	textBox := rect{Left: area.Left, Top: top, Right: area.Right, Bottom: top + measured.height()}
	drawText(dc, text, &textBox, dtWordBreak|dtNoPrefix)
}

func drawText(dc uintptr, text string, area *rect, flags uintptr) {
	utf16, _ := windows.UTF16FromString(text)
	lengthWithoutNul := len(utf16) - 1
	procDrawTextW.Call(dc, uintptr(unsafe.Pointer(&utf16[0])), uintptr(lengthWithoutNul), uintptr(unsafe.Pointer(area)), flags)
}

func fillRect(dc uintptr, area rect, color uintptr) {
	brush, _, _ := procCreateSolidBrush.Call(color)
	defer procDeleteObject.Call(brush)
	procFillRect.Call(dc, uintptr(unsafe.Pointer(&area)), brush)
}

func fillRoundRect(dc uintptr, area rect, radius int32, color uintptr) {
	diameter := uintptr(radius * 2)
	// The region's right and bottom edges are exclusive, so +1 covers the whole rect.
	region, _, _ := procCreateRoundRectRgn.Call(uintptr(area.Left), uintptr(area.Top), uintptr(area.Right+1), uintptr(area.Bottom+1), diameter, diameter)
	fillRegion(dc, region, color)
}

func fillEllipse(dc uintptr, area rect, color uintptr) {
	region, _, _ := procCreateEllipticRgn.Call(uintptr(area.Left), uintptr(area.Top), uintptr(area.Right), uintptr(area.Bottom))
	fillRegion(dc, region, color)
}

// fillRegion fills and then frees region.
func fillRegion(dc, region, color uintptr) {
	defer procDeleteObject.Call(region)
	brush, _, _ := procCreateSolidBrush.Call(color)
	defer procDeleteObject.Call(brush)
	procFillRgn.Call(dc, region, brush)
}

func (o *Overlay) invalidate() {
	procInvalidateRect.Call(o.hwnd, 0, 0)
}
