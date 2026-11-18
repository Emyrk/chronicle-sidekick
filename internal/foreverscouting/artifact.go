package foreverscouting

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

const (
	Filename = "sidekick-events-v1.json"
	Kind     = "sidekick-events"
)

type Envelope struct {
	SchemaVersion int      `json:"schema_version"`
	Kind          string   `json:"kind"`
	Sources       []Source `json:"sources"`
}

type Source struct {
	Account     string `json:"account"`
	Source      string `json:"source"`
	ScoutingLua string `json:"scouting_lua"`
}

type Warning struct {
	Path string
	Err  error
}

func (w Warning) Error() string { return fmt.Sprintf("%s: %v", w.Path, w.Err) }

// Build discovers every regular, non-symlink Forever SavedVariables source.
// Invalid files are returned as warnings and do not prevent other accounts.
func Build(root string) (Envelope, []Warning, error) {
	envelope := Envelope{SchemaVersion: 1, Kind: Kind, Sources: []Source{}}
	accountRoot := filepath.Join(root, "WTF", "Account")
	entries, err := os.ReadDir(accountRoot)
	if errors.Is(err, os.ErrNotExist) {
		return envelope, nil, nil
	}
	if err != nil {
		return envelope, nil, err
	}
	var warnings []Warning
	for _, entry := range entries {
		account := entry.Name()
		accountDir := filepath.Join(accountRoot, account)
		info, err := os.Lstat(accountDir)
		if err != nil {
			warnings = append(warnings, Warning{accountDir, err})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			warnings = append(warnings, Warning{accountDir, errors.New("account path is not a regular directory")})
			continue
		}
		savedDir := filepath.Join(accountDir, "SavedVariables")
		info, err = os.Lstat(savedDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, Warning{savedDir, err})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			warnings = append(warnings, Warning{savedDir, errors.New("SavedVariables path is not a regular directory")})
			continue
		}
		path := filepath.Join(savedDir, "ChronicleCompanionForever.lua")
		info, err = os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, Warning{path, err})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			warnings = append(warnings, Warning{path, errors.New("source is not a regular file")})
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, Warning{path, err})
			continue
		}
		scouting, present, err := Extract(body)
		if err != nil {
			warnings = append(warnings, Warning{path, err})
			continue
		}
		if !present {
			continue
		}
		envelope.Sources = append(envelope.Sources, Source{
			Account:     account,
			Source:      filepath.ToSlash(filepath.Join("WTF", "Account", account, "SavedVariables", "ChronicleCompanionForever.lua")),
			ScoutingLua: scouting,
		})
	}
	sort.Slice(envelope.Sources, func(i, j int) bool {
		if envelope.Sources[i].Account != envelope.Sources[j].Account {
			return envelope.Sources[i].Account < envelope.Sources[j].Account
		}
		return envelope.Sources[i].Source < envelope.Sources[j].Source
	})
	return envelope, warnings, nil
}

// Write builds and durably writes an artifact. It returns an empty path when no
// account contains non-empty scouting data.
func Write(root, path string) (string, []Warning, error) {
	envelope, warnings, err := Build(root)
	if err != nil || len(envelope.Sources) == 0 {
		return "", warnings, err
	}
	body, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return "", warnings, err
	}
	body = append(body, '\n')
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", warnings, err
	}
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return "", warnings, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", warnings, err
	}
	if err := f.Close(); err != nil {
		return "", warnings, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", warnings, err
	}
	removeTmp = false
	if runtime.GOOS != "windows" {
		if dir, err := os.Open(filepath.Dir(path)); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	return path, warnings, nil
}
