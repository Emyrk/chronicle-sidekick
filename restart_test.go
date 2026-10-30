package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestartArgs(t *testing.T) {
	require.Equal(t, []string{"--server", "https://example.com", "--background", "--wait-for-instance"}, restartArgs([]string{"--server", "https://example.com"}))
	require.Equal(t, []string{"--background", "--wait-for-instance"}, restartArgs([]string{"--background", "--wait-for-instance"}))
}

func TestRestartCommandPreservesArgumentsAndEnvironment(t *testing.T) {
	t.Setenv("CHRONICLE_RESTART_TEST", "present")
	cmd := restartCommand("sidekick-new", []string{"--server", "https://example.com"})
	require.Equal(t, []string{"sidekick-new", "--server", "https://example.com"}, cmd.Args)
	require.Contains(t, cmd.Env, "CHRONICLE_RESTART_TEST=present")
	require.Equal(t, os.Environ(), cmd.Env)
}
