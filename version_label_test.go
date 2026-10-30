package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionLabel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Version: v0.4.0", versionLabel("0.4.0"))
	require.Equal(t, "Version: v0.4.0", versionLabel("v0.4.0"))
	require.Equal(t, "Version: development build", versionLabel("dev"))
	require.Equal(t, "Version: development build", versionLabel(""))
}
