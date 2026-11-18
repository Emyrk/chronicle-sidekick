package foreverscouting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"
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

// Build captures only the newest regular, non-symlink Forever SavedVariables
// source by collections.last_updated (or file mtime when unavailable). Ties use
// file mtime, then account/source ascending. Empty or invalid collections never
// fall back to older accounts.
func Build(root string) (Envelope, []Warning, error) {
	return build(root, "")
}

// selectedPath pins recapture to the account selected at the start of a drain.
func build(root, selectedPath string) (Envelope, []Warning, error) {
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
	var selected Source
	var newest *big.Rat
	var newestMtime time.Time
	var selectedErr error
	var selectedPresent bool
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
		if selectedPath != "" && path != selectedPath {
			continue
		}
		source := filepath.ToSlash(filepath.Join("WTF", "Account", account, "SavedVariables", "ChronicleCompanionForever.lua"))
		body, readErr := os.ReadFile(path)
		var span Span
		var present bool
		var updated *big.Rat
		if readErr == nil {
			span, present, updated, readErr = locate(body)
		}
		freshness := updated
		if freshness == nil {
			freshness = unixInstant(info.ModTime())
		}
		comparison := 1
		if newest != nil {
			comparison = freshness.Cmp(newest)
		}
		if selected.path == "" || comparison > 0 || (comparison == 0 &&
			(info.ModTime().After(newestMtime) || (info.ModTime().Equal(newestMtime) &&
				(account < selected.Account || (account == selected.Account && source < selected.Source))))) {
			selected = Source{Account: account, Source: source, path: path, body: body, span: span}
			newest, newestMtime = freshness, info.ModTime()
			selectedErr, selectedPresent = readErr, present
		}
	}

	if selected.path == "" {
		return envelope, warnings, nil
	}
	if selectedErr != nil {
		return envelope, append(warnings, Warning{selected.path, selectedErr}), nil
	}
	if selectedPresent {
		selected.CollectionsLua = string(selected.body[selected.span.Start:selected.span.End])
		envelope.Sources = append(envelope.Sources, selected)
	}

	return envelope, warnings, nil
}

// unixInstant preserves subsecond mtime precision when comparing it to a marker
// expressed in whole Unix seconds, without float rounding or int64 nanosecond overflow.
func unixInstant(stamp time.Time) *big.Rat {
	seconds := new(big.Rat).SetInt64(stamp.Unix())
	fraction := new(big.Rat).SetFrac64(int64(stamp.Nanosecond()), 1_000_000_000)
	return seconds.Add(seconds, fraction)
}

// Write builds and durably writes an artifact. It returns an empty path when
// the newest account has no non-empty, valid collections data.
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
// It selects one newest account and pins retries to that source, leaving all other
// accounts untouched. persist must durably retain the envelope and associate it
// with every batch log.
// It is deliberately independent of process state: live clients may restore their
// in-memory collections on their next flush. External writers cannot be locked.
func Drain(root string, persist func(Envelope) error) ([]Warning, error) {
	selectedPath := ""
	for attempt := 0; attempt < 8; attempt++ {
		envelope, warnings, err := build(root, selectedPath)
		if err != nil || len(envelope.Sources) == 0 {
			return warnings, err
		}
		selectedPath = envelope.Sources[0].path
		if err := persist(envelope); err != nil {
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
