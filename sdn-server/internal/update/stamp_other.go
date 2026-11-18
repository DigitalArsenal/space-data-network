//go:build !linux && !darwin

package update

import "os"

// fileStamp is empty where the change time and inode are not read: no verdict
// is recorded, and every start verifies in full.
type fileStamp struct{}

func stampOf(os.FileInfo) (fileStamp, bool) { return fileStamp{}, false }
