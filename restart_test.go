package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestartCommandPreservesArgumentsAndEnvironment(t *testing.T) {
	t.Setenv("CHRONICLE_RESTART_TEST", "present")
	cmd := restartCommand("sidekick-new", []string{"--server", "https://example.com"})
	require.Equal(t, []string{"sidekick-new", "--server", "https://example.com"}, cmd.Args)
	require.Contains(t, cmd.Env, "CHRONICLE_RESTART_TEST=present")
	require.Equal(t, os.Environ(), cmd.Env)
}
