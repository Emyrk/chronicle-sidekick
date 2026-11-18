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

func TestBuildSelectsNewestAccountWithoutFallback(t *testing.T) {
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
			old := writeAccount(t, root, other, `ChronicleCompanionForeverDB = { collections = { last_updated = 1700000000, older = true } }`)
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
			const olderBody = `ChronicleCompanionForeverDB={collections={last_updated=1700000000,older=1},setting="untouched"}`
			older := writeAccount(t, root, "Alpha", olderBody)
			selected := writeAccount(t, root, "Zulu", `ChronicleCompanionForeverDB={collections={last_updated=1700003600,newer=1},setting="original"}`)
			stamp := time.Unix(1700000000, 0)
			setAccountTime(t, older, stamp)
			setAccountTime(t, selected, stamp.Add(time.Hour))
			calls := 0
			warnings, err := foreverscouting.Drain(root, func(envelope foreverscouting.Envelope) error {
				calls++
				require.Len(t, envelope.Sources, 1)
				require.Equal(t, "Zulu", envelope.Sources[0].Account)
				if calls == 1 {
					require.Equal(t, `{last_updated=1700003600,newer=1}`, envelope.Sources[0].CollectionsLua)
					if recapture {
						require.NoError(t, os.WriteFile(selected, []byte(`ChronicleCompanionForeverDB={collections={last_updated=1,changed={unknown=true}},setting="latest"}`), 0600))
						setAccountTime(t, selected, stamp.Add(-time.Hour))
					}
				} else {
					require.Equal(t, `{last_updated=1,changed={unknown=true}}`, envelope.Sources[0].CollectionsLua)
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

func TestBuildCollectionsFreshness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, alpha, zulu   string
		alphaTime, zuluTime time.Time
		want                string
	}{
		{"marker beats newer file", "last_updated=1700000200", "last_updated=1700000100", time.Unix(1700000000, 0), time.Unix(1700000300, 0), "Alpha"},
		{"mixed marker and fallback", "last_updated=1700000200", "", time.Unix(1700000000, 0), time.Unix(1700000100, 0), "Alpha"},
		{"fallback beats marker", "last_updated=1700000200", "", time.Unix(1700000000, 0), time.Unix(1700000300, 0), "Zulu"},
		{"equal marker uses mtime", "last_updated=1700000200", "last_updated=1700000200", time.Unix(1700000000, 0), time.Unix(1700000100, 0), "Zulu"},
		{"equal marker and mtime uses account", "last_updated=1700000200", "last_updated=1700000200", time.Unix(1700000000, 0), time.Unix(1700000000, 0), "Alpha"},
		{"fallback preserves subsecond", "last_updated=1700000200", "", time.Unix(1700000300, 0), time.Unix(1700000200, 500000000), "Zulu"},
		{"zero is valid", "last_updated=0", "last_updated=1", time.Unix(1700000000, 0), time.Unix(1, 0), "Zulu"},
		{"nested marker ignored", "future={last_updated=1700000400}", "last_updated=1700000200", time.Unix(1700000000, 0), time.Unix(1700000100, 0), "Zulu"},
		{"hex marker", "last_updated=0x6553f1c8", "last_updated=1700000100", time.Unix(1700000000, 0), time.Unix(1700000300, 0), "Alpha"},
		{"bracketed marker", `["last_updated"]=1700000200`, "last_updated=1700000100", time.Unix(1700000000, 0), time.Unix(1700000300, 0), "Alpha"},
		{"integral decimal", "last_updated=1700000200.0", "last_updated=1700000100", time.Unix(1700000000, 0), time.Unix(1700000300, 0), "Alpha"},
		{"integral exponent", "last_updated=1.7000002e9", "last_updated=1700000100", time.Unix(1700000000, 0), time.Unix(1700000300, 0), "Alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			alpha := writeAccount(t, root, "Alpha", `ChronicleCompanionForeverDB={collections={payload=true,`+tc.alpha+`}}`)
			zulu := writeAccount(t, root, "Zulu", `ChronicleCompanionForeverDB={collections={payload=true,`+tc.zulu+`}}`)
			setAccountTime(t, alpha, tc.alphaTime)
			setAccountTime(t, zulu, tc.zuluTime)
			envelope, warnings, err := foreverscouting.Build(root)
			require.NoError(t, err)
			require.Empty(t, warnings)
			require.Len(t, envelope.Sources, 1)
			require.Equal(t, tc.want, envelope.Sources[0].Account)
		})
	}
}

func TestBuildInvalidMarkersFallBackToMtime(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{`-1`, `1700000200.5`, `"1700000200"`, `false`, `true`, `nil`, `{}`, `1e999`, `-1e999`, `0.00000000000000000001`} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			alpha := writeAccount(t, root, "Alpha", `ChronicleCompanionForeverDB={collections={last_updated=`+marker+`,payload=true}}`)
			zulu := writeAccount(t, root, "Zulu", `ChronicleCompanionForeverDB={collections={last_updated=1700000100,payload=true}}`)
			setAccountTime(t, alpha, time.Unix(1700000200, 0))
			setAccountTime(t, zulu, time.Unix(1700000300, 0))
			envelope, warnings, err := foreverscouting.Build(root)
			require.NoError(t, err)
			require.Empty(t, warnings)
			require.Len(t, envelope.Sources, 1)
			require.Equal(t, "Alpha", envelope.Sources[0].Account)
			require.Contains(t, envelope.Sources[0].CollectionsLua, "last_updated="+marker)
		})
	}
}
