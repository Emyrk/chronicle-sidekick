package pair_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/Emyrk/chronicle-sidekick/internal/pair"
)

func TestDeviceAuthorization(t *testing.T) {
	t.Parallel()
	forms := make(chan url.Values, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/device/authorize", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		forms <- cloneValues(r.Form)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "device-code",
			"user_code":        "ABCD-EFGH",
			"verification_uri": "https://chronicle.example/oauth/device?from=test",
			"expires_in":       600,
			"interval":         5,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := &pair.Client{BaseURL: srv.URL}
	device, err := client.Start(context.Background())
	require.NoError(t, err)
	form := <-forms
	require.Equal(t, "chronicle-sidekick", form.Get("client_id"))
	require.Equal(t, "all", form.Get("scope"))
	require.Equal(t, "device-code", device.DeviceCode)
	require.Equal(t, "ABCD-EFGH", device.UserCode)
	require.WithinDuration(t, time.Now().Add(10*time.Minute), device.Expiry, 2*time.Second)

	verification, err := url.Parse(pair.VerificationURL(device))
	require.NoError(t, err)
	require.Equal(t, "https://chronicle.example/oauth/device", verification.Scheme+"://"+verification.Host+verification.Path)
	require.Equal(t, "test", verification.Query().Get("from"))
	require.Equal(t, "ABCD-EFGH", verification.Query().Get("user_code"))
}

func TestWaitOutcomes(t *testing.T) {
	t.Parallel()
	device := &oauth2.DeviceAuthResponse{DeviceCode: "device-code"}
	for _, tc := range []struct {
		name    string
		token   *oauth2.Token
		err     error
		outcome pair.Outcome
		wantErr bool
	}{
		{name: "approved", token: &oauth2.Token{AccessToken: "access", RefreshToken: "refresh"}, outcome: pair.Approved},
		{name: "denied", err: &oauth2.RetrieveError{ErrorCode: "access_denied"}, outcome: pair.Denied},
		{name: "expired", err: &oauth2.RetrieveError{ErrorCode: "expired_token"}, outcome: pair.Expired},
		{name: "server error", err: errors.New("offline"), outcome: pair.Expired, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &pair.Client{DeviceAccessToken: func(context.Context, *oauth2.Config, *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
				return tc.token, tc.err
			}}
			outcome, token, err := client.Wait(context.Background(), device)
			require.Equal(t, tc.outcome, outcome)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.outcome == pair.Approved {
				require.Equal(t, "access", token.AccessToken)
			} else {
				require.Nil(t, token)
			}
		})
	}
}

func TestRefreshAndRevoke(t *testing.T) {
	t.Parallel()
	tokenForms := make(chan url.Values, 1)
	revokeForms := make(chan url.Values, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tokenForms <- cloneValues(r.Form)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "bearer",
			"expires_in":    3600,
		})
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		revokeForms <- cloneValues(r.Form)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := &pair.Client{BaseURL: srv.URL}
	token, err := client.Refresh(context.Background(), &oauth2.Token{
		AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
	})
	require.NoError(t, err)
	refreshForm := <-tokenForms
	require.Equal(t, "refresh_token", refreshForm.Get("grant_type"))
	require.Equal(t, "old-refresh", refreshForm.Get("refresh_token"))
	require.Equal(t, "chronicle-sidekick", refreshForm.Get("client_id"))
	require.Equal(t, "new-access", token.AccessToken)
	require.Equal(t, "new-refresh", token.RefreshToken)
	require.WithinDuration(t, time.Now().Add(time.Hour), token.Expiry, 2*time.Second)

	require.NoError(t, client.Revoke(context.Background(), token))
	revoked := <-revokeForms
	require.Equal(t, "new-refresh", revoked.Get("token"))
	require.Equal(t, "refresh_token", revoked.Get("token_type_hint"))
	require.Equal(t, "chronicle-sidekick", revoked.Get("client_id"))
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, entries := range values {
		clone[key] = append([]string(nil), entries...)
	}
	return clone
}

func TestRefreshRejectsInvalidGrant(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(srv.Close)

	client := &pair.Client{BaseURL: srv.URL}
	_, err := client.Refresh(context.Background(), &oauth2.Token{
		AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
	})
	require.ErrorIs(t, err, pair.ErrUnauthorized)
}
