package watch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
)

func TestSystemProcsExistsChecksImageName(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	s := &systemProcs{}
	t.Cleanup(s.close)
	ctx := context.Background()
	pid := int32(os.Getpid())

	ok, err := s.Exists(ctx, pid, "not-the-test-process.exe")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, s.h, "a mismatched process must not be cached")

	for range 2 {
		ok, err = s.Exists(ctx, pid, filepath.Base(exe))
		require.NoError(t, err)
		require.True(t, ok)
	}
}

// Opt in with a running legacy client whose process permissions reject
// PROCESS_QUERY_LIMITED_INFORMATION but allow PROCESS_QUERY_INFORMATION.
func TestSystemProcsLegacyClient(t *testing.T) {
	value := os.Getenv("SIDEKICK_TEST_WOW_PID")
	if value == "" {
		t.Skip("set SIDEKICK_TEST_WOW_PID to exercise a running legacy client")
	}
	pid, err := strconv.ParseInt(value, 10, 32)
	require.NoError(t, err)
	s := &systemProcs{}
	t.Cleanup(s.close)
	exe, err := s.Exe(context.Background(), int32(pid))
	require.NoError(t, err)
	for range 2 {
		ok, err := s.Exists(context.Background(), int32(pid), filepath.Base(exe))
		require.NoError(t, err)
		require.True(t, ok)
	}
	g := NewGameForInstall(tenant.DefaultLayout, s)
	for range 2 {
		require.True(t, g.Running(context.Background(), []string{filepath.Dir(exe)}))
	}
}
