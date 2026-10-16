// Package sidekick reports this installation's health and configuration to Chronicle.
package sidekick

import (
	"bytes"
	"context"
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

// Status is the complete installation snapshot sent in each heartbeat.
type Status struct {
	Platform       string         `json:"platform,omitempty"`
	Version        string         `json:"version,omitempty"`
	WoWDirectories []WoWDirectory `json:"wow_directories"`
	Issues         []Issue        `json:"issues"`
}

// Client reports status to one Chronicle OAuth server.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Update replaces Chronicle's status snapshot for this Sidekick installation.
func (c *Client) Update(ctx context.Context, status Status) error {
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(c.BaseURL, "/")+statusPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if detail := strings.TrimSpace(string(responseBody)); detail != "" {
		return fmt.Errorf("update Sidekick status: HTTP %d: %s", resp.StatusCode, detail)
	}
	return fmt.Errorf("update Sidekick status: HTTP %d", resp.StatusCode)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}
