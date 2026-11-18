package foreverscouting_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func setAccountTime(t *testing.T, path string, stamp time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, stamp, stamp))
}

func TestBuildSelectsNewestAccountBeforeParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, account, body     string
		equal, omitted, warning bool
	}{
		{name: "newer zulu", account: "Zulu", body: `{ ["Player-Z"] = { delta = -1 }, future = { unknown = true } }`},
		{name: "newer alpha", account: "Alpha", body: `{ [1] = "first" }`},
		{name: "equal mtime", account: "Alpha", body: `{ tie = true }`, equal: true},
		{name: "empty", account: "Zulu", body: `{}`, omitted: true},
		{name: "nil", account: "Zulu", body: `nil`, omitted: true},
		{name: "false", account: "Zulu", body: `false`, omitted: true},
		{name: "missing", account: "Zulu", omitted: true},
		{name: "malformed", account: "Zulu", body: `collectgarbage()`, omitted: true, warning: true},
		{name: "invalid value", account: "Zulu", body: `42`, omitted: true, warning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			other := "Alpha"
			if tc.account == other {
				other = "Zulu"
			}
			old := writeAccount(t, root, other, `ChronicleCompanionForeverDB = { collections = { older = true } }`)
			body := `ChronicleCompanionForeverDB = { settings = true }`
			if tc.body != "" {
				body = `ChronicleCompanionForeverDB = { collections = ` + tc.body + ` }`
			}
			selected := writeAccount(t, root, tc.account, body)
			stamp := time.Unix(1700000000, 0)
			setAccountTime(t, old, stamp)
			if !tc.equal {
				stamp = stamp.Add(time.Hour)
			}
			setAccountTime(t, selected, stamp)
			envelope, warnings, err := foreverscouting.Build(root)
			require.NoError(t, err)
			if tc.warning {
				require.Len(t, warnings, 1)
				require.Equal(t, selected, warnings[0].Path)
			} else {
				require.Empty(t, warnings)
			}
			require.Equal(t, 1, envelope.SchemaVersion)
			require.Equal(t, foreverscouting.Kind, envelope.Kind)
			if tc.omitted {
				require.Empty(t, envelope.Sources)
				artifact := filepath.Join(root, foreverscouting.Filename)
				got, _, err := foreverscouting.Write(root, artifact)
				require.NoError(t, err)
				require.Empty(t, got)
				require.NoFileExists(t, artifact)
				return
			}
			require.Len(t, envelope.Sources, 1)
			require.Equal(t, tc.account, envelope.Sources[0].Account)
			require.Equal(t, "WTF/Account/"+tc.account+"/SavedVariables/ChronicleCompanionForever.lua", envelope.Sources[0].Source)
			require.Equal(t, tc.body, envelope.Sources[0].CollectionsLua)
		})
	}
}

func TestBuildRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.lua")
	require.NoError(t, os.WriteFile(target, []byte(`ChronicleCompanionForeverDB = { collections = { 1 } }`), 0o600))
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
	empty := writeAccount(t, root, "Empty", `ChronicleCompanionForeverDB = { collections = false }`)
	setAccountTime(t, empty, time.Unix(1700000000, 0))
	got, warnings, err := foreverscouting.Write(root, path)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Empty(t, got)
	require.NoFileExists(t, path)

	data := writeAccount(t, root, "Data", `ChronicleCompanionForeverDB = { collections = { stamp = 1700000000 } }`)
	setAccountTime(t, data, time.Unix(1700003600, 0))
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

func TestDrainOnlyResetsSelectedAccountAndPinsRecapture(t *testing.T) {
	t.Parallel()
	for _, recapture := range []bool{false, true} {
		name := "unchanged"
		if recapture {
			name = "changed selected file becomes older"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			const olderBody = `ChronicleCompanionForeverDB={collections={older=1},setting="untouched"}`
			older := writeAccount(t, root, "Alpha", olderBody)
			selected := writeAccount(t, root, "Zulu", `ChronicleCompanionForeverDB={collections={newer=1},setting="original"}`)
			stamp := time.Unix(1700000000, 0)
			setAccountTime(t, older, stamp)
			setAccountTime(t, selected, stamp.Add(time.Hour))
			calls := 0
			warnings, err := foreverscouting.Drain(root, func(envelope foreverscouting.Envelope) error {
				calls++
				require.Len(t, envelope.Sources, 1)
				require.Equal(t, "Zulu", envelope.Sources[0].Account)
				if calls == 1 {
					require.Equal(t, `{newer=1}`, envelope.Sources[0].CollectionsLua)
					if recapture {
						require.NoError(t, os.WriteFile(selected, []byte(`ChronicleCompanionForeverDB={collections={changed={unknown=true}},setting="latest"}`), 0600))
						setAccountTime(t, selected, stamp.Add(-time.Hour))
					}
				} else {
					require.Equal(t, `{changed={unknown=true}}`, envelope.Sources[0].CollectionsLua)
				}
				return nil
			})
			require.NoError(t, err)
			require.Empty(t, warnings)
			expectedCalls, setting := 1, "original"
			if recapture {
				expectedCalls, setting = 2, "latest"
			}
			require.Equal(t, expectedCalls, calls)
			body, err := os.ReadFile(selected)
			require.NoError(t, err)
			require.Equal(t, `ChronicleCompanionForeverDB={collections={},setting="`+setting+`"}`, string(body))
			body, err = os.ReadFile(older)
			require.NoError(t, err)
			require.Equal(t, olderBody, string(body))
			info, err := os.Stat(older)
			require.NoError(t, err)
			require.True(t, info.ModTime().Equal(stamp))
		})
	}
}
