// Command macos-icon renders a square source PNG as a multi-resolution ICNS.
//
// Output is deterministic so re-running the command is a no-op.
//
// Usage: go run ./scripts/macos-icon <logo.png> <app.icns>
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

type iconSize struct {
	typeCode string
	size     int
}

var iconSizes = []iconSize{
	{"icp4", 16},
	{"icp5", 32},
	{"icp6", 64},
	{"ic07", 128},
	{"ic08", 256},
	{"ic09", 512},
	{"ic10", 1024},
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: macos-icon <logo.png> <app.icns>")
		os.Exit(2)
	}
	if err := generate(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

	var chunks bytes.Buffer
	for _, icon := range iconSizes {
		dst := image.NewNRGBA(image.Rect(0, 0, icon.size, icon.size))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, dst); err != nil {
			return fmt.Errorf("encode %d px icon: %w", icon.size, err)
		}
		chunks.WriteString(icon.typeCode)
		if err := binary.Write(&chunks, binary.BigEndian, uint32(encoded.Len()+8)); err != nil {
			return err
		}
		chunks.Write(encoded.Bytes())
	}

	var icns bytes.Buffer
	icns.WriteString("icns")
	if err := binary.Write(&icns, binary.BigEndian, uint32(chunks.Len()+8)); err != nil {
		return err
	}
	icns.Write(chunks.Bytes())
	if err := os.WriteFile(destination, icns.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write icon: %w", err)
	}
	return nil
}
