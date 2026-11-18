//go:build windows

package update

import "os"

// dirWritable is not consulted on Windows: the install folder is always the
// update root there (PathsFor), and nothing probes it.
func dirWritable(string) bool { return true }

// ownedNotShared is not consulted on Windows, which has no home bundle.
func ownedNotShared(string, os.FileInfo) error { return nil }

func ownedByUser(string, os.FileInfo) error { return nil }

func ownedByUserOrRoot(string, os.FileInfo) error { return nil }
