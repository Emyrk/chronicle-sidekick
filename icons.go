package main

//go:generate go run ./scripts/uploader-icon icons/tray_logo_original.webp icons
//go:generate go run ./scripts/windows-icon packaging/chronicle-logo.png packaging/AppIcon.ico
//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico packaging/AppIcon.ico -o rsrc_windows_amd64.syso
//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch arm64 -ico packaging/AppIcon.ico -o rsrc_windows_arm64.syso

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync"

	"github.com/Emyrk/chronicle-sidekick/internal/engine"
)

// The logo bodies and mark masks are rendered from icons/tray_logo_original.webp
// (go generate). The mask is white with alpha; it is tinted with the state colour
// and composited over the gold arrows.
var (
	//go:embed icons/logo_black_36.png
	logoBlack []byte // macOS body for a light menu bar, 36 px = 18 pt @2x
	//go:embed icons/logo_white_36.png
	logoWhite []byte // macOS body for a dark menu bar
	//go:embed icons/logo_color_16.png
	logoColor16 []byte
	//go:embed icons/logo_color_32.png
	logoColor32 []byte
	//go:embed icons/logo_mark_mask_16.png
	logoMarkMask16 []byte
	//go:embed icons/logo_mark_mask_32.png
	logoMarkMask32 []byte
	//go:embed icons/logo_mark_mask_36.png
	logoMarkMask36 []byte
)

var stateColors = map[engine.State]color.NRGBA{
	engine.StateIdle:       {0x9c, 0xa3, 0xaf, 0xff}, // gray: waiting for WoW
	engine.StateNoLog:      {0xf4, 0x3f, 0x5e, 0xff}, // red: combat logging not detected
	engine.StateWaiting:    {0xfb, 0x92, 0x3c, 0xff}, // orange: combat logging started, waiting for data
	engine.StateRecording:  {0x22, 0xc5, 0x5e, 0xff}, // green: combat log receiving data
	engine.StateFinalizing: {0xa8, 0x55, 0xf7, 0xff}, // purple: finalizing combat log
	engine.StateUploading:  {0x3b, 0x82, 0xf6, 0xff}, // blue: uploading or retrying
	engine.StateUploaded:   {0x22, 0xc5, 0x5e, 0xff}, // green: upload complete
	engine.StateReconnect:  {0xfa, 0xcc, 0x15, 0xff}, // yellow: Chronicle sign-in required
}

var logoGold = color.NRGBA{R: 0xd7, G: 0xb6, B: 0x6f, A: 0xff}

type iconKey struct {
	state engine.State
	dark  bool
}

var (
	iconMu    sync.Mutex
	iconCache = map[iconKey][]byte{}
)

// trayIcon returns the Chronicle logo with a bright state dot, in the
// platform's icon format.
func trayIcon(s engine.State) []byte {
	dark := menuBarDark()
	iconMu.Lock()
	defer iconMu.Unlock()
	key := iconKey{s, dark}
	if b, ok := iconCache[key]; ok {
		return b
	}
	b := platformIcon(buildIcons(s, dark))
	iconCache[key] = b
	return b
}

type sizedPNG struct {
	data []byte
	size int
}

// buildIcons composites the platform logo and a bright state dot.
func buildIcons(s engine.State, dark bool) []sizedPNG {
	type pair struct {
		body, mask []byte
		foreground color.NRGBA
	}
	pairs := []pair{
		{logoColor16, logoMarkMask16, logoGold},
		{logoColor32, logoMarkMask32, logoGold},
	}
	if monoGlyph {
		pairs = []pair{{logoBlack, logoMarkMask36, color.NRGBA{A: 0xff}}}
		if dark {
			pairs = []pair{{logoWhite, logoMarkMask36, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}}}
		}
	}
	out := make([]sizedPNG, 0, len(pairs))
	for _, p := range pairs {
		img := composeIcon(p.body, p.mask, p.foreground, stateColors[s])
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		out = append(out, sizedPNG{buf.Bytes(), img.Bounds().Dx()})
	}
	return out
}

func composeIcon(bodyPNG, maskPNG []byte, foreground, state color.NRGBA) *image.NRGBA {
	body := decodePNG(bodyPNG)
	robot := decodePNG(maskPNG)
	dst := image.NewNRGBA(body.Bounds())
	draw.Draw(dst, dst.Bounds(), body, image.Point{}, draw.Src)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(foreground), image.Point{}, robot, image.Point{}, draw.Over)
	outer, inner := statusDotMasks(body.Bounds())
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(color.NRGBA{R: 0x20, G: 0x21, B: 0x24, A: 0xff}), image.Point{}, outer, image.Point{}, draw.Over)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(state), image.Point{}, inner, image.Point{}, draw.Over)
	return dst
}

func statusDotMasks(bounds image.Rectangle) (*image.Alpha, *image.Alpha) {
	w, h := float64(bounds.Dx()), float64(bounds.Dy())
	cx := float64(bounds.Min.X) + w*0.78
	cy := float64(bounds.Min.Y) + h*0.78
	return circleMask(bounds, cx, cy, w*0.20), circleMask(bounds, cx, cy, w*0.14)
}

func circleMask(bounds image.Rectangle, cx, cy, radius float64) *image.Alpha {
	const samples = 4
	mask := image.NewAlpha(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			inside := 0
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					px := float64(x) + (float64(sx)+0.5)/samples
					py := float64(y) + (float64(sy)+0.5)/samples
					if math.Hypot(px-cx, py-cy) <= radius {
						inside++
					}
				}
			}
			mask.SetAlpha(x, y, color.Alpha{A: uint8(inside * 255 / (samples * samples))})
		}
	}
	return mask
}

func decodePNG(b []byte) image.Image {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		panic(err) // embedded asset
	}
	return img
}
