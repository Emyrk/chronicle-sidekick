// Package wowdir locates supported World of Warcraft installs, their combat
// logs, and the Chronicle tenant selected by realmlist.wtf.
package wowdir

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
)

var (
	// ErrNotFound is returned when no supported WoW install can be found at a path.
	ErrNotFound = errors.New("no supported World of Warcraft install found")
	// ErrUnsupportedRealm means the folder is WoW, but its realmlist is not mapped.
	ErrUnsupportedRealm = errors.New("unsupported WoW realmlist")
)

// UnsupportedRealmError identifies a realmlist that Sidekick cannot route yet.
type UnsupportedRealmError struct {
	Host string
}

func (e *UnsupportedRealmError) Error() string {
	return fmt.Sprintf("%s: %s", ErrUnsupportedRealm, e.Host)
}
func (e *UnsupportedRealmError) Unwrap() error { return ErrUnsupportedRealm }

// Install is one independently configured WoW client.
type Install struct {
	// Flavor is a display label, either the Blizzard flavor folder or realm name.
	Flavor string
	// Dir is the folder containing the game executable.
	Dir string
	// LogDir is retained for compatibility and is the primary log directory.
	LogDir string
	// LogGlobs are combat-log filename patterns for this client.
	LogGlobs []string
	// BaseURL is the Chronicle tenant that receives this client's logs.
	BaseURL string
	// Realmlist is the normalized host read from realmlist.wtf, when present.
	Realmlist string
}

func foreverInstall(dir string) Install {
	logDir := filepath.Join(dir, "Logs")
	return Install{
		Flavor:   filepath.Base(dir),
		Dir:      dir,
		LogDir:   logDir,
		LogGlobs: wowCombatLogGlobs(logDir),
		BaseURL:  tenant.ForeverBaseURL,
	}
}

func legacyInstall(dir, host string, realm tenant.Realm) Install {
	logDir := filepath.Join(dir, "Logs")
	return Install{
		Flavor:    realm.Name,
		Dir:       dir,
		LogDir:    logDir,
		LogGlobs:  append(wowCombatLogGlobs(logDir), filepath.Join(dir, "CustomData", "Chronicle_*.txt")),
		BaseURL:   realm.BaseURL,
		Realmlist: host,
	}
}

func wowCombatLogGlobs(logDir string) []string {
	return []string{
		filepath.Join(logDir, "WoWCombatLog.txt"),
		filepath.Join(logDir, "WoWCombatLog-*.txt"),
	}
}

// StandardRoots lists the places WoW is commonly installed on this OS.
func StandardRoots() []string {
	home, _ := os.UserHomeDir()
	var roots []string
	switch runtime.GOOS {
	case "darwin":
		roots = append(roots, "/Applications/World of Warcraft")
		if home != "" {
			roots = append(roots, filepath.Join(home, "Applications", "World of Warcraft"))
		}
	case "windows":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			if p := os.Getenv(env); p != "" {
				roots = append(roots, filepath.Join(p, "World of Warcraft"))
			}
		}
		roots = append(roots,
			`C:\Program Files (x86)\World of Warcraft`,
			`C:\Program Files\World of Warcraft`,
		)
		for _, drive := range []string{"C", "D", "E", "F"} {
			roots = append(roots, drive+`:\Games\World of Warcraft`)
		}
	default:
		if home == "" {
			break
		}
		roots = append(roots,
			filepath.Join(home, ".wine", "drive_c", "Program Files (x86)", "World of Warcraft"),
			filepath.Join(home, "Games", "world-of-warcraft"),
		)
		for _, pattern := range []string{
			filepath.Join(home, "Games", "*", "drive_c", "Program Files (x86)", "World of Warcraft"),
			filepath.Join(home, ".local", "share", "lutris", "runners", "wine", "*", "drive_c", "Program Files (x86)", "World of Warcraft"),
			filepath.Join(home, ".local", "share", "bottles", "bottles", "*", "drive_c", "Program Files (x86)", "World of Warcraft"),
		} {
			matches, _ := filepath.Glob(pattern)
			roots = append(roots, matches...)
		}
	}
	return roots
}

// Detect scans install roots for Blizzard flavor folders and legacy clients.
func Detect(roots []string, _ tenant.Layout) []Install {
	var out []Install
	seen := map[string]bool{}
	for _, root := range roots {
		for _, inst := range scan(root) {
			if !seen[inst.Dir] {
				seen[inst.Dir] = true
				out = append(out, inst)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// Resolve normalizes a chosen root, Logs or CustomData folder, application
// bundle, realmlist, or executable to the supported clients it contains.
func Resolve(path string, _ tenant.Layout) ([]Install, error) {
	path = filepath.Clean(path)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	dir, climb := path, 0
	if !fi.IsDir() {
		dir, climb = filepath.Dir(path), 6
	} else {
		base := strings.ToLower(filepath.Base(path))
		if strings.Contains(strings.ToLower(path), ".app") || base == "logs" || base == "customdata" || base == "data" {
			climb = 6
		}
	}

	for {
		if found := scan(dir); len(found) > 0 {
			return found, nil
		}
		if host, err := ReadRealmlist(dir); err == nil {
			if _, ok := tenant.ForRealm(host); !ok {
				return nil, &UnsupportedRealmError{Host: host}
			}
		}
		parent := filepath.Dir(dir)
		if climb == 0 || parent == dir {
			return nil, ErrNotFound
		}
		dir, climb = parent, climb-1
	}
}

func scan(root string) []Install {
	var out []Install
	if inst, ok := installAt(root); ok {
		out = append(out, inst)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if inst, ok := installAt(filepath.Join(root, e.Name())); ok {
			out = append(out, inst)
		}
	}
	return out
}

func installAt(dir string) (Install, bool) {
	if exists(filepath.Join(dir, ".flavor.info")) {
		return foreverInstall(dir), true
	}
	host, err := ReadRealmlist(dir)
	if err != nil {
		return Install{}, false
	}
	realm, ok := tenant.ForRealm(host)
	if !ok {
		return Install{}, false
	}
	return legacyInstall(dir, host, realm), true
}

// ReadRealmlist finds and parses a legacy client's realmlist.wtf. Clients place
// it either beside Wow.exe or under a localized Data/<locale> directory.
func ReadRealmlist(dir string) (string, error) {
	paths := []string{filepath.Join(dir, "realmlist.wtf")}
	localized, _ := filepath.Glob(filepath.Join(dir, "Data", "*", "realmlist.wtf"))
	paths = append(paths, localized...)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		host := parseRealmlist(f)
		_ = f.Close()
		if host != "" {
			return host, nil
		}
	}
	return "", ErrNotFound
}

func parseRealmlist(r io.Reader) string {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.EqualFold(fields[0], "set") || !strings.EqualFold(fields[1], "realmlist") {
			continue
		}
		host := strings.Trim(fields[2], "\"'")
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if colon := strings.LastIndex(host, ":"); colon > 0 {
			host = host[:colon]
		}
		return host
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
