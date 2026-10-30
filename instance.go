package main

import (
	"errors"
	"time"
)

var errAlreadyRunning = errors.New("Chronicle Sidekick is already running")

func acquireInstanceWithWait(path string, wait bool) (instanceLock, error) {
	for {
		lock, err := acquireInstance(path)
		if !errors.Is(err, errAlreadyRunning) || !wait {
			return lock, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type instanceLock interface {
	Close() error
}
