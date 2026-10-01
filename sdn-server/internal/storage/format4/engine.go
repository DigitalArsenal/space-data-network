package format4

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/internal/cidv1"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// Options opens a format-4 engine.
type Options struct {
	DataRoot      string // <data>; the engine root is <data>/fsql4
	Create        CreateMode
	GseqFloor     uint64 // CreateFresh / CreateForMigration
	Cores         int    // 0 = runtime.NumCPU()
	Tuning        Tuning
	Wasm          []byte // nil = flatsqlrt.P4ThreadsWasm()
	AOTCacheDir   string
	CompileOnMiss bool                   // tests and prewarm only
	Store         *flatsqlrt.NativeStore // the node's shared native I/O store, as format 2
	OnFailure     func(error)            // instance trapped or hung; the Engine is fenced
}

// Control exports (contract §3.3), called on the instance's exec thread.
const (
	exportRegisterType = "flatsql_p4_register_type"
	exportSetQuota     = "flatsql_p4_set_quota"
	exportActivate     = "flatsql_p4_activate"
	exportStats        = "flatsql_p4_stats"
	statsEntries       = 40 // §3.10; the engine may append more
	statsCap           = 8 * 256
)

// control is the instance's control surface (*flatsqlrt.P4Instance).
type control interface {
	ControlBytes(export string, in []byte) (int32, error)
	ControlCall(export string, args ...interface{}) (int32, error)
	ControlOut(export string, capacity int) ([]byte, int32, error)
	StopWithin(deadline time.Duration) (int32, error)
}

// Engine is the format-4 store over the real engine.
type Engine struct {
	ctl     control
	mb      *mailbox
	reqCap  int // the write pool's request bytes
	readCap int // the read pool's request bytes (C-6: 64 KiB by default)
	fenced  atomic.Pointer[error]
	closing atomic.Bool
	once    sync.Once
	stopErr error
}

var _ API = (*Engine)(nil)

// Open initializes and starts the engine (init + start). *Engine implements
// API.
func Open(ctx context.Context, opt Options) (*Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := strings.TrimSpace(opt.DataRoot)
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("format4: the data root %q is not an absolute path", opt.DataRoot)
	}
	if err := checkDataRoot(root, opt.Create); err != nil {
		return nil, err
	}
	engineRoot := filepath.Join(root, marker.Dir)
	if opt.Create != OpenExisting {
		if err := os.MkdirAll(engineRoot, 0o700); err != nil {
			return nil, fmt.Errorf("format4: %w", err)
		}
	}
	wasm := opt.Wasm
	if wasm == nil {
		wasm = flatsqlrt.P4ThreadsWasm()
	}
	if len(wasm) == 0 {
		return nil, errors.New("format4: this build embeds no format-4 engine (flatsql-p4-threads.wasm)")
	}
	e := &Engine{}
	inst, err := flatsqlrt.OpenP4Instance(flatsqlrt.P4Config{
		Wasm: wasm, AOTCacheDir: opt.AOTCacheDir, CompileOnMiss: opt.CompileOnMiss,
		Store: opt.Store, StoreRoot: root, InitConfig: encodeConfig(engineRoot, opt),
		OnFailure: func(_ *flatsqlrt.P4Instance, cause error) {
			e.fence(cause)
			if opt.OnFailure != nil {
				opt.OnFailure(cause)
			}
		},
	})
	if err != nil {
		var ce *flatsqlrt.P4CallError
		if errors.As(err, &ce) {
			return nil, &StatusError{Op: "init", Status: ce.Status, Msg: err.Error()}
		}
		return nil, err
	}
	if err := e.attach(inst.Memory(), inst, inst, inst.EngineLayout()); err != nil {
		_, _ = inst.StopWithin(10 * time.Second)
		return nil, err
	}
	return e, nil
}

