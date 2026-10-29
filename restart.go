package main

import (
	"fmt"
	"os"
	"os/exec"
)

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
	cmd := restartCommand(executable, os.Args[1:])
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updated Sidekick: %w", err)
	}
	return nil
}
