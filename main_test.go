package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/engine"
	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

func TestStateDebugSequence(t *testing.T) {
	t.Parallel()
	require.Equal(t, 3*time.Second, stateDebugInterval)
	require.Equal(t, []engine.Status{
		{State: engine.StateIdle, Detail: "Gray: Waiting for WoW"},
		{State: engine.StateNoLog, Detail: "Red: Combat logging not detected"},
		{State: engine.StateWaiting, Detail: "Orange: Combat logging started, waiting for data"},
		{State: engine.StateRecording, Detail: "Green: Combat log receiving data"},
		{State: engine.StateFinalizing, Detail: "Purple: Finalizing combat log"},
		{State: engine.StateUploading, Detail: "Blue: Uploading or retrying"},
		{State: engine.StateReconnect, Detail: "Yellow: Chronicle sign-in required"},
	}, stateDebugStatuses)
}

func TestEnsureInstallationPersistsStableID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := store.Open(path)
	require.NoError(t, err)

	first, err := ensureInstallation(st)
	require.NoError(t, err)
	second, err := ensureInstallation(st)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	_, err = uuid.Parse(first.ID)
	require.NoError(t, err)

	again, err := store.Open(path)
	require.NoError(t, err)
	third, err := ensureInstallation(again)
	require.NoError(t, err)
	require.Equal(t, first.ID, third.ID)
}

func TestInstallationMetadataFitsAPI(t *testing.T) {
	t.Parallel()
	require.Equal(t, "short", installationMetadata("short"))
	require.Len(t, installationMetadata(string(make([]byte, 101))), 100)
}

func TestSidekickStatusIncludesWoWDirectories(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("games", "warmane")
	id := sidekick.DirectoryID(dir)
	config := sidekick.SidekickConfig{
		Version: sidekick.CurrentConfigVersion,
		Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 7},
		WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
			id: {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
		},
	}
	issues := []sidekick.Issue{{Code: "upload_failed", Message: "A combat log upload failed."}}
	status := sidekickStatus([]wowdir.Install{{
		Flavor: "Warmane", Dir: dir,
		BaseURL: "https://warmane.chronicleclassic.com", Realmlist: "logon.warmane.com",
	}}, config, issues, "windows/amd64", "1.2.3", true)
	require.Equal(t, sidekick.Status{
		Platform: "windows/amd64", SidekickVersion: "1.2.3",
		StartAtLogin: true, AutoUploadCapabilities: sidekick.AutoUploadCapabilities(),
		WoWDirectories: []sidekick.WoWDirectory{{
			ID: id, Path: dir, TenantName: "Warmane",
			TenantBaseURL: "https://warmane.chronicleclassic.com", Realmlist: "logon.warmane.com",
			GameFormat: sidekick.GameFormatWrath335a,
		}},
		Issues: issues,
	}, status)
}

func TestSidekickStatusLimitsWoWDirectories(t *testing.T) {
	t.Parallel()
	installs := make([]wowdir.Install, maxStatusWoWDirectories+1)
	for i := range installs {
		installs[i] = wowdir.Install{Flavor: "WoW", Dir: filepath.Join("games", string(rune('a'+i))), BaseURL: "https://example.com"}
	}
	require.Len(t, sidekickStatus(installs, sidekick.DefaultConfig(), nil, "linux/amd64", "dev", false).WoWDirectories, maxStatusWoWDirectories)
}

func TestAutoUploadTriggerUsesDirectoryConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := fakeLegacyInstall(t, root, "Warmane", "logon.warmane.com")
	st, err := store.Open(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *store.State) {
		s.WoWPaths = []string{dir}
		s.WoWPathsSet = true
		s.Config = sidekick.SidekickConfig{
			Version: sidekick.CurrentConfigVersion,
			Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 7},
			WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
				sidekick.DirectoryID(dir): {
					GameFormat: sidekick.GameFormatWrath335a,
					AutoUpload: sidekick.AutoUploadOnGameExit,
				},
			},
		}
	}))
	a := &app{store: st, layout: tenant.DefaultLayout}
	require.True(t, a.autoUploadTrigger(wowdir.Install{Dir: dir}).OnExit)
	require.False(t, a.autoUploadTrigger(wowdir.Install{Dir: filepath.Join(dir, "other")}).OnExit)
	require.Equal(t, sidekick.GameFormatWrath335a, a.gameFormatForPath(filepath.Join(dir, "Logs", "WoWCombatLog.txt")))
}

