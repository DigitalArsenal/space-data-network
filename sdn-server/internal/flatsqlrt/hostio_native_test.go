package flatsqlrt

// C host I/O module tests (design §5.4, §18 T5 #3/#4, A12, A23, A29). These
// call the module's C entry points directly; psinstance_test.go drives the
// same code through real guest threads.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const childEnv = "SDN_HIO_CHILD"

// runChild runs a scenario that needs its own RLIMIT_NOFILE, in a child
// process, and prints its JSON result on stdout.
func runChild(name string) int {
	var res interface{}
	var err error
	switch name {
	case "lru":
		res, err = childLRU()
	case "revoke":
		res, err = childRevokeReplacement()
	default:
		err = fmt.Errorf("unknown child scenario %q", name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
	return 0
}

func runChildScenario(t *testing.T, name string, env []string, out interface{}) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append(os.Environ(), childEnv+"="+name), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("child %s: %v\n%s", name, err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		t.Fatalf("child %s output %q: %v", name, stdout.String(), err)
	}
}

func newTestStore(t testing.TB) (*NativeStore, string) {
	t.Helper()
	if !NativeHostIOSupported() {
		t.Skip("native host I/O is POSIX-only")
	}
	root := t.TempDir()
	st, err := OpenNativeStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Release)
	return st, root
}

func newTestIO(t testing.TB, st *NativeStore, class HostIOClass, budget int) *NativeHostIO {
	t.Helper()
	h, err := NewNativeHostIO(st, class, budget, 1<<15)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.Revoke()
		h.ReleaseParked()
		h.Reap()
		_ = h.Free()
	})
	return h
}

func TestNativeHostIOFileLifecycle(t *testing.T) {
	st, root := newTestStore(t)
	h := newTestIO(t, st, HostIOWriter, 64)

	path := "fsql2/p/00000001/m-000000.fsl"
	fd := h.Open(path, ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents)
	if fd <= 0 {
		t.Fatalf("open with CREATE_PARENTS: %d", fd)
	}
	// Three directories created; each new directory's parent fsynced, plus the
	// new file's directory: 4 directory syncs.
	if s := h.Stats(); s.DirsCreated != 3 || s.DirSyncs != 4 {
		t.Fatalf("dirs created %d, dir syncs %d; want 3 and 4", s.DirsCreated, s.DirSyncs)
	}
	payload := bytes.Repeat([]byte("frame!"), 1000)
	if n := h.WriteAt(fd, payload, 4096); n != int32(len(payload)) {
		t.Fatalf("write %d", n)
	}
	if rc := h.Sync(fd); rc != 0 {
		t.Fatalf("sync %d", rc)
	}
	if sz := h.Size(fd); sz != float64(4096+len(payload)) {
		t.Fatalf("size %v", sz)
	}
	got := make([]byte, len(payload))
	if n := h.ReadAt(fd, got, 4096); n != int32(len(payload)) || !bytes.Equal(got, payload) {
		t.Fatalf("read back %d bytes, equal=%t", n, bytes.Equal(got, payload))
	}
	head := make([]byte, 16)
	if n := h.ReadAt(fd, head, 0); n != 16 || !bytes.Equal(head, make([]byte, 16)) {
		t.Fatalf("gap before the write must read as zeros: %d %x", n, head)
	}
	if rc := h.Truncate(fd, 100); rc != 0 || h.Size(fd) != 100 {
		t.Fatalf("truncate %d size %v", rc, h.Size(fd))
	}
	if rc := h.Close(fd); rc != 0 {
		t.Fatalf("close %d", rc)
	}
	if rc := h.Close(fd); rc != ioErrBadHandle {
		t.Fatalf("double close: %d", rc)
	}
	if n := h.ReadAt(fd, head, 0); n != ioErrBadHandle {
		t.Fatalf("stale handle read: %d", n)
	}
	// Same file, existing now: no directory is created and none is synced.
	before := h.Stats()
	fd2 := h.Open(path, ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents)
	if fd2 <= 0 || fd2 == fd {
		t.Fatalf("reopen gave %d (old %d): a reused slot must carry a new generation", fd2, fd)
	}
	if s := h.Stats(); s.DirsCreated != before.DirsCreated || s.DirSyncs != before.DirSyncs {
		t.Fatalf("existing file created dirs or synced: %+v", s)
	}
	h.Close(fd2)
	if rc := h.Open(path, ioFlagProbe); rc != 0 {
		t.Fatalf("probe existing: %d", rc)
	}
	if rc := h.Open(path, ioFlagUnlink); rc != 0 {
		t.Fatalf("unlink: %d", rc)
	}
	if rc := h.Open(path, ioFlagProbe); rc != ioErrNoEnt {
		t.Fatalf("probe after unlink: %d", rc)
	}
	if _, err := os.Stat(filepath.Join(root, "fsql2", "p", "00000001")); err != nil {
		t.Fatalf("partition directory: %v", err)
	}
	// Absolute paths under the root (either spelling) are accepted.
	abs := h.Open(filepath.Join(root, "abs.bin"), ioFlagWrite|ioFlagCreate)
	if abs <= 0 {
		t.Fatalf("absolute path under root: %d", abs)
	}
	h.Close(abs)
	if rc := h.Open("x", 0x0800); rc != ioErrGeneric {
		t.Fatalf("unknown flag bit accepted: %d", rc)
	}
}

