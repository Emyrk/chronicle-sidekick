package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
	"github.com/stretchr/testify/require"
)

// A synthetic proc tree keeps Wine tests independent of installed runners,
// process timing, the user's environment, and access to other live processes.
func TestLinuxProcessExe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, loader, command, env string
		wantGame                   bool
	}{
		{"umu custom drive", "wine-preloader", `X:\Downloads\Capycraft\WoW.exe`, "prefix", true},
		{"unix executable path", "wine-preloader", "GAME", "", true},
		{"root drive", "wine-preloader", "ROOTGAME", "prefix", true},
		{"parent at drive root", "wine-preloader", `X:\..\Downloads\Capycraft\WoW.exe`, "prefix", true},
		{"lowercase drive", "wine64-preloader", `x:\Downloads\Capycraft\WoW.exe`, "prefix", true},
		{"forward slashes", "wine64", `X:/Downloads/Capycraft/WoW.exe`, "prefix", true},
		{"loader argv zero", "wine", "wine\x00" + `X:\Downloads\Capycraft\WoW.exe`, "prefix", true},
		{"default prefix", "wine-preloader", `X:\Downloads\Capycraft\WoW.exe`, "home", true},
		{"missing prefix", "wine-preloader", `X:\Downloads\Capycraft\WoW.exe`, "", false},
		{"unmapped drive", "wine-preloader", `Y:\Downloads\Capycraft\WoW.exe`, "prefix", false},
		{"missing file", "wine-preloader", `X:\Downloads\Capycraft\Missing.exe`, "prefix", false},
		{"directory", "wine-preloader", `X:\Downloads\Capycraft`, "prefix", false},
		{"drive relative path", "wine-preloader", `X:Downloads\Capycraft\WoW.exe`, "prefix", false},
		{"do not scan arguments", "wine-preloader", "other.exe\x00" + `X:\Downloads\Capycraft\WoW.exe`, "prefix", false},
		{"non wine process", "launcher", `X:\Downloads\Capycraft\WoW.exe`, "prefix", false},
		{"empty command", "wine-preloader", "", "prefix", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "prefix with spaces")
			proc := filepath.Join(root, "proc")
			prefix := filepath.Join(root, ".wine")
			drive := filepath.Join(root, "drive")
			game := filepath.Join(drive, "Downloads", "Capycraft", "WoW.exe")
			require.NoError(t, os.MkdirAll(filepath.Dir(game), 0700))
			require.NoError(t, os.WriteFile(game, nil, 0600))
			require.NoError(t, os.MkdirAll(filepath.Join(prefix, "dosdevices"), 0700))
			require.NoError(t, os.Symlink(drive, filepath.Join(prefix, "dosdevices", "x:")))
			require.NoError(t, os.Symlink("/", filepath.Join(prefix, "dosdevices", "z:")))
			require.NoError(t, os.MkdirAll(proc, 0700))
			loader := filepath.Join(root, "bin", tc.loader)
			require.NoError(t, os.Symlink(loader, filepath.Join(proc, "exe")))
			require.NoError(t, os.WriteFile(filepath.Join(proc, "cmdline"), []byte(strings.ReplaceAll(strings.ReplaceAll(tc.command, "ROOTGAME", "Z:"+game), "GAME", game)+"\x00"), 0600))
			env := ""
			switch tc.env {
			case "prefix":
				env = "WINEPREFIX=" + prefix + "\x00"
			case "home":
				env = "HOME=" + root + "\x00"
			}
			require.NoError(t, os.WriteFile(filepath.Join(proc, "environ"), []byte(env), 0600))
			got, err := linuxProcessExe(proc)
			require.NoError(t, err)
			if tc.wantGame {
				require.Equal(t, game, got)
			} else {
				require.Equal(t, loader, got)
			}

			// Exercise the same resolver through per-install detection, including a
			// second installation that must not be activated by this process.
			procs := &wineTestProcs{procDir: proc}
			detector := NewGameForInstall(tenant.DefaultLayout, procs)
			require.Equal(t, tc.wantGame, detector.Running(context.Background(), []string{filepath.Dir(game)}))
			require.Equal(t, tc.wantGame, detector.Running(context.Background(), []string{filepath.Dir(game)}))
			other := NewGameForInstall(tenant.DefaultLayout, procs)
			require.False(t, other.Running(context.Background(), []string{filepath.Join(root, "other")}))
		})
	}
}

type wineTestProcs struct{ procDir string }

func (*wineTestProcs) List(context.Context) ([]Process, error) {
	return []Process{{PID: 42, Name: "WoW.exe"}}, nil
}
func (p *wineTestProcs) Exe(context.Context, int32) (string, error) {
	return linuxProcessExe(p.procDir)
}
func (*wineTestProcs) Exists(context.Context, int32, string) (bool, error) { return true, nil }

func TestLinuxProcessExeMissingProcFiles(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	_, err := linuxProcessExe(proc)
	require.Error(t, err, "an inaccessible process must not resolve")
	loader := "/runner/bin/wine-preloader"
	require.NoError(t, os.Symlink(loader, filepath.Join(proc, "exe")))
	got, err := linuxProcessExe(proc)
	require.NoError(t, err)
	require.Equal(t, loader, got, "missing cmdline must not identify an installation")
	require.NoError(t, os.WriteFile(filepath.Join(proc, "cmdline"), []byte("X:\\WoW.exe\x00"), 0600))
	got, err = linuxProcessExe(proc)
	require.NoError(t, err)
	require.Equal(t, loader, got, "missing environ must not use Sidekick's own prefix")
}
