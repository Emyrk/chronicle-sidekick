// Command uploader-icon renders the Chronicle logo into the committed tray
// assets. The dark logo disc is emitted as the tray body, while the gold mark
// becomes an alpha mask that Sidekick tints with its current state colour.
//
// Output is deterministic so re-running the command is a no-op.
//
// Usage: go run ./scripts/uploader-icon <logo.png> <tray-dir>
package main

import (
	"bytes"
	"encoding/binary"
	"hash/adler32"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

type rgb = [3]float64

var (
	logoBackground = rgb{0x2b, 0x2b, 0x2b}
	logoMark       = rgb{0xc1, 0xa4, 0x6e}
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
	decoded, err := png.Decode(f)
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
	return &logo{img: img}
}

// markAmount estimates how far a source pixel lies between the dark logo disc
// and the gold logo mark. This retains the source artwork's antialiasing while
// allowing the mark to be recoloured at runtime.
func markAmount(c color.NRGBA) float64 {
	var dot, length float64
	for i, v := range []float64{float64(c.R), float64(c.G), float64(c.B)} {
		d := logoMark[i] - logoBackground[i]
		dot += (v - logoBackground[i]) * d
		length += d * d
	}
	return math.Max(0, math.Min(1, dot/length))
}

const samples = 8

// renderBody paints the logo disc in bodyColour. The state-coloured mark is
// composited over this image by the application.
func (l *logo) renderBody(size int, bodyColour color.NRGBA) *image.NRGBA {
	return l.render(size, func(c color.NRGBA) color.NRGBA {
		bodyColour.A = c.A
		return bodyColour
	})
}

// renderMarkMask emits the gold logo mark as white alpha.
func (l *logo) renderMarkMask(size int) *image.NRGBA {
	return l.render(size, func(c color.NRGBA) color.NRGBA {
		return color.NRGBA{R: 255, G: 255, B: 255, A: uint8(float64(c.A)*markAmount(c) + 0.5)}
	})
}

func (l *logo) render(size int, paint func(color.NRGBA) color.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	bounds := l.img.Bounds()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sum [4]float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/samples) / float64(size)
					v := (float64(y) + (float64(sy)+0.5)/samples) / float64(size)
					px := min(int(u*float64(bounds.Dx())), bounds.Dx()-1)
					py := min(int(v*float64(bounds.Dy())), bounds.Dy()-1)
					c := paint(l.img.NRGBAAt(px, py))
					sum[0] += float64(c.R)
					sum[1] += float64(c.G)
					sum[2] += float64(c.B)
					sum[3] += float64(c.A)
				}
			}
			const count = samples * samples
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(sum[0]/count + 0.5),
				G: uint8(sum[1]/count + 0.5),
				B: uint8(sum[2]/count + 0.5),
				A: uint8(sum[3]/count + 0.5),
			})
		}
	}
	return out
}

// encode writes img as an 8-bit RGBA PNG with every byte hand-framed (filter 0,
// one IDAT of stored deflate blocks), so output does not depend on the Go
// version's image/png or compress/zlib.
func encode(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	raw := make([]byte, 0, h*(1+4*w))
	for y := 0; y < h; y++ {
		raw = append(raw, 0) // filter: none
		raw = append(raw, img.Pix[y*img.Stride:y*img.Stride+4*w]...)
	}

	// zlib: header, stored blocks (<= 65535 bytes), adler32.
	z := []byte{0x78, 0x01}
	for off := 0; ; {
		n := min(len(raw)-off, 65535)
		final := byte(0)
		if off+n == len(raw) {
			final = 1
		}
		z = append(z, final, byte(n), byte(n>>8), ^byte(n), ^byte(n>>8))
		z = append(z, raw[off:off+n]...)
		off += n
		if final == 1 {
			break
		}
	}
	z = binary.BigEndian.AppendUint32(z, adler32.Checksum(raw))

	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(data)))
		start := out.Len()
		out.WriteString(typ)
		out.Write(data)
		_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(out.Bytes()[start:]))
	}
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, 6, 0, 0, 0) // 8-bit, RGBA, deflate, filter 0, no interlace
	chunk("IHDR", ihdr)
	chunk("IDAT", z)
	chunk("IEND", nil)
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
		"logo_color_16.png":     encode(l.renderBody(16, color.NRGBA{R: 0x2b, G: 0x2b, B: 0x2b, A: 255})),
		"logo_color_32.png":     encode(l.renderBody(32, color.NRGBA{R: 0x2b, G: 0x2b, B: 0x2b, A: 255})),
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