// checkDataRoot refuses, before any file is touched, a data root format 4
// must not open (contract §2.4): an activated format-2 or format-3 store
// (fsql2/MIGRATED, or control.flatsqldb turned into a directory without
// fsql4), and, for a fresh create, an unmigrated format-1 store (a store
// created beside it would hide its records).
func checkDataRoot(root string, mode CreateMode) error {
	m, err := marker.Read(root)
	if err != nil {
		return err
	}
	if m.Format4() {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, format2.Dir, "MIGRATED")); err == nil || m.LegacyControlDir {
		return ErrWrongFormat
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("format4: %w", err)
	}
	if m.LegacyControlFile && mode == CreateFresh {
		return ErrNotMigrated
	}
	return nil
}

// attach builds the mailbox over the instance's layout.
func (e *Engine) attach(mem memory, h host, ctl control, raw []byte) error {
	lay, err := parseLayout(raw)
	if err != nil {
		return err
	}
	mb, err := newMailbox(mem, h, lay)
	if err != nil {
		return err
	}
	e.ctl, e.mb, e.reqCap, e.readCap = ctl, mb, int(lay.reqBytes[0]), int(lay.reqBytes[1])
	return nil
}

// encodeConfig is flatsql_p4_init's TLV (§3.3): the root, the create mode,
// the floor and cores, then the tuning; zero is the engine default.
func encodeConfig(engineRoot string, opt Options) []byte {
	cores := opt.Cores
	if cores <= 0 {
		cores = runtime.NumCPU()
	}
	t := opt.Tuning
	c := tlvText(nil, 1, engineRoot)
	c = tlvU8(c, 2, uint8(opt.Create))
	c = tlv(c).u32(3, t.WriterThreads).u32(4, t.ReaderLanes).u32(5, t.BulkLanes).u32(6, t.SandboxLanes).
		u32(7, t.WriteSlots).u32(8, t.ReadSlots).u32(9, t.WriteRequestBytes).u32(10, t.ReadRequestBytes).
		u32(11, t.RingBytes).u64(12, opt.GseqFloor).u32(13, uint32(cores)).u64(20, t.EngineBytes).
		u32(21, t.WriterConns).u32(22, t.WriterCacheKiB).u32(23, t.ReaderConns).u32(24, t.ReaderCacheKiB).
		u64(25, t.PendingMapBytes).u64(26, t.SoftHeap).u64(27, t.HardHeap).u32(40, t.GroupCommitRecords).
		u32(41, t.GroupCommitMs)
	return append(c, t.Extra...)
}

// fence makes every later call return ErrStopped (§5.2: a trap or hang).
func (e *Engine) fence(cause error) {
	err := fmt.Errorf("format4: engine fenced: %w", cause)
	e.fenced.CompareAndSwap(nil, &err)
}

// live refuses a call on a fenced or closed engine.
func (e *Engine) live(op string) error {
	if p := e.fenced.Load(); p != nil {
		return &StatusError{Op: op, Status: StatusStopped, Msg: (*p).Error()}
	}
	if e.closing.Load() {
		return &StatusError{Op: op, Status: StatusStopped, Msg: ErrClosed.Error()}
	}
	return nil
}

// stopped maps the mailbox's ErrStopped (the instance is stopping) onto the
// fence's cause when there is one.
func (e *Engine) stopped(op string, err error) error {
	if errors.Is(err, ErrStopped) {
		if lerr := e.live(op); lerr != nil {
			return lerr
		}
	}
	return err
}

func statusOf(op string, rc int32, err error) error {
	if err != nil {
		return fmt.Errorf("format4: %s: %w", op, err)
	}
	if rc < 0 {
		return &StatusError{Op: op, Status: rc}
	}
	return nil
}

// ---- control ----------------------------------------------------------------

func (e *Engine) RegisterType(spec TypeSpec) error {
	if err := e.live("register_type"); err != nil {
		return err
	}
	rc, err := e.ctl.ControlBytes(exportRegisterType, spec.Encode())
	return statusOf("register_type "+spec.TypeName(), rc, err)
}

func (e *Engine) SetQuota(bytes int64) error {
	if err := e.live("set_quota"); err != nil {
		return err
	}
	if bytes < 0 {
		return &StatusError{Op: "set_quota", Status: StatusArg, Msg: "negative quota"}
	}
	rc, err := e.ctl.ControlCall(exportSetQuota, float64(bytes))
	return statusOf("set_quota", rc, err)
}

