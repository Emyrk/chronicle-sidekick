// Package inspection audits the known components required by a WoW client.
package inspection

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Emyrk/chronicle-sidekick/internal/logformat"
)

// Status summarizes whether an installation needs user action.
type Status string

const (
	StatusReady          Status = "ready"
	StatusActionRequired Status = "action_required"
	StatusUnknown        Status = "unknown"
)

// Category identifies the kind of component being checked.
type Category string

const (
	CategoryAddon Category = "addon"
	CategoryDLL   Category = "dll"
)

// CheckStatus reports whether one known component exists.
type CheckStatus string

const (
	CheckPresent CheckStatus = "present"
	CheckMissing CheckStatus = "missing"
)

// Check is one allowlisted addon or DLL requirement.
type Check struct {
	ID       string      `json:"id"`
	Category Category    `json:"category"`
	Name     string      `json:"name"`
	Required bool        `json:"required"`
	Status   CheckStatus `json:"status"`
}

// Report is the complete component inspection for one WoW directory.
type Report struct {
	Status Status  `json:"status"`
	Checks []Check `json:"checks"`
}

type requirement struct {
	id       string
	category Category
	name     string
	required bool
}

var profiles = map[logformat.LogFormat][]requirement{
	// Forever writes native CLEU logs without requiring an addon or injected DLL.
	logformat.LogFormatV22Cleu: {},
	logformat.LogFormat112aCcAddon: {
		{id: "addon.chronicle_companion", category: CategoryAddon, name: "ChronicleCompanion", required: true},
		{id: "dll.nampower", category: CategoryDLL, name: "nampower.dll", required: true},
		{id: "dll.classic_api", category: CategoryDLL, name: "ClassicAPI.dll"},
		{id: "dll.superwow_hook", category: CategoryDLL, name: "SuperWoWhook.dll"},
	},
	logformat.LogFormatHermesproxy1142Cc: {
		{id: "addon.chronicle_companion_jims_proxy", category: CategoryAddon, name: "ChronicleCompanionJimsProxy", required: true},
	},
	logformat.LogFormat243CcAddon: {
		{id: "addon.chronicle_companion_tbc", category: CategoryAddon, name: "ChronicleCompanionTBC", required: true},
	},
	logformat.LogFormat335aCcAddon: {
		{id: "addon.chronicle_companion_wotlk", category: CategoryAddon, name: "ChronicleCompanionWoTLK", required: true},
	},
}

// Inspect checks only the components allowlisted for gameFormat. Addons are
// directories under Interface/AddOns; DLLs are regular files in the WoW root.
func Inspect(dir string, format logformat.LogFormat) Report {
	requirements, ok := profiles[format]
	if !ok {
		return Report{Status: StatusUnknown, Checks: []Check{}}
	}

	addons := directoryEntries(filepath.Join(dir, "Interface", "AddOns"), true)
	dlls := directoryEntries(dir, false)
	report := Report{Status: StatusReady, Checks: make([]Check, 0, len(requirements))}
	for _, requirement := range requirements {
		entries := addons
		if requirement.category == CategoryDLL {
			entries = dlls
		}
		status := CheckMissing
		if entries[strings.ToLower(requirement.name)] {
			status = CheckPresent
		} else if requirement.required {
			report.Status = StatusActionRequired
		}
		report.Checks = append(report.Checks, Check{
			ID: requirement.id, Category: requirement.category, Name: requirement.name,
			Required: requirement.required, Status: status,
		})
	}
	return report
}

func directoryEntries(dir string, wantDirectory bool) map[string]bool {
	found := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return found
	}
	for _, entry := range entries {
		if entry.IsDir() != wantDirectory {
			continue
		}
		if !wantDirectory {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
		}
		found[strings.ToLower(entry.Name())] = true
	}
	return found
}
