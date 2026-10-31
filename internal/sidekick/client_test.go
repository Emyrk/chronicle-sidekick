package sidekick_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
)

func TestHashConfig(t *testing.T) {
	t.Parallel()
	canonical, err := sidekick.CanonicalConfigJSON(sidekick.DefaultConfig())
	require.NoError(t, err)
	require.JSONEq(t, `{"version":1,"archive":{"enabled":true,"retention_days":7},"wow_directories":{}}`, string(canonical))
	require.Equal(t, `{"version":1,"archive":{"enabled":true,"retention_days":7},"wow_directories":{}}`, string(canonical))
	require.NotContains(t, string(canonical), "start_at_login")

	hash, err := sidekick.HashConfig(sidekick.DefaultConfig())
	require.NoError(t, err)
	require.Equal(t, "03ba74a583c3e096527eb448a456583b975a0f8c32d8517c630ff3954e3528e4", hash)
}

func TestGameFormatUploadFormat(t *testing.T) {
	t.Parallel()
	require.Equal(t, "2.4.3-cc-addon", sidekick.GameFormatTBC243.UploadFormat())
	require.Equal(t, "3.3.5a-cc-addon", sidekick.GameFormatWrath335a.UploadFormat())
	require.Empty(t, sidekick.GameFormat("future").UploadFormat())
}

func TestGameFormatSidekickStorage(t *testing.T) {
	t.Parallel()
	require.False(t, sidekick.GameFormatTBC243.IncludesSidekickStorage())
	require.True(t, sidekick.GameFormatWrath335a.IncludesSidekickStorage())
	require.False(t, sidekick.GameFormat("future").IncludesSidekickStorage())
}

func TestConfigValidatesFormatAndAutoUploadMode(t *testing.T) {
	t.Parallel()
	valid := sidekick.SidekickConfig{
		Version: sidekick.CurrentConfigVersion,
		Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 7},
		WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
			"tbc":   {GameFormat: sidekick.GameFormatTBC243, AutoUpload: sidekick.AutoUploadOnGameExit},
			"wrath": {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
		},
	}
	require.NoError(t, valid.Validate())

	unknownFormat := valid
	unknownFormat.WoWDirectories = map[string]sidekick.WoWDirectoryConfig{
		"dir": {GameFormat: "future", AutoUpload: sidekick.AutoUploadOnGameExit},
	}
	require.ErrorContains(t, unknownFormat.Validate(), "unsupported game format")

	unknownMode := valid
	unknownMode.WoWDirectories = map[string]sidekick.WoWDirectoryConfig{
		"dir": {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: "future"},
	}
	require.ErrorContains(t, unknownMode.Validate(), "unsupported auto-upload mode")

	invalidRetention := valid
	invalidRetention.Archive.RetentionDays = 0
	require.ErrorContains(t, invalidRetention.Validate(), "between 1 and 365")
	invalidRetention.Archive.RetentionDays = 366
	require.ErrorContains(t, invalidRetention.Validate(), "between 1 and 365")
	invalidRetention.Archive.Enabled = false
	require.NoError(t, invalidRetention.Validate())
}

func TestStatusAlwaysSerializesLocalSettings(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(sidekick.Status{})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"start_at_login": false,
		"auto_upload_capabilities": null,
		"wow_directories": null,
		"issues": null
	}`, string(body))
	require.NotContains(t, string(body), "archive_after_upload")
	require.NotContains(t, string(body), `"version"`)
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	type receivedRequest struct {
		method, path, authorization string
		status                      sidekick.Status
		err                         error
	}
	requests := make(chan receivedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := receivedRequest{method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization")}
		got.err = json.NewDecoder(r.Body).Decode(&got.status)
		requests <- got
		_ = json.NewEncoder(w).Encode(sidekick.UpdateResponse{
			Installation: json.RawMessage(`{"id":"installation-1"}`),
			SettingsURL:  "https://chronicle.example/settings/sidekick",
			ConfigHash:   "hash-2",
			Config: &sidekick.SidekickConfig{
				Version: sidekick.CurrentConfigVersion,
				Archive: sidekick.ArchivePolicy{Enabled: true, RetentionDays: 30},
				WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
					"wowdir_1": {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
				},
			},
		})
	}))
	t.Cleanup(server.Close)

	status := sidekick.Status{
		Platform: "windows/amd64", SidekickVersion: "1.2.3", ConfigHash: "hash-1",
		StartAtLogin:           true,
		AutoUploadCapabilities: sidekick.AutoUploadCapabilities(),
		WoWDirectories: []sidekick.WoWDirectory{{
			ID: "wowdir_1", Path: `C:\\Games\\WoW`, TenantName: "Warmane",
			TenantBaseURL: "https://warmane.chronicleclassic.com", Realmlist: "logon.warmane.com",
			GameFormat: sidekick.GameFormatWrath335a,
		}},
		Issues: []sidekick.Issue{},
	}
	client := sidekick.Client{BaseURL: server.URL + "/", Token: "access-token"}
	response, err := client.Update(context.Background(), status)
	require.NoError(t, err)
	require.Equal(t, "hash-2", response.ConfigHash)
	require.Equal(t, "https://chronicle.example/settings/sidekick", response.SettingsURL)
	require.NotNil(t, response.Config)
	require.Equal(t, sidekick.AutoUploadOnGameExit, response.Config.WoWDirectories["wowdir_1"].AutoUpload)
	request := <-requests
	require.NoError(t, request.err)
	require.Equal(t, http.MethodPut, request.method)
	require.Equal(t, "/api/v1/sidekick/status", request.path)
	require.Equal(t, "Bearer access-token", request.authorization)
	require.Equal(t, status, request.status)
}

func TestUpdateToleratesEmptyResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := sidekick.Client{BaseURL: server.URL, Token: "access-token"}
	response, err := client.Update(context.Background(), sidekick.Status{ConfigHash: "current"})
	require.NoError(t, err)
	require.Equal(t, sidekick.UpdateResponse{}, response)
}

func TestUpdateReturnsServerError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "reconnect Sidekick", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	client := sidekick.Client{BaseURL: server.URL, Token: "access-token"}
	_, err := client.Update(context.Background(), sidekick.Status{})
	require.ErrorContains(t, err, "HTTP 400")
	require.ErrorContains(t, err, "reconnect Sidekick")
}
