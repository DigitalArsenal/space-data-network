package status

// snapshot_persist.go — lane frames that survive a restart.
//
// snapshot.go takes the store read off the request path; it does not survive a
// process restart. host-01 spends 60-100 minutes hydrating under the store
// lock after every daemon restart, and for that whole window the dashboard
// lane had never built once, so /api/v1/dashboard/stats answered SNAPSHOT_COLD
// and the page had nothing to paint. A node that forgets, on every restart,
// everything it was serving a minute earlier is not "available immediately".
//
// Each lane's latest frame is therefore written to <dir>/lane-<name>.bin as the
// frame bytes plus the generation and build time that identify them, and loaded
// at Start() BEFORE the first background build. Frame() then serves the previous
// boot's bytes from the first request onward.
//
// ON STALENESS: the frame bytes are opaque to this package — it holds a
// pre-serialized buffer and cannot rewrite a flag inside it. It serves the
// restored frame as-is. That is truthful because the $NDS frame carries its own
// AS_OF: a client reading AS_OF sees exactly how old the numbers are, and the
// first successful build of this boot replaces the frame outright. A restored
// frame whose STALE flag was baked false is therefore not a false claim about
// NOW, it is a true claim about AS_OF — the same contract a cached HTTP
// response has. Snapshot.Restored is exported so a caller that wants to say
// more (a header, a log line) can.
//
// Generation is restored too, so ETags do not regress across a restart: a
// client holding "nds-7" from before the restart is still correctly told 304
// until the frame actually changes.

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"time"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("sdn-status")

// snapshotPersistMagic identifies the file and its layout version. A file that
// does not start with it is ignored, never migrated.
var snapshotPersistMagic = [8]byte{'S', 'D', 'N', 'L', 'A', 'N', 'E', '1'}

// snapshotPersistHeaderLen is magic(8) + generation(8) + builtAtUnixNano(8) +
// frameLen(4).
const snapshotPersistHeaderLen = 8 + 8 + 8 + 4

// snapshotPersistMaxFrame caps a restored frame at 64 MiB so a corrupt length
// cannot make the node allocate the file's claim rather than its size.
const snapshotPersistMaxFrame = 64 << 20

var errSnapshotPersistFormat = errors.New("status: snapshot file is not a lane frame")

// SetPersistDir points the cache at a directory for lane frames. Call BEFORE
// Start; an empty dir (the default) is RAM-only, exactly the original behavior.
// The directory is created 0700 on the first write.
func (c *SnapshotCache) SetPersistDir(dir string) {
	if c == nil {
		return
	}
	c.persistDir = dir
}

// laneFilePath is where lane name is stored, or "" when the cache has no
// directory or the name cannot be a filename. Lane names are code constants,
// but refusing anything outside [A-Za-z0-9_-] keeps a future dynamic name from
// escaping the directory.
func (c *SnapshotCache) laneFilePath(name string) string {
	if c == nil || c.persistDir == "" || name == "" {
		return ""
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return ""
		}
	}
	return filepath.Join(c.persistDir, "lane-"+name+".bin")
}

// loadPersisted seeds every lane from disk. Every failure — no file, short
// file, bad magic, impossible length — leaves that lane cold. Persistence is
// an optimization, never a dependency.
func (c *SnapshotCache) loadPersisted() {
	if c == nil || c.persistDir == "" {
		return
	}
	for name, l := range c.lanes {
		path := c.laneFilePath(name)
		if path == "" {
			continue
		}
		frame, generation, builtAt, err := readLaneFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Debugf("snapshot cache: cannot restore lane %s from %s: %v", name, path, err)
			}
			continue
		}
		l.mu.Lock()
		// Never overwrite a frame this boot already built (Start is idempotent
		// and a Refresh may have raced in).
		if l.snap.Generation == 0 {
			l.snap.Frame = frame
			l.snap.Generation = generation
			l.snap.BuiltAt = builtAt
			l.snap.Restored = true
		}
		l.mu.Unlock()
		log.Debugf("snapshot cache: restored lane %s (generation %d, built %s)", name, generation, builtAt.UTC().Format(time.RFC3339))
	}
}

func readLaneFile(path string) ([]byte, uint64, time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if len(raw) < snapshotPersistHeaderLen {
		return nil, 0, time.Time{}, errSnapshotPersistFormat
	}
	for i, b := range snapshotPersistMagic {
		if raw[i] != b {
			return nil, 0, time.Time{}, errSnapshotPersistFormat
		}
	}
	generation := binary.LittleEndian.Uint64(raw[8:16])
	builtNanos := int64(binary.LittleEndian.Uint64(raw[16:24]))
	frameLen := int(binary.LittleEndian.Uint32(raw[24:28]))
	if frameLen < 0 || frameLen > snapshotPersistMaxFrame || snapshotPersistHeaderLen+frameLen != len(raw) {
		return nil, 0, time.Time{}, errSnapshotPersistFormat
	}
	if generation == 0 || frameLen == 0 {
		return nil, 0, time.Time{}, errSnapshotPersistFormat
	}
	frame := make([]byte, frameLen)
	copy(frame, raw[snapshotPersistHeaderLen:])
	builtAt := time.Time{}
	if builtNanos > 0 {
		builtAt = time.Unix(0, builtNanos)
	}
	return frame, generation, builtAt, nil
}

// persistLane writes one lane's current frame atomically (temp file + rename,
// 0600). It runs on the lane goroutine, after a frame actually changed — never
// on a request. A write failure is logged and dropped: the served frame is
// unaffected.
func (c *SnapshotCache) persistLane(name string, snap Snapshot) {
	path := c.laneFilePath(name)
	if path == "" || snap.Generation == 0 || len(snap.Frame) == 0 {
		return
	}
	if len(snap.Frame) > snapshotPersistMaxFrame {
		return
	}
	if err := writeLaneFile(path, snap); err != nil {
		log.Debugf("snapshot cache: cannot persist lane %s to %s: %v", name, path, err)
	}
}

func writeLaneFile(path string, snap Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	buf := make([]byte, snapshotPersistHeaderLen, snapshotPersistHeaderLen+len(snap.Frame))
	copy(buf[0:8], snapshotPersistMagic[:])
	binary.LittleEndian.PutUint64(buf[8:16], snap.Generation)
	var builtNanos int64
	if !snap.BuiltAt.IsZero() {
		builtNanos = snap.BuiltAt.UnixNano()
	}
	binary.LittleEndian.PutUint64(buf[16:24], uint64(builtNanos))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(len(snap.Frame)))
	buf = append(buf, snap.Frame...)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
