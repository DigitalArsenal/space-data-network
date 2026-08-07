package flatsqlrt

// hostio_test.go — the seven-import file layer, tested at the Go level against
// the SAME scenarios the flatsql repo's native CTest and its JS backends run.
// A divergence here is not a Go bug, it is a lane divergence: the engine sees
// one contract and must get identical answers from every host that claims to
// satisfy it.

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestHostIO(t *testing.T) (*HostIO, string) {
	t.Helper()
	dir := t.TempDir()
	io, err := NewHostIO(dir)
	if err != nil {
		t.Fatalf("NewHostIO: %v", err)
	}
	t.Cleanup(io.CloseAll)
	return io, dir
}

func TestHostIORoundTripAtOffsets(t *testing.T) {
	io, dir := newTestHostIO(t)
	path := filepath.Join(dir, "pages.db")

	h := io.Open(path, ioFlagRead|ioFlagWrite|ioFlagCreate)
	if h < 0 {
		t.Fatalf("open: status %d", h)
	}

	if n := io.WriteAt(h, []byte("ABCD"), 0); n != 4 {
		t.Fatalf("write at 0 = %d, want 4", n)
	}
	// Writing past EOF must EXTEND the file and leave a zero-filled gap. The
	// browser's chunked backend emulates exactly this, so the engine's pager may
	// rely on it in both lanes.
	if n := io.WriteAt(h, []byte("Z"), 9); n != 1 {
		t.Fatalf("write at 9 = %d, want 1", n)
	}
	if got := io.Size(h); got != 10 {
		t.Fatalf("size = %v, want 10", got)
	}
	buf := make([]byte, 10)
	if n := io.ReadAt(h, buf, 0); n != 10 {
		t.Fatalf("read = %d, want 10", n)
	}
	want := []byte{'A', 'B', 'C', 'D', 0, 0, 0, 0, 0, 'Z'}
	if string(buf) != string(want) {
		t.Fatalf("content = %q, want %q", buf, want)
	}

	// A SHORT READ AT EOF IS A SUCCESS returning the byte count — never an
	// error. The VFS zero-fills the remainder and raises
	// SQLITE_IOERR_SHORT_READ itself; a host that returned an error here would
	// turn every ordinary page-boundary read into a corrupt database.
	tail := make([]byte, 8)
	if n := io.ReadAt(h, tail, 6); n != 4 {
		t.Fatalf("short read = %d, want 4", n)
	}
	// A read entirely past EOF is 0 bytes, still not an error.
	if n := io.ReadAt(h, tail, 100); n != 0 {
		t.Fatalf("read past EOF = %d, want 0", n)
	}

	if s := io.Truncate(h, 2); s != ioStatusSuccess {
		t.Fatalf("truncate: %d", s)
	}
	if got := io.Size(h); got != 2 {
		t.Fatalf("size after truncate = %v, want 2", got)
	}
	if s := io.Sync(h); s != ioStatusSuccess {
		t.Fatalf("sync: %d", s)
	}
	if s := io.Close(h); s != ioStatusSuccess {
		t.Fatalf("close: %d", s)
	}
	// Handle is dead after close.
	if s := io.Sync(h); s != ioErrBadHandle {
		t.Fatalf("sync after close = %d, want %d", s, ioErrBadHandle)
	}
}

