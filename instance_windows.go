//go:build windows

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// The tray and login item are scoped to the interactive Windows session, so
// the singleton follows the same boundary.
const instanceMutexName = `Local\ChronicleSidekick`

type windowsInstanceLock struct {
	handle windows.Handle
}

func acquireInstance(string) (instanceLock, error) {
	name, err := windows.UTF16PtrFromString(instanceMutexName)
	if err != nil {
		return nil, fmt.Errorf("encode instance mutex name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return nil, errAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("create instance mutex: %w", err)
	}
	return &windowsInstanceLock{handle: handle}, nil
}

func (l *windowsInstanceLock) Close() error {
	return windows.CloseHandle(l.handle)
}
