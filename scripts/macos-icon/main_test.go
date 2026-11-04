package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateICNS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.png")
	destination := filepath.Join(dir, "app.icns")
	bounded := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			bounded.SetNRGBA(x, y, color.NRGBA{R: 0xd7, G: 0xb6, B: 0x6f, A: 0xff})
		}
	}
	f, err := os.Create(source)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, bounded))
	require.NoError(t, f.Close())

	require.NoError(t, generate(source, destination))
	got, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, "icns", string(got[:4]))
	require.Equal(t, uint32(len(got)), binary.BigEndian.Uint32(got[4:8]))
	for _, typeCode := range []string{"icp4", "icp5", "icp6", "ic07", "ic08", "ic09", "ic10"} {
		require.Contains(t, string(got), typeCode)
	}
}
