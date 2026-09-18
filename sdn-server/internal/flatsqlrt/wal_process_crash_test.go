package flatsqlrt

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wal_process_crash_test.go — the claim an operator will doubt after the first
// `kill -9` deploy swap.
//
// The control store runs WAL at synchronous=NORMAL (storage/flatsql_boot_state
// .go). NORMAL means SQLite does NOT fsync the WAL on every commit, and the
// obvious reading of that is "a crash can lose committed rows". For an OS
// crash or power loss that reading is correct and bounded. For a PROCESS crash
// — panic, OOM kill, SIGKILL mid-deploy — it is wrong, and wrong in a way no
// amount of quoting SQLite's documentation settles, because the guarantee
// rests on a property of THIS VFS rather than of SQLite:
//
//	a commit's WAL frames reach the OS page cache before COMMIT returns, so a
//	different process reading the same file sees them whether or not anything
//	was ever fsynced.
//
// That holds here only because there is no userspace buffering between the
// engine and the kernel: HostIO.WriteAt (hostio.go) is a bare os.File.WriteAt,
// no bufio, no batching. If someone ever wraps that handle in a buffer to make
// writes cheaper, THIS test is the one that fails, and it is supposed to.
//
// So the probe is a real crash, not a simulated one: a child process commits,
// the parent SIGKILLs it with no Destroy and no Close, and a fresh engine in
// this process reopens the file and counts. The rows come back out of a -wal
// file that was never fsynced.
//
// Note what is NOT on disk afterwards: there is no -shm. The wal-index lives
// on the heap (diskstate.go), so it dies with the child and the reopening
// process runs full WAL recovery off the WAL file itself — which is precisely
// why the unsynced tail survives.
const (
	crashChildRootEnv = "FLATSQLRT_WAL_CRASH_CHILD_ROOT"
	crashChildRows    = 500
	crashChildCommits = 5
	crashReadyMarker  = "WAL-CRASH-CHILD-COMMITTED"
)

func TestWALProcessCrashKeepsCommittedRows(t *testing.T) {
	if root := os.Getenv(crashChildRootEnv); root != "" {
		runWALCrashChild(root)
		return
	}

	root := t.TempDir()
	dbPath := filepath.Join(root, "c.db")

	cmd := exec.Command(os.Args[0], "-test.run=^TestWALProcessCrashKeepsCommittedRows$", "-test.v")
	cmd.Env = append(os.Environ(), crashChildRootEnv+"="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("child stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, crashReadyMarker) {
				ready <- line
				return
			}
		}
		ready <- ""
	}()

	var readyLine string
	select {
	case readyLine = <-ready:
	case <-time.After(180 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child never reported %s", crashReadyMarker)
	}
	if readyLine == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child exited before committing; see its output above")
	}

	// SIGKILL. No Destroy, no Close, no checkpoint, no chance to flush
	// anything — the process simply stops existing between the COMMIT and
	// whatever it would have done next.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatalf("child exited cleanly; it was supposed to be killed")
	}
	fmt.Printf("PROBE crash: child %s\n", waitErr)

	// The rows are in the WAL and nowhere else: nothing fsynced it and no
	// checkpoint ran, so this file IS the evidence.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		names = append(names, fmt.Sprintf("%s(%d)", e.Name(), info.Size()))
	}
	fmt.Printf("PROBE crash: post-crash directory %s\n", strings.Join(names, " "))
	walInfo, err := os.Stat(dbPath + "-wal")
	if err != nil {
		t.Fatalf("no -wal after the crash (a checkpoint would defeat the point of this test): %v", err)
	}
	if walInfo.Size() == 0 {
		t.Fatalf("-wal is empty; the committed frames are not where this test claims they are")
	}

	// A DIFFERENT engine in a DIFFERENT process, reading what the dead one
	// left behind.
	rt := newDiskRuntime(t, root)
	db, err := rt.OpenDatabase(ommTestSchema, "recover", dbPath, JournalWAL)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Destroy()

	res, err := db.Query("SELECT COUNT(*) FROM crashrows")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	got, ok := res.Rows[0][0].(int64)
	if !ok {
		t.Fatalf("count returned %#v, not an int64", res.Rows[0][0])
	}
	fmt.Printf("PROBE crash: recovered %d/%d rows committed at synchronous=NORMAL\n", got, crashChildRows)
	if got != crashChildRows {
		t.Fatalf("recovered %d rows, want %d — a committed transaction was lost to a PROCESS crash, "+
			"which NORMAL is not supposed to permit; check for userspace buffering in HostIO.WriteAt", got, crashChildRows)
	}
}

// runWALCrashChild is the other half of the process, selected by the env guard.
// It deliberately leaks the runtime and the database, and it never returns:
// returning would let the test binary exit cleanly, and a clean exit is the one
// thing this test must not do.
func runWALCrashChild(root string) {
	rt, err := New(childRuntimeOptions(root)...)
	if err != nil {
		fmt.Printf("child New: %v\n", err)
		os.Exit(3)
	}
	db, err := rt.OpenDatabase(ommTestSchema, "crash", filepath.Join(root, "c.db"), JournalWAL)
	if err != nil {
		fmt.Printf("child open: %v\n", err)
		os.Exit(3)
	}
	// The mode under test. JournalWAL already implies NORMAL in the engine;
	// stating it makes the test independent of that default, which is the
	// thing the control store stopped overriding.
	if _, err := db.Query("PRAGMA synchronous=NORMAL"); err != nil {
		fmt.Printf("child pragma: %v\n", err)
		os.Exit(3)
	}
	if _, err := db.Query(`CREATE TABLE IF NOT EXISTS crashrows (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		fmt.Printf("child create: %v\n", err)
		os.Exit(3)
	}
	perCommit := crashChildRows / crashChildCommits
	for c := 0; c < crashChildCommits; c++ {
		if _, err := db.Query("BEGIN"); err != nil {
			fmt.Printf("child begin: %v\n", err)
			os.Exit(3)
		}
		for r := 0; r < perCommit; r++ {
			if _, err := db.Query(`INSERT INTO crashrows (v) VALUES (?)`, fmt.Sprintf("%d-%d", c, r)); err != nil {
				fmt.Printf("child insert: %v\n", err)
				os.Exit(3)
			}
		}
		if _, err := db.Query("COMMIT"); err != nil {
			fmt.Printf("child commit: %v\n", err)
			os.Exit(3)
		}
	}
	// Committed. From here the child must do NOTHING that could make the data
	// durable — no checkpoint, no sync, no close — and wait to be killed.
	fmt.Printf("%s %d\n", crashReadyMarker, crashChildRows)
	_ = os.Stdout.Sync()
	time.Sleep(180 * time.Second)
	fmt.Println("child was never killed; the parent is broken")
	os.Exit(4)
}

// childRuntimeOptions mirrors newDiskRuntime without its t.Cleanup(rt.Close):
// the child is killed, so a registered close would never run anyway, but
// relying on that would make the test depend on how testing happens to tear
// down rather than on the crash itself.
func childRuntimeOptions(root string) []Option {
	opts := []Option{WithFileIORoot(root)}
	if os.Getenv("FLATSQLRT_INTERPRET") == "" && os.Getenv("FLATSQLRT_WASM_FILE") == "" {
		if base, err := os.UserCacheDir(); err == nil {
			opts = append(opts, WithAOTCache(base+"/sdn-flatsqlrt-test-aot"))
		}
	}
	return opts
}
