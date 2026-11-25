package overlay

import (
	"errors"
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GDI+ draws anti-aliased shapes and text. It ships with Windows; its flat C API is called directly.
var (
	gdiplus = windows.NewLazySystemDLL("gdiplus.dll")

	gdipStartup              = gdiplus.NewProc("GdiplusStartup")
	gdipCreateBitmapScan0    = gdiplus.NewProc("GdipCreateBitmapFromScan0")
	gdipGetImageGraphics     = gdiplus.NewProc("GdipGetImageGraphicsContext")
	gdipDeleteGraphics       = gdiplus.NewProc("GdipDeleteGraphics")
	gdipDisposeImage         = gdiplus.NewProc("GdipDisposeImage")
	gdipSetSmoothingMode     = gdiplus.NewProc("GdipSetSmoothingMode")
	gdipSetPixelOffsetMode   = gdiplus.NewProc("GdipSetPixelOffsetMode")
	gdipSetTextRenderingHint = gdiplus.NewProc("GdipSetTextRenderingHint")
	gdipGraphicsClear        = gdiplus.NewProc("GdipGraphicsClear")
	gdipCreateSolidFill      = gdiplus.NewProc("GdipCreateSolidFill")
	gdipDeleteBrush          = gdiplus.NewProc("GdipDeleteBrush")
	gdipCreatePath           = gdiplus.NewProc("GdipCreatePath")
	gdipDeletePath           = gdiplus.NewProc("GdipDeletePath")
	gdipAddPathArc           = gdiplus.NewProc("GdipAddPathArc")
	gdipClosePathFigure      = gdiplus.NewProc("GdipClosePathFigure")
	gdipFillPath             = gdiplus.NewProc("GdipFillPath")
	gdipFillEllipse          = gdiplus.NewProc("GdipFillEllipse")
	gdipCreateFamilyFromName = gdiplus.NewProc("GdipCreateFontFamilyFromName")
	gdipDeleteFontFamily     = gdiplus.NewProc("GdipDeleteFontFamily")
	gdipCreateFont           = gdiplus.NewProc("GdipCreateFont")
	gdipDeleteFont           = gdiplus.NewProc("GdipDeleteFont")
	gdipCreateStringFormat   = gdiplus.NewProc("GdipCreateStringFormat")
	gdipDeleteStringFormat   = gdiplus.NewProc("GdipDeleteStringFormat")
	gdipSetStringAlign       = gdiplus.NewProc("GdipSetStringFormatAlign")
	gdipSetStringLineAlign   = gdiplus.NewProc("GdipSetStringFormatLineAlign")
	gdipSetStringTrimming    = gdiplus.NewProc("GdipSetStringFormatTrimming")
	gdipDrawString           = gdiplus.NewProc("GdipDrawString")
	gdipSaveImageToFile      = gdiplus.NewProc("GdipSaveImageToFile")
)

const (
	pixelFormat32bppPARGB = 0x000E200B
	smoothingAntiAlias    = 4
	pixelOffsetHalf       = 4
	textAntiAliasGridFit  = 3
	unitPixel             = 2
	stringFormatNoWrap    = 0x1000
	stringAlignNear       = 0
	stringAlignCenter     = 1
	trimmingEllipsisChar  = 3
)

// pngEncoder is GDI+'s PNG encoder CLSID, {557CF406-1A04-11D3-9A73-0000F81EF32E}.
var pngEncoder = windows.GUID{Data1: 0x557CF406, Data2: 0x1A04, Data3: 0x11D3, Data4: [8]byte{0x9A, 0x73, 0x00, 0x00, 0xF8, 0x1E, 0xF3, 0x2E}}

var startGDIPlus = sync.OnceValue(func() error {
	input := struct {
		Version                  uint32
		DebugEventCallback       uintptr
		SuppressBackgroundThread int32
		SuppressExternalCodecs   int32
	}{Version: 1}
	var token uintptr
	if status, _, _ := gdipStartup.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&input)), 0); status != 0 {
		return errors.New("gdi+ did not start")
	}
	return nil
})

// f passes a float32 to GDI+. Windows' x64 calling convention takes the first four arguments in
// both integer and XMM registers, and later ones in 8-byte stack slots, so the bits are enough.
func f(v float32) uintptr { return uintptr(math.Float32bits(v)) }

// canvas is an ARGB bitmap GDI+ draws on, premultiplied so it can go straight to the screen.
type canvas struct {
	bitmap, graphics uintptr
}

