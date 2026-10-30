package main

import (
	"fmt"
	"os"
	"os/exec"
)

func hasArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func restartArgs(args []string) []string {
	result := append([]string(nil), args...)
	for _, required := range []string{"--background", "--wait-for-instance"} {
		if !hasArg(result, required) {
			result = append(result, required)
		}
	}
	return result
}

func restartCommand(executable string, args []string) *exec.Cmd {
	cmd := exec.Command(executable, args...)
	cmd.Env = os.Environ()
	return cmd
}

// restartSidekick starts the newly installed executable. The caller should
// cancel the current app only after this succeeds.
func restartSidekick() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate updated Sidekick: %w", err)
	}
	cmd := restartCommand(executable, restartArgs(os.Args[1:]))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updated Sidekick: %w", err)
	}
	return nil
}
