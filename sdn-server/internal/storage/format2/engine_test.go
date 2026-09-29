package format2

// End-to-end tests of the published engine (flatsql-ps-threads.wasm, 3.1.0)
// under the WasmEdge substrate: a writer instance and reader instances over
// one host store, driven through rings and mailboxes in shared memory.
// They need the patched runtime (the release's static WasmEdge; CI's
// substrate lane) and skip on an upstream library unless
// SDN_WASM_REQUIRE_PATCHED=1.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

var (
	aotDirOnce sync.Once
	aotDir     string
)

// testAOTDir is a per-user cache: the AOT key is the artifact's sha256 and
// the runtime tag, so a stale entry can never load, and a warm run skips the
// minute-long compile.
func testAOTDir(t testing.TB) string {
	aotDirOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		aotDir = filepath.Join(base, "sdn-format2-test-aot")
	})
	return aotDir
}

func requireEngine(t testing.TB) {
	t.Helper()
	if !flatsqlrt.NativeHostIOSupported() {
		t.Skip("the C host I/O module is not available on this platform")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
}

type testStore struct {
	root   string
	native *flatsqlrt.NativeStore
	opt    InstanceOptions
}

func newTestStore(t testing.TB, root string) *testStore {
	t.Helper()
	requireEngine(t)
	if root == "" {
		root = t.TempDir()
	}
	ns, err := flatsqlrt.OpenNativeStore(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &testStore{root: root, native: ns,
		opt: InstanceOptions{Store: ns, AOTCacheDir: testAOTDir(t), CompileOnMiss: true}}
	t.Cleanup(ns.Release)
	return s
}

func (s *testStore) writer(t testing.TB, cfg WriterConfig) *Writer {
	t.Helper()
	w, err := OpenWriter(s.opt, cfg)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Stop() })
	return w
}

func (s *testStore) reader(t testing.TB, role flatsqlrt.PSRole, cfg ReaderConfig) *Reader {
	t.Helper()
	r, err := OpenReader(s.opt, role, cfg)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return r
}

// testOMM builds an OMM record (a plain FlatBuffer with "$OMM", root at 0).
func testOMM(norad uint32, epoch time.Time, name string) []byte {
	b := flatbuffers.NewBuilder(256)
	id := b.CreateString(fmt.Sprintf("2026-%03dA", norad%1000))
	nm := b.CreateString(name)
	ep := b.CreateString(epoch.UTC().Format("2006-01-02T15:04:05.000000"))
	OMM.OMMStart(b)
	OMM.OMMAddOBJECT_NAME(b, nm)
	OMM.OMMAddOBJECT_ID(b, id)
	OMM.OMMAddEPOCH(b, ep)
	OMM.OMMAddMEAN_MOTION(b, 15.5)
	OMM.OMMAddNORAD_CAT_ID(b, norad)
	root := OMM.OMMEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$OMM"))
	return append([]byte(nil), b.FinishedBytes()...)
}

// cidOf is CIDv1 raw sha2-256 in binary: 0x01 0x55 0x12 0x20 + digest.
func cidOf(data []byte) []byte {
	d := sha256.Sum256(data)
	return append([]byte{0x01, 0x55, 0x12, 0x20}, d[:]...)
}

func frameOf(data []byte) []byte {
	f := binary.LittleEndian.AppendUint32(nil, uint32(len(data)))
	return append(f, data...)
}

