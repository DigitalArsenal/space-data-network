//go:build windows

package update

import "os"

// dirWritable reports whether this process can create a file in dir.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".sdn-write-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}
