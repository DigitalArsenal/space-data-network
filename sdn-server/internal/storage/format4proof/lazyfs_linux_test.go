//go:build linux

package format4proof

// LazyFS power loss (contract §4; the flatsqlrt/hostio_lazyfs_test.go
// pattern). Run through lazyfs.sh, which builds LazyFS in a Linux container,
// mounts it and sets:
//
//	P4PROOF_LAZYFS_MOUNT      the LazyFS mount (the stores live on it)
//	P4PROOF_LAZYFS_FIFO       LazyFS's fault FIFO ("lazyfs::clear-cache")
//	P4PROOF_LAZYFS_FIFO_DONE  its completion FIFO ("finished::clear-cache")
//	P4PROOF_LAZYFS_ROUNDS     rounds per scenario
//	P4PROOF_LAZYFS_NEGATIVE   a shared object to LD_PRELOAD into the writer
//	                          (fsync a no-op): the loop must then report
//	                          losses, which proves it can see them
//
// Each round is a crash round (crash.go) with a power loss between the
// kill -9 and the verifier: LazyFS drops every byte not synced. Acked
// records must survive; seqs must not be reused; REBUILD verify must pass.
// The ack and follower logs live off the mount (they are the witness).
//
// LIMIT (as hostio_lazyfs_test.go): LazyFS caches only file data, so these
// trials do not drop an un-fsynced directory entry.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProofLazyFSPowerLoss(t *testing.T) {
	mount, fifo, done := os.Getenv("P4PROOF_LAZYFS_MOUNT"), os.Getenv("P4PROOF_LAZYFS_FIFO"), os.Getenv("P4PROOF_LAZYFS_FIFO_DONE")
	if mount == "" || fifo == "" || done == "" {
		t.Skip("run through lazyfs.sh (Linux, FUSE, LazyFS)")
	}
	c := requireEnv(t, EnvWork, EnvOut)
	rounds := envInt("P4PROOF_LAZYFS_ROUNDS", 25)
	negative := os.Getenv("P4PROOF_LAZYFS_NEGATIVE")
	arm := os.Getenv("P4PROOF_LAZYFS_ARM")
	if arm == "" {
		arm = ArmS
	}
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
	powerLoss := func() error {
		if _, err := fw.WriteString("lazyfs::clear-cache\n"); err != nil {
			return err
		}
		line, err := completions.ReadString('\n')
		if err != nil || line != "finished::clear-cache\n" {
			return fmt.Errorf("clear-cache completion %q: %v", line, err)
		}
		return nil
	}
	label := "lazyfs"
	var env []string
	if negative != "" {
		label = "lazyfs-negative"
		env = []string{"LD_PRELOAD=" + negative}
	}
	for _, sc := range []string{ScenarioIngest, ScenarioSupersede} {
		store := filepath.Join(mount, label+"-"+arm+"-"+sc)
		_ = os.RemoveAll(store)
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := CrashLoop(context.Background(), CrashLoopSpec{Arm: arm, Scenario: sc, Store: store, Work: c.Work, Out: c.Out,
			Rounds: rounds, Batch: 1024, AfterKill: powerLoss, WriterEnv: env,
			Logs: filepath.Join(c.Work, label+"-logs-"+arm+"-"+sc)}, logfOf(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("RESULT %s %s %s: %d rounds, %d killed mid-run, %d with violations; first: %s", label, arm, sc, res.Rounds, res.Killed,
			res.Violating, res.FirstViolation)
		switch {
		case negative == "" && res.Violating > 0:
			t.Errorf("%s %s: acked state lost across a power loss: %s", arm, sc, res.FirstViolation)
		case negative != "" && sc == ScenarioIngest && res.Violating == 0:
			t.Errorf("negative control: fsync was a no-op and no round lost an acked record: the loop cannot see a loss")
		}
		_ = os.RemoveAll(store)
	}
}