func (e *Engine) Activate(ctx context.Context) error {
	if err := e.live("activate"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rc, err := e.ctl.ControlCall(exportActivate)
	return statusOf("activate", rc, err)
}

func (e *Engine) Stats() ([]uint64, error) {
	if err := e.live("stats"); err != nil {
		return nil, err
	}
	b, rc, err := e.ctl.ControlOut(exportStats, statsCap)
	if err := statusOf("stats", rc, err); err != nil {
		return nil, err
	}
	if len(b)%8 != 0 || len(b) < 8*statsEntries {
		return nil, fmt.Errorf("format4: stats wrote %d bytes, want at least %d", len(b), 8*statsEntries)
	}
	out := make([]uint64, len(b)/8)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(b[8*i:])
	}
	return out, nil
}

// Close stops the engine within ctx's deadline (30 s without one). A second
// Close returns the first one's answer.
func (e *Engine) Close(ctx context.Context) error {
	e.once.Do(func() {
		e.closing.Store(true)
		deadline := 30 * time.Second
		if d, ok := ctx.Deadline(); ok {
			deadline = max(time.Until(d), 0)
		}
		rc, err := e.ctl.StopWithin(deadline)
		if err == nil && rc < 0 {
			err = &StatusError{Op: "stop", Status: rc, Msg: "the engine did not drain within the deadline"}
		}
		e.stopErr = err
	})
	return e.stopErr
}

// ---- requests ---------------------------------------------------------------

