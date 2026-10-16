package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

func TestWoWFolderErrorIsActionable(t *testing.T) {
	t.Parallel()
	title, message := wowFolderError(&wowdir.UnsupportedRealmError{Host: "logon.example.test"})
	require.Equal(t, "Unsupported WoW server", title)
	require.Contains(t, message, "logon.example.test")
	require.Contains(t, message, "not added")

	title, message = wowFolderError(errors.New("bad folder"))
	require.Equal(t, "WoW folder not recognized", title)
	require.Contains(t, message, "not added")
}

func TestPairingStatusShowsDeviceCode(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Waiting for device code…", pairingStatus(""))
	require.Equal(t, "Code ABCD-EFGH: approve in your browser…", pairingStatus("ABCD-EFGH"))
}

func TestAddAndRemoveWoWPaths(t *testing.T) {
	t.Parallel()
	st := store.State{WoWPath: filepath.Join("old", "wow")}
	require.True(t, addWoWPath(&st, filepath.Join("new", "wow")))
	require.Empty(t, st.WoWPath)
	require.Equal(t, []string{filepath.Join("old", "wow"), filepath.Join("new", "wow")}, st.WoWPaths)
	require.False(t, addWoWPath(&st, filepath.Join("new", "wow")))

	removeWoWPath(&st, filepath.Join("old", "wow"))
	require.Equal(t, []string{filepath.Join("new", "wow")}, st.WoWPaths)
	removeWoWPath(&st, filepath.Join("new", "wow"))
	require.Empty(t, st.WoWPaths)
}

func TestWoWUploadTargets(t *testing.T) {
	t.Parallel()
	installs := []wowdir.Install{
		{Flavor: "VanillaPlus", Dir: filepath.Join("games", "vanilla")},
		{Flavor: "Warmane", Dir: filepath.Join("games", "warmane")},
	}
	require.Equal(t, []wowUploadTarget{
		{Name: "VanillaPlus", Dirs: []string{filepath.Join("games", "vanilla")}},
		{Name: "Warmane", Dirs: []string{filepath.Join("games", "warmane")}},
	}, wowUploadTargets(installs))

	duplicates := wowUploadTargets([]wowdir.Install{
		{Flavor: "Warmane", Dir: filepath.Join("games", "one")},
		{Flavor: "Warmane", Dir: filepath.Join("games", "two")},
	})
	require.Equal(t, "Warmane (one)", duplicates[0].Name)
	require.Equal(t, "Warmane (two)", duplicates[1].Name)
}

func TestWoWFolderSites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	capy := fakeLegacyInstall(t, root, "Capy", "apac.capycraft.io")
	a := &app{layout: tenant.DefaultLayout}
	require.Equal(t, []wowSiteLink{{Name: "CapyCraft", URL: "https://capy.chronicleclassic.com"}}, a.wowFolderSites(capy))

	fakeLegacyInstall(t, root, "Warmane", "logon.warmane.com")
	require.ElementsMatch(t, []wowSiteLink{
		{Name: "CapyCraft", URL: "https://capy.chronicleclassic.com"},
		{Name: "Warmane", URL: "https://warmane.chronicleclassic.com"},
	}, a.wowFolderSites(root))
}

func TestWoWFolderLabel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	capy := fakeLegacyInstall(t, root, "Capy Client", "apac.capycraft.io")
	a := &app{layout: tenant.DefaultLayout}
	require.Equal(t, "CapyCraft — Capy Client", a.wowFolderLabel(capy))
}

func TestInstallsReturnsAllConfiguredWoWFolders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	capy := fakeLegacyInstall(t, root, "Capy", "apac.capycraft.io")
	warmane := fakeLegacyInstall(t, root, "Warmane", "logon.warmane.com")

	st, err := store.Open(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *store.State) {
		s.WoWPaths = []string{capy, warmane, capy}
	}))

	a := &app{store: st, layout: tenant.DefaultLayout}
	got := a.installs()
	require.Len(t, got, 2)
	require.Equal(t, capy, got[0].Dir)
	require.Equal(t, "https://capy.chronicleclassic.com", got[0].BaseURL)
	require.Equal(t, warmane, got[1].Dir)
	require.Equal(t, "https://warmane.chronicleclassic.com", got[1].BaseURL)
}

func TestInstallsReadsLegacySingleWoWPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	octo := fakeLegacyInstall(t, root, "Octo", "play.octowow.st")
	st, err := store.Open(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *store.State) { s.WoWPath = octo }))

	a := &app{store: st, layout: tenant.DefaultLayout}
	got := a.installs()
	require.Len(t, got, 1)
	require.Equal(t, octo, got[0].Dir)
}

func fakeLegacyInstall(t *testing.T, root, name, realmlist string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "Logs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "realmlist.wtf"), []byte("set realmlist "+realmlist+"\n"), 0o644))
	return dir
}