func TestEngineWriterAndReaderRoundTripRecords(t *testing.T) {
	s := newTestStore(t, "")
	w := s.writer(t, WriterConfig{Writers: 2, Create: true})
	spec, err := TypeSpecFor("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if string(spec.FID[:]) != "$OMM" {
		t.Fatalf("OMM file identifier %q", spec.FID[:])
	}
	if err := w.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	peers := []string{"source:celestrak", "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	const perPeer = 500
	want := map[string]bool{}
	for pi, peer := range peers {
		p, err := w.Partition([]byte(peer), spec.FID)
		if err != nil {
			t.Fatal(err)
		}
		var last uint64
		for i := 0; i < perPeer; i++ {
			data := testOMM(uint32(10000+i), base.Add(time.Duration(pi*perPeer+i)*time.Minute), fmt.Sprintf("SAT-%d-%d", pi, i))
			attr := BuildRecordAttr(RecordAttr{PeerID: []byte(peer), SourceTimestamp: 1780000000,
				Tag: SourceTag{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "b1"}})
			rseq, err := p.Enqueue(ctx, &Entry{Kind: EntRecord, Flags: FlagCidPresent, ArrivalMs: base.UnixMilli(),
				CID: cidOf(data), Attr: attr, Frame: frameOf(data)})
			if err != nil {
				t.Fatalf("enqueue %d: %v", i, err)
			}
			last = rseq
			want[string(cidOf(data))] = true
		}
		if err := p.WaitAck(ctx, last); err != nil {
			t.Fatalf("ack: %v", err)
		}
		if rej := p.Rejects(); len(rej) != 0 {
			t.Fatalf("rejects: %v", rej)
		}
	}
	// A resend of an acked record dedupes (0 new rows).
	p0, _ := w.Partition([]byte(peers[0]), spec.FID)
	data0 := testOMM(10000, base, "SAT-0-0")
	rseq, err := p0.Enqueue(ctx, &Entry{Kind: EntRecord, Flags: FlagCidPresent, ArrivalMs: base.UnixMilli(),
		CID: cidOf(data0), Attr: BuildRecordAttr(RecordAttr{PeerID: []byte(peers[0]), SourceTimestamp: 1780000000,
			Tag: SourceTag{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "b1"}}), Frame: frameOf(data0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := p0.WaitAck(ctx, rseq); err != nil {
		t.Fatal(err)
	}
	// A frame whose CID does not match is rejected, never trapped.
	bad := testOMM(1, base, "BAD")
	rseq, err = p0.Enqueue(ctx, &Entry{Kind: EntRecord, Flags: FlagCidPresent, ArrivalMs: base.UnixMilli(),
		CID: cidOf([]byte("not the frame")), Attr: BuildRecordAttr(RecordAttr{PeerID: []byte(peers[0])}), Frame: frameOf(bad)})
	if err != nil {
		t.Fatal(err)
	}
	var rej *RejectError
	if err := p0.WaitAck(ctx, rseq); err == nil || !asReject(err, &rej) || rej.Code != RejCid {
		t.Fatalf("mismatched CID: %v, want reject %d", err, RejCid)
	}

	r := s.reader(t, flatsqlrt.PSRoleReader, ReaderConfig{Lanes: 2})
	// Type-level reads see a record once its type owner labeled it.
	deadline := time.Now().Add(30 * time.Second)
	var n int64
	for time.Now().Before(deadline) {
		// Aggregates come from the type head (A16 TotalCount), not a scan:
		// count(*) over OMM is unbounded and an interactive lane refuses it.
		res, err := r.Query(ctx, Request{SQL: "SELECT first_live_count FROM flatsql_types WHERE type = 'OMM'"})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		n = res.Rows[0][0].Int64()
		if n == int64(len(want)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n != int64(len(want)) {
		t.Fatalf("OMM count %d, want %d", n, len(want))
	}
	res, err := r.Query(ctx, Request{SQL: "SELECT _cid_bin, NORAD_CAT_ID, _source FROM OMM ORDER BY _gseq LIMIT 10"})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(res.Rows) != 10 {
		t.Fatalf("window rows %d", len(res.Rows))
	}
	for _, row := range res.Rows {
		if !want[string(row[0].B)] {
			t.Fatalf("unknown cid % x", row[0].B)
		}
	}
	if _, err := r.Query(ctx, Request{SQL: "SELECT count(*) FROM OMM"}); !IsStatus(err, StatusNeedsBulk) {
		t.Fatalf("count(*) over OMM on an interactive lane: %v, want needs-bulk", err)
	}
	parts, err := r.Query(ctx, Request{SQL: "SELECT pid, live_count, live_bytes FROM flatsql_partitions ORDER BY pid"})
	if err != nil {
		t.Fatalf("flatsql_partitions: %v", err)
	}
	if len(parts.Rows) != 2 {
		t.Fatalf("partitions %v", parts.Rows)
	}
	for _, row := range parts.Rows {
		if row[1].Int64() != perPeer {
			t.Fatalf("partition %d live_count %d, want %d", row[0].I, row[1].I, perPeer)
		}
	}
	t.Logf("partitions: %v", parts.Rows)
}

func asReject(err error, out **RejectError) bool {
	re, ok := err.(*RejectError)
	if ok {
		*out = re
	}
	return ok
}
