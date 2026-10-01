package format4_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/format4test"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// recordingDouble keeps the last request of each op.
type recorder struct {
	mu   sync.Mutex
	last map[uint32][]byte
}

func (d *double) record(r *recorder) {
	d.onRequest = func(op uint32, req []byte) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.last == nil {
			r.last = map[uint32][]byte{}
		}
		r.last[op] = append([]byte(nil), req...)
	}
}

func (r *recorder) get(op uint32) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last[op]
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// TestConformanceThroughTheMailbox runs the shared suite on the real client
// over the engine double: every op it covers round-trips through the request
// encoders, the slots, queues, doorbells, a 4 KiB ring and the RB1 decoders.
func TestConformanceThroughTheMailbox(t *testing.T) {
	format4test.Conformance(t, func(t *testing.T) format4.API {
		return newDouble(t, defaultDoubleCfg()).engine(t)
	})
}

// The contract's golden vectors (§3.5, §3.7), as the client writes them.
func TestGoldenRequests(t *testing.T) {
	d := newDouble(t, defaultDoubleCfg())
	var rec recorder
	d.record(&rec)
	e := d.engine(t)
	hello := "bafkreibm6jg3ux5qumhcn2b3flc3tyu6dmlb4xa7u5bf44yegnrjhc4yeq"
	_, err := e.Put(ctxT(t), format4.Batch{Type: "OMM", Peer: "12D3KooWExample", At: 1790000000,
		Tags:    []format4.Tag{{Provider: "celestrak", Source: "celestrak-gp", Batch: "b1"}},
		Records: []format4.In{{CID: hello, Plain: []byte("hello"), TS: 1790000000}}})
	if !errors.Is(err, format4.ErrNoType) {
		t.Fatalf("PUT on an unregistered type: %v", err)
	}
	const putGolden = "0100030000004f4d4d32000f000000313244334b6f6f574578616d706c6533002900000001000900000063656c65737472616b02000c00000063656c65737472616b2d67700400020000006231340001000000003500430000000100000000000000015512202cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824803bb16a00000000090000000500000068656c6c6f0000360008000000803bb16a00000000"
	if got := hex.EncodeToString(rec.get(1)); got != putGolden {
		t.Fatalf("PUT request\n got %s\nwant %s", got, putGolden)
	}
	if _, err := e.Get(ctxT(t), "OMM", []string{hello}, false, true); !errors.Is(err, format4.ErrNoType) {
		t.Fatalf("GET on an unregistered type: %v", err)
	}
	const getGolden = "0100030000004f4d4d0200010000000128002800000001000000015512202cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := hex.EncodeToString(rec.get(10)); got != getGolden {
		t.Fatalf("GET request\n got %s\nwant %s", got, getGolden)
	}
	_, _ = e.Scan(ctxT(t), format4.Query{Type: "OMM", Preds: []format4.Pred{{Field: format4.FieldCol0, Op: format4.OpEq,
		Values: []format2.Cell{format2.Int(25544)}}}})
	pred, _ := hex.DecodeString("0a01010001c863000000000000")
	want := append([]byte{17, 0, byte(len(pred)), 0, 0, 0}, pred...)
	if !bytes.Contains(rec.get(12), want) {
		t.Fatalf("SCAN request %x carries no predicate %x", rec.get(12), want)
	}
}

