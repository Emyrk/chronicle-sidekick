package autostart

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEntries(t *testing.T) {
	t.Parallel()
	p := plist(`/Applications/Chronicle & Co/uploader`)
	require.Contains(t, p, "<string>/Applications/Chronicle &amp; Co/uploader</string>")
	require.Contains(t, p, "<key>RunAtLoad</key>")
	require.Contains(t, p, "<string>--background</string>")

	d := desktopEntry(`/home/me/My Apps/uploader`)
	require.Contains(t, d, `Exec="/home/me/My Apps/uploader" --background`)
	require.True(t, strings.HasPrefix(d, "[Desktop Entry]"))
}
