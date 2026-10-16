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
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	status := sidekick.Status{
		Platform: "windows/amd64", Version: "1.2.3",
		WoWDirectories: []sidekick.WoWDirectory{{
			Path: `C:\\Games\\WoW`, TenantName: "Warmane",
			TenantBaseURL: "https://warmane.chronicleclassic.com", Realmlist: "logon.warmane.com",
		}},
		Issues: []sidekick.Issue{},
	}
	client := sidekick.Client{BaseURL: server.URL + "/", Token: "access-token"}
	require.NoError(t, client.Update(context.Background(), status))
	request := <-requests
	require.NoError(t, request.err)
	require.Equal(t, http.MethodPut, request.method)
	require.Equal(t, "/api/v1/sidekick/status", request.path)
	require.Equal(t, "Bearer access-token", request.authorization)
	require.Equal(t, status, request.status)
}

func TestUpdateReturnsServerError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "reconnect Sidekick", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	client := sidekick.Client{BaseURL: server.URL, Token: "access-token"}
	err := client.Update(context.Background(), sidekick.Status{})
	require.ErrorContains(t, err, "HTTP 400")
	require.ErrorContains(t, err, "reconnect Sidekick")
}
