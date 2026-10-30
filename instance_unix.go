//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireInstance(path string) (instanceLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errAlreadyRunning
		}
		return nil, fmt.Errorf("lock Sidekick instance: %w", err)
	}
	return file, nil
}
