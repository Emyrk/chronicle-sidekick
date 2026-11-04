// Command uploader-icon renders the canonical Chronicle Sidekick logo into the
// committed platform tray assets. The branded C is emitted as the static body,
// while the three-dot mark is emitted as an alpha mask.
//
// Output is deterministic so re-running the command is a no-op.
//
// Usage: go run ./scripts/uploader-icon <logo.png> <tray-dir>
package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"

	_ "golang.org/x/image/webp"
)

var gold = color.NRGBA{R: 0xd7, G: 0xb6, B: 0x6f, A: 255}

type logo struct {
	img *image.NRGBA
}

func loadLogo(path string) *logo {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	decoded, _, err := image.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != bounds.Dy() {
		log.Fatalf("tray artwork must be square, got %v", bounds)
	}
	img := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			img.Set(x, y, decoded.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return &logo{img: img}
}

// logoAmount separates the gold artwork from its charcoal background. The wide
// ramp retains antialiasing while dropping the source background.
func logoAmount(c color.NRGBA) float64 {
	luma := (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
	return math.Max(0, math.Min(1, (luma-0.27)/0.24)) * float64(c.A) / 255
}

// markRegion isolates the three circles from the branded Chronicle C. Values
// are normalized to the canonical 512 px artwork so the renderer remains
// deterministic if a larger square export is supplied later.
func markRegion(x, y, size int) bool {
	px := float64(x) * 512 / float64(size)
	py := float64(y) * 512 / float64(size)
	for _, dot := range []struct{ x, y float64 }{{256, 210}, {211, 292}, {301, 292}} {
		if math.Hypot(px-dot.x, py-dot.y) <= 40 {
			return true
		}
	}
	return false
}

const samples = 8

func (l *logo) renderBody(size int, bodyColor color.NRGBA) *image.NRGBA {
	return l.render(size, false, bodyColor)
}

func (l *logo) renderMarkMask(size int) *image.NRGBA {
	return l.render(size, true, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
}

func (l *logo) render(size int, mark bool, fill color.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	sourceSize := l.img.Bounds().Dx()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var alpha float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/samples) / float64(size)
					v := (float64(y) + (float64(sy)+0.5)/samples) / float64(size)
					px := min(int(u*float64(sourceSize)), sourceSize-1)
					py := min(int(v*float64(sourceSize)), sourceSize-1)
					isMark := markRegion(px, py, sourceSize)
					if mark && isMark {
						alpha++
					} else if !mark && !isMark {
						alpha += logoAmount(l.img.NRGBAAt(px, py))
					}
				}
			}
			const count = samples * samples
			fill.A = uint8(alpha/count*255 + 0.5)
			out.SetNRGBA(x, y, fill)
		}
	}
	return out
}

func encode(img image.Image) []byte {
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&out, img); err != nil {
		panic(err)
	}
	return out.Bytes()
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: uploader-icon <logo.png> <tray-dir>")
	}
	l := loadLogo(os.Args[1])
	trayDir := os.Args[2]
	outputs := map[string][]byte{
		"logo_black_36.png":     encode(l.renderBody(36, color.NRGBA{A: 255})),
		"logo_white_36.png":     encode(l.renderBody(36, color.NRGBA{R: 255, G: 255, B: 255, A: 255})),
		"logo_color_16.png":     encode(l.renderBody(16, gold)),
		"logo_color_32.png":     encode(l.renderBody(32, gold)),
		"logo_mark_mask_16.png": encode(l.renderMarkMask(16)),
		"logo_mark_mask_32.png": encode(l.renderMarkMask(32)),
		"logo_mark_mask_36.png": encode(l.renderMarkMask(36)),
	}
	for name, data := range outputs {
		if err := os.WriteFile(filepath.Join(trayDir, name), data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