func TestNativeHostIOConfinement(t *testing.T) {
	st, root := newTestStore(t)
	h := newTestIO(t, st, HostIOWriter, 16)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "leaf")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"../outside", "a/../../b", filepath.Join(outside, "secret"), "/etc/passwd",
		"escape/secret", "leaf",
	} {
		if rc := h.Open(p, ioFlagRead); rc >= 0 {
			t.Errorf("%q opened (handle %d): confinement failed", p, rc)
		}
	}
	if rc := h.Open("escape/new", ioFlagWrite|ioFlagCreate|ioFlagCreateParents); rc >= 0 {
		t.Errorf("created a file through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("a file appeared outside the root")
	}
}

func TestNativeHostIOUnlinkIfUnused(t *testing.T) {
	st, _ := newTestStore(t)
	writer := newTestIO(t, st, HostIOWriter, 16)
	reader := newTestIO(t, st, HostIOReader, 16)
	w := writer.Open("seg.fsd", ioFlagRead|ioFlagWrite|ioFlagCreate)
	if w <= 0 {
		t.Fatal(w)
	}
	r := reader.Open("seg.fsd", ioFlagRead)
	if r <= 0 {
		t.Fatal(r)
	}
	writer.Close(w)
	if rc := writer.Open("seg.fsd", ioFlagUnlinkIfUnused); rc != ioErrBusy {
		t.Fatalf("unlink-if-unused while a reader holds the file: %d, want BUSY", rc)
	}
	reader.Close(r)
	if rc := writer.Open("seg.fsd", ioFlagUnlinkIfUnused); rc != 0 {
		t.Fatalf("unlink-if-unused once released: %d", rc)
	}
	if writer.Stats().UnlinkBusy != 1 {
		t.Fatalf("busy count %d", writer.Stats().UnlinkBusy)
	}
}

// T5 #3 (part): with RLIMIT_NOFILE=256, 10,000 files cycle through the LRU
// with 0 errors.
func TestNativeHostIOLRUUnderLowRlimit(t *testing.T) {
	if !NativeHostIOSupported() {
		t.Skip("POSIX-only")
	}
	var res lruResult
	runChildScenario(t, "lru", []string{"SDN_HIO_ROOT=" + t.TempDir()}, &res)
	t.Logf("RLIMIT_NOFILE=%d files=%d rounds=%d ops=%d errors=%d evictions=%d reopens=%d max-open=%d process-fds-peak=%d",
		res.Rlimit, res.Files, res.Rounds, res.Ops, res.Errors, res.Evictions, res.Reopens, res.MaxOpen, res.ProcessFDs)
	if res.Rlimit != 256 || res.Files != 10000 || res.Errors != 0 || res.Ops != int64(res.Files*res.Rounds*2) {
		t.Fatalf("LRU cycle: %+v", res)
	}
	if res.Evictions == 0 || res.Reopens == 0 || res.MaxOpen > int64(res.Budget) {
		t.Fatalf("the LRU did not bound the fds: %+v", res)
	}
}