// busyBackoff is the BUSY retry: 1 ms doubling to 100 ms (§5.2 rules).
func busyBackoff(ctx context.Context, attempt int) error {
	d := min(time.Millisecond<<uint(min(attempt, 7)), 100*time.Millisecond)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do runs one request and decodes its RB1 response. P4_E_BUSY means nothing
// was done: it is retried until ctx ends, for writes and reads alike (reads
// never return ErrBusy).
func (e *Engine) do(ctx context.Context, op uint32, class Class, req []byte, want []string) (*rows, error) {
	name := opNames[op]
	for attempt := 0; ; attempt++ {
		if err := e.live(name); err != nil {
			return nil, err
		}
		var body []byte
		o, err := e.mb.run(ctx, call{op: op, class: class, req: req}, func(b []byte) error {
			body = append(body, b...)
			return nil
		})
		if err != nil {
			return nil, e.stopped(name, err)
		}
		if o.status == StatusBusy {
			if err := busyBackoff(ctx, attempt); err != nil {
				return nil, err
			}
			continue
		}
		rs, derr := decodeRows(name, body, want)
		if o.status != StatusOK {
			return rs, &StatusError{Op: name, Status: o.status, Msg: o.err}
		}
		return rs, derr
	}
}

func readClass(bulk bool) Class {
	if bulk {
		return ClassBulk
	}
	return ClassInteractive
}

// ---- writes -------------------------------------------------------------------

// maxRecordSlack is C-6: a record may use the write request area less 64 KiB.
const maxRecordSlack = 64 << 10

// Put stores a batch. Records go in chunks that fit a write slot, in input
// order; a record larger than C-6's bound is rejected here with
// RejectTooLarge, the rest are sent.
func (e *Engine) Put(ctx context.Context, b Batch) ([]Outcome, error) {
	if b.Type == "" {
		return nil, &StatusError{Op: "PUT", Status: StatusArg, Msg: "no type"}
	}
	out := make([]Outcome, len(b.Records))
	limit := e.reqCap - putOverhead(b)
	var (
		chunk []byte
		idx   []int
	)
	flush := func() error {
		if len(idx) == 0 {
			return nil
		}
		rs, err := e.do(ctx, opPut, ClassWrite, encodePut(b, len(idx), chunk), colsPut)
		if err != nil {
			return err
		}
		if len(rs.cells) != len(idx) {
			return fmt.Errorf("format4: PUT returned %d rows for %d records", len(rs.cells), len(idx))
		}
		for _, r := range rs.cells {
			i := int(cellInt(r[0]))
			if i < 0 || i >= len(idx) {
				return fmt.Errorf("format4: PUT row index %d of %d", i, len(idx))
			}
			out[idx[i]] = Outcome{Action: Action(cellInt(r[1])), Seq: cellInt(r[2]), Reject: int32(cellInt(r[3]))}
		}
		chunk, idx = chunk[:0], idx[:0]
		return nil
	}
	for i, in := range b.Records {
		entry, err := encodeRecord(nil, in, b.Peer)
		if err != nil {
			if errors.Is(err, cidv1.ErrForm) {
				out[i] = Outcome{Action: ActRejected, Reject: RejectCIDForm}
				continue
			}
			return nil, &StatusError{Op: "PUT", Status: StatusArg, Msg: fmt.Sprintf("record %d: %v", i, err)}
		}
		if len(entry) > e.reqCap-maxRecordSlack || len(entry) > limit {
			out[i] = Outcome{Action: ActRejected, Reject: RejectTooLarge}
			continue
		}
		if len(chunk)+len(entry) > limit {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		chunk = append(chunk, entry...)
		idx = append(idx, i)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Engine) Supersede(ctx context.Context, typ, provider, source, keepBatch string, apply bool) (SupersedeResult, error) {
	req := tlv(nil).text(tagType, typ).text(tagLane, provider).text(tagLane+1, source).text(tagKeepBatch, keepBatch).
		flag(tagApply, apply)
	rs, err := e.do(ctx, opSupersede, ClassWrite, req, colsSupersede)
	if err != nil {
		return SupersedeResult{}, err
	}
	r, err := rs.one()
	if err != nil {
		return SupersedeResult{}, err
	}
	return SupersedeResult{TagsDeleted: cellInt(r[0]), RecordsDeleted: cellInt(r[1]), FilesDeleted: cellInt(r[2])}, nil
}

func (e *Engine) Delete(ctx context.Context, typ string, cids []string) (int64, error) {
	var n int64
	err := e.byCIDs(ctx, opDelete, ClassWrite, e.reqCap, tlv(nil).text(tagType, typ), nil, cids, colsDelete,
		func(rs *rows) error {
			r, err := rs.one()
			if err == nil {
				n += cellInt(r[0])
			}
			return err
		})
	return n, err
}

// byCIDs runs op over cids in as many requests as the slot's request area
// (capacity bytes) needs: each request is head, a share of the CID list
// (tag 40: u32 n, then 36 bytes a CID), then tail. A GET of thousands of
// CIDs does not fit a 64 KiB read slot in one request (C-6). Every CID is
// parsed before the first request is sent; each response goes to each, in
// request order.
func (e *Engine) byCIDs(ctx context.Context, op uint32, class Class, capacity int, head, tail tlv, cids []string,
	want []string, each func(*rows) error) error {
	per := max((capacity-len(head)-len(tail)-6-4)/cidv1.Len, 1)
	var reqs []tlv
	for len(cids) > 0 {
		chunk := cids[:min(per, len(cids))]
		cids = cids[len(chunk):]
		req, err := tlv(append([]byte(nil), head...)).cids(chunk)
		if err != nil {
			return &StatusError{Op: opNames[op], Status: StatusArg, Msg: err.Error()}
		}
		reqs = append(reqs, append(req, tail...))
	}
	for _, req := range reqs {
		rs, err := e.do(ctx, op, class, req, want)
		if err != nil {
			return err
		}
		if err := each(rs); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) QuotaGC(ctx context.Context, maxBytes int64) (QuotaResult, error) {
	if maxBytes < 0 {
		return QuotaResult{}, &StatusError{Op: "QUOTA_GC", Status: StatusArg, Msg: "negative max bytes"}
	}
	rs, err := e.do(ctx, opQuotaGC, ClassWrite, tlvU64(nil, tagMaxBytes, uint64(maxBytes)), colsQuota)
	if err != nil {
		return QuotaResult{}, err
	}
	r, err := rs.one()
	if err != nil {
		return QuotaResult{}, err
	}
	return QuotaResult{FilesDropped: cellInt(r[0]), RecordsDropped: cellInt(r[1]), BytesFreed: cellInt(r[2])}, nil
}

func (e *Engine) Rebuild(ctx context.Context, typ string, what RebuildWhat) ([]RebuildRow, error) {
	if what == 0 {
		return nil, &StatusError{Op: "REBUILD", Status: StatusArg, Msg: "nothing to rebuild"}
	}
	rs, err := e.do(ctx, opRebuild, ClassWrite, tlv(nil).text(tagType, typ).u32(tagWhat, uint32(what)), colsRebuild)
	if err != nil {
		return nil, err
	}
	out := make([]RebuildRow, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, RebuildRow{Type: cellStr(r[0]), Entries: cellInt(r[1]), Mismatches: cellInt(r[2])})
	}
	return out, nil
}

// ---- reads --------------------------------------------------------------------

func (e *Engine) Get(ctx context.Context, typ string, cids []string, allCopies, hydrate bool) ([]Rec, error) {
	if len(cids) == 0 {
		return nil, nil
	}
	out := []Rec{}
	err := e.byCIDs(ctx, opGet, ClassInteractive, e.readCap, tlv(nil).text(tagType, typ).flag(tagHydrate, hydrate),
		tlv(nil).flag(tagEveryCopy, allCopies), cids, colsRec, func(rs *rows) error {
			out = append(out, rs.recs()...)
			return nil
		})
	return out, err
}

func (e *Engine) Tags(ctx context.Context, typ string, cids []string) ([]TagRow, error) {
	if len(cids) == 0 {
		return nil, nil
	}
	out := []TagRow{}
	err := e.byCIDs(ctx, opTags, ClassInteractive, e.readCap, tlv(nil).text(tagType, typ), nil, cids, colsTags,
		func(rs *rows) error {
			for _, r := range rs.cells {
				out = append(out, TagRow{CID: cellStr(r[0]), Seq: cellInt(r[1]), Producer: cellStr(r[2]),
					TagInstance: TagInstance{Tag: Tag{Provider: cellStr(r[3]), Source: cellStr(r[4]), SourceURL: cellStr(r[5]),
						Batch: cellStr(r[6]), ContentKeyID: cellStr(r[7]), ProducerPeer: cellStr(r[8]), ProducerPubkey: cellStr(r[9])},
						At: cellInt(r[10])}})
			}
			return nil
		})
	return out, err
}

func (e *Engine) query(ctx context.Context, op uint32, q Query, want []string) (*rows, error) {
	req, err := encodeQuery(op, q)
	if err != nil {
		return nil, err
	}
	return e.do(ctx, op, readClass(q.Bulk), req, want)
}

func (e *Engine) Scan(ctx context.Context, q Query) ([]Rec, error) {
	rs, err := e.query(ctx, opScan, q, colsRec)
	if err != nil {
		return nil, err
	}
	return rs.recs(), nil
}

func (e *Engine) Head(ctx context.Context, q Query) (Head, error) {
	rs, err := e.query(ctx, opHead, q, colsHead)
	if err != nil {
		return Head{}, err
	}
	r, err := rs.one()
	if err != nil {
		return Head{}, err
	}
	return Head{N: cellInt(r[0]), Bytes: cellInt(r[1]), MaxSeq: cellInt(r[2]), MaxTS: cellInt(r[3]), MaxAt: cellInt(r[4]),
		Through: cellInt(r[5]), More: cellInt(r[6]) != 0}, nil
}

func (e *Engine) Window(ctx context.Context, q Query) ([]Rec, error) {
	rs, err := e.query(ctx, opWindow, q, colsRec)
	if err != nil {
		return nil, err
	}
	return rs.recs(), nil
}

func (e *Engine) IndexPage(ctx context.Context, q Query) ([]IndexRow, error) {
	rs, err := e.query(ctx, opIndexPage, q, colsIndex)
	if err != nil {
		return nil, err
	}
	out := make([]IndexRow, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, IndexRow{Col0: cellOptInt(r[0]), Epoch: cellOptInt(r[1]), CID: cellStr(r[2])})
	}
	return out, nil
}

func (e *Engine) epoch(ctx context.Context, q EpochQuery, count bool, want []string) (*rows, error) {
	req, err := encodeEpoch(q, count)
	if err != nil {
		return nil, err
	}
	return e.do(ctx, opEpoch, readClass(q.Bulk), req, want)
}

func (e *Engine) Epoch(ctx context.Context, q EpochQuery) ([]Rec, error) {
	if q.Profile == EpochCoverage {
		return nil, &StatusError{Op: "EPOCH", Status: StatusArg, Msg: "coverage is Coverage"}
	}
	rs, err := e.epoch(ctx, q, false, colsRec)
	if err != nil {
		return nil, err
	}
	return rs.recs(), nil
}

func (e *Engine) EpochCount(ctx context.Context, q EpochQuery) (int64, error) {
	if q.Profile == EpochCoverage {
		return 0, &StatusError{Op: "EPOCH", Status: StatusArg, Msg: "coverage has no count"}
	}
	rs, err := e.epoch(ctx, q, true, colsCount)
	if err != nil {
		return 0, err
	}
	r, err := rs.one()
	if err != nil {
		return 0, err
	}
	return cellInt(r[0]), nil
}

func (e *Engine) Coverage(ctx context.Context, q EpochQuery) ([]CoverageBucket, error) {
	q.Profile = EpochCoverage
	rs, err := e.epoch(ctx, q, false, colsCoverage)
	if err != nil {
		return nil, err
	}
	out := make([]CoverageBucket, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, CoverageBucket{Day: cellStr(r[0]), N: cellInt(r[1]), MinEpoch: cellInt(r[2]), MaxEpoch: cellInt(r[3])})
	}
	return out, nil
}

// Summary kinds (request tag 45).
const (
	summaryTypes      = 1
	summaryPartitions = 2
	summaryLanes      = 3
	summaryDisk       = 4
	summaryFTS        = 5
)

func (e *Engine) summary(ctx context.Context, kind uint8, typ string, want []string) (*rows, error) {
	return e.do(ctx, opSummary, ClassInteractive, tlv(nil).text(tagType, typ).u8(tagKind, kind), want)
}

func (e *Engine) Types(ctx context.Context) ([]TypeSummary, error) {
	rs, err := e.summary(ctx, summaryTypes, "", colsTypes)
	if err != nil {
		return nil, err
	}
	out := make([]TypeSummary, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, TypeSummary{Type: cellStr(r[0]), Records: cellInt(r[1]), Copies: cellInt(r[2]), Bytes: cellInt(r[3]),
			CopyBytes: cellInt(r[4]), MinEpoch: cellOptInt(r[5]), MaxEpoch: cellOptInt(r[6]), MinTS: cellInt(r[7]),
			MaxTS: cellInt(r[8]), MaxSeq: cellInt(r[9]), Through: cellInt(r[10])})
	}
	return out, nil
}

