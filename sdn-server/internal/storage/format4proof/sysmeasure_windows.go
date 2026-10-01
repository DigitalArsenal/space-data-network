//go:build windows

package format4proof

import (
	"io/fs"
	"os"
)

// MaxRSSMB is not measured on Windows (the harness runs on macOS and Linux).
func MaxRSSMB() float64 { return 0 }

// ChildMaxRSSMB is not measured on Windows.
func ChildMaxRSSMB(*os.ProcessState) float64 { return 0 }

func allocatedBytes(info fs.FileInfo) int64 { return info.Size() }