type lruResult struct {
	Rlimit, Files, Rounds, Budget   int
	Ops, Errors, Evictions, Reopens int64
	MaxOpen                         int64
	ProcessFDs                      int
}

func childLRU() (lruResult, error) {
	res := lruResult{Rlimit: 256, Files: 10000, Rounds: 3, Budget: 160}
	if err := setNoFileSoft(uint64(res.Rlimit)); err != nil {
		return res, err
	}
	st, err := OpenNativeStore(os.Getenv("SDN_HIO_ROOT"))
	if err != nil {
		return res, err
	}
	h, err := NewNativeHostIO(st, HostIOWriter, res.Budget, 1<<15)
	if err != nil {
		return res, err
	}
	handles := make([]int32, res.Files)
	for i := range handles {
		// CREATE_PARENTS (and its directory fsyncs) only for each directory's
		// first file: this test is about fd cycling, not directory durability.
		flags := ioFlagRead | ioFlagWrite | ioFlagCreate
		if i < 97 {
			flags |= ioFlagCreateParents
		}
		handles[i] = h.Open(fmt.Sprintf("p/%04x/d-%06x.fsd", i%97, i), flags)
		if handles[i] <= 0 {
			return res, fmt.Errorf("open %d: %d", i, handles[i])
		}
	}
	peak := 0
	buf := make([]byte, 64)
	for round := 0; round < res.Rounds; round++ {
		for i, fd := range handles {
			copy(buf, fmt.Sprintf("file %06d round %d", i, round))
			if n := h.WriteAt(fd, buf, int64(64*round)); n != int32(len(buf)) {
				res.Errors++
			}
			got := make([]byte, 64)
			if n := h.ReadAt(fd, got, int64(64*round)); n != 64 || !bytes.Equal(got, buf) {
				res.Errors++
			}
			res.Ops += 2
			if i%1000 == 0 {
				if n := countOpenFDs(); n > peak {
					peak = n
				}
			}
		}
	}
	s := h.Stats()
	res.Errors += s.Errors
	res.Evictions, res.Reopens, res.MaxOpen, res.ProcessFDs = s.LRUEvictions, s.Reopens, s.MaxOpenFDs, peak
	return res, nil
}

// A23 + T5 #4: after revoke returns, the revoked instance writes 0 bytes,
// including a thread that was blocked in a write during the revoke; with
// RLIMIT_NOFILE=64 and a replacement opening 1,000 files during the revoke,
// no foreign byte reaches the replacement's files.
func TestNativeHostIORevokeFencesWritesAndFDReuse(t *testing.T) {
	if !NativeHostIOSupported() {
		t.Skip("POSIX-only")
	}
	var res revokeResult
	runChildScenario(t, "revoke", []string{"SDN_HIO_ROOT=" + t.TempDir()}, &res)
	t.Logf("%+v", res)
	if res.Rlimit != 64 || res.ReplacementFiles != 1000 || res.ReplacementErrors != 0 {
		t.Fatalf("replacement did not run cleanly: %+v", res)
	}
	if res.ForeignBytes != 0 || res.WritesAfterRevoke != 0 || res.RevokedFileChanges != 0 {
		t.Fatalf("revoked instance wrote after revoke: %+v", res)
	}
	// The blocked writer read its fd before the revoke and wrote after it:
	// the revoke drained it (so the drain spans its stall) and the write hit
	// the sentinel, not the file.
	if res.RevokeDrain < 100*time.Millisecond || res.BlockedWriteResult >= 0 {
		t.Fatalf("the blocked writer was not fenced: %+v", res)
	}
	if res.CallsAfterRevoke == 0 || res.Parked == 0 {
		t.Fatalf("revoked callers were not parked: %+v", res)
	}
}

