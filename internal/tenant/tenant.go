// Package tenant describes the Chronicle deployment the uploader talks to.
package tenant

import "strings"

const (
	// AuthBaseURL is the shared Chronicle deployment used for pairing and API
	// health checks. Its OAuth tokens are valid for every tenant host.
	AuthBaseURL = "https://legacy.chronicleclassic.com"
	// ForeverBaseURL is used for Blizzard's WoW Forever client, which has no
	// legacy realmlist.wtf file to select a community.
	ForeverBaseURL = "https://forever.chronicleclassic.com"
)

// Tenant is retained in state for compatibility with older sidekick releases.
// BaseURL is now the shared authentication deployment. Log uploads are routed
// per WoW install by realmlist instead.
type Tenant struct {
	BaseURL string `json:"base_url"`
	Format  string `json:"format,omitempty"` // legacy state, no longer sent
	Flavor  string `json:"flavor,omitempty"` // legacy state, no longer sent
	Layout  Layout `json:"layout,omitempty"` // legacy state
}

// Layout describes the process names used by supported WoW clients. The disk
// layout is discovered per install because legacy clients do not use Blizzard
// flavor folders.
type Layout struct {
	LogDir          string   `json:"log_dir,omitempty"`       // legacy state
	FlavorMarker    string   `json:"flavor_marker,omitempty"` // legacy state
	FlavorAffix     string   `json:"flavor_affix,omitempty"`  // legacy state
	ProcessNames    []string `json:"process_names,omitempty"`
	ProcessPrefixes []string `json:"process_prefixes,omitempty"`
}

// DefaultLayout recognises both Blizzard and legacy WoW executables.
var DefaultLayout = Layout{
	ProcessNames: []string{
		"wow.exe", "wow-64.exe", "wowb.exe", "wowt.exe",
		"wowclassic.exe", "wowclassicb.exe", "wowclassict.exe",
	},
	ProcessPrefixes: []string{"world of warcraft"},
}

// Known is kept for compatibility with callers and saved state. The only
// process-wide tenant is the shared authentication deployment.
var Known = []Tenant{{BaseURL: AuthBaseURL, Layout: DefaultLayout}}

// Realm maps a realmlist host to its Chronicle tenant URL.
type Realm struct {
	Name    string
	BaseURL string
}

var realms = map[string]Realm{
	"192.168.1.214":           {Name: "OctoPTR", BaseURL: "https://octoptr.chronicleclassic.com"},
	"185.165.170.6":           {Name: "OctoWoW", BaseURL: "https://octo.chronicleclassic.com"},
	"play.octowow.st":         {Name: "OctoWoW", BaseURL: "https://octo.chronicleclassic.com"},
	"apac.capycraft.io":       {Name: "CapyCraft", BaseURL: "https://capy.chronicleclassic.com"},
	"logon.vanillaplus.org":   {Name: "VanillaPlus", BaseURL: "https://vanillaplus.chronicleclassic.com"},
	"login.twinstar-wow.com":  {Name: "Kronos", BaseURL: "https://kronos.chronicleclassic.com"},
	"login2.twinstar-wow.com": {Name: "Kronos", BaseURL: "https://kronos.chronicleclassic.com"},
	"login3.twinstar-wow.com": {Name: "Kronos", BaseURL: "https://kronos.chronicleclassic.com"},
	"login.moonwell.su":       {Name: "Moonwell", BaseURL: "https://moonwell.chronicleclassic.com"},
	"crusader-storm.com":      {Name: "Crusader Storm", BaseURL: "https://crusader-storm.chronicleclassic.com"},
	"logon.chromiecraft.com":  {Name: "ChromieCraft", BaseURL: "https://chromie.chronicleclassic.com"},
	"logon.warmane.com":       {Name: "Warmane", BaseURL: "https://warmane.chronicleclassic.com"},
}

// ForRealm returns the Chronicle tenant for a normalized realmlist host.
func ForRealm(host string) (Realm, bool) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	r, ok := realms[host]
	return r, ok
}

// Resolve picks the shared authentication server. A non-empty override remains
// useful for development and sends every upload to that server as well.
func Resolve(_ Tenant, override string) Tenant {
	baseURL := AuthBaseURL
	if override != "" {
		baseURL = strings.TrimRight(override, "/")
	}
	return Tenant{BaseURL: baseURL, Layout: DefaultLayout}
}