func registerRaw(t *testing.T, api format4.API) {
	t.Helper()
	spec, err := format4.TypeSpecFor("PNM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	spec.Flags = 0 // any bytes: these tests move payloads, not records
	if err := api.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
}

func randomRecord(n int) format4.In {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return format4test.In(b, 1790000000)
}

// Responses far larger than the ring stream through it, wrapping, with the
// engine waiting for space.
func TestRingWrapsAndFlowControls(t *testing.T) {
	d := newDouble(t, defaultDoubleCfg())
	e := d.engine(t)
	registerRaw(t, e)
	var ins []format4.In
	var cids []string
	for i := 0; i < 12; i++ {
		in := randomRecord(30_000 + 997*i)
		ins = append(ins, in)
		cids = append(cids, in.CID)
	}
	out, err := e.Put(ctxT(t), format4.Batch{Type: "PNM", Peer: "p", Records: ins})
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range out {
		if o.Action != format4.ActNew {
			t.Fatalf("record %d: %+v", i, o)
		}
	}
	recs, err := e.Get(ctxT(t), "PNM", cids, false, true)
	if err != nil || len(recs) != len(ins) {
		t.Fatalf("Get: %d rows, %v", len(recs), err)
	}
	for i, r := range recs {
		if !bytes.Equal(r.Data, ins[i].Plain) || r.CID != ins[i].CID {
			t.Fatalf("record %d came back changed", i)
		}
	}
}

// A batch larger than a write slot goes in chunks, in input order; a record
// past C-6's bound is rejected per record and the rest still land.
func TestPutChunksAndRejectsOversize(t *testing.T) {
	cfg := defaultDoubleCfg()
	cfg.reqBytes[0] = 160 << 10 // largest storable record: 96 KiB
	d := newDouble(t, cfg)
	e := d.engine(t)
	registerRaw(t, e)
	var ins []format4.In
	for i := 0; i < 20; i++ {
		ins = append(ins, randomRecord(20_000))
	}
	ins[7] = randomRecord(100_000)
	out, err := e.Put(ctxT(t), format4.Batch{Type: "PNM", Peer: "p", Records: ins})
	if err != nil {
		t.Fatal(err)
	}
	if out[7] != (format4.Outcome{Action: format4.ActRejected, Reject: format4.RejectTooLarge}) {
		t.Fatalf("oversize record: %+v", out[7])
	}
	var last int64
	for i, o := range out {
		if i == 7 {
			continue
		}
		if o.Action != format4.ActNew || o.Seq <= last {
			t.Fatalf("record %d: %+v after seq %d", i, o, last)
		}
		last = o.Seq
	}
	if n := d.puts.Load(); n < 3 {
		t.Fatalf("%d PUT calls for 19 x 20 KB through a 160 KiB slot", n)
	}
}

// P4_E_BUSY means nothing was done: the client retries the same chunk.
func TestBusyIsRetried(t *testing.T) {
	d := newDouble(t, defaultDoubleCfg())
	e := d.engine(t)
	registerRaw(t, e)
	d.busyWrites.Store(3)
	in := randomRecord(100)
	out, err := e.Put(ctxT(t), format4.Batch{Type: "PNM", Peer: "p", Records: []format4.In{in}})
	if err != nil || out[0].Action != format4.ActNew {
		t.Fatalf("Put through BUSY: %+v %v", out, err)
	}
	if n := d.puts.Load(); n != 1 {
		t.Fatalf("the engine executed %d PUTs, want 1", n)
	}
	recs, err := e.Get(ctxT(t), "PNM", []string{in.CID}, true, false)
	if err != nil || len(recs) != 1 {
		t.Fatalf("one copy after retries: %+v %v", recs, err)
	}
}

// A caller that leaves cancels its request; the slot comes back once the
// engine finishes it, and later reads are served.
func TestCancelFreesTheSlot(t *testing.T) {
	defer format4.SetAbandonGrace(20 * time.Millisecond)()
	d := newDouble(t, defaultDoubleCfg())
	e := d.engine(t)
	registerRaw(t, e)
	in := randomRecord(10)
	if _, err := e.Put(ctxT(t), format4.Batch{Type: "PNM", Peer: "p", Records: []format4.In{in}}); err != nil {
		t.Fatal(err)
	}
	d.slowReads.Store(int64(300 * time.Millisecond))
	for i := 0; i < 12; i++ { // more than the 8 read slots
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, err := e.Get(ctx, "PNM", []string{in.CID}, false, false)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: %v, want the deadline", i, err)
		}
	}
	d.slowReads.Store(0)
	recs, err := e.Get(ctxT(t), "PNM", []string{in.CID}, false, false)
	if err != nil || len(recs) != 1 {
		t.Fatalf("after cancels: %+v %v", recs, err)
	}
}

