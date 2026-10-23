// Package store persists the uploader's settings and sign-in token.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
)

const keyringService = "chronicle-sidekick"
const keyringUser = "default"

// State is everything the uploader remembers between runs.
type State struct {
	// InstallationID is the stable identity Chronicle uses to manage this
	// Sidekick installation. It is generated once and reused across pairings.
	InstallationID string `json:"installation_id,omitempty"`
	// Tenant is the server chosen with --server or by pairing; empty means
	// the default tenant.
	Tenant tenant.Tenant `json:"tenant,omitempty"`
	// WoWPath is the single folder saved by older releases. It is read as a
	// fallback and cleared the next time the folder list changes.
	WoWPath string `json:"wow_path,omitempty"`
	// WoWPaths are user-selected WoW folders. Multiple clients can be watched
	// and routed independently by their realmlists.
	WoWPaths []string `json:"wow_paths,omitempty"`
	// KeepLog turns off renaming the combat log after it was uploaded. The
	// zero value archives, which is the default.
	KeepLog bool `json:"keep_log,omitempty"`
	// Config is the last valid server-managed config applied by Sidekick.
	Config sidekick.SidekickConfig `json:"config,omitempty"`
	// Uploaded maps a log path to the size that has been uploaded.
	Uploaded map[string]int64 `json:"uploaded,omitempty"`
	// Logs is the upload ledger: it maps a log's content-keyed source id
	// (upload.Identify) to what Chronicle has of it. Unlike Uploaded, which is
	// keyed by path and forgotten when the file is renamed or moved, the ledger
	// is what keeps a log the server already has from being sent again.
	Logs map[string]Log `json:"logs,omitempty"`
	// LastReportURL is the most recent report, for the tray menu.
	LastReportURL string `json:"last_report_url,omitempty"`
}

// EffectiveConfig returns the valid config currently applied. Older state files
// have no config, so they use the disabled default.
func (s State) EffectiveConfig() sidekick.SidekickConfig {
	if s.Config.Validate() == nil {
		return s.Config
	}
	return sidekick.DefaultConfig()
}

// EffectiveConfigHash computes the hash of the config currently applied.
func (s State) EffectiveConfigHash() string {
	hash, err := sidekick.HashConfig(s.EffectiveConfig())
	if err != nil {
		return ""
	}
	return hash
}

// Log is one entry of the upload ledger.
type Log struct {
	// Size is how many bytes of the log reached the server.
	Size int64 `json:"size"`
	// ReportURL is where it can be viewed.
	ReportURL string `json:"report_url,omitempty"`
	// At is when the entry was last written. The oldest entries are dropped
	// once the ledger is full.
	At time.Time `json:"at"`
}

// LedgerMax bounds the ledger: one entry is kept per log file ever uploaded,
// which is a handful per play session, so the state file cannot grow forever.
const LedgerMax = 1000

// RecordLog adds or replaces the ledger entry for a source id, dropping the
// oldest entries once the ledger is full.
func (s *State) RecordLog(id string, l Log) {
	if s.Logs == nil {
		s.Logs = map[string]Log{}
	}
	s.Logs[id] = l
	for len(s.Logs) > LedgerMax {
		oldest, at := "", time.Time{}
		for k, v := range s.Logs {
			if oldest == "" || v.At.Before(at) {
				oldest, at = k, v.At
			}
		}
		delete(s.Logs, oldest)
	}
}

// Store is a JSON file on disk.
type Store struct {
	path string
	mu   sync.Mutex
	st   State
}

// Dir is the directory holding the uploader's files.
func Dir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "chronicle-sidekick"), nil
}

// Open loads the state file at path, or starts empty.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		// A corrupt file must not stop the uploader; start over.
		s.st = State{}
	}
	return s, nil
}

// Get returns a copy of the state.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.st
	cp.WoWPaths = append([]string(nil), s.st.WoWPaths...)
	cp.Uploaded = make(map[string]int64, len(s.st.Uploaded))
	for k, v := range s.st.Uploaded {
		cp.Uploaded[k] = v
	}
	cp.Logs = make(map[string]Log, len(s.st.Logs))
	for k, v := range s.st.Logs {
		cp.Logs[k] = v
	}
	return cp
}

// Update applies fn and saves.
func (s *Store) Update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// LoadToken reads OAuth credentials from the OS keychain. Raw bearer tokens
// from older development builds are accepted as non-expiring access tokens.
func LoadToken() (*oauth2.Token, error) {
	encoded, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeToken(encoded)
}

// SaveToken stores OAuth credentials in the OS keychain.
func SaveToken(token *oauth2.Token) error {
	encoded, err := encodeToken(token)
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, keyringUser, encoded)
}

// SaveAccessToken stores a non-expiring bearer token for local development.
func SaveAccessToken(token string) error {
	return SaveToken(&oauth2.Token{AccessToken: token, TokenType: "Bearer"})
}

func encodeToken(token *oauth2.Token) (string, error) {
	if token == nil || token.AccessToken == "" {
		return "", errors.New("cannot save empty OAuth token")
	}
	encoded, err := json.Marshal(token)
	return string(encoded), err
}

func decodeToken(encoded string) (*oauth2.Token, error) {
	var token oauth2.Token
	if err := json.Unmarshal([]byte(encoded), &token); err != nil {
		// Before OAuth device authorization, Sidekick stored a raw bearer token.
		// Preserve that format for development tokens and a smooth upgrade.
		if encoded != "" {
			return &oauth2.Token{AccessToken: encoded, TokenType: "Bearer"}, nil
		}
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, errors.New("saved OAuth token has no access token")
	}
	return &token, nil
}

// DeleteToken removes the OAuth credentials from the OS keychain.
func DeleteToken() error {
	err := keyring.Delete(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
