//go:build windows

package main

import (
	"errors"
	"os"
)

// execHomeBuild is not available on Windows, where a home bundle is never made
// (update.HomeUpdateRoot refuses).
func execHomeBuild(string, []string, []string) error {
	return errors.New("a home build cannot run on Windows")
}

// reraise is not used on Windows (no trial build runs there).
func reraise(os.Signal) {}
