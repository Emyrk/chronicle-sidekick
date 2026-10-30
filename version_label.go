package main

import "strings"

func versionLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "dev" {
		return "Version: development build"
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	return "Version: " + value
}
