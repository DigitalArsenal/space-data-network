package storage

// Store liveness lock (loop C.6b). The v2 FlatSQL store is SINGLE-WRITER:
// one in-process engine owns the compact record metadata log and the
// stream appenders. Two independent processes opening the same basePath —
// the celestrak.eth prod topology was `spacedatanetwork` (daemon) plus
// `spacedatanetwork-ingest.service` on ONE path — would interleave metadata
// frames and stream appends and corrupt the store. Every writer open
// therefore takes an EXCLUSIVE OS advisory lock (flock on Unix, LockFileEx
// on Windows) on <basePath>/store.lock before touching any store file, and
// a second writer open fails immediately with ErrStoreLocked instead of
// corrupting anything.
//
// Stale-lease semantics: the lock is kernel-owned and tied to the open file
// description, so it is released automatically when the holding process
// exits — including SIGKILL/kill -9 and crashes. No heartbeat, no takeover
// protocol, no stale lock files to clean: a subsequent open simply
// succeeds. The JSON metadata written into store.lock (pid/hostname/time)
// is purely advisory, used to produce an actionable error message; it is
// never consulted to decide whether the lock is held.
//
// The lock file itself is intentionally NOT unlinked on release: deleting
// it would race a third process that already opened (but not yet locked)
// the old inode.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const storeLockFileName = "store.lock"

// ErrStoreLocked reports that another live process holds the store's
// exclusive writer lock. Callers can match it with errors.Is.
var ErrStoreLocked = errors.New("datastore is locked by another process")

// storeLockInfo is the advisory holder metadata written into store.lock.
type storeLockInfo struct {
	PID        int    `json:"pid"`
	Hostname   string `json:"hostname,omitempty"`
	AcquiredAt string `json:"acquired_at,omitempty"`
}

type storeLock struct {
	f    *os.File
	path string
}

// acquireStoreLock takes the exclusive writer lock for the store rooted at
// basePath, failing fast (never blocking) when another process holds it.
func acquireStoreLock(basePath string) (*storeLock, error) {
	path := filepath.Join(basePath, storeLockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open store lock file %s: %w", path, err)
	}
	if err := lockFileExclusive(f); err != nil {
		holder := describeLockHolder(path)
		f.Close()
		return nil, fmt.Errorf(
			"store at %s is already open for writing%s: the FlatSQL v2 store is single-writer (in-process engine + compact record metadata) — "+
				"stop the other process before opening this store: %w",
			basePath, holder, ErrStoreLocked)
	}

	// Advisory holder metadata for the error message of the NEXT contender.
	// Best effort: metadata failures never fail the open (the kernel lock is
	// what actually protects the store).
	hostname, _ := os.Hostname()
	if payload, err := json.Marshal(storeLockInfo{
		PID:        os.Getpid(),
		Hostname:   hostname,
		AcquiredAt: time.Now().UTC().Format(time.RFC3339),
	}); err == nil {
		if err := f.Truncate(0); err == nil {
			_, _ = f.WriteAt(payload, 0)
			_ = f.Sync()
		}
	}
	return &storeLock{f: f, path: path}, nil
}

// describeLockHolder renders the advisory holder metadata (", held by pid
// 1234 on host since ...") for the ErrStoreLocked message. Returns "" when
// the metadata is unreadable — the metadata is advisory only.
func describeLockHolder(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var info storeLockInfo
	if err := json.Unmarshal(raw, &info); err != nil || info.PID == 0 {
		return ""
	}
	desc := fmt.Sprintf(", held by pid %d", info.PID)
	if info.Hostname != "" {
		desc += " on " + info.Hostname
	}
	if info.AcquiredAt != "" {
		desc += " since " + info.AcquiredAt
	}
	return desc
}

// release drops the lock and closes the file. Safe on a nil receiver.
func (l *storeLock) release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := unlockFile(l.f)
	closeErr := l.f.Close()
	l.f = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
