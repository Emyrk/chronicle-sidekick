//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework Sparkle
void SidekickStartUpdater(void);
void SidekickCheckForUpdates(void);
*/
import "C"

func nativeUpdaterAvailable() bool { return true }
func startNativeUpdater()          { C.SidekickStartUpdater() }
func checkNativeUpdater()          { C.SidekickCheckForUpdates() }