func TestHostIOProbeAndUnlink(t *testing.T) {
	io, dir := newTestHostIO(t)
	path := filepath.Join(dir, "journal")

	// xAccess and xDelete ride on open FLAGS rather than costing two more
	// imports. Both are pure path->status and must allocate no handle.
	if s := io.Open(path, ioFlagProbe); s != ioErrNoEnt {
		t.Fatalf("probe missing = %d, want %d", s, ioErrNoEnt)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := io.Open(path, ioFlagProbe); s != ioStatusSuccess {
		t.Fatalf("probe existing = %d, want 0", s)
	}
	if s := io.Open(path, ioFlagUnlink); s != ioStatusSuccess {
		t.Fatalf("unlink = %d, want 0", s)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file survived unlink: %v", err)
	}
	// The VFS maps NOENT-on-delete to SQLITE_OK, so "already gone" must be a
	// distinguishable status and not a generic IO error.
	if s := io.Open(path, ioFlagUnlink); s != ioErrNoEnt {
		t.Fatalf("unlink missing = %d, want %d", s, ioErrNoEnt)
	}
	if io.opens.Load() != 0 {
		t.Fatalf("probe/unlink allocated %d handles, want 0", io.opens.Load())
	}
}

func TestHostIODeleteOnClose(t *testing.T) {
	io, dir := newTestHostIO(t)
	path := filepath.Join(dir, "temp-journal")
	h := io.Open(path, ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagDeleteOnClose)
	if h < 0 {
		t.Fatalf("open: %d", h)
	}
	io.WriteAt(h, []byte("scratch"), 0)
	if s := io.Close(h); s != ioStatusSuccess {
		t.Fatalf("close: %d", s)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("DELETE_ON_CLOSE file survived close: %v", err)
	}
}

// TestHostIOConfinement is the fail-closed half of the design. The engine
// treats paths as opaque host strings, so the HOST is the only thing deciding
// what a guest may reach.
func TestHostIOConfinement(t *testing.T) {
	io, dir := newTestHostIO(t)

	outside := filepath.Join(filepath.Dir(dir), "escape.db")
	t.Cleanup(func() { os.Remove(outside) })

	for _, tc := range []struct {
		name string
		path string
	}{
		{"absolute outside", outside},
		{"dotdot climb", filepath.Join(dir, "..", "escape.db")},
		{"absolute system path", "/etc/passwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if s := io.Open(tc.path, ioFlagRead|ioFlagWrite|ioFlagCreate); s != ioErrAccess {
				t.Fatalf("open %q = %d, want %d (ACCESS)", tc.path, s, ioErrAccess)
			}
			if s := io.Open(tc.path, ioFlagUnlink); s != ioErrAccess {
				t.Fatalf("unlink %q = %d, want %d (ACCESS)", tc.path, s, ioErrAccess)
			}
		})
	}

	// A symlink planted INSIDE the root that points outside must not become a
	// hole: containment is checked against the real location of the parent
	// directory, not the spelling of the path.
	linkDir := filepath.Join(dir, "link")
	target := t.TempDir()
	if err := os.Symlink(target, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if s := io.Open(filepath.Join(linkDir, "x.db"), ioFlagRead|ioFlagWrite|ioFlagCreate); s != ioErrAccess {
		t.Fatalf("open through escaping symlink = %d, want %d", s, ioErrAccess)
	}

	// Relative paths resolve INSIDE the root rather than against the process cwd.
	h := io.Open("nested.db", ioFlagRead|ioFlagWrite|ioFlagCreate)
	if h < 0 {
		t.Fatalf("relative open = %d", h)
	}
	io.Close(h)
	if _, err := os.Stat(filepath.Join(dir, "nested.db")); err != nil {
		t.Fatalf("relative path did not land in the root: %v", err)
	}
}

func TestHostIORejectsBadHandles(t *testing.T) {
	io, _ := newTestHostIO(t)
	buf := make([]byte, 4)
	if s := io.ReadAt(99, buf, 0); s != ioErrBadHandle {
		t.Fatalf("read bad handle = %d", s)
	}
	if s := io.WriteAt(-1, buf, 0); s != ioErrBadHandle {
		t.Fatalf("write bad handle = %d", s)
	}
	if s := io.Truncate(99, 0); s != ioErrBadHandle {
		t.Fatalf("truncate bad handle = %d", s)
	}
	if got := io.Size(99); got != float64(ioErrBadHandle) {
		t.Fatalf("size bad handle = %v", got)
	}
	if s := io.Close(99); s != ioErrBadHandle {
		t.Fatalf("close bad handle = %d", s)
	}
}

func TestNewHostIORequiresExistingDirectory(t *testing.T) {
	// Creating the root here would let a typo in a config produce a second,
	// silently empty store — the failure mode this whole lane exists to remove.
	if _, err := NewHostIO(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("NewHostIO accepted a missing directory")
	}
	if _, err := NewHostIO(""); err == nil {
		t.Fatal("NewHostIO accepted an empty root")
	}
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostIO(f); err == nil {
		t.Fatal("NewHostIO accepted a regular file as a root")
	}
}
