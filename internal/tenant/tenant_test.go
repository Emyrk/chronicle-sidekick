package tenant_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
)

func TestResolve(t *testing.T) {
	t.Parallel()
	require.Equal(t, tenant.AuthBaseURL, tenant.Resolve(tenant.Tenant{}, "").BaseURL)
	require.Equal(t, "https://x.example", tenant.Resolve(tenant.Tenant{}, "https://x.example/").BaseURL)
	// Old saved single-server configuration migrates to the shared auth host.
	old := tenant.Tenant{BaseURL: tenant.ForeverBaseURL, Format: "v22-cleu", Flavor: "vanilla,wow-forever"}
	require.Equal(t, tenant.AuthBaseURL, tenant.Resolve(old, "").BaseURL)
}

func TestForRealm(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"192.168.1.214":           "https://octoptr.chronicleclassic.com",
		"185.165.170.6":           "https://octo.chronicleclassic.com",
		"PLAY.OCTOWOW.ST.":        "https://octo.chronicleclassic.com",
		"apac.capycraft.io":       "https://capy.chronicleclassic.com",
		"logon.vanillaplus.org":   "https://vanillaplus.chronicleclassic.com",
		"login.twinstar-wow.com":  "https://kronos.chronicleclassic.com",
		"login2.twinstar-wow.com": "https://kronos.chronicleclassic.com",
		"login3.twinstar-wow.com": "https://kronos.chronicleclassic.com",
		"login.moonwell.su":       "https://moonwell.chronicleclassic.com",
		"crusader-storm.com":      "https://crusader-storm.chronicleclassic.com",
		"logon.chromiecraft.com":  "https://chromie.chronicleclassic.com",
		"logon.warmane.com":       "https://warmane.chronicleclassic.com",
	}
	for host, want := range tests {
		realm, ok := tenant.ForRealm(host)
		require.True(t, ok, host)
		require.Equal(t, want, realm.BaseURL, host)
	}
	_, ok := tenant.ForRealm("unknown.example")
	require.False(t, ok)
}
