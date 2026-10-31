package wowdir_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

// fakeInstall builds <root>/<flavor>/.flavor.info and returns the root.
func fakeInstall(t *testing.T, flavors ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "World of Warcraft")
	for _, f := range flavors {
		require.NoError(t, os.MkdirAll(filepath.Join(root, f, "Logs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, f, ".flavor.info"), []byte("Product Flavor!STRING:0:\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(root, f, "Wow.exe"), nil, 0o644))
	}
	// Noise that must not be treated as a flavor.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_unrelated_"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Data"), 0o755))
	return root
}

func TestDetect(t *testing.T) {
	t.Parallel()
	root := fakeInstall(t, "_classic_beta_")
	got := wowdir.Detect([]string{filepath.Join(t.TempDir(), "missing"), root}, tenant.DefaultLayout)
	require.Len(t, got, 1)
	require.Equal(t, "_classic_beta_", got[0].Flavor)
	require.Equal(t, filepath.Join(root, "_classic_beta_", "Logs"), got[0].LogDir)
}

func TestDetectsClassic1142FromBuildInfo(t *testing.T) {
	t.Parallel()
	root := fakeInstall(t, "_classic_era_")
	buildInfo := `Branch!STRING:0|Active!DEC:1|Version!STRING:0|Product!STRING:0
main|1|2.4.3.8606|wow_classic
main|1|1.14.2.42597|wow_classic_era
`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".build.info"), []byte(buildInfo), 0o644))
	hermesConfig := filepath.Join(filepath.Dir(root), "Hermes", "HermesProxy.config")
	require.NoError(t, os.MkdirAll(filepath.Dir(hermesConfig), 0o755))
	require.NoError(t, os.WriteFile(hermesConfig, []byte(`<add key="ServerAddress" value="login2.twinstar-wow.com:3724" />`), 0o644))

	got := wowdir.Detect([]string{root}, tenant.DefaultLayout)
	require.Len(t, got, 1)
	require.Equal(t, "classic_1.14.2", got[0].DetectedGameFormat)
	require.Equal(t, "Kronos", got[0].Flavor)
	require.Equal(t, "login2.twinstar-wow.com", got[0].Realmlist)
	require.Equal(t, "https://kronos.chronicleclassic.com", got[0].BaseURL)
}

func TestDoesNotGuessClassicFormatFromFlavorMarker(t *testing.T) {
	t.Parallel()
	root := fakeInstall(t, "_classic_era_")

	got := wowdir.Detect([]string{root}, tenant.DefaultLayout)
	require.Len(t, got, 1)
	require.Empty(t, got[0].DetectedGameFormat)
}

func TestResolve(t *testing.T) {
	t.Parallel()
	// The flavor folder name must never matter.
	root := fakeInstall(t, "_renamed_at_launch_")
	flavor := filepath.Join(root, "_renamed_at_launch_")
	app := filepath.Join(flavor, "World of Warcraft.app")
	require.NoError(t, os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755))

	for name, path := range map[string]string{
		"install root": root,
		"flavor dir":   flavor,
		"logs dir":     filepath.Join(flavor, "Logs"),
		"exe":          filepath.Join(flavor, "Wow.exe"),
		"app bundle":   app,
		"inside app":   filepath.Join(app, "Contents", "MacOS"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := wowdir.Resolve(path, tenant.DefaultLayout)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, flavor, got[0].Dir)
			require.Equal(t, filepath.Join(flavor, "Logs"), got[0].LogDir)
		})
	}
}

func TestResolveNotFound(t *testing.T) {
	t.Parallel()
	_, err := wowdir.Resolve(t.TempDir(), tenant.DefaultLayout)
	require.ErrorIs(t, err, wowdir.ErrNotFound)
	_, err = wowdir.Resolve(filepath.Join(t.TempDir(), "nope"), tenant.DefaultLayout)
	require.Error(t, err)
}

func TestResolveLegacyClients(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		realmlist  string
		wantRealm  string
		wantTenant string
		localized  bool
	}{
		{name: "octo hostname", realmlist: `SET realmlist "play.octowow.st"`, wantRealm: "play.octowow.st", wantTenant: "https://octo.chronicleclassic.com"},
		{name: "octo ip", realmlist: "set realmlist 185.165.170.6", wantRealm: "185.165.170.6", wantTenant: "https://octo.chronicleclassic.com"},
		{name: "capy", realmlist: "set realmlist apac.capycraft.io", wantRealm: "apac.capycraft.io", wantTenant: "https://capy.chronicleclassic.com"},
		{name: "vanilla plus", realmlist: "set realmlist logon.vanillaplus.org", wantRealm: "logon.vanillaplus.org", wantTenant: "https://vanillaplus.chronicleclassic.com"},
		{name: "kronos", realmlist: "set realmlist login3.twinstar-wow.com", wantRealm: "login3.twinstar-wow.com", wantTenant: "https://kronos.chronicleclassic.com"},
		{name: "moonwell", realmlist: "set realmlist login.moonwell.su", wantRealm: "login.moonwell.su", wantTenant: "https://moonwell.chronicleclassic.com"},
		{name: "crusader storm", realmlist: "set realmlist crusader-storm.com", wantRealm: "crusader-storm.com", wantTenant: "https://crusader-storm.chronicleclassic.com"},
		{name: "chromiecraft localized", realmlist: "set realmlist logon.chromiecraft.com", wantRealm: "logon.chromiecraft.com", wantTenant: "https://chromie.chronicleclassic.com", localized: true},
		{name: "warmane with port", realmlist: "set realmlist logon.warmane.com:3724", wantRealm: "logon.warmane.com", wantTenant: "https://warmane.chronicleclassic.com", localized: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "WoW")
			realmPath := filepath.Join(root, "realmlist.wtf")
			if tt.localized {
				realmPath = filepath.Join(root, "Data", "enUS", "realmlist.wtf")
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(realmPath), 0o755))
			require.NoError(t, os.WriteFile(realmPath, []byte("# comment\n"+tt.realmlist+"\n"), 0o644))

			got, err := wowdir.Resolve(root, tenant.DefaultLayout)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tt.wantRealm, got[0].Realmlist)
			require.Equal(t, tt.wantTenant, got[0].BaseURL)
			require.Contains(t, got[0].LogGlobs, filepath.Join(root, "Logs", "WoWCombatLog.txt"))
			require.Contains(t, got[0].LogGlobs, filepath.Join(root, "CustomData", "Chronicle_*.txt"))
		})
	}
}

func TestResolveRejectsUnknownRealm(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "realmlist.wtf"), []byte("set realmlist unknown.example\n"), 0o644))
	_, err := wowdir.Resolve(root, tenant.DefaultLayout)
	require.ErrorIs(t, err, wowdir.ErrUnsupportedRealm)
	var unsupported *wowdir.UnsupportedRealmError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "unknown.example", unsupported.Host)
}
