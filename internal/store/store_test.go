package store_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
)

func TestLedgerRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := store.Open(path)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.Update(func(st *store.State) {
		st.RecordLog("chr1-1-aa", store.Log{Size: 10, ReportURL: "https://x/logs/1", At: now})
	}))

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, store.Log{Size: 10, ReportURL: "https://x/logs/1", At: now}, again.Get().Logs["chr1-1-aa"])

	// Get hands out a copy; writing to it must not reach the store.
	again.Get().Logs["chr1-1-aa"] = store.Log{}
	require.Equal(t, int64(10), again.Get().Logs["chr1-1-aa"].Size)
}

func TestWoWPathsRoundTripAndCopy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Update(func(st *store.State) {
		st.WoWPaths = []string{"/wow/capy", "/wow/warmane"}
		st.WoWPathsSet = true
	}))

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, []string{"/wow/capy", "/wow/warmane"}, again.Get().WoWPaths)
	require.True(t, again.Get().WoWPathsSet)
	copy := again.Get()
	copy.WoWPaths[0] = "/changed"
	require.Equal(t, "/wow/capy", again.Get().WoWPaths[0])
}

func TestOldStateDefaultsAutoUploadOff(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"keep_log":true}`), 0o600))

	s, err := store.Open(path)
	require.NoError(t, err)
	require.False(t, s.Get().EffectiveConfig().AutoUpload.Enabled)
	require.Equal(t, "b568659ffa9fe99b312ce1c6ad2724d8b46924833e0641423329c91d9f698be5", s.Get().EffectiveConfigHash())
}

func TestManagedConfigRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := store.Open(path)
	require.NoError(t, err)
	cfg := sidekick.SidekickConfig{
		Version:    sidekick.CurrentConfigVersion,
		AutoUpload: sidekick.AutoUploadConfig{Enabled: true},
	}
	require.NoError(t, s.Update(func(st *store.State) {
		st.Config = cfg
	}))

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, cfg, again.Get().EffectiveConfig())
	require.Equal(t, "5adfecf0ef27760f25242db79e5b41cd0b676794a2caacd2b11fb7dd88a55169", again.Get().EffectiveConfigHash())
}

func TestCorruptStateStartsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	s, err := store.Open(path)
	require.NoError(t, err)
	// An empty ledger means "ask the server", never "upload everything again".
	require.Empty(t, s.Get().Logs)
}

func TestLedgerDropsTheOldest(t *testing.T) {
	t.Parallel()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, s.Update(func(st *store.State) {
		for i := 0; i < store.LedgerMax+5; i++ {
			st.RecordLog(fmt.Sprintf("chr1-1-%d", i), store.Log{Size: 1, At: now.Add(time.Duration(i) * time.Minute)})
		}
	}))
	logs := s.Get().Logs
	require.Len(t, logs, store.LedgerMax)
	require.NotContains(t, logs, "chr1-1-0")
	require.Contains(t, logs, fmt.Sprintf("chr1-1-%d", store.LedgerMax+4))
}