type revokeResult struct {
	Rlimit             int
	RevokeDrain        time.Duration
	BlockedWriteResult int32
	WritesAfterRevoke  int64
	CallsAfterRevoke   int64
	Parked             int64
	RevokedFileChanges int
	ReplacementFiles   int
	ReplacementErrors  int
	ForeignBytes       int
}

func childRevokeReplacement() (revokeResult, error) {
	res := revokeResult{Rlimit: 64, ReplacementFiles: 1000}
	if err := setNoFileSoft(uint64(res.Rlimit)); err != nil {
		return res, err
	}
	root := os.Getenv("SDN_HIO_ROOT")
	st, err := OpenNativeStore(root)
	if err != nil {
		return res, err
	}
	old, err := NewNativeHostIO(st, HostIOWriter, 8, 1024)
	if err != nil {
		return res, err
	}
	const oldFiles = 8
	var handles [oldFiles]int32
	for i := range handles {
		if handles[i] = old.Open(fmt.Sprintf("old/f%d", i), ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents); handles[i] <= 0 {
			return res, fmt.Errorf("old open %d", handles[i])
		}
	}
	marker := bytes.Repeat([]byte{0xEE}, 512)
	var stop atomic.Bool
	var wg sync.WaitGroup
	// Hammer threads: pwrite the marker as fast as they can, forever.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			runtime.LockOSThread()
			for i := 0; !stop.Load(); i++ {
				old.WriteAt(handles[(w+i)%oldFiles], marker, int64(512*(i%64)))
			}
		}(w)
	}
	time.Sleep(50 * time.Millisecond)
	// One writer blocked inside a host call across the revoke.
	old.SetFault(0, 150*time.Millisecond)
	blocked := make(chan int32, 1)
	go func() { blocked <- old.WriteAt(handles[0], marker, 0) }()
	time.Sleep(20 * time.Millisecond)

	// The replacement starts opening files the moment the revoke begins.
	replacement, err := NewNativeHostIO(st, HostIOWriter, 16, 4096)
	if err != nil {
		return res, err
	}
	replErrs := make(chan int, 1)
	go func() {
		errs := 0
		for i := 0; i < res.ReplacementFiles; i++ {
			flags := ioFlagRead | ioFlagWrite | ioFlagCreate
			if i == 0 {
				flags |= ioFlagCreateParents
			}
			fd := replacement.Open(fmt.Sprintf("new/f%04d", i), flags)
			if fd <= 0 {
				errs++
				continue
			}
			pattern := bytes.Repeat([]byte{byte(0x10 + i%64)}, 512)
			for off := int64(0); off < 4*512; off += 512 {
				if replacement.WriteAt(fd, pattern, off) != 512 {
					errs++
				}
			}
			if i%3 == 0 {
				replacement.Close(fd)
			}
		}
		replErrs <- errs
	}()
	res.RevokeDrain = old.Revoke()
	snapshot := func() []byte {
		var all []byte
		for i := 0; i < oldFiles; i++ {
			b, _ := os.ReadFile(filepath.Join(root, "old", fmt.Sprintf("f%d", i)))
			all = append(all, b...)
			all = append(all, '|')
		}
		return all
	}
	before := snapshot()
	time.Sleep(200 * time.Millisecond) // the hammers keep calling; they park now
	after := snapshot()
	if !bytes.Equal(before, after) {
		res.RevokedFileChanges = 1
	}
	res.ReplacementErrors = <-replErrs
	stop.Store(true)
	old.ReleaseParked()
	wg.Wait()
	res.BlockedWriteResult = <-blocked
	s := old.Stats()
	res.WritesAfterRevoke, res.CallsAfterRevoke, res.Parked = s.WritesAfterRevoke, s.CallsAfterRevoke, s.Parked
	// Every replacement byte must be its own pattern: no 0xEE anywhere.
	for i := 0; i < res.ReplacementFiles; i++ {
		b, err := os.ReadFile(filepath.Join(root, "new", fmt.Sprintf("f%04d", i)))
		if err != nil {
			res.ReplacementErrors++
			continue
		}
		want := byte(0x10 + i%64)
		for _, c := range b {
			if c != want {
				res.ForeignBytes++
			}
		}
	}
	return res, nil
}

