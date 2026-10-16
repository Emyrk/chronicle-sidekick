package pair

const (
	clientID = "chronicle-sidekick"
	scopeAll = "all"
)

type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}
