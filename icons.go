package main

//go:generate go run ./scripts/uploader-icon packaging/chronicle-logo.png icons

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"

	"github.com/Emyrk/chronicle-sidekick/internal/engine"
)

// The logo bodies and mark masks are rendered from packaging/chronicle-logo.png
// (make icons). The mask is white with alpha; it is tinted with the state colour
// and composited over the body.
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
	engine.StateIdle:      {0x8a, 0x8f, 0x98, 0xff}, // grey: waiting for WoW
	engine.StateNoLog:     {0xe5, 0x48, 0x4d, 0xff}, // red: WoW running, not logging
	engine.StateWaiting:   {0xf9, 0x73, 0x16, 0xff}, // orange: logging on, waiting for a fight
	engine.StateRecording: {0x22, 0xc5, 0x5e, 0xff}, // green: recording
	engine.StateUploading: {0x3b, 0x82, 0xf6, 0xff}, // blue: uploading
	engine.StateUploaded:  {0x22, 0xc5, 0x5e, 0xff}, // green: uploaded
	engine.StateReconnect: {0xf2, 0xc1, 0x2e, 0xff}, // yellow: not connected
}

type iconKey struct {
	state engine.State
	dark  bool
}

var (
	iconMu    sync.Mutex
	iconCache = map[iconKey][]byte{}
)

// trayIcon returns the Chronicle logo with its mark in the state's colour, in
// the platform's icon format.
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

// buildIcons composites the tinted logo mark onto the body or bodies this
// platform uses.
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
		img := markOnBody(p.body, p.mask, stateColors[s])
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		out = append(out, sizedPNG{buf.Bytes(), img.Bounds().Dx()})
	}
	return out
}

// markOnBody paints the mask tinted with c over the body.
func markOnBody(bodyPNG, maskPNG []byte, c color.NRGBA) *image.NRGBA {
	body := decodePNG(bodyPNG)
	mask := decodePNG(maskPNG)
	dst := image.NewNRGBA(body.Bounds())
	draw.Draw(dst, dst.Bounds(), body, image.Point{}, draw.Src)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
	return dst
}

func decodePNG(b []byte) image.Image {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		panic(err) // embedded asset
	}
	return img
}
