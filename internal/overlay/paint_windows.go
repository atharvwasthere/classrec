package overlay

import (
	"unsafe"
)

// Layout in logical pixels (1/96 inch); the scale turns them into the monitor's pixels.
const (
	cardW, cardH = 240, 110
	tabW, tabH   = 10, 56 // the collapsed edge tab
	edgeMargin   = 16     // between the card and the screen edge

	pad         = 12
	rowTop      = 10
	rowH        = 32
	dotX        = 21
	dotR        = 4
	textX       = 33
	collapseBox = 24
	buttonTop   = 54
	buttonH     = 44
	radius      = 6
	statusSize  = 13 // font sizes, in pixels
	buttonSize  = 14
	glyphW      = 10
)

// palette is the colours of one system theme, as ARGB.
type palette struct {
	card, text, button, buttonHover, buttonPressed, buttonEdge, hover uint32
	green, amber, red, saved, notSaved                                uint32
}

var (
	darkPalette = palette{
		card: 0xFF2B2B2B, text: 0xFFFFFFFF,
		button: 0x0FFFFFFF, buttonHover: 0x1AFFFFFF, buttonPressed: 0x08FFFFFF, buttonEdge: 0x14FFFFFF, hover: 0x14FFFFFF,
		green: 0xFF6CCB5F, amber: 0xFFFFB900, red: 0xFFC42B1C, saved: 0xFF6CCB5F, notSaved: 0xFFFF99A4,
	}
	lightPalette = palette{
		card: 0xFFF3F3F3, text: 0xFF1B1B1B,
		button: 0xB3FFFFFF, buttonHover: 0xFFFFFFFF, buttonPressed: 0x80F9F9F9, buttonEdge: 0x0F000000, hover: 0x0A000000,
		green: 0xFF0F7B0F, amber: 0xFFC27C0E, red: 0xFFC42B1C, saved: 0xFF0F7B0F, notSaved: 0xFFC42B1C,
	}
)

// Pointer targets on the card.
type part int

const (
	partNone part = iota
	partButton
	partCollapse
)

// scene is everything the card shows at one moment.
type scene struct {
	level      Level
	text       string
	dark       bool
	backdrop   bool // the card's own background is left clear for Windows' acrylic
	collapsed  bool
	hot        part // under the pointer
	pressed    part
	flash      string  // "Saved ✓" or "Not saved" on the button, "" when none
	flashOK    bool    //
	flashAlpha float32 // 1 → 0 as the flash fades
}

func (s scene) palette() palette {
	if s.dark {
		return darkPalette
	}
	return lightPalette
}

func (s scene) stateColor() uint32 {
	p := s.palette()
	switch s.level {
	case Amber:
		return p.amber
	case Red:
		return p.red
	}
	return p.green
}

// size is the window size in logical pixels.
func (s scene) size() (w, h int32) {
	if s.collapsed {
		return tabW, tabH
	}
	return cardW, cardH
}

// Hit areas in logical pixels.
var (
	buttonArea   = rect{Left: pad, Top: buttonTop, Right: cardW - pad, Bottom: buttonTop + buttonH}
	collapseArea = rect{Left: cardW - pad - collapseBox, Top: rowTop + (rowH-collapseBox)/2, Right: cardW - pad, Bottom: rowTop + (rowH+collapseBox)/2}
)

func (s scene) partAt(p point) part {
	switch {
	case s.collapsed:
		return partNone
	case buttonArea.contains(p):
		return partButton
	case collapseArea.contains(p):
		return partCollapse
	}
	return partNone
}

// draw paints the scene at scale (1 at 96 dpi) onto c.
func (s scene) draw(c *canvas, scale float32) {
	p := s.palette()
	px := func(v int32) float32 { return float32(v) * scale }
	if s.backdrop {
		c.clear(0) // see-through: Windows paints acrylic behind
	} else {
		c.clear(p.card)
	}
	if s.collapsed {
		c.roundRect(0, 0, px(tabW), px(tabH), px(tabW)/2, s.stateColor())
		return
	}
	s.drawStatus(c, px)
	s.drawButton(c, px)
}

