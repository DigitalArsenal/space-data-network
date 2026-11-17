package storage

import (
	"os"
	"path/filepath"
)

// sortTempDirName holds the external sort's files (publication fingerprint
// runs and plans) under the store's directory, so they count against the
// node's own disk and not a small system temporary file system. Each file is
// removed when its sort ends, on success or error; the directory is emptied
// when the store opens, which removes what a crash left.
const sortTempDirName = "publication-sort"

// sortTempDir is the directory the external sort writes in, created on
// demand; "" (the system temporary directory) for a store without a
// directory.
func (s *FlatSQLStore) sortTempDir() (string, error) {
	if s == nil || s.basePath == "" {
		return "", nil
	}
	dir := filepath.Join(s.basePath, sortTempDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// clearSortTemp removes the external sort's files a stopped process left.
func (s *FlatSQLStore) clearSortTemp() {
	if s == nil || s.basePath == "" {
		return
	}
	if err := os.RemoveAll(filepath.Join(s.basePath, sortTempDirName)); err != nil {
		log.Warnf("remove stale publication sort files: %v", err)
	}
}
