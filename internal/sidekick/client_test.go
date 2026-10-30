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
	require.JSONEq(t, `{"version":1,"wow_directories":{}}`, string(canonical))
	require.Equal(t, `{"version":1,"wow_directories":{}}`, string(canonical))
	require.NotContains(t, string(canonical), "start_at_login")

	hash, err := sidekick.HashConfig(sidekick.DefaultConfig())
	require.NoError(t, err)
	require.Equal(t, "6cf556c64ba0052d92ec879851a5ff6c5e60756e88f2da5f2eca540cda76497f", hash)
}

func TestConfigValidatesFormatAndAutoUploadMode(t *testing.T) {
	t.Parallel()
	valid := sidekick.SidekickConfig{
		Version: sidekick.CurrentConfigVersion,
		WoWDirectories: map[string]sidekick.WoWDirectoryConfig{
			"dir": {GameFormat: sidekick.GameFormatWrath335a, AutoUpload: sidekick.AutoUploadOnGameExit},
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
			ConfigHash:   "hash-2",
			Config: &sidekick.SidekickConfig{
				Version: sidekick.CurrentConfigVersion,
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