func (s scene) drawStatus(c *canvas, px func(int32) float32) {
	p := s.palette()
	textColor, dotColor := p.text, s.stateColor()
	if s.level == Red { // the one state that must be impossible to miss
		c.roundRect(px(6), px(6), px(cardW-12), px(rowH+8), px(radius), p.red)
		textColor, dotColor = 0xFFFFFFFF, 0xFFFFFFFF
	}
	c.circle(px(dotX), px(rowTop+rowH/2), px(dotR), dotColor)
	c.text(s.text, px(textX), px(rowTop), px(collapseArea.Left-textX-4), px(rowH), px(statusSize), true, false, textColor)

	if s.hot == partCollapse {
		r := collapseArea
		c.roundRect(px(r.Left), px(r.Top), px(r.width()), px(r.height()), px(4), p.hover)
	}
	// "–": a 10 x 1.5 bar, drawn rather than typed so it sits exactly in the middle
	cx, cy := px((collapseArea.Left+collapseArea.Right)/2), px((collapseArea.Top+collapseArea.Bottom)/2)
	c.roundRect(cx-px(glyphW)/2, cy-px(3)/4, px(glyphW), px(3)/2, px(3)/4, textColor)
}

func (s scene) drawButton(c *canvas, px func(int32) float32) {
	p := s.palette()
	fill := p.button
	switch {
	case s.pressed == partButton:
		fill = p.buttonPressed
	case s.hot == partButton:
		fill = p.buttonHover
	}
	r := buttonArea
	x, y, w, h := px(r.Left), px(r.Top), px(r.width()), px(r.height())
	c.roundRect(x, y, w, h, px(radius), p.buttonEdge)
	c.roundRect(x+1, y+1, w-2, h-2, px(radius)-1, fill)

	label := alpha(p.text, 1-s.flashAlpha)
	c.text("I didn't get this", x, y, w, h, px(buttonSize), true, true, label)
	if s.flash != "" {
		color := p.saved
		if !s.flashOK {
			color = p.notSaved
		}
		c.text(s.flash, x, y, w, h, px(buttonSize), true, true, alpha(color, s.flashAlpha))
	}
}

// alpha scales a colour's opacity by a (0..1).
func alpha(argb uint32, a float32) uint32 {
	a = max(0, min(1, a))
	return uint32(float32(argb>>24)*a)<<24 | argb&0x00FFFFFF
}

// paint draws the scene into a 32-bit bitmap and copies it, alpha and all, onto the window.
func (o *Overlay) paint() {
	var ps paintStruct
	windowDC, _, _ := procBeginPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(o.hwnd, uintptr(unsafe.Pointer(&ps)))

	s := o.scene()
	lw, lh := s.size()
	w, h := o.px(lw), o.px(lh)
	dc, _, _ := procCreateCompatibleDC.Call(windowDC)
	defer procDeleteDC.Call(dc)
	info := bitmapInfo{Size: 40, Width: w, Height: -h, Planes: 1, BitCount: 32} // negative height: top-down rows
	var pixels unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 {
		return
	}
	defer procDeleteObject.Call(bitmap)
	previous, _, _ := procSelectObject.Call(dc, bitmap)
	defer procSelectObject.Call(dc, previous)

	c, err := newCanvas(w, h, pixels)
	if err != nil {
		return
	}
	s.draw(c, float32(o.dpi)/96)
	c.close()
	procBitBlt.Call(windowDC, 0, 0, uintptr(w), uintptr(h), dc, 0, 0, srcCopy)
}

// RenderPNG writes one state of the card to path, at scale (1 = 100%), on the theme's solid
// background, for reviewing the look without a class.
func RenderPNG(path string, level Level, text string, saved, dark bool, scale float32) error {
	s := scene{level: level, text: text, dark: dark}
	if level == Normal {
		s.text = normalText
	}
	if saved {
		s.flash, s.flashOK, s.flashAlpha = flashSaved, true, 1
	}
	w, h := int32(float32(cardW)*scale), int32(float32(cardH)*scale)
	pixels := make([]uint32, w*h)
	c, err := newCanvas(w, h, unsafe.Pointer(&pixels[0]))
	if err != nil {
		return err
	}
	defer c.close()
	s.draw(c, scale)
	return c.savePNG(path)
}

// BITMAPINFOHEADER, enough for a 32-bit DIB.
type bitmapInfo struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
	_                            [4]byte // one RGBQUAD, unused for 32-bit
}
