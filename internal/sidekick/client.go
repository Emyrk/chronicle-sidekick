// Package sidekick reports this installation's health and configuration to Chronicle.
package sidekick

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Emyrk/chronicle-sidekick/internal/inspection"
	"github.com/Emyrk/chronicle-sidekick/internal/logformat"
)

const statusPath = "/api/v1/sidekick/status"

// WoWDirectory is one configured World of Warcraft installation.
type WoWDirectory struct {
	ID            string              `json:"id"`
	Path          string              `json:"path"`
	TenantName    string              `json:"tenant_name"`
	TenantBaseURL string              `json:"tenant_base_url"`
	Realmlist     string              `json:"realmlist,omitempty"`
	LogFormat     logformat.LogFormat `json:"log_format,omitempty"`
	Inspection    inspection.Report   `json:"inspection"`
}

// DirectoryID returns the opaque, stable-on-this-machine ID used for local and managed settings.
func DirectoryID(path string) string {
	path = filepath.Clean(path)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	sum := sha256.Sum256([]byte(path))
	return "wowdir_" + hex.EncodeToString(sum[:])
}

// Issue is one currently active, sanitized Sidekick problem.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

const CurrentConfigVersion = 1

// AutoUploadMode selects a Sidekick-owned upload trigger implementation.
type AutoUploadMode string

const (
	AutoUploadDisabled   AutoUploadMode = "disabled"
	AutoUploadOnGameExit AutoUploadMode = "on_game_exit"
)

// WoWDirectoryConfig is the server-managed behavior for one reported directory ID.
type WoWDirectoryConfig struct {
	AutoUpload AutoUploadMode `json:"auto_upload"`
}

// ArchivePolicy controls local retention of successfully uploaded combat logs.
type ArchivePolicy struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

// SidekickConfig is the server-managed behavior applied by Sidekick.
type SidekickConfig struct {
	Version        int                           `json:"version"`
	Archive        ArchivePolicy                 `json:"archive"`
	WoWDirectories map[string]WoWDirectoryConfig `json:"wow_directories"`
}

// DefaultConfig is the behavior used before Chronicle has sent a config.
func DefaultConfig() SidekickConfig {
	return SidekickConfig{
		Version:        CurrentConfigVersion,
		Archive:        ArchivePolicy{Enabled: true, RetentionDays: 7},
		WoWDirectories: map[string]WoWDirectoryConfig{},
	}
}

// AutoUploadCapabilities lists the implementations in this Sidekick build.
func AutoUploadCapabilities() map[logformat.LogFormat][]AutoUploadMode {
	return map[logformat.LogFormat][]AutoUploadMode{
		logformat.LogFormat112aCcAddon:       {AutoUploadOnGameExit},
		logformat.LogFormatHermesproxy1142Cc: {AutoUploadOnGameExit},
		logformat.LogFormat243CcAddon:        {AutoUploadOnGameExit},
		logformat.LogFormat335aCcAddon:       {AutoUploadOnGameExit},
	}
}

// CanonicalConfigJSON returns the stable JSON representation Chronicle hashes.
// Struct field order is fixed, and encoding/json deterministically orders any
// future string-keyed maps.
func CanonicalConfigJSON(config SidekickConfig) ([]byte, error) {
	return json.Marshal(config)
}

// HashConfig returns the lowercase SHA-256 hex digest of CanonicalConfigJSON.
func HashConfig(config SidekickConfig) (string, error) {
	canonical, err := CanonicalConfigJSON(config)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Validate rejects configs this Sidekick version cannot safely apply.
func (c SidekickConfig) Validate() error {
	if c.Version != CurrentConfigVersion {
		return fmt.Errorf("unsupported Sidekick config version %d", c.Version)
	}
	if c.Archive.Enabled && (c.Archive.RetentionDays < 1 || c.Archive.RetentionDays > 365) {
		return fmt.Errorf("archive retention days must be between 1 and 365")
	}
	for id, directory := range c.WoWDirectories {
		if directory.AutoUpload != AutoUploadDisabled && directory.AutoUpload != AutoUploadOnGameExit {
			return fmt.Errorf("directory %s has unsupported auto-upload mode %q", id, directory.AutoUpload)
		}
	}
	return nil
}

// Status is the complete installation snapshot sent in each heartbeat.
type Status struct {
	Platform        string `json:"platform,omitempty"`
	SidekickVersion string `json:"sidekick_version,omitempty"`
	ConfigHash      string `json:"config_hash,omitempty"`
	// StartAtLogin is observed local state. It is not server-managed config.
	StartAtLogin           bool                                     `json:"start_at_login"`
	AutoUploadCapabilities map[logformat.LogFormat][]AutoUploadMode `json:"auto_upload_capabilities"`
	WoWDirectories         []WoWDirectory                           `json:"wow_directories"`
	Issues                 []Issue                                  `json:"issues"`
}

// UpdateResponse tells Sidekick which managed config Chronicle wants applied.
// Installation is intentionally left as raw JSON because Sidekick does not use
// the server's installation representation.
type UpdateResponse struct {
	Installation json.RawMessage `json:"installation,omitempty"`
	SettingsURL  string          `json:"settings_url,omitempty"`
	ConfigHash   string          `json:"config_hash,omitempty"`
	Config       *SidekickConfig `json:"config,omitempty"`
}

// Client reports status to one Chronicle OAuth server.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Update replaces Chronicle's status snapshot for this Sidekick installation
// and returns the server-managed config when it differs from status.ConfigHash.
func (c *Client) Update(ctx context.Context, status Status) (UpdateResponse, error) {
	body, err := json.Marshal(status)
	if err != nil {
		return UpdateResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(c.BaseURL, "/")+statusPath, bytes.NewReader(body))
	if err != nil {
		return UpdateResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return UpdateResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return UpdateResponse{}, fmt.Errorf("read Sidekick status response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if detail := strings.TrimSpace(string(responseBody)); detail != "" {
			return UpdateResponse{}, fmt.Errorf("update Sidekick status: HTTP %d: %s", resp.StatusCode, detail)
		}
		return UpdateResponse{}, fmt.Errorf("update Sidekick status: HTTP %d", resp.StatusCode)
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return UpdateResponse{}, nil
	}
	var result UpdateResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return UpdateResponse{}, fmt.Errorf("decode Sidekick status response: %w", err)
	}
	return result, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}
