// Package pair implements Chronicle's OAuth 2.0 device authorization flow.
package pair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Outcome is how a device authorization ended.
type Outcome int

const (
	// Approved means Token is set.
	Approved Outcome = iota
	Denied
	Expired
)

// ErrUnauthorized means the saved OAuth credentials can no longer be used.
var ErrUnauthorized = errors.New("oauth credentials rejected")

// Client talks to one Chronicle OAuth service.
type Client struct {
	// BaseURL is the Chronicle site, e.g. https://chronicleclassic.com.
	BaseURL string
	HTTP    *http.Client
	// DeviceAccessToken overrides OAuth polling in tests.
	DeviceAccessToken func(context.Context, *oauth2.Config, *oauth2.DeviceAuthResponse) (*oauth2.Token, error)
}

// Start begins the RFC 8628 device authorization flow.
func (c *Client) Start(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	ctx = c.clientContext(ctx)
	out, err := c.config().DeviceAuth(ctx)
	if err == nil && (out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "") {
		err = errors.New("server sent an incomplete device authorization response")
	}
	return out, err
}

// VerificationURL returns the browser URL, including the user code when the
// server did not provide verification_uri_complete itself.
func VerificationURL(d *oauth2.DeviceAuthResponse) string {
	if d == nil {
		return ""
	}
	if d.VerificationURIComplete != "" {
		return d.VerificationURIComplete
	}
	u, err := url.Parse(d.VerificationURI)
	if err != nil {
		return d.VerificationURI
	}
	q := u.Query()
	q.Set("user_code", d.UserCode)
	u.RawQuery = q.Encode()
	return u.String()
}

// Wait polls until the user approves, denies, or lets the device code expire.
func (c *Client) Wait(ctx context.Context, d *oauth2.DeviceAuthResponse) (Outcome, *oauth2.Token, error) {
	poll := c.DeviceAccessToken
	if poll == nil {
		poll = func(ctx context.Context, config *oauth2.Config, d *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
			return config.DeviceAccessToken(ctx, d)
		}
	}
	token, err := poll(c.clientContext(ctx), c.config(), d)
	if err == nil {
		if token == nil || token.AccessToken == "" || token.RefreshToken == "" {
			return Expired, nil, errors.New("server sent incomplete OAuth credentials")
		}
		return Approved, token, nil
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		switch retrieve.ErrorCode {
		case "access_denied":
			return Denied, nil, nil
		case "expired_token":
			return Expired, nil, nil
		}
	}
	if errors.Is(err, context.DeadlineExceeded) && d != nil && !d.Expiry.IsZero() {
		return Expired, nil, nil
	}
	return Expired, nil, err
}

// Refresh returns a usable access token, refreshing and rotating the saved
// refresh token when necessary.
func (c *Client) Refresh(ctx context.Context, token *oauth2.Token) (*oauth2.Token, error) {
	if token == nil || token.AccessToken == "" {
		return nil, ErrUnauthorized
	}
	next, err := c.config().TokenSource(c.clientContext(ctx), token).Token()
	if err == nil {
		return next, nil
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) && (retrieve.ErrorCode == "invalid_grant" || retrieve.ErrorCode == "invalid_client") {
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, retrieve.ErrorCode)
	}
	return nil, err
}

// Revoke invalidates the refresh token and its associated access token. OAuth
// revocation is best effort; callers should still remove local credentials if
// this fails because the user explicitly asked to disconnect.
func (c *Client) Revoke(ctx context.Context, token *oauth2.Token) error {
	if token == nil {
		return nil
	}
	value, hint := token.RefreshToken, "refresh_token"
	if value == "" {
		value, hint = token.AccessToken, "access_token"
	}
	if value == "" {
		return nil
	}
	form := url.Values{
		"client_id":       {clientID},
		"token":           {value},
		"token_type_hint": {hint},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/oauth/revoke"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var out oauthError
	_ = json.Unmarshal(body, &out)
	detail := out.Description
	if detail == "" {
		detail = out.Code
	}
	if detail != "" {
		return fmt.Errorf("revoke OAuth token: HTTP %d: %s", resp.StatusCode, detail)
	}
	return fmt.Errorf("revoke OAuth token: HTTP %d", resp.StatusCode)
}

func (c *Client) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: clientID,
		Scopes:   []string{scopeAll},
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: c.endpoint("/oauth/device/authorize"),
			TokenURL:      c.endpoint("/oauth/token"),
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}
}

func (c *Client) clientContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, c.httpClient())
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}