func (e *Engine) Partitions(ctx context.Context) ([]PartitionSummary, error) {
	rs, err := e.summary(ctx, summaryPartitions, "", colsParts)
	if err != nil {
		return nil, err
	}
	out := make([]PartitionSummary, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, PartitionSummary{Type: cellStr(r[0]), Producer: cellStr(r[1]), Peer: cellStr(r[2]),
			Records: cellInt(r[3]), Bytes: cellInt(r[4]), MinTS: cellInt(r[5]), MaxTS: cellInt(r[6]), MaxSeq: cellInt(r[7]),
			Files: cellInt(r[8])})
	}
	return out, nil
}

func (e *Engine) Lanes(ctx context.Context, typ string) ([]Lane, error) {
	rs, err := e.summary(ctx, summaryLanes, typ, colsLanes)
	if err != nil {
		return nil, err
	}
	out := make([]Lane, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, Lane{Type: cellStr(r[0]), Producer: cellStr(r[1]),
			Tag: Tag{Provider: cellStr(r[2]), Source: cellStr(r[3]), Batch: cellStr(r[4]), ContentKeyID: cellStr(r[5]),
				ProducerPeer: cellStr(r[6]), ProducerPubkey: cellStr(r[7]), SourceURL: cellStr(r[8])},
			Records: cellInt(r[9]), Bytes: cellInt(r[10]), MaxSeq: cellInt(r[11]), First: cellInt(r[12]),
			Updated: cellInt(r[13]), MinW: cellInt(r[14]), MaxW: cellInt(r[15])})
	}
	return out, nil
}

