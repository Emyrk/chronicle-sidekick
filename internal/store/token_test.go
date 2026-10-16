package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestOAuthTokenEncoding(t *testing.T) {
	t.Parallel()
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	encoded, err := encodeToken(&oauth2.Token{
		AccessToken: "access", TokenType: "Bearer", RefreshToken: "refresh", Expiry: expiry,
	})
	require.NoError(t, err)

	token, err := decodeToken(encoded)
	require.NoError(t, err)
	require.Equal(t, "access", token.AccessToken)
	require.Equal(t, "refresh", token.RefreshToken)
	require.Equal(t, expiry, token.Expiry)
}

func TestLegacyRawTokenIsStillReadable(t *testing.T) {
	t.Parallel()
	token, err := decodeToken("legacy-bearer-token")
	require.NoError(t, err)
	require.Equal(t, "legacy-bearer-token", token.AccessToken)
	require.Empty(t, token.RefreshToken)
	require.True(t, token.Expiry.IsZero())
}
