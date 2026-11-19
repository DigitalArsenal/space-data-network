package storage

// FreeDiskBytes is the space available to this process on the filesystem
// that holds path.
func FreeDiskBytes(path string) (uint64, error) {
	return migrationFreeBytes(path)
}
