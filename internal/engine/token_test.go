package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

func TestTokenChangedRefreshesBeforeReleasingBlockedSnapshots(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"fresh", "rejected", ""} {
		t.Run("token="+token, func(t *testing.T) {
			t.Parallel()
			st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
			require.NoError(t, err)
			var e *Engine
			e = New(Config{
				Store:          st,
				Installs:       func() []wowdir.Install { return nil },
				RunningInstall: func(context.Context, wowdir.Install) bool { return false },
				Token: func() string {
					// No worker may consume blocked snapshots before this refresh.
					require.Empty(t, e.pending)
					require.Len(t, e.blocked, 1)
					return token
				},
			})
			// Run only the poll transition, without scheduling a worker. This
			// makes the refresh-before-release invariant deterministic.
			e.workerOnce.Do(func() {})
			e.token, e.rejectedToken, e.tokenLoaded = "rejected", "rejected", true
			blocked := job{path: "live-log", snapshotPath: "immutable-log", groupID: "group"}
			e.blocked = []job{blocked}

			e.TokenChanged()
			require.Empty(t, e.pending)
			require.Equal(t, []job{blocked}, e.blocked)
			require.Empty(t, e.kick)
			e.step(context.Background(), time.Now(), false)

			require.Equal(t, token, e.token)
			if token == "fresh" {
				require.Equal(t, []job{blocked}, e.pending)
				require.Empty(t, e.blocked)
			} else {
				require.Empty(t, e.pending)
				require.Equal(t, []job{blocked}, e.blocked)
			}
		})
	}
}