func (e *Engine) Disk(ctx context.Context) ([]DiskSummary, error) {
	rs, err := e.summary(ctx, summaryDisk, "", colsDisk)
	if err != nil {
		return nil, err
	}
	out := make([]DiskSummary, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, DiskSummary{Type: cellStr(r[0]), Files: cellInt(r[1]), DBBytes: cellInt(r[2]), WALBytes: cellInt(r[3]),
			JournalBytes: cellInt(r[4]), IndexBytes: cellInt(r[5]), FTSBytes: cellInt(r[6]), FreeBytes: cellInt(r[7])})
	}
	return out, nil
}

func (e *Engine) FTS(ctx context.Context) ([]FTSState, error) {
	rs, err := e.summary(ctx, summaryFTS, "", colsFTS)
	if err != nil {
		return nil, err
	}
	out := make([]FTSState, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, FTSState{Type: cellStr(r[0]), State: cellStr(r[1]), Through: cellInt(r[2])})
	}
	return out, nil
}

// SQL runs one statement and streams its output (RB1, or raw frames with
// Raw) to sink. A sandboxed statement runs on the sandbox lane unless a class
// is given.
func (e *Engine) SQL(ctx context.Context, req SQLRequest, sink func(chunk []byte) error) (SQLStats, error) {
	class := req.Class
	switch {
	case class == 0 && req.Sandbox:
		class = ClassSandbox
	case class == 0:
		class = ClassInteractive
	case class == ClassWrite || class > ClassSandbox:
		return SQLStats{}, &StatusError{Op: "SQL", Status: StatusArg, Msg: fmt.Sprintf("class %d", class)}
	}
	var flags uint32
	if req.Raw {
		flags |= flagRaw
	}
	if req.Sandbox {
		flags |= flagSandbox
	}
	body := tlv(nil).raw(tagSQL, []byte(req.SQL))
	if p := format2.EncodeParams(req.Params); p != nil {
		body = body.raw(tagParams, p)
	}
	for attempt := 0; ; attempt++ {
		if err := e.live("SQL"); err != nil {
			return SQLStats{}, err
		}
		sent := false
		o, err := e.mb.run(ctx, call{op: opSQL, class: class, flags: flags, req: body, caps: req.Caps}, func(b []byte) error {
			sent = true
			if sink == nil {
				return nil
			}
			return sink(b)
		})
		if err != nil {
			return SQLStats{}, e.stopped("SQL", err)
		}
		st := SQLStats{Rows: o.rows, RowsExamined: o.rowsExamined, BytesRead: o.bytesRead, Queue: o.queue, Run: o.run}
		if o.status == StatusBusy && !sent {
			if err := busyBackoff(ctx, attempt); err != nil {
				return st, err
			}
			continue
		}
		if o.status != StatusOK {
			return st, &StatusError{Op: "SQL", Status: o.status, Msg: o.err}
		}
		return st, nil
	}
}

func (e *Engine) Surface(ctx context.Context) ([]Relation, error) {
	rs, err := e.do(ctx, opSurface, ClassInteractive, nil, colsSurface)
	if err != nil {
		return nil, err
	}
	var out []Relation
	for _, r := range rs.cells {
		name := cellStr(r[0])
		if n := len(out); n == 0 || out[n-1].Name != name {
			out = append(out, Relation{Name: name, Kind: cellStr(r[1]), Source: cellStr(r[2]), Bound: cellInt(r[5])})
		}
		rel := &out[len(out)-1]
		col := cellStr(r[3])
		rel.Columns = append(rel.Columns, col)
		if cellInt(r[4]) != 0 {
			rel.Placeholder = append(rel.Placeholder, col)
		}
	}
	return out, nil
}
