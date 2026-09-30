package main

// The host-02 format-2 cutover rehearsal's store-migrate findings
// (sdn-format2-cutover-fixes-20260930).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// legacyPLOG is a publication log entry as the log service built it before
// its buffers carried the "PLOG" file identifier (size-prefixed, no
// identifier): format 1 stored it, the format-2 engine refuses it (-102).
func legacyPLOG(seq uint64, recordCID string) []byte {
	b := flatbuffers.NewBuilder(256)
	schema := b.CreateString("OMM.fbs")
	publisher := b.CreateString("16Uiu2HAmLegacyLogPublisher")
	rec := b.CreateString(recordCID)
	hash := b.CreateString(fmt.Sprintf("%064x", seq))
	b.StartObject(11)
	b.PrependUint64Slot(0, seq, 0)
	b.PrependUOffsetTSlot(1, schema, 0)
	b.PrependUOffsetTSlot(2, publisher, 0)
	b.PrependUOffsetTSlot(3, rec, 0)
	b.PrependUOffsetTSlot(5, hash, 0)
	b.PrependUint64Slot(6, 1780000000+seq, 0)
	b.FinishSizePrefixed(b.EndObject())
	return append([]byte(nil), b.FinishedBytes()...)
}

// B2 (cutover rehearsal): the engine stops staging a partition while its 32
// reject slots are undrained, and store-migrate waited for its last entry's
// ack before draining them: --delta on a store holding 1,050 identifier-less
// PLOG copies hung in drain -> WaitAck. The pass must end, with every
// refused copy reported (and so not activated), whatever the count.
func TestStoreMigrateDeltaPastTheRejectRing(t *testing.T) {
	requirePSEngine(t)
	live := t.TempDir()
	buildLegacyStore(t, live)
	snap := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(snap, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control.flatsqldb", "control.flatsqldb-wal", "control.flatsqldb.fsdata", "auxiliary.flatsqlmeta"} {
		if _, err := os.Stat(filepath.Join(live, name)); err != nil {
			continue
		}
		if out, err := exec.Command("cp", filepath.Join(live, name), filepath.Join(snap, name)).CombinedOutput(); err != nil {
			t.Fatalf("copy %s: %v %s", name, err, out)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	opt := migrateOptions{Store: live, Snapshot: snap, AOTCacheDir: migrateTestAOTDir(t), CompileOnMiss: true, PageRows: 64}
	if _, err := migrateStore(ctx, opt, nil); err != nil {
		t.Fatalf("snapshot pass: %v", err)
	}
	// After the snapshot the node logged 100 publications (more than the
	// ring's 32 reject slots) the way the log service wrote them.
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(live, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	const logged = 100
	for i := 0; i < logged; i++ {
		if _, err := s.Store("PLOG.fbs", legacyPLOG(uint64(i+1), fmt.Sprintf("bafkreilegacy%03d", i)), "16Uiu2HAmLegacyLogPublisher", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opt.Snapshot, opt.Delta = "", true
	start := time.Now()
	rep, err := migrateStore(ctx, opt, nil)
	if ctx.Err() != nil {
		t.Fatalf("--delta did not end in %v (reject ring)", time.Since(start))
	}
	if err == nil || rep == nil || rep.Activated {
		t.Fatalf("--delta with %d refused copies: err %v, activated %v; want verification to refuse activation", logged, err, rep != nil && rep.Activated)
	}
	if len(rep.Rejected) != logged {
		t.Fatalf("%d copies reported refused, want the %d PLOG copies: %+v", len(rep.Rejected), logged, rep.Rejected)
	}
	for _, r := range rep.Rejected {
		if r.Code != -102 {
			t.Fatalf("refused copy %+v, want every one refused for its file identifier (-102)", r)
		}
	}
	t.Logf("--delta ended in %v with %d refused PLOG copies reported", time.Since(start).Round(time.Millisecond), len(rep.Rejected))
}
