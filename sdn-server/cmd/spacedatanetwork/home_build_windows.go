//go:build windows

package main

import "errors"

// execHomeBuild is not available on Windows, where a home bundle is never made
// (update.HomeUpdateRoot refuses).
func execHomeBuild(string, []string, []string) error {
	return errors.New("a home build cannot run on Windows")
}
