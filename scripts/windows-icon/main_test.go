package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateICO(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.png")
	output := filepath.Join(dir, "app.ico")
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 30), G: uint8(y * 30), B: 0x80, A: 0xff})
		}
	}
	var sourcePNG bytes.Buffer
	require.NoError(t, png.Encode(&sourcePNG, img))
	require.NoError(t, os.WriteFile(source, sourcePNG.Bytes(), 0o600))

	require.NoError(t, generate(source, output))
	first, err := os.ReadFile(output)
	require.NoError(t, err)
	require.NoError(t, generate(source, output))
	second, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, first, second)

	require.Equal(t, []byte{0, 0, 1, 0, byte(len(iconSizes)), 0}, first[:6])
	for i, expected := range iconSizes {
		entry := first[6+i*16 : 6+(i+1)*16]
		width := int(entry[0])
		if width == 0 {
			width = 256
		}
		require.Equal(t, expected, width)
		require.Equal(t, entry[0], entry[1])
		require.Equal(t, uint16(1), binary.LittleEndian.Uint16(entry[4:6]))
		require.Equal(t, uint16(32), binary.LittleEndian.Uint16(entry[6:8]))
		offset := binary.LittleEndian.Uint32(entry[12:16])
		require.Equal(t, uint32(40), binary.LittleEndian.Uint32(first[offset:offset+4]))
		require.Equal(t, int32(expected), int32(binary.LittleEndian.Uint32(first[offset+4:offset+8])))
		require.Equal(t, int32(expected*2), int32(binary.LittleEndian.Uint32(first[offset+8:offset+12])))
	}
}
