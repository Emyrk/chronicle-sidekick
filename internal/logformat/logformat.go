// Package logformat defines the Chronicle combat-log parser formats Sidekick can emit.
package logformat

// LogFormat identifies the parser format sent with a Chronicle upload.
type LogFormat string

// Keep these wire values synchronized with Chronicle's database.LogFormat.
// Source: https://github.com/Emyrk/chronicle/blob/main/database/models.go
const (
	LogFormat112aSuperwowAddon LogFormat = "1.12a-superwow-addon"
	LogFormat112aCcAddon       LogFormat = "1.12a-cc-addon"
	LogFormat335aCcAddon       LogFormat = "3.3.5a-cc-addon"
	LogFormatAzerothcoreMod    LogFormat = "azerothcore-mod"
	LogFormat243CcAddon        LogFormat = "2.4.3-cc-addon"
	LogFormatV9Cleu            LogFormat = "v9-cleu"
	LogFormatHermesproxy1142Cc LogFormat = "hermesproxy_1_14_2_cc"
	LogFormatV22Cleu           LogFormat = "v22-cleu"
)

// SidekickFormats lists the formats Sidekick can currently upload.
func SidekickFormats() []LogFormat {
	return []LogFormat{
		LogFormat112aCcAddon,
		LogFormatHermesproxy1142Cc,
		LogFormat243CcAddon,
		LogFormat335aCcAddon,
	}
}

// ValidSidekick reports whether Sidekick can upload this format.
func (f LogFormat) ValidSidekick() bool {
	for _, known := range SidekickFormats() {
		if f == known {
			return true
		}
	}
	return false
}

// IncludesSidekickStorage reports whether uploads include the optional
// ChronicleSidekickBridge SavedVariables artifact.
func (f LogFormat) IncludesSidekickStorage() bool {
	return f == LogFormat335aCcAddon
}

// Label returns the client name shown in Sidekick's UI.
func (f LogFormat) Label() string {
	switch f {
	case LogFormat112aCcAddon:
		return "Vanilla 1.12a"
	case LogFormatHermesproxy1142Cc:
		return "Classic 1.14.2 through HermesProxy"
	case LogFormat243CcAddon:
		return "TBC 2.4.3"
	case LogFormat335aCcAddon:
		return "Wrath 3.3.5a"
	default:
		return "Unknown"
	}
}
