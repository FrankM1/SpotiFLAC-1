//go:build !darwin

package main

// hideFromDock has nothing to do outside macOS.
func hideFromDock() {}
