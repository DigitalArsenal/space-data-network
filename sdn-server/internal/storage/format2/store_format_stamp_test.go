package format2

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// The build's store-format stamp (versioninfo, A5/TB03s) names the engine it
// was derived from. A repin of flatsql-ps-threads.wasm fails here until
// PSEngineStoreFormatMax is re-derived from the new engine (the test below).
func TestBuildStampDescribesTheEmbeddedEngine(t *testing.T) {
	if versioninfo.PSEngineSHA256 != flatsqlrt.PSThreadsSHA256 {
		t.Fatalf("versioninfo.PSEngineSHA256 %s describes another engine than the embedded %s (%s): run TestEmbeddedEngineStoreFormatIsTheBuildStamp on the new engine and set PSEngineSHA256 and PSEngineStoreFormatMax from it",
			versioninfo.PSEngineSHA256, flatsqlrt.PSThreadsSHA256, flatsqlrt.PSThreadsPackage)
	}
}

// versioninfo.PSEngineStoreFormatMax is what the embedded engine does, not a
// number typed beside it: a fresh store is written at exactly that format, SDN's
// own STORE reader accepts it, and the engine refuses (and leaves untouched) a
// store one level above it. The update guard trusts this stamp to decide which
// builds may run on a store, so it is held to the engine here.
func TestEmbeddedEngineStoreFormatIsTheBuildStamp(t *testing.T) {
	root := t.TempDir()
	s := openTestStore(t, root)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "stamp", License: "CC-BY-4.0"}
	// One acked record, so the registry is not empty: an engine may recreate a
	// store whose registry is empty (flatsql-ps-terabyte §3, Ratchet 4), and
	// the refusal below must be of a store with something in it.
	res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(1, 0, base, "STAMP"), "source:celestrak", nil, tags)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("record rejected: %v", r.Err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	storePath := filepath.Join(root, Dir, "STORE")
	original, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != storeFileLen || binary.LittleEndian.Uint32(original) != magicStore {
		t.Fatalf("fsql2/STORE is not a STORE file: % x", original)
	}
	written := int(binary.LittleEndian.Uint16(original[4:]))
	if written != versioninfo.PSEngineStoreFormatMax {
		t.Fatalf("the embedded engine writes a fresh store at format %d; versioninfo.PSEngineStoreFormatMax is %d", written, versioninfo.PSEngineStoreFormatMax)
	}
	if _, err := ReadStoreFile(root); err != nil {
		t.Fatalf("SDN's STORE reader refuses the format the engine writes: %v", err)
	}
	if migrated, err := Migrated(root); err != nil || !migrated {
		t.Fatalf("Migrated = %v, %v on the engine's own fresh store", migrated, err)
	}

	ts := newTestStore(t, root)
	reopen := func() error {
		w, err := OpenWriter(ts.opt, WriterConfig{Writers: 1, RequireMigrated: true})
		if err != nil {
			return err
		}
		return w.Stop()
	}
	// The control: this root reopens as it stands.
	if err := reopen(); err != nil {
		t.Fatalf("reopen at format %d: %v", written, err)
	}

	above := append([]byte(nil), original...)
	binary.LittleEndian.PutUint16(above[4:], uint16(written+1))
	binary.LittleEndian.PutUint32(above[56:], crc32.Checksum(above[:56], castagnoli))
	if err := os.WriteFile(storePath, above, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reopen(); err == nil {
		t.Fatalf("the embedded engine opened a store at format %d, above versioninfo.PSEngineStoreFormatMax %d", written+1, versioninfo.PSEngineStoreFormatMax)
	} else {
		t.Logf("engine refusal at format %d: %v", written+1, err)
	}
	if after, err := os.ReadFile(storePath); err != nil || !bytes.Equal(after, above) {
		t.Fatalf("the refused open rewrote fsql2/STORE (%v)", err)
	}
	if migrated, err := Migrated(root); err == nil || migrated {
		t.Fatalf("SDN's STORE reader accepts format %d: Migrated = %v, %v", written+1, migrated, err)
	}

	// And the refusal was the format: restored, the store opens again.
	if err := os.WriteFile(storePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reopen(); err != nil {
		t.Fatalf("reopen after restoring STORE: %v", err)
	}
}

// A store at level 2 (what store-migrate wrote on host-02 with the 3.5.1
// engine, reproduced with the SDN_F2_WRITE_FORMAT pin) is raised by the first
// open of this engine: the writer's stats say so, SDN reads the raised STORE,
// and the store starts again, twice, with its record (the raised store's
// second start is what a STORE reader that knew only format 2 refused). A
// pinned open never raises it.
func TestAStoreTheEngineRaisesStartsAgain(t *testing.T) {
	requireEngine(t)
	top := uint64(versioninfo.PSEngineStoreFormatMax)
	root := t.TempDir()
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "raise", License: "CC-BY-4.0"}
	start := func(pin string) (*Store, WriterStats) {
		t.Helper()
		t.Setenv(WriteFormatEnv, pin)
		s, err := Open(StoreConfig{Root: root, AOTCacheDir: testAOTDir(t), CompileOnMiss: true, AllowFresh: true,
			Topology: Topology{Writers: 1, InteractiveLanes: 1, BulkLanes: 1}, GatePeriod: 50 * time.Millisecond})
		if err != nil {
			t.Fatalf("open (pin %q): %v", pin, err)
		}
		st, err := s.w.Stats()
		if err != nil {
			_ = s.Close()
			t.Fatalf("writer stats: %v", err)
		}
		return s, st
	}
	s, st := start("2")
	if st.StoreFormat != 2 || st.EngineFormatMax != top {
		t.Fatalf("pinned fresh store: level %d, engine max %d; want 2 and %d", st.StoreFormat, st.EngineFormatMax, top)
	}
	res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(3, 0, base, "RAISE"), "source:celestrak", nil, tags)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("record rejected: %v", r.Err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, st = start("2") // pinned again: stays
	_ = s.Close()
	if sf, err := ReadStoreFile(root); err != nil || sf.Format != 2 || st.RaisedFrom != 0 {
		t.Fatalf("pinned reopen: STORE %+v, %v, raised from %d; want level 2, no raise", sf, err, st.RaisedFrom)
	}
	for i := 0; i < 2; i++ {
		s, st = start("")
		wantFrom := uint64(0)
		if i == 0 && top > 2 {
			wantFrom = 2
		}
		if st.StoreFormat != top || st.RaisedFrom != wantFrom {
			_ = s.Close()
			t.Fatalf("start %d: level %d raised from %d; want %d from %d", i+1, st.StoreFormat, st.RaisedFrom, top, wantFrom)
		}
		for _, r := range res {
			if _, err := s.GetRecord(ctx, "OMM.fbs", r.CID); err != nil {
				_ = s.Close()
				t.Fatalf("start %d: record %s: %v", i+1, r.CID, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if sf, err := ReadStoreFile(root); err != nil || uint64(sf.Format) != top {
			t.Fatalf("start %d: STORE %+v, %v; want level %d", i+1, sf, err, top)
		}
		if ok, err := Migrated(root); err != nil || !ok {
			t.Fatalf("start %d: Migrated = %v, %v", i+1, ok, err)
		}
	}
}
