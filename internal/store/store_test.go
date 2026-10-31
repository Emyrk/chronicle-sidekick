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

func TestArchiveGroupsRoundTripAndCopy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := store.Open(path)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	group := store.ArchiveGroup{
		ID:               "group-1",
		InstallDirectory: "/wow",
		InstallID:        "wowdir_1",
		SourceKey:        "_thrall.txt",
		LogID:            "log-1",
		ReportURL:        "https://x/logs/1",
		UploadID:         "550e8400-e29b-41d4-a716-446655440000",
		ChunkSequence:    2,
		CreatedAt:        now,
		LastExitAt:       now.Add(time.Minute),
		LastSuccessAt:    now.Add(2 * time.Minute),
		DeleteAfter:      now.Add(7 * 24 * time.Hour),
		Pending:          true,
		Files:            []string{"/wow/Logs/WoWCombatLog.txt", "/wow/WTF/Chronicle.lua"},
	}
	require.NoError(t, s.Update(func(st *store.State) {
		st.ArchiveGroups = map[string]store.ArchiveGroup{group.ID: group}
	}))

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, group, again.Get().ArchiveGroups[group.ID])

	copy := again.Get()
	copy.ArchiveGroups[group.ID] = store.ArchiveGroup{}
	require.Equal(t, group, again.Get().ArchiveGroups[group.ID])
	copy = again.Get()
	copyGroup := copy.ArchiveGroups[group.ID]
	copyGroup.Files[0] = "/changed"
	require.Equal(t, group.Files[0], again.Get().ArchiveGroups[group.ID].Files[0])
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

func TestIssuesRoundTripAndCopy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := store.Open(path)
	require.NoError(t, err)
	issues := []sidekick.Issue{{
		Code: "upload_failed", Message: "A combat log upload failed.",
		Details: "Chronicle returned HTTP 500 (Internal Server Error).",
	}}
	require.NoError(t, s.Update(func(st *store.State) {
		st.Issues = issues
	}))

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, issues, again.Get().Issues)
	copy := again.Get()
	copy.Issues[0].Message = "changed"
	require.Equal(t, issues, again.Get().Issues)
}

func TestOldStateDefaultsAutoUploadOff(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"keep_log":true}`), 0o600))

	s, err := store.Open(path)
	require.NoError(t, err)
	require.Empty(t, s.Get().EffectiveConfig().WoWDirectories)
	require.Equal(t, "03ba74a583c3e096527eb448a456583b975a0f8c32d8517c630ff3954e3528e4", s.Get().EffectiveConfigHash())
	require.NoError(t, s.Update(func(*store.State) {}))
	persisted, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(persisted), "keep_log")
}

func TestManagedConfigRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := store.Open(path)
	require.NoError(t, err)
	cfg := sidekick.SidekickConfig{
		Version: sidekick.CurrentConfigVersion,
		Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 14},
		WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
			"dir": {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
		},
	}
	require.NoError(t, s.Update(func(st *store.State) {
		st.Config = cfg
	}))

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, cfg, again.Get().EffectiveConfig())
	hash, err := sidekick.HashConfig(cfg)
	require.NoError(t, err)
	require.Equal(t, hash, again.Get().EffectiveConfigHash())
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