// A23: revoke returns within 50 ms while one thread is stuck in an injected
// 30 s fsync (syncs are not drained).
func TestNativeHostIORevokeDoesNotWaitForSync(t *testing.T) {
	st, _ := newTestStore(t)
	h, err := NewNativeHostIO(st, HostIOWriter, 8, 64)
	if err != nil {
		t.Fatal(err)
	}
	fd := h.Open("m.fsl", ioFlagRead|ioFlagWrite|ioFlagCreate)
	h.SetFault(1, 30*time.Second)
	syncDone := make(chan int32, 1)
	go func() { syncDone <- h.Sync(fd) }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	h.Revoke()
	took := time.Since(start)
	h.ReleaseParked() // ends the injected stall so the thread can leave
	rc := <-syncDone
	t.Logf("revoke returned in %s with a sync stalled; the stalled sync then returned %d", took, rc)
	if took > 50*time.Millisecond {
		t.Fatalf("revoke took %s behind a stuck fsync", took)
	}
	if rc == 0 {
		t.Fatal("the stalled sync reported success on a revoked handle")
	}
	for i := 0; i < 1000 && h.Reap() != 0; i++ {
		time.Sleep(time.Millisecond)
	}
	if err := h.Free(); err != nil {
		t.Fatal(err)
	}
}

// A29: each instance's table lock is taken only by its own class; the store
// registry lock is the one shared lock, on open/close only, and its hold time
// is bounded.
func TestNativeHostIOLockClassesAreDisjoint(t *testing.T) {
	st, _ := newTestStore(t)
	writer := newTestIO(t, st, HostIOWriter, 32)
	reader := newTestIO(t, st, HostIOReader, 32)
	bulk := newTestIO(t, st, HostIOBulk, 32)
	var wg sync.WaitGroup
	run := func(h *NativeHostIO, name string) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for i := 0; i < 400; i++ {
			fd := h.Open(fmt.Sprintf("%s/%03d", name, i%64), ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents)
			h.WriteAt(fd, buf, 0)
			h.ReadAt(fd, buf, 0)
			h.Close(fd)
		}
	}
	wg.Add(3)
	go run(writer, "w")
	go run(reader, "r")
	go run(bulk, "b")
	wg.Wait()
	ws, rs, bs, ss := writer.Stats(), reader.Stats(), bulk.Stats(), st.Stats()
	t.Logf("writer lock: %d acq, max %s; reader lock: %d acq, max %s; bulk: %d acq, max %s; store registry: %d acq, max %s, classes %03b",
		ws.LockAcquisitions, ws.LockHoldMax, rs.LockAcquisitions, rs.LockHoldMax, bs.LockAcquisitions, bs.LockHoldMax,
		ss.LockAcquisitions, ss.LockHoldMax, ss.LockClasses)
	if ws.LockClasses != 1<<HostIOWriter || rs.LockClasses != 1<<HostIOReader || bs.LockClasses != 1<<HostIOBulk {
		t.Fatalf("instance locks shared across classes: writer %b reader %b bulk %b", ws.LockClasses, rs.LockClasses, bs.LockClasses)
	}
	for _, d := range []time.Duration{ws.LockHoldMax, rs.LockHoldMax, bs.LockHoldMax, ss.LockHoldMax} {
		if d > 50*time.Millisecond {
			t.Fatalf("a C-host lock was held %s (> 50 ms)", d)
		}
	}
}
