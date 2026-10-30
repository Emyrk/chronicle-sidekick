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
	"strings"
	"time"
)

const statusPath = "/api/v1/sidekick/status"

// WoWDirectory is one configured World of Warcraft installation.
type WoWDirectory struct {
	Path          string `json:"path"`
	TenantName    string `json:"tenant_name"`
	TenantBaseURL string `json:"tenant_base_url"`
	Realmlist     string `json:"realmlist,omitempty"`
}

// Issue is one currently active, sanitized Sidekick problem.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

const CurrentConfigVersion = 1

// SidekickConfig is the server-managed behavior applied by Sidekick.
type SidekickConfig struct {
	Version    int              `json:"version"`
	AutoUpload AutoUploadConfig `json:"auto_upload"`
}

// AutoUploadConfig controls automatic combat-log uploads.
type AutoUploadConfig struct {
	Enabled bool `json:"enabled"`
}

// DefaultConfig is the behavior used before Chronicle has sent a config.
func DefaultConfig() SidekickConfig {
	return SidekickConfig{Version: CurrentConfigVersion}
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
	return nil
}

// Status is the complete installation snapshot sent in each heartbeat.
type Status struct {
	Platform   string `json:"platform,omitempty"`
	Version    string `json:"version,omitempty"`
	ConfigHash string `json:"config_hash,omitempty"`
	// StartAtLogin is observed local state. It is not server-managed config.
	StartAtLogin bool `json:"start_at_login"`
	// ArchiveAfterUpload is the local tray setting, inverse of Store.KeepLog.
	ArchiveAfterUpload bool           `json:"archive_after_upload"`
	WoWDirectories     []WoWDirectory `json:"wow_directories"`
	Issues             []Issue        `json:"issues"`
}

// UpdateResponse tells Sidekick which managed config Chronicle wants applied.
// Installation is intentionally left as raw JSON because Sidekick does not use
// the server's installation representation.
type UpdateResponse struct {
	Installation json.RawMessage `json:"installation,omitempty"`
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