func TestResolveSettingsURL(t *testing.T) {
	t.Parallel()
	const base = "https://chronicle.example"
	require.Equal(t, "https://chronicle.example/settings/sidekick", resolveSettingsURL(base, "/settings/sidekick"))
	require.Equal(t, "https://chronicle.example/settings/sidekick", resolveSettingsURL(base, "https://chronicle.example/settings/sidekick"))
	require.Empty(t, resolveSettingsURL(base, "https://evil.example/settings"))
	require.Empty(t, resolveSettingsURL(base, "javascript:alert(1)"))
}

func TestReconcileConfigPersistsMismatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := store.Open(path)
	require.NoError(t, err)
	directoryID := sidekick.DirectoryID("/wow/warmane")
	cfg := sidekick.SidekickConfig{
		Version: sidekick.CurrentConfigVersion,
		Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 7},
		WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
			directoryID: {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
		},
	}

	hash, err := sidekick.HashConfig(cfg)
	require.NoError(t, err)
	changed, err := reconcileConfig(st, sidekick.UpdateResponse{ConfigHash: hash, Config: &cfg})
	require.NoError(t, err)
	require.True(t, changed)

	again, err := store.Open(path)
	require.NoError(t, err)
	require.Equal(t, hash, again.Get().EffectiveConfigHash())
	require.Equal(t, sidekick.AutoUploadOnGameExit, again.Get().EffectiveConfig().WoWDirectories[directoryID].AutoUpload)
}

func TestReconcileConfigToleratesAbsentConfig(t *testing.T) {
	t.Parallel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)

	changed, err := reconcileConfig(st, sidekick.UpdateResponse{ConfigHash: "desired"})
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, "03ba74a583c3e096527eb448a456583b975a0f8c32d8517c630ff3954e3528e4", st.Get().EffectiveConfigHash())
}

func TestReconcileConfigRejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	cfg := sidekick.SidekickConfig{Version: sidekick.CurrentConfigVersion + 1}

	changed, err := reconcileConfig(st, sidekick.UpdateResponse{ConfigHash: "future", Config: &cfg})
	require.ErrorContains(t, err, "unsupported Sidekick config version")
	require.False(t, changed)
	require.Equal(t, "03ba74a583c3e096527eb448a456583b975a0f8c32d8517c630ff3954e3528e4", st.Get().EffectiveConfigHash())
}

func TestReconcileConfigRejectsMismatchedHash(t *testing.T) {
	t.Parallel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	directoryID := sidekick.DirectoryID("/wow/warmane")
	cfg := sidekick.SidekickConfig{
		Version: sidekick.CurrentConfigVersion,
		Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 7},
		WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
			directoryID: {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
		},
	}

	changed, err := reconcileConfig(st, sidekick.UpdateResponse{ConfigHash: "wrong", Config: &cfg})
	require.ErrorContains(t, err, "config hash mismatch")
	require.False(t, changed)
	require.Empty(t, st.Get().EffectiveConfig().WoWDirectories)
	require.Equal(t, "03ba74a583c3e096527eb448a456583b975a0f8c32d8517c630ff3954e3528e4", st.Get().EffectiveConfigHash())
}

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

func TestCancelPairingCancelsActiveContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{pairing: true, pairCancel: cancel}

	a.cancelPairing()

	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestBeginPairingCommitSerializesCancellation(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	canceled := &app{pairing: true, pairCancel: cancel}
	canceled.cancelPairing()
	require.False(t, canceled.beginPairingCommit(canceledCtx))

	approvedCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	approved := &app{pairing: true, pairCancel: cancel}
	require.True(t, approved.beginPairingCommit(approvedCtx))
	approved.cancelPairing()
	require.NoError(t, approvedCtx.Err())
}

func TestPairingStatusShowsDeviceCode(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Waiting for device code…", pairingStatus(""))
	require.Equal(t, "Code ABCD-EFGH: approve in your browser…", pairingStatus("ABCD-EFGH"))
}

