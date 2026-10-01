//go:build windows

package format4proof

import (
	"errors"
	"io/fs"
	"os"
)

// MaxRSSMB is not measured on Windows (the harness runs on macOS and Linux).
func MaxRSSMB() float64 { return 0 }

// ChildMaxRSSMB is not measured on Windows.
func ChildMaxRSSMB(*os.ProcessState) float64 { return 0 }

func allocatedBytes(info fs.FileInfo) int64 { return info.Size() }

// FreeBytes is not measured on Windows.
func FreeBytes(string) (int64, error) {
	return 0, errors.New("format4proof: free space is not measured on Windows")
}

func countFDs() int { return 0 }
