//go:build !darwin

package main

func nativeUpdaterAvailable() bool { return false }
func startNativeUpdater()          {}
func checkNativeUpdater()          {}
