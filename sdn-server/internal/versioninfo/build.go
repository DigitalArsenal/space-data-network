package versioninfo

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
	"sync"
)

// BuildSHA256 is the sha256 of the executable this process is running, hashed
// once. It is the build identity a node reports as build_sha256: fleet
// harnesses compare it across nodes, and the update helper's health gate
// requires it to equal the binary the update just installed.
//
// On Linux it reads /proc/self/exe, which is the file this process was exec'd
// from even after its path has been renamed away or replaced, so the hash is
// of the code that is running and never of whatever now sits at the path.
// Elsewhere it reads the path the OS reports for the executable. An
// unreadable executable yields "" rather than a fabricated identity.
func BuildSHA256() string { return buildSHA256() }

var buildSHA256 = sync.OnceValue(func() string {
	f, err := openRunningExecutable()
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
})

func openRunningExecutable() (*os.File, error) {
	if runtime.GOOS == "linux" {
		if f, err := os.Open("/proc/self/exe"); err == nil {
			return f, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Open(exe)
}
