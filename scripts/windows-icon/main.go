// Command windows-icon renders a source PNG as a multi-resolution Windows ICO.
//
// Output is deterministic so re-running the command is a no-op.
//
// Usage: go run ./scripts/windows-icon <logo.png> <app.ico>
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"
)

var iconSizes = []int{16, 24, 32, 48, 64, 128, 256}

type iconImage struct {
	size int
	data []byte
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: windows-icon <logo.png> <app.ico>")
		os.Exit(2)
	}
	if err := generate(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ColorsUsed    uint32
	ColorsNeeded  uint32
}

func encodeDIB(img *image.NRGBA) ([]byte, error) {
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	pixelBytes := width * height * 4
	maskStride := ((width + 31) / 32) * 4
	var encoded bytes.Buffer
	if err := binary.Write(&encoded, binary.LittleEndian, bitmapInfoHeader{
		Size: 40, Width: int32(width), Height: int32(height * 2),
		Planes: 1, BitCount: 32, SizeImage: uint32(pixelBytes),
	}); err != nil {
		return nil, err
	}
	for y := height - 1; y >= 0; y-- {
		for x := 0; x < width; x++ {
			pixel := img.NRGBAAt(x, y)
			encoded.Write([]byte{pixel.B, pixel.G, pixel.R, pixel.A})
		}
	}
	encoded.Write(make([]byte, maskStride*height))
	return encoded.Bytes(), nil
}

func generate(source, destination string) error {
	f, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source icon: %w", err)
	}
	defer f.Close()

	src, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("decode source icon: %w", err)
	}
	if src.Bounds().Dx() != src.Bounds().Dy() {
		return fmt.Errorf("source icon must be square, got %v", src.Bounds())
	}

	images := make([]iconImage, 0, len(iconSizes))
	for _, size := range iconSizes {
		dst := image.NewNRGBA(image.Rect(0, 0, size, size))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		data, err := encodeDIB(dst)
		if err != nil {
			return fmt.Errorf("encode %d px icon: %w", size, err)
		}
		images = append(images, iconImage{size: size, data: data})
	}

	var ico bytes.Buffer
	if err := binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, uint16(len(images))}); err != nil {
		return err
	}
	offset := 6 + 16*len(images)
	for _, img := range images {
		dimension := byte(img.size)
		if img.size == 256 {
			dimension = 0
		}
		ico.Write([]byte{dimension, dimension, 0, 0})
		if err := binary.Write(&ico, binary.LittleEndian, []uint16{1, 32}); err != nil {
			return err
		}
		if err := binary.Write(&ico, binary.LittleEndian, uint32(len(img.data))); err != nil {
			return err
		}
		if err := binary.Write(&ico, binary.LittleEndian, uint32(offset)); err != nil {
			return err
		}
		offset += len(img.data)
	}
	for _, img := range images {
		ico.Write(img.data)
	}
	if err := os.WriteFile(destination, ico.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write icon: %w", err)
	}
	return nil
}
