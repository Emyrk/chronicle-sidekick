//go:build !windows

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcquireInstanceExcludesSecondProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidekick.lock")
	first, err := acquireInstance(path)
	require.NoError(t, err)

	_, err = acquireInstance(path)
	require.ErrorIs(t, err, errAlreadyRunning)

	require.NoError(t, first.Close())
	second, err := acquireInstance(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
}

func TestAcquireInstanceWithWaitTakesOverAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidekick.lock")
	first, err := acquireInstance(path)
	require.NoError(t, err)

	type result struct {
		lock instanceLock
		err  error
	}
	results := make(chan result, 1)
	go func() {
		lock, err := acquireInstanceWithWait(path, true)
		results <- result{lock: lock, err: err}
	}()

	require.NoError(t, first.Close())
	var got result
	select {
	case got = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting instance did not acquire the released lock")
	}
	require.NoError(t, got.err)
	require.NotNil(t, got.lock)
	require.NoError(t, got.lock.Close())
}
