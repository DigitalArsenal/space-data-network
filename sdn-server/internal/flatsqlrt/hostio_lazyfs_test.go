package flatsqlrt

// T5 #5, power-loss trials under LazyFS (Linux, FUSE). Run through
// scripts/lazyfs-dir-durability.sh, which mounts LazyFS and sets:
//
//	SDN_LAZYFS_MOUNT      the LazyFS mount point (the store root)
//	SDN_LAZYFS_FIFO       LazyFS's fault FIFO ("lazyfs::clear-cache")
//	SDN_LAZYFS_FIFO_DONE  its completion FIFO ("finished::clear-cache")
//	SDN_LAZYFS_TRIALS     trials (acceptance: 1000)
//	SDN_LAZYFS_NEGATIVE=1 skip the segment's data sync; the harness must then
//	                      report losses, which proves it can see them
//
// Each trial builds a partition the way the writer does (design §4.1, §6.4):
// the segment is created with CREATE_PARENTS, written and synced; then the
// head naming it is created, written and synced; then an unsynced tail is
// written. LazyFS drops every unsynced byte at a random step ("power loss").
// Invariant: once the head's sync has returned, the partition directory, the
// head and the segment it names exist after the loss, with the synced bytes.
//
// LIMIT, read in LazyFS's source (lazyfs/src/lazyfs.cpp lfs_mkdir, lfs_create
// at commit fa7d32e): LazyFS forwards mkdir and create to the backing file
// system at once and only caches file DATA. These trials therefore prove the
// data ordering and the parent-directory creation path, not the loss of an
// un-fsynced directory entry; that needs a block-level replay
// (dm-log-writes) on a Linux host, which the script documents.

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLazyFSDirectoryDurability(t *testing.T) {
	mount, fifo, done := os.Getenv("SDN_LAZYFS_MOUNT"), os.Getenv("SDN_LAZYFS_FIFO"), os.Getenv("SDN_LAZYFS_FIFO_DONE")
	if mount == "" || fifo == "" || done == "" {
		t.Skip("run through scripts/lazyfs-dir-durability.sh (needs Linux and FUSE)")
	}
	trials := envInt(t, "SDN_LAZYFS_TRIALS", 1000)
	negative := os.Getenv("SDN_LAZYFS_NEGATIVE") == "1"
	st, err := OpenNativeStore(mount)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Release()
	h, err := NewNativeHostIO(st, HostIOWriter, 64, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { h.Revoke(); h.ReleaseParked(); h.Reap(); h.Free() }()

	fw, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	fr, err := os.OpenFile(done, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	completions := bufio.NewReader(fr)
	powerLoss := func() {
		if _, err := fw.WriteString("lazyfs::clear-cache\n"); err != nil {
			t.Fatal(err)
		}
		line, err := completions.ReadString('\n')
		if err != nil || line != "finished::clear-cache\n" {
			t.Fatalf("clear-cache completion %q: %v", line, err)
		}
	}

	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))
	var durable, violations, lostTails, attempts int
	const steps = 7
	// A trial counts once its head was durable before the loss; losses that
	// land earlier are run too (they must not break anything) but prove
	// nothing about the invariant, so the loop runs until `trials` did.
	for trial := 0; durable < trials && trial < 20*trials; trial++ {
		attempts++
		dir := fmt.Sprintf("s%05d/fsql2/p/%08x", trial, trial+1)
		crashAfter := rng.Intn(steps + 1) // 0 = before anything
		payload := make([]byte, 1+rng.Intn(64<<10))
		rng.Read(payload)
		var seg, head int32
		headDurable := false
		for step := 0; step <= steps; step++ {
			if step == crashAfter {
				powerLoss()
			}
			switch step {
			case 0:
				seg = h.Open(dir+"/d-000001.fsd", ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents)
			case 1:
				h.WriteAt(seg, payload, 0)
			case 2:
				if !negative && h.Sync(seg) != 0 {
					t.Fatal("segment sync failed")
				}
			case 3:
				head = h.Open(dir+"/h.fsh", ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents)
			case 4:
				rec := fmt.Sprintf("d-000001.fsd %d %08x\n", len(payload), crc32.ChecksumIEEE(payload))
				h.WriteAt(head, []byte(rec), 0)
			case 5:
				headDurable = h.Sync(head) == 0
			case 6:
				h.WriteAt(seg, bytes.Repeat([]byte{0xEE}, 4096), int64(len(payload))) // never synced
			}
		}
		h.Close(seg)
		h.Close(head)
		if !headDurable || crashAfter <= 5 {
			continue // the loss came before the head was durable
		}
		durable++
		ok := true
		rec, err := os.ReadFile(filepath.Join(mount, dir, "h.fsh"))
		var name string
		var n int
		var crc uint32
		if err != nil {
			ok = false
		} else if _, err := fmt.Sscanf(string(rec), "%s %d %x", &name, &n, &crc); err != nil {
			ok = false
		} else if b, err := os.ReadFile(filepath.Join(mount, dir, name)); err != nil || len(b) < n ||
			crc32.ChecksumIEEE(b[:n]) != crc {
			ok = false
		} else if len(b) == n {
			lostTails++
		}
		if !ok {
			violations++
		}
	}
	t.Logf("seed %d: %d power-loss trials with a durable head before the loss (%d run in all), %d violations, %d unsynced tails dropped (negative control: %t)",
		seed, durable, attempts, violations, lostTails, negative)
	if negative {
		if violations == 0 {
			t.Fatal("the negative control lost nothing: this harness cannot see a missing sync")
		}
		return
	}
	if violations != 0 {
		t.Fatalf("%d durable heads named data that did not survive the power loss", violations)
	}
}
