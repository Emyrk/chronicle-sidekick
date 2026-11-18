package foreverscouting

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func cleanupSource(t *testing.T, body string) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "WTF", "Account", "A", "SavedVariables", "ChronicleCompanionForever.lua")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0640))
	return root, path
}

func TestDrainPreservesOutsideCollectionsAndUnknownFields(t *testing.T) {
	t.Parallel()
	const table = `{scouting={a=1}, battleground={future={unknown="keep"}}, [77]=false}`
	const prefix = "-- header\nChronicleCompanionForeverDB = { settings = {sidekick_enabled=true}, collections = "
	const suffix = ", mystery = {future=42} } -- footer\n"
	root, path := cleanupSource(t, prefix+table+suffix)
	beforeInfo, err := os.Stat(path)
	require.NoError(t, err)
	calls := 0
	warnings, err := Drain(root, func(envelope Envelope) error {
		calls++
		require.Len(t, envelope.Sources, 1)
		require.Equal(t, table, envelope.Sources[0].CollectionsLua)
		archived, _, err := WriteEnvelope(envelope, filepath.Join(t.TempDir(), Filename), nil)
		require.NoError(t, err)
		require.FileExists(t, archived)
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, 1, calls)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, prefix+"{}"+suffix, string(body))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, beforeInfo.Mode().Perm(), info.Mode().Perm())
}

func TestDrainRecapturesChangedSnapshotAndLatestSettings(t *testing.T) {
	t.Parallel()
	root, path := cleanupSource(t, `ChronicleCompanionForeverDB={collections={old=1},setting="old"}`)
	calls := 0
	_, err := Drain(root, func(envelope Envelope) error {
		calls++
		if calls == 1 {
			require.Equal(t, `{old=1}`, envelope.Sources[0].CollectionsLua)
			return os.WriteFile(path, []byte(`ChronicleCompanionForeverDB={collections={new={unknown=true}},setting="latest"}`), 0600)
		}
		require.Equal(t, `{new={unknown=true}}`, envelope.Sources[0].CollectionsLua)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, `ChronicleCompanionForeverDB={collections={},setting="latest"}`, string(body))
}

func TestDrainArchiveFailureLeavesSource(t *testing.T) {
	t.Parallel()
	const original = `ChronicleCompanionForeverDB={collections={unknown=1}}`
	root, path := cleanupSource(t, original)
	_, err := Drain(root, func(Envelope) error { return errors.New("archive unavailable") })
	require.ErrorContains(t, err, "archive unavailable")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(body))
}

func TestNoScoutingFallback(t *testing.T) {
	t.Parallel()
	_, present, err := Extract([]byte(`ChronicleCompanionForeverDB={scouting={legacy=1}}`))
	require.NoError(t, err)
	require.False(t, present)
}
