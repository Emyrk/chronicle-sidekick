package foreverscouting_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/foreverscouting"
)

func writeAccount(t *testing.T, root, account, body string) string {
	t.Helper()
	path := filepath.Join(root, "WTF", "Account", account, "SavedVariables", "ChronicleCompanionForever.lua")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestBuildKeepsAccountsSeparateSortedAndSkipsEmptyOrMalformed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAccount(t, root, "Zulu", `ChronicleCompanionForeverDB = { scouting = { ["Player-Z"] = { delta = -1 } } }`)
	writeAccount(t, root, "Alpha", `ChronicleCompanionForeverDB = { ["scouting"] = { [1] = "first" } }`)
	writeAccount(t, root, "Empty", `ChronicleCompanionForeverDB = { scouting = {} }`)
	writeAccount(t, root, "Bad", `ChronicleCompanionForeverDB = { scouting = collectgarbage() }`)

	envelope, warnings, err := foreverscouting.Build(root)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Equal(t, 1, envelope.SchemaVersion)
	require.Equal(t, foreverscouting.Kind, envelope.Kind)
	require.Equal(t, []string{"Alpha", "Zulu"}, []string{envelope.Sources[0].Account, envelope.Sources[1].Account})
	require.Equal(t, "WTF/Account/Alpha/SavedVariables/ChronicleCompanionForever.lua", envelope.Sources[0].Source)
	require.Contains(t, envelope.Sources[1].ScoutingLua, `"Player-Z"`)
}

func TestBuildRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.lua")
	require.NoError(t, os.WriteFile(target, []byte(`ChronicleCompanionForeverDB = { scouting = { 1 } }`), 0o600))
	path := filepath.Join(root, "WTF", "Account", "Account", "SavedVariables", "ChronicleCompanionForever.lua")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	envelope, warnings, err := foreverscouting.Build(root)
	require.NoError(t, err)
	require.Empty(t, envelope.Sources)
	require.Len(t, warnings, 1)
}

func TestWriteOmitsEmptyArtifactAndWritesVersionedJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, foreverscouting.Filename)
	writeAccount(t, root, "Empty", `ChronicleCompanionForeverDB = { scouting = false }`)
	got, warnings, err := foreverscouting.Write(root, path)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Empty(t, got)
	require.NoFileExists(t, path)

	writeAccount(t, root, "Data", `ChronicleCompanionForeverDB = { scouting = { stamp = 1700000000 } }`)
	got, warnings, err = foreverscouting.Write(root, path)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, path, got)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var envelope foreverscouting.Envelope
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Len(t, envelope.Sources, 1)
}
