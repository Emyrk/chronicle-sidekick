// Command uploader-icon renders the supplied Chronicle tray artwork into the
// committed platform assets. The gold arrows remain static, while the robot is
// emitted as an alpha mask that Sidekick tints with its current state colour.
//
// Output is deterministic so re-running the command is a no-op.
//
// Usage: go run ./scripts/uploader-icon <logo.webp> <tray-dir>
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

var (
	artworkCrop = image.Rect(238, 214, 998, 974)
	gold        = color.NRGBA{R: 0xd7, G: 0xb6, B: 0x6f, A: 255}
)

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
	img := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			img.Set(x, y, decoded.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	if !artworkCrop.In(img.Bounds()) {
		log.Fatalf("tray artwork is %v, need at least %v", img.Bounds(), artworkCrop)
	}
	return &logo{img: img}
}

// logoAmount separates the bright gold artwork from its charcoal background.
// The wide ramp retains antialiasing while dropping the source background.
func logoAmount(c color.NRGBA) float64 {
	luma := (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
	return math.Max(0, math.Min(1, (luma-0.27)/0.24)) * float64(c.A) / 255
}

// robotRegion isolates the robot from the surrounding arrows. Two rectangles
// avoid catching the arrow ring around the antenna and body corners.
func robotRegion(x, y int) bool {
	body := x >= 408 && x <= 848 && y >= 494 && y <= 769
	antenna := x >= 568 && x <= 688 && y >= 379 && y <= 524
	return body || antenna
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
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var alpha float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/samples) / float64(size)
					v := (float64(y) + (float64(sy)+0.5)/samples) / float64(size)
					px := min(artworkCrop.Min.X+int(u*float64(artworkCrop.Dx())), artworkCrop.Max.X-1)
					py := min(artworkCrop.Min.Y+int(v*float64(artworkCrop.Dy())), artworkCrop.Max.Y-1)
					if robotRegion(px, py) == mark {
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
		log.Fatal("usage: uploader-icon <logo.webp> <tray-dir>")
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
