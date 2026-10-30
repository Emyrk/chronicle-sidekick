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
	require.JSONEq(t, `{"version":1,"auto_upload":{"enabled":false}}`, string(canonical))
	require.Equal(t, `{"version":1,"auto_upload":{"enabled":false}}`, string(canonical))
	require.NotContains(t, string(canonical), "start_at_login")

	hash, err := sidekick.HashConfig(sidekick.DefaultConfig())
	require.NoError(t, err)
	require.Equal(t, "b568659ffa9fe99b312ce1c6ad2724d8b46924833e0641423329c91d9f698be5", hash)
}

func TestStatusAlwaysSerializesLocalSettings(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(sidekick.Status{})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"start_at_login": false,
		"archive_after_upload": false,
		"wow_directories": null,
		"issues": null
	}`, string(body))
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
				Version:    sidekick.CurrentConfigVersion,
				AutoUpload: sidekick.AutoUploadConfig{Enabled: true},
			},
		})
	}))
	t.Cleanup(server.Close)

	status := sidekick.Status{
		Platform: "windows/amd64", Version: "1.2.3", ConfigHash: "hash-1",
		StartAtLogin: true, ArchiveAfterUpload: true,
		WoWDirectories: []sidekick.WoWDirectory{{
			Path: `C:\\Games\\WoW`, TenantName: "Warmane",
			TenantBaseURL: "https://warmane.chronicleclassic.com", Realmlist: "logon.warmane.com",
		}},
		Issues: []sidekick.Issue{},
	}
	client := sidekick.Client{BaseURL: server.URL + "/", Token: "access-token"}
	response, err := client.Update(context.Background(), status)
	require.NoError(t, err)
	require.Equal(t, "hash-2", response.ConfigHash)
	require.NotNil(t, response.Config)
	require.True(t, response.Config.AutoUpload.Enabled)
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
