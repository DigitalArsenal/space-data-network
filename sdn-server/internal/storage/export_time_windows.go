//go:build windows

package storage

import (
	"os"
	"time"
)

// refreshFileTimeNow sets path's access and modification times to now.
func refreshFileTimeNow(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}
