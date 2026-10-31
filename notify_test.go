package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileExplorerCommand(t *testing.T) {
	t.Parallel()

	path := `/tmp/Chronicle Sidekick`
	for _, tc := range []struct {
		name string
		goos string
		want string
	}{
		{name: "macOS", goos: "darwin", want: "open"},
		{name: "Windows", goos: "windows", want: "explorer.exe"},
		{name: "Linux", goos: "linux", want: "xdg-open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name, args := fileExplorerCommand(tc.goos, path)
			require.Equal(t, tc.want, name)
			require.Equal(t, []string{path}, args)
		})
	}
}