// More concurrent reads than read slots: every one completes.
func TestReadsWaitForSlots(t *testing.T) {
	d := newDouble(t, defaultDoubleCfg())
	e := d.engine(t)
	registerRaw(t, e)
	in := randomRecord(5000)
	if _, err := e.Put(ctxT(t), format4.Batch{Type: "PNM", Peer: "p", Records: []format4.In{in}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs, err := e.Scan(ctxT(t), format4.Query{Type: "PNM", Hydrate: true})
			if err == nil && (len(recs) != 1 || !bytes.Equal(recs[0].Data, in.Plain)) {
				err = errors.New("wrong scan result")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Control calls, stats and close; a closed or stopped engine answers
// ErrStopped, never a miss.
func TestControlAndStop(t *testing.T) {
	d := newDouble(t, defaultDoubleCfg())
	e := d.engine(t)
	registerRaw(t, e)
	if err := e.SetQuota(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := e.Activate(ctxT(t)); !errors.Is(err, format4.ErrFormat) {
		t.Fatalf("Activate outside create mode 2: %v", err)
	}
	st, err := e.Stats()
	if err != nil || len(st) < 40 || st[33] != 1 {
		t.Fatalf("Stats: %v %v", st, err)
	}
	in := randomRecord(10)
	if _, err := e.Put(ctxT(t), format4.Batch{Type: "PNM", Peer: "p", Records: []format4.In{in}}); err != nil {
		t.Fatal(err)
	}
	d.stop() // the instance goes away under the client
	if _, err := e.Get(ctxT(t), "PNM", []string{in.CID}, false, false); !errors.Is(err, format4.ErrStopped) {
		t.Fatalf("Get on a stopped instance: %v", err)
	}
	if err := e.Close(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Head(ctxT(t), format4.Query{Type: "OMM"}); !errors.Is(err, format4.ErrStopped) {
		t.Fatalf("Head after Close: %v", err)
	}
}

func TestQueryFieldsAnOpDoesNotTakeAreRefused(t *testing.T) {
	d := newDouble(t, defaultDoubleCfg())
	e := d.engine(t)
	registerRaw(t, e)
	for name, call := range map[string]func() error{
		"window seq": func() error { _, err := e.Window(ctxT(t), format4.Query{Type: "OMM", SeqAfter: 3}); return err },
		"index peer": func() error { _, err := e.IndexPage(ctxT(t), format4.Query{Type: "OMM", Peer: "x"}); return err },
		"scan cap":   func() error { _, err := e.Scan(ctxT(t), format4.Query{Type: "OMM", ByteCap: 10}); return err },
		"no type":    func() error { _, err := e.Scan(ctxT(t), format4.Query{}); return err },
		"epoch cid": func() error {
			_, err := e.Epoch(ctxT(t), format4.EpochQuery{Query: format4.Query{Type: "OMM", CID: "x"}, Profile: format4.EpochAsOf})
			return err
		},
	} {
		var se *format4.StatusError
		if err := call(); !errors.As(err, &se) || se.Status != format4.StatusArg {
			t.Fatalf("%s: %v, want StatusArg", name, err)
		}
	}
}

func TestSelected(t *testing.T) {
	for v, want := range map[string]bool{"": false, "1": false, "2": false, "4": true, " 4 ": true, "sqlite": true, "SQLite": true, "sqlite3": false} {
		t.Setenv(format4.FormatEnv, v)
		if format4.Selected() != want {
			t.Fatalf("SDN_STORE_FORMAT=%q: Selected %v", v, !want)
		}
	}
	if typ, err := format4.TypeOf("OMM.fbs"); err != nil || typ != "OMM" {
		t.Fatalf("TypeOf: %q %v", typ, err)
	}
}

func TestStatusErrors(t *testing.T) {
	err := &format4.StatusError{Op: "PUT", Status: format4.StatusBusy, Msg: "credit"}
	if !errors.Is(err, format4.ErrBusy) || errors.Is(err, format4.ErrNoType) {
		t.Fatal("errors.Is does not match by status")
	}
	if !strings.Contains(err.Error(), "PUT") || format4.RejectReason(format4.RejectCIDForm) == "" {
		t.Fatal("error text")
	}
}

// A format-4 Open refuses formats 2 and 3, and a fresh create beside an
// unmigrated format-1 store, before it touches a file (contract §2.4).
func TestOpenRefusesOtherFormats(t *testing.T) {
	mk := func(t *testing.T, files map[string]string, dirs ...string) string {
		root := t.TempDir()
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	for _, tc := range []struct {
		name string
		root func(t *testing.T) string
		mode format4.CreateMode
		want error
	}{
		{"format 2 activated", func(t *testing.T) string {
			return mk(t, map[string]string{"fsql2/MIGRATED": "m", "fsql2/STORE": "s"}, "control.flatsqldb")
		}, format4.CreateFresh, format4.ErrWrongFormat},
		{"format 3 migrated, control still a file", func(t *testing.T) string {
			return mk(t, map[string]string{"fsql2/MIGRATED": "m", "control.flatsqldb": "db"})
		}, format4.OpenExisting, format4.ErrWrongFormat},
		{"format-2 activation begun", func(t *testing.T) string { return mk(t, nil, "control.flatsqldb") },
			format4.CreateForMigration, format4.ErrWrongFormat},
		{"unmigrated format 1, fresh", func(t *testing.T) string { return mk(t, map[string]string{"control.flatsqldb": "db"}) },
			format4.CreateFresh, format4.ErrNotMigrated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)
			_, err := format4.Open(ctxT(t), format4.Options{DataRoot: root, Create: tc.mode, Wasm: []byte("not reached")})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open: %v, want %v", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "fsql4")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused Open created fsql4/: %v", err)
			}
		})
	}
}

// The build stamp names the embedded engine (versioninfo pins).
func TestP4EngineIsTheBuildStamp(t *testing.T) {
	if versioninfo.P4EngineSHA256 != flatsqlrt.P4ThreadsSHA256 {
		t.Fatalf("versioninfo.P4EngineSHA256 %s describes another engine than the embedded %s (%s)",
			versioninfo.P4EngineSHA256, flatsqlrt.P4ThreadsSHA256, flatsqlrt.P4ThreadsPackage)
	}
	if versioninfo.P4StoreFormat != marker.StoreFormat || versioninfo.MaxStoreFormat < marker.StoreFormat {
		t.Fatalf("P4StoreFormat %d, marker format %d, MaxStoreFormat %d", versioninfo.P4StoreFormat, marker.StoreFormat, versioninfo.MaxStoreFormat)
	}
}