func TestWoWMenuTitleDistinguishesAutomaticFallback(t *testing.T) {
	t.Parallel()
	require.Equal(t, "World of Warcraft folders (automatic)", wowMenuTitle(0, true))
	require.Equal(t, "World of Warcraft folders", wowMenuTitle(0, false))
	require.Equal(t, "World of Warcraft folders (2)", wowMenuTitle(2, false))
}

func TestAddAndRemoveWoWPaths(t *testing.T) {
	t.Parallel()
	st := store.State{WoWPath: filepath.Join("old", "wow")}
	require.True(t, addWoWPath(&st, filepath.Join("new", "wow")))
	require.True(t, st.WoWPathsSet)
	require.Empty(t, st.WoWPath)
	require.Equal(t, []string{filepath.Join("old", "wow"), filepath.Join("new", "wow")}, st.WoWPaths)
	require.False(t, addWoWPath(&st, filepath.Join("new", "wow")))

	removeWoWPath(&st, filepath.Join("old", "wow"))
	require.Equal(t, []string{filepath.Join("new", "wow")}, st.WoWPaths)
	removeWoWPath(&st, filepath.Join("new", "wow"))
	require.Empty(t, st.WoWPaths)
}

func TestAddDetectedWoWPathsPreservesManualFolders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manual := fakeLegacyInstall(t, root, "Manual", "apac.capycraft.io")
	detected := fakeLegacyInstall(t, root, "Detected", "logon.warmane.com")
	st := store.State{WoWPaths: []string{manual}}

	added := addDetectedWoWPaths(&st, []wowdir.Install{
		{Dir: manual},
		{Dir: detected},
		{Dir: detected},
	}, tenant.DefaultLayout)

	require.Equal(t, []string{detected}, added)
	require.True(t, st.WoWPathsSet)
	require.Equal(t, []string{manual, detected}, st.WoWPaths)
	require.Empty(t, st.WoWPath)
	require.Empty(t, addDetectedWoWPaths(&st, []wowdir.Install{{Dir: detected}}, tenant.DefaultLayout))
}

func TestAutomaticWoWFallbackStopsAfterFolderListIsManaged(t *testing.T) {
	t.Parallel()
	require.True(t, useAutomaticWoWFallback(store.State{}))
	require.False(t, useAutomaticWoWFallback(store.State{WoWPaths: []string{"/wow"}}))
	require.False(t, useAutomaticWoWFallback(store.State{WoWPathsSet: true}))
	require.False(t, useAutomaticWoWFallback(store.State{WoWPath: "/legacy/wow"}))
}

func TestAddDetectedWoWPathsDeduplicatesSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manual := fakeLegacyInstall(t, root, "Manual", "apac.capycraft.io")
	alias := filepath.Join(root, "Manual alias")
	if err := os.Symlink(manual, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	st := store.State{WoWPaths: []string{manual}}
	require.Empty(t, addDetectedWoWPaths(&st, []wowdir.Install{{Dir: alias}}, tenant.DefaultLayout))
	require.Equal(t, []string{manual}, st.WoWPaths)
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

func TestInstallsRereadsChangedRealmlist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := fakeLegacyInstall(t, root, "Swappable", "apac.capycraft.io")
	st, err := store.Open(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *store.State) {
		s.WoWPaths = []string{dir}
		s.WoWPathsSet = true
	}))
	a := &app{store: st, layout: tenant.DefaultLayout}

	require.Equal(t, "https://capy.chronicleclassic.com", a.installs()[0].BaseURL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "realmlist.wtf"),
		[]byte("set realmlist logon.warmane.com\n"),
		0o644,
	))
	require.Equal(t, "https://warmane.chronicleclassic.com", a.installs()[0].BaseURL)
}

func TestInstallsDeduplicatesConfiguredPathAliases(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manual := fakeLegacyInstall(t, root, "Manual", "apac.capycraft.io")
	alias := filepath.Join(root, "Manual alias")
	if err := os.Symlink(manual, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	st, err := store.Open(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *store.State) {
		s.WoWPaths = []string{manual, alias}
		s.WoWPathsSet = true
	}))

	a := &app{store: st, layout: tenant.DefaultLayout}
	got := a.installs()
	require.Len(t, got, 1)
	require.Equal(t, manual, got[0].Dir)
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
