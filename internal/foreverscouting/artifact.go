package foreverscouting

import (
	"bytes"
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
	Account        string `json:"account"`
	Source         string `json:"source"`
	CollectionsLua string `json:"collections_lua"`
	path           string
	body           []byte
	span           Span
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
		span, present, err := Locate(body)
		if err != nil {
			warnings = append(warnings, Warning{path, err})
			continue
		}
		if !present {
			continue
		}
		envelope.Sources = append(envelope.Sources, Source{
			Account:        account,
			Source:         filepath.ToSlash(filepath.Join("WTF", "Account", account, "SavedVariables", "ChronicleCompanionForever.lua")),
			CollectionsLua: string(body[span.Start:span.End]),
			path:           path, body: body, span: span,
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
// account contains non-empty collections data.
func Write(root, path string) (string, []Warning, error) {
	envelope, warnings, err := Build(root)
	if err != nil || len(envelope.Sources) == 0 {
		return "", warnings, err
	}
	return WriteEnvelope(envelope, path, warnings)
}

// WriteEnvelope durably saves a captured envelope before its sources may be reset.
func WriteEnvelope(envelope Envelope, path string, warnings []Warning) (string, []Warning, error) {
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
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return "", warnings, err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return "", warnings, err
		}
		if closeErr != nil {
			return "", warnings, closeErr
		}
	}
	return path, warnings, nil
}

// Drain archives the latest complete collections before replacing only that value.
// persist must durably retain the envelope and associate it with every batch log.
// It is deliberately independent of process state: live clients may restore their
// in-memory collections on their next flush. External writers cannot be locked.
func Drain(root string, persist func(Envelope) error) ([]Warning, error) {
	retained := map[string]Source{}
	for attempt := 0; attempt < 8; attempt++ {
		envelope, warnings, err := Build(root)
		if err != nil || len(envelope.Sources) == 0 {
			return warnings, err
		}
		for _, source := range envelope.Sources {
			retained[source.Source] = source
		}
		archive := Envelope{SchemaVersion: 1, Kind: Kind}
		for _, source := range retained {
			archive.Sources = append(archive.Sources, source)
		}
		sort.Slice(archive.Sources, func(i, j int) bool { return archive.Sources[i].Source < archive.Sources[j].Source })
		if err := persist(archive); err != nil {
			return warnings, err
		}
		changed := false
		for _, source := range envelope.Sources {
			if err := reset(source); err != nil {
				if errors.Is(err, errChanged) {
					changed = true
					continue
				}
				return warnings, err
			}
		}
		if !changed {
			return warnings, nil
		}
	}
	return nil, errors.New("collections kept changing during capture; last durable capture retained")
}

var errChanged = errors.New("SavedVariables changed during capture")

func reset(source Source) error {
	info, err := os.Lstat(source.path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	body := append([]byte{}, source.body[:source.span.Start]...)
	body = append(body, []byte("{}")...)
	body = append(body, source.body[source.span.End:]...)
	f, err := os.CreateTemp(filepath.Dir(source.path), ".sidekick-collections-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	latest, err := os.ReadFile(source.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(latest, source.body) {
		return errChanged
	}
	if err := os.Rename(f.Name(), source.path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(source.path))
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	}
	return nil
}
