//go:build darwin

package update

import (
	"os"
	"syscall"
)

// fileStamp is what the verdict compares: a file whose bytes, permissions or
// identity change gets another stamp.
type fileStamp struct {
	Size  int64  `json:"size"`
	Mode  uint32 `json:"mode"`
	Mtime int64  `json:"mtime_ns"`
	Ctime int64  `json:"ctime_ns"`
	Ino   uint64 `json:"ino"`
	Dev   uint64 `json:"dev"`
}

func stampOf(info os.FileInfo) (fileStamp, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStamp{}, false
	}
	return fileStamp{
		Size: info.Size(), Mode: uint32(info.Mode()), Mtime: info.ModTime().UnixNano(),
		Ctime: st.Ctimespec.Nano(), Ino: st.Ino, Dev: uint64(st.Dev),
	}, true
}
