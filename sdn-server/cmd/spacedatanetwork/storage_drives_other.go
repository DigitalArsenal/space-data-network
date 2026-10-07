//go:build !linux && !darwin

package main

// Drive listing is for Linux and macOS nodes; elsewhere the editor shows the
// current folder and limit only.
func listDrives() []storageDrive { return nil }

func mountOf(string) string { return "" }

func statDrive(string) (int64, int64, bool) { return 0, 0, false }
