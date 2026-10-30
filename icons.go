package main

//go:generate go run ./scripts/uploader-icon icons/tray_logo_original.webp icons

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
	engine.StateIdle:      {0x4b, 0x55, 0x63, 0xff}, // muted gray: waiting for WoW
	engine.StateNoLog:     {0x7f, 0x3f, 0x46, 0xff}, // muted red: combat logging not detected
	engine.StateWaiting:   {0x8a, 0x5a, 0x32, 0xff}, // muted orange: combat logging started, waiting for data
	engine.StateRecording: {0x35, 0x6b, 0x4b, 0xff}, // muted green: combat log receiving data
	engine.StateUploading: {0x3f, 0x62, 0x8b, 0xff}, // muted blue: uploading or retrying
	engine.StateUploaded:  {0x35, 0x6b, 0x4b, 0xff}, // muted green: upload complete
	engine.StateReconnect: {0x80, 0x6d, 0x36, 0xff}, // muted yellow: Chronicle sign-in required
}

type iconKey struct {
	state engine.State
	dark  bool
}

var (
	iconMu    sync.Mutex
	iconCache = map[iconKey][]byte{}
)

// trayIcon returns the Chronicle logo with a muted state-coloured background,
// in the platform's icon format.
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

// buildIcons composites a muted state background, the platform body and a
// high-contrast white robot.
func buildIcons(s engine.State, dark bool) []sizedPNG {
	type pair struct{ body, mask []byte }
	pairs := []pair{{logoColor16, logoMarkMask16}, {logoColor32, logoMarkMask32}}
	if monoGlyph {
		pairs = []pair{{logoBlack, logoMarkMask36}}
		if dark {
			pairs = []pair{{logoWhite, logoMarkMask36}}
		}
	}
	out := make([]sizedPNG, 0, len(pairs))
	for _, p := range pairs {
		img := composeIcon(p.body, p.mask, stateColors[s])
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		out = append(out, sizedPNG{buf.Bytes(), img.Bounds().Dx()})
	}
	return out
}

func composeIcon(bodyPNG, maskPNG []byte, background color.NRGBA) *image.NRGBA {
	body := decodePNG(bodyPNG)
	robot := decodePNG(maskPNG)
	dst := image.NewNRGBA(body.Bounds())
	stateMask := stateBackgroundMask(body.Bounds())
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(background), image.Point{}, stateMask, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), body, image.Point{}, draw.Over)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(color.NRGBA{255, 255, 255, 255}), image.Point{}, robot, image.Point{}, draw.Over)
	return dst
}

// stateBackgroundMask fills the circular area inside the arrows. Supersampling
// keeps its edge smooth at 16 px without adding another embedded asset.
func stateBackgroundMask(bounds image.Rectangle) *image.Alpha {
	const samples = 4
	mask := image.NewAlpha(bounds)
	w, h := float64(bounds.Dx()), float64(bounds.Dy())
	cx, cy := float64(bounds.Min.X)+w*0.5, float64(bounds.Min.Y)+h*0.5
	rx, ry := w*0.32, h*0.32
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			inside := 0
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/samples - cx) / rx
					py := (float64(y) + (float64(sy)+0.5)/samples - cy) / ry
					if math.Hypot(px, py) <= 1 {
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
