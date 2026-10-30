package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/engine"
)

var allStates = []engine.State{engine.StateIdle, engine.StateNoLog, engine.StateWaiting,
	engine.StateRecording, engine.StateUploading, engine.StateUploaded, engine.StateReconnect}

func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

// checkIcon asserts that the platform logo remains intact and the state is
// carried by a bright, dark-outlined dot.
func checkIcon(t *testing.T, s engine.State, img image.Image, body image.Image, robot image.Image, foreground color.NRGBA, msg string) {
	t.Helper()
	outer, inner := statusDotMasks(img.Bounds())
	robotPixels, bodyPixels, outlinePixels, statePixels := 0, 0, 0, 0
	b := robot.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			bodyPixel := nrgbaAt(body, x, y)
			robotPixel := nrgbaAt(robot, x, y)
			iconPixel := nrgbaAt(img, x, y)
			outerAlpha := outer.AlphaAt(x, y).A
			innerAlpha := inner.AlphaAt(x, y).A
			if robotPixel.A == 255 && outerAlpha == 0 {
				robotPixels++
				require.Equal(t, foreground, iconPixel, "%s: robot pixel (%d,%d)", msg, x, y)
			}
			if bodyPixel.A == 255 && robotPixel.A == 0 && outerAlpha == 0 {
				bodyPixels++
			}
			if outerAlpha > innerAlpha {
				outlinePixels++
			}
			if innerAlpha == 255 {
				statePixels++
				require.Equal(t, stateColors[s], iconPixel, "%s: state dot pixel (%d,%d)", msg, x, y)
			}
		}
	}
	require.NotZero(t, robotPixels, "%s: no opaque robot pixel", msg)
	require.NotZero(t, bodyPixels, "%s: no opaque arrow pixel", msg)
	require.NotZero(t, outlinePixels, "%s: no opaque dot outline pixel", msg)
	require.NotZero(t, statePixels, "%s: no opaque state dot pixel", msg)
}

func TestStateColors(t *testing.T) {
	t.Parallel()
	want := map[engine.State]color.NRGBA{
		engine.StateIdle:      {0x9c, 0xa3, 0xaf, 0xff},
		engine.StateNoLog:     {0xf4, 0x3f, 0x5e, 0xff},
		engine.StateWaiting:   {0xfb, 0x92, 0x3c, 0xff},
		engine.StateRecording: {0x22, 0xc5, 0x5e, 0xff},
		engine.StateUploading: {0x3b, 0x82, 0xf6, 0xff},
		engine.StateUploaded:  {0x22, 0xc5, 0x5e, 0xff},
		engine.StateReconnect: {0xfa, 0xcc, 0x15, 0xff},
	}
	require.Equal(t, want, stateColors)
}

func TestTrayIconShowsBrightStateDot(t *testing.T) {
	t.Parallel()
	masks := map[int][]byte{16: logoMarkMask16, 32: logoMarkMask32, 36: logoMarkMask36}
	for _, s := range allStates {
		require.NotEmpty(t, trayIcon(s), "state %v", s)
		for _, dark := range []bool{false, true} {
			bodies := map[int][]byte{16: logoColor16, 32: logoColor32, 36: logoBlack}
			foregrounds := map[int]color.NRGBA{16: logoGold, 32: logoGold, 36: {A: 0xff}}
			if dark {
				bodies[36] = logoWhite
				foregrounds[36] = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
			}
			for _, i := range buildIcons(s, dark) {
				img, err := png.Decode(bytes.NewReader(i.data))
				require.NoError(t, err)
				checkIcon(t, s, img, decodePNG(bodies[i.size]), decodePNG(masks[i.size]), foregrounds[i.size], "buildIcons")
			}
		}
		// The macOS path, whatever platform the tests run on.
		for _, tc := range []struct {
			body       []byte
			foreground color.NRGBA
		}{
			{logoBlack, color.NRGBA{A: 0xff}},
			{logoWhite, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}},
		} {
			checkIcon(t, s, composeIcon(tc.body, logoMarkMask36, tc.foreground, stateColors[s]), decodePNG(tc.body), decodePNG(logoMarkMask36), tc.foreground, "mono")
		}
	}
}

// TestContactSheet writes the proof sheet to $UPLOADER_ICON_SHEET: six states
// (uploaded shares green with recording) on a light and a dark menu bar, at
// 36 px and at 4x. Skipped unless the variable is set; not committed.
func TestContactSheet(t *testing.T) {
	path := os.Getenv("UPLOADER_ICON_SHEET")
	if path == "" {
		t.Skip("UPLOADER_ICON_SHEET not set")
	}
	states := []engine.State{engine.StateIdle, engine.StateNoLog, engine.StateWaiting,
		engine.StateRecording, engine.StateUploading, engine.StateReconnect}
	const cell, pad = 36 * 4, 12
	w := pad + len(states)*(cell+pad)
	rowH := 36 + pad + cell + pad
	sheet := image.NewNRGBA(image.Rect(0, 0, w, 2*rowH))
	bars := []struct {
		bg, foreground color.NRGBA
		body           []byte
	}{
		{color.NRGBA{0xe6, 0xe6, 0xe6, 0xff}, color.NRGBA{A: 0xff}, logoBlack},
		{color.NRGBA{0x2b, 0x2b, 0x2b, 0xff}, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, logoWhite},
	}
	for r, bar := range bars {
		draw.Draw(sheet, image.Rect(0, r*rowH, w, (r+1)*rowH), image.NewUniform(bar.bg), image.Point{}, draw.Src)
		for i, s := range states {
			icon := composeIcon(bar.body, logoMarkMask36, bar.foreground, stateColors[s])
			x := pad + i*(cell+pad)
			y := r * rowH
			draw.Draw(sheet, image.Rect(x, y+pad/2, x+36, y+pad/2+36), icon, image.Point{}, draw.Over)
			big := image.Rect(x, y+36+pad, x+cell, y+36+pad+cell)
			for py := 0; py < cell; py++ {
				for px := 0; px < cell; px++ {
					p := nrgbaAt(icon, px/4, py/4)
					draw.Draw(sheet, image.Rect(big.Min.X+px, big.Min.Y+py, big.Min.X+px+1, big.Min.Y+py+1), image.NewUniform(p), image.Point{}, draw.Over)
				}
			}
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, sheet))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}