// newCanvas wraps pixels, width*height 32-bit words, as a drawing surface.
func newCanvas(width, height int32, pixels unsafe.Pointer) (*canvas, error) {
	if err := startGDIPlus(); err != nil {
		return nil, err
	}
	c := &canvas{}
	if s, _, _ := gdipCreateBitmapScan0.Call(uintptr(width), uintptr(height), uintptr(width*4), pixelFormat32bppPARGB,
		uintptr(pixels), uintptr(unsafe.Pointer(&c.bitmap))); s != 0 {
		return nil, errors.New("gdi+ could not make a bitmap")
	}
	gdipGetImageGraphics.Call(c.bitmap, uintptr(unsafe.Pointer(&c.graphics)))
	gdipSetSmoothingMode.Call(c.graphics, smoothingAntiAlias)
	gdipSetPixelOffsetMode.Call(c.graphics, pixelOffsetHalf)
	gdipSetTextRenderingHint.Call(c.graphics, textAntiAliasGridFit)
	return c, nil
}

func (c *canvas) close() {
	gdipDeleteGraphics.Call(c.graphics)
	gdipDisposeImage.Call(c.bitmap)
}

func (c *canvas) clear(argb uint32) { gdipGraphicsClear.Call(c.graphics, uintptr(argb)) }

func (c *canvas) roundRect(x, y, w, h, radius float32, argb uint32) {
	if argb>>24 == 0 {
		return
	}
	var path uintptr
	gdipCreatePath.Call(0, uintptr(unsafe.Pointer(&path)))
	defer gdipDeletePath.Call(path)
	d := 2 * min(radius, w/2, h/2)
	gdipAddPathArc.Call(path, f(x), f(y), f(d), f(d), f(180), f(90))
	gdipAddPathArc.Call(path, f(x+w-d), f(y), f(d), f(d), f(270), f(90))
	gdipAddPathArc.Call(path, f(x+w-d), f(y+h-d), f(d), f(d), f(0), f(90))
	gdipAddPathArc.Call(path, f(x), f(y+h-d), f(d), f(d), f(90), f(90))
	gdipClosePathFigure.Call(path)
	c.withBrush(argb, func(brush uintptr) { gdipFillPath.Call(c.graphics, brush, path) })
}

func (c *canvas) circle(cx, cy, r float32, argb uint32) {
	c.withBrush(argb, func(brush uintptr) {
		gdipFillEllipse.Call(c.graphics, brush, f(cx-r), f(cy-r), f(2*r), f(2*r))
	})
}

// text draws one line inside the box, centred vertically, cut with "…" if it does not fit.
func (c *canvas) text(s string, x, y, w, h, size float32, semibold, centred bool, argb uint32) {
	if argb>>24 == 0 || s == "" {
		return
	}
	font := newFont(size, semibold)
	if font == 0 {
		return
	}
	defer gdipDeleteFont.Call(font)
	var format uintptr
	gdipCreateStringFormat.Call(stringFormatNoWrap, 0, uintptr(unsafe.Pointer(&format)))
	defer gdipDeleteStringFormat.Call(format)
	align := uintptr(stringAlignNear)
	if centred {
		align = stringAlignCenter
	}
	gdipSetStringAlign.Call(format, align)
	gdipSetStringLineAlign.Call(format, stringAlignCenter)
	gdipSetStringTrimming.Call(format, trimmingEllipsisChar)
	box := [4]float32{x, y, w, h}
	utf16, _ := windows.UTF16FromString(s)
	c.withBrush(argb, func(brush uintptr) {
		gdipDrawString.Call(c.graphics, uintptr(unsafe.Pointer(&utf16[0])), uintptr(len(utf16)-1), font,
			uintptr(unsafe.Pointer(&box)), format, brush)
	})
}

func (c *canvas) withBrush(argb uint32, draw func(brush uintptr)) {
	var brush uintptr
	gdipCreateSolidFill.Call(uintptr(argb), uintptr(unsafe.Pointer(&brush)))
	defer gdipDeleteBrush.Call(brush)
	draw(brush)
}

// savePNG writes the canvas to path.
func (c *canvas) savePNG(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if s, _, _ := gdipSaveImageToFile.Call(c.bitmap, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&pngEncoder)), 0); s != 0 {
		return errors.New("gdi+ could not save the png")
	}
	return nil
}

// Fonts, most wanted first: Windows 11's Segoe UI Variable, then Segoe UI on older Windows.
var (
	regularFamilies  = []string{"Segoe UI Variable Text", "Segoe UI"}
	semiboldFamilies = []string{"Segoe UI Variable Text Semibold", "Segoe UI Semibold", "Segoe UI"}
)

// newFont returns a font of size pixels, or 0 if no family exists.
func newFont(size float32, semibold bool) uintptr {
	families := regularFamilies
	if semibold {
		families = semiboldFamilies
	}
	for _, name := range families {
		utf16, _ := windows.UTF16PtrFromString(name)
		var family uintptr
		if s, _, _ := gdipCreateFamilyFromName.Call(uintptr(unsafe.Pointer(utf16)), 0, uintptr(unsafe.Pointer(&family))); s != 0 {
			continue
		}
		var font uintptr
		gdipCreateFont.Call(family, f(size), 0, unitPixel, uintptr(unsafe.Pointer(&font)))
		gdipDeleteFontFamily.Call(family)
		if font != 0 {
			return font
		}
	}
	return 0
}
