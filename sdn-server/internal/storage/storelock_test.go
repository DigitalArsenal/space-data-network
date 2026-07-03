package storage

// Single-writer liveness lock tests (loop C.6b). The two-process corruption
// scenario (daemon + separate ingest worker on one store path) must be
// PROVABLY closed, so the contention tests spawn a real second PROCESS
// (re-exec of the test binary) — in-process locking would not prove flock
// semantics across independent processes.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const (
	storeLockHelperEnv     = "SDN_STORELOCK_HELPER"
	storeLockHelperPathEnv = "SDN_STORELOCK_PATH"
	storeLockHelperModeEnv = "SDN_STORELOCK_MODE"
)

// TestStoreLockHelperProcess is not a test: it is the body of the helper
// subprocess spawned by the cross-process tests below. It acquires (or
// fails to acquire) the store lock at $SDN_STORELOCK_PATH and reports the
// outcome on stdout.
func TestStoreLockHelperProcess(t *testing.T) {
	if os.Getenv(storeLockHelperEnv) != "1" {
		t.Skip("helper process entry point")
	}
	base := os.Getenv(storeLockHelperPathEnv)
	mode := os.Getenv(storeLockHelperModeEnv)

	lock, err := acquireStoreLock(base)
	if err != nil {
		fmt.Printf("LOCK_FAIL locked=%v err=%v\n", errors.Is(err, ErrStoreLocked), err)
		os.Stdout.Sync()
		os.Exit(3)
	}
	fmt.Println("LOCK_OK")
	os.Stdout.Sync()
	if mode == "hold" {
		// Hold the lock until the parent kills us (the kill -9 recovery
		// test) or the safety timeout fires.
		time.Sleep(2 * time.Minute)
	}
	_ = lock.release()
	os.Exit(0)
}

// spawnLockHelper re-execs the test binary as an independent process that
// runs only TestStoreLockHelperProcess against basePath.
func spawnLockHelper(t *testing.T, basePath, mode string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestStoreLockHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		storeLockHelperEnv+"=1",
		storeLockHelperPathEnv+"="+basePath,
		storeLockHelperModeEnv+"="+mode,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return cmd, bufio.NewReader(stdout)
}

// readHelperLine reads one line of helper output with a timeout.
func readHelperLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	type lineResult struct {
		line string
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			line = strings.TrimSpace(line)
			// Skip go test chatter (=== RUN etc.) — we only care about our
			// LOCK_* protocol lines.
			if err == nil && !strings.HasPrefix(line, "LOCK_") {
				continue
			}
			ch <- lineResult{line, err}
			return
		}
	}()
	select {
	case res := <-ch:
		if res.err != nil && res.err != io.EOF {
			t.Fatalf("read helper output: %v", res.err)
		}
		return res.line
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for helper output")
		return ""
	}
}

// TestStoreLockSecondProcessOpenFailsCleanly proves the production scenario
// is closed: while a full store (daemon stand-in) holds the lock, a writer
// open attempt from a SECOND PROCESS fails with ErrStoreLocked and touches
// nothing.
func TestStoreLockSecondProcessOpenFailsCleanly(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	base := filepath.Join(t.TempDir(), "store")
	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	journalBefore, err := os.ReadFile(filepath.Join(base, controlJournalFileName))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}

	cmd, out := spawnLockHelper(t, base, "acquire")
	line := readHelperLine(t, out)
	if !strings.HasPrefix(line, "LOCK_FAIL") {
		t.Fatalf("second-process lock attempt output = %q, want LOCK_FAIL", line)
	}
	if !strings.Contains(line, "locked=true") {
		t.Fatalf("second-process failure did not match ErrStoreLocked: %q", line)
	}
	if !strings.Contains(line, "single-writer") {
		t.Fatalf("lock error is not actionable: %q", line)
	}
	err = cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("helper exit = %v, want exit code 3 (clean lock failure)", err)
	}

	// The failed contender must not have altered the journal.
	journalAfter, err := os.ReadFile(filepath.Join(base, controlJournalFileName))
	if err != nil {
		t.Fatalf("read journal after: %v", err)
	}
	if string(journalBefore) != string(journalAfter) {
		t.Fatalf("control journal changed after failed second-process open")
	}
}

// TestStoreLockHeldByOtherProcessBlocksStoreOpen is the converse: a second
// process holds the lock, so NewFlatSQLStore in THIS process must fail with
// ErrStoreLocked — and after the holder is killed with SIGKILL (no cleanup
// whatsoever), the open must succeed, proving stale-lease recovery.
func TestStoreLockHeldByOtherProcessBlocksStoreOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics test is unix-only")
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	base := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cmd, out := spawnLockHelper(t, base, "hold")
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if line := readHelperLine(t, out); line != "LOCK_OK" {
		t.Fatalf("helper output = %q, want LOCK_OK", line)
	}

	// Writer open against the held store fails cleanly.
	if _, err := NewFlatSQLStore(base, validator); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("open of held store: err = %v, want ErrStoreLocked", err)
	}

	// kill -9 the holder: the kernel drops the flock with zero cleanup from
	// the dead process; a new open takes over immediately.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill -9 helper: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("wait helper: %v", err)
	}

	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("open after kill -9 of previous holder: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestStoreLockReleasedOnCleanClose proves the clean-shutdown path: Close
// releases the lock and an immediate reopen succeeds.
func TestStoreLockReleasedOnCleanClose(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	base := filepath.Join(t.TempDir(), "store")

	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("reopen after clean close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
}

// TestStoreLockSecondOpenSameProcessFails: flock conflicts across
// independent open file descriptions even inside one process, so a double
// open in-process fails the same way (defense in depth for CLI verbs that
// might race the daemon within a shared PID namespace).
func TestStoreLockSecondOpenSameProcessFails(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	base := filepath.Join(t.TempDir(), "store")
	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if _, err := NewFlatSQLStore(base, validator); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second in-process open: err = %v, want ErrStoreLocked", err)
	}
}
