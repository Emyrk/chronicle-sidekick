package inspection_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/inspection"
)

func TestInspectVanillaComponents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "Interface", "AddOns", "chroniclecompanion"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "NAMPOWER.DLL"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SuperWoWhook.dll"), nil, 0o644))

	report := inspection.Inspect(dir, "vanilla_1.12a")
	require.Equal(t, inspection.StatusReady, report.Status)
	require.Equal(t, []inspection.Check{
		{ID: "addon.chronicle_companion", Category: inspection.CategoryAddon, Name: "ChronicleCompanion", Required: true, Status: inspection.CheckPresent},
		{ID: "dll.nampower", Category: inspection.CategoryDLL, Name: "nampower.dll", Required: true, Status: inspection.CheckPresent},
		{ID: "dll.classic_api", Category: inspection.CategoryDLL, Name: "ClassicAPI.dll", Required: false, Status: inspection.CheckMissing},
		{ID: "dll.superwow_hook", Category: inspection.CategoryDLL, Name: "SuperWoWhook.dll", Required: false, Status: inspection.CheckPresent},
	}, report.Checks)
}

func TestInspectMissingRequiredComponentNeedsAction(t *testing.T) {
	t.Parallel()
	report := inspection.Inspect(t.TempDir(), "vanilla_1.12a")
	require.Equal(t, inspection.StatusActionRequired, report.Status)
	require.Equal(t, inspection.CheckMissing, report.Checks[0].Status)
	require.Equal(t, inspection.CheckMissing, report.Checks[1].Status)
}

func TestInspectAddonProfiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		format string
		addon  string
		id     string
	}{
		{format: "classic_1.14.2", addon: "ChronicleCompanionJimsProxy", id: "addon.chronicle_companion_jims_proxy"},
		{format: "tbc_2.4.3", addon: "ChronicleCompanionTBC", id: "addon.chronicle_companion_tbc"},
		{format: "wotlk_3.3.5a", addon: "ChronicleCompanionWoTLK", id: "addon.chronicle_companion_wotlk"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "Interface", "AddOns", tt.addon), 0o755))
			report := inspection.Inspect(dir, tt.format)
			require.Equal(t, inspection.StatusReady, report.Status)
			require.Equal(t, []inspection.Check{{
				ID: tt.id, Category: inspection.CategoryAddon, Name: tt.addon,
				Required: true, Status: inspection.CheckPresent,
			}}, report.Checks)
		})
	}
}

func TestInspectUnknownFormat(t *testing.T) {
	t.Parallel()
	report := inspection.Inspect(t.TempDir(), "")
	require.Equal(t, inspection.StatusUnknown, report.Status)
	require.NotNil(t, report.Checks)
	require.Empty(t, report.Checks)
}
