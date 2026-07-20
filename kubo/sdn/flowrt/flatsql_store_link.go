package flowrt

// flatsql_store_link.go — the LOAD-time in-wasm FlatSQL store linkage for a
// wasi-threads composed flow (the OD supplemental-OMM write lane). Proven-safe
// composition mechanism (TestFlatSQLLinkCompositionSpike): the flatsql engine
// CANNOT join the WithWASIThreads executor (mutually exclusive), so it runs as a
// DEDICATED-THREAD flatsqlrt engine and the composed flow's single import
// `flatsql.exec_envelope` resolves to a HOST TRAMPOLINE that marshals each
// envelope into the engine's high-level API under the engine module lock.
//
// The store node (com.digitalarsenal.hostcap.flatsql-store) builds an envelope in
// ITS reactor memory and calls exec_envelope(env_ptr, env_len). ALL record logic
// (SDS-type derivation, decode, CREATE TABLE / INSERT OR IGNORE into sds_omm/
// sds_ocm/sds_obd) is IN-WASM in the store node's C++. The host here is DUMB: it
// reads opaque envelope bytes from the flow's shared memory, runs the SQL, and
// returns a row count. Persistence is an OPAQUE whole-arena ExportData snapshot
// written via the fs connector — the host NEVER decodes a record (no type
// derivation, no frame-splitting, no per-record keying). Growing any record
// awareness here re-creates the repudiated Go storage sink.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/second-state/WasmEdge-go/wasmedge"
)

// flatsqlExecEnvelopeName is the ONE symbol the store node imports from module
// "flatsql". Signature: int64 exec_envelope(uint32 env_ptr, uint32 env_len).
const flatsqlExecEnvelopeName = "exec_envelope"

// flatsqlIngestRecordName is the SECOND symbol the store node imports from module
// "flatsql". Signature: int64 ingest_record(uint32 buf_ptr, uint32 buf_len). The
// store node builds a wrapper FlatBuffer (sds_omm/sds_ocm/sds_obd shape, file
// identifier SOMM/SOCM/SOBD) in ITS reactor memory and calls this to APPEND it to
// the exportable arena. This is the persistence-capable write path: FlatSQL only
// snapshots the FlatBuffer arena (ExportData), and its arena vtabs REJECT a SQL
// INSERT ("table may not be modified") — so stored records MUST arrive via ingest,
// not INSERT, to survive a snapshot/rebuild (restart). exec_envelope stays the
// READ path (the store node's in-wasm dedup pre-check + queries).
const flatsqlIngestRecordName = "ingest_record"

// flatsqlStoreSchema DECLARES the OD write-lane tables sds_omm/sds_ocm/sds_obd in
// the flow Database's FlatBuffers schema so their rows live in the ExportData
// arena (a runtime-only `CREATE TABLE` exports 0 bytes and is LOST on restart).
// The columns match the store node's wrapper table EXACTLY (cid is the (key)
// dedup field; data is the raw $OMM/$OCM/$OBD BLOB):
//
//	cid:string (key)  provider:string  source_name:string  batch_id:string  data:[ubyte]  pulled_at:long
//
// pulled_at (voffset 14, additive) is the fire timestamp (unix ms) the store node
// writes from its "trigger" input port. Additive: old 5-field wrappers (the shipped
// 10,847-object snapshot) still load — pulled_at reads 0 for records ingested before
// this field existed, so a snapshot rebuild does NOT break.
const flatsqlStoreSchema = `
  table sds_omm { cid:string (key); provider:string; source_name:string; batch_id:string; data:[ubyte]; pulled_at:long; }
  table sds_ocm { cid:string (key); provider:string; source_name:string; batch_id:string; data:[ubyte]; pulled_at:long; }
  table sds_obd { cid:string (key); provider:string; source_name:string; batch_id:string; data:[ubyte]; pulled_at:long; }
`

// flatsqlStoreFileIDs maps each store-local 4-byte FlatBuffer file identifier to
// its arena table. RegisterFileID both MATERIALIZES the arena vtab and routes
// IngestOne(wrapper) to the right table by the buffer's embedded identifier. The
// ids are store-local (SOMM/SOCM/SOBD) — deliberately NOT the SDS ids ($OMM/...) —
// so a raw SDS record can never be mis-ingested into a wrapper table.
var flatsqlStoreFileIDs = []struct{ fileID, table string }{
	{"SOMM", "sds_omm"},
	{"SOCM", "sds_ocm"},
	{"SOBD", "sds_obd"},
}

// ParamTag values (must match the store node's flatsql_capi.cpp ParamTag).
const (
	paramTagText = 4
	paramTagBlob = 5
)

// LinkedStore is a per-flow dedicated-thread FlatSQL engine + Database the
// composed flow drives via the exec_envelope trampoline. Safe for concurrent use
// (the engine lock serializes; the OD flow's drain calls it single-threaded-in-
// effect after od workers join).
type LinkedStore struct {
	rt           *flatsqlrt.Runtime
	db           *flatsqlrt.Database
	snapshotPath string
	// pendingTombstones is the set of 1-based export ordinals (== global ingest
	// sequences) the store node has asked to drop; compact() reclaims them. The
	// host NEVER decides what to tombstone — the wasm store node computes the
	// keep-latest-K policy and passes exact sequences.
	pendingTombstones map[uint64]bool
	mu                sync.Mutex
}

// OpenLinkedStore opens the dedicated-thread engine + a Database, rebuilding from
// an existing OPAQUE snapshot when present (persistence across restarts).
func OpenLinkedStore(aotCacheDir, snapshotPath string) (*LinkedStore, error) {
	rt, err := flatsqlrt.New(flatsqlrt.WithAOTCache(aotCacheDir))
	if err != nil {
		return nil, fmt.Errorf("flowrt: open flatsql engine: %w", err)
	}
	db, err := rt.CreateDatabase(flatsqlStoreSchema, "sdn_flow_store")
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("flowrt: create flow store db: %w", err)
	}
	// Materialize the arena vtabs so IngestOne(wrapper) lands in the exported
	// arena (and so a rebuilt snapshot re-registers the same routing). Must run
	// before LoadAndRebuild, mirroring flatsqlrt's export/rebuild round-trip.
	for _, m := range flatsqlStoreFileIDs {
		if e := db.RegisterFileID(m.fileID, m.table); e != nil {
			rt.Close()
			return nil, fmt.Errorf("flowrt: register store file id %s->%s: %w", m.fileID, m.table, e)
		}
	}
	s := &LinkedStore{rt: rt, db: db, snapshotPath: snapshotPath, pendingTombstones: map[uint64]bool{}}
	if snapshotPath != "" {
		if blob, e := os.ReadFile(snapshotPath); e == nil && len(blob) > 0 {
			if e := db.LoadAndRebuild(blob); e != nil {
				log.Warnf("flowrt: rebuild flow store from snapshot %s failed: %v (starting empty)", snapshotPath, e)
			} else {
				log.Infof("flowrt: flow store rebuilt from opaque snapshot %s (%d bytes)", snapshotPath, len(blob))
			}
		}
	}
	return s, nil
}

// execEnvelope parses one opaque envelope and runs it on the flow DB, returning
// the SQL row count (>=0) or a negative error code. Shared by the host
// trampoline and unit tests. It NEVER inspects record content — the SQL + params
// are produced entirely in-wasm by the store node.
func (s *LinkedStore) execEnvelope(env []byte) int64 {
	sql, params, err := parseExecEnvelope(env)
	if err != nil {
		return -2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Query(sql, params...)
	if err != nil {
		return -3
	}
	// A single-cell integer result (the store node's dedup pre-check
	// `SELECT COUNT(*) ... WHERE cid=?` or `SELECT 1 ... LIMIT 1`) returns that
	// scalar so the guest can branch on existence; any other result returns the
	// row count. Either way >0 means "already stored".
	if len(res.Rows) == 1 && len(res.Rows[0]) == 1 {
		if n, ok := res.Rows[0][0].(int64); ok {
			return n
		}
	}
	return int64(len(res.Rows))
}

// ExecEnvelopeHostFunc returns the `flatsql.exec_envelope` trampoline bound to
// this store, reading the envelope from the CALLING flow's (shared) memory.
func (s *LinkedStore) ExecEnvelopeHostFunc() wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }
	return wasmrt.HostFunc{
		Name:    flatsqlExecEnvelopeName,
		Params:  []*wasmedge.ValType{i32(), i32()},
		Returns: []*wasmedge.ValType{i64()},
		Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			envPtr := uint32(params[0].(int32))
			envLen := uint32(params[1].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			env, err := mem.GetData(uint(envPtr), uint(envLen))
			if err != nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			return []interface{}{s.execEnvelope(env)}, wasmedge.Result_Success
		},
	}
}

// IngestRecordHostFunc returns the `flatsql.ingest_record` trampoline: it copies
// the store node's wrapper FlatBuffer out of the calling flow's (shared) memory
// and INGESTS it into the arena, routed to sds_omm/sds_ocm/sds_obd by the
// buffer's file identifier (SOMM/SOCM/SOBD). This is the persistence-capable
// write path — arena rows are captured by ExportData, whereas a SQL INSERT into
// an arena vtab is rejected by the engine. The host reads OPAQUE bytes only: no
// decode, no SDS-type derivation, no per-record keying (that all stays in-wasm).
func (s *LinkedStore) IngestRecordHostFunc() wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }
	return wasmrt.HostFunc{
		Name:    flatsqlIngestRecordName,
		Params:  []*wasmedge.ValType{i32(), i32()},
		Returns: []*wasmedge.ValType{i64()},
		Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			bufPtr := uint32(params[0].(int32))
			bufLen := uint32(params[1].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			buf, err := mem.GetData(uint(bufPtr), uint(bufLen))
			if err != nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			return []interface{}{s.ingestRecord(buf)}, wasmedge.Result_Success
		},
	}
}

// ingestRecord appends one opaque wrapper FlatBuffer to the arena, returning the
// engine record sequence (>=0) or a negative error. It NEVER inspects the buffer;
// routing is by the buffer's own file identifier through the RegisterFileID map.
func (s *LinkedStore) ingestRecord(buf []byte) int64 {
	if len(buf) == 0 {
		return -2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := s.db.IngestOne(buf)
	if err != nil {
		return -3
	}
	return int64(seq)
}

// BuildTestWrapperRow constructs one store-node wrapper FlatBuffer — the
// EXACT shape ingest_record expects (cid/provider/source_name/batch_id/data,
// stamped with the given store-local file identifier SOMM/SOCM/SOBD) — and
// IngestTestRow below appends it directly to a store's arena. Both exist SO
// OTHER READ-SIDE PACKAGES (e.g. sdn/sdnodresults) can build realistic
// fixtures over a real LinkedStore without a full wasm mount. Production
// records always arrive through the wasm store node's ingest_record
// trampoline (IngestRecordHostFunc) — this is a test/fixture convenience,
// never a second production write path.
func BuildTestWrapperRow(fileID, cid, provider, sourceName, batchID string, data []byte) []byte {
	b := flatbuffers.NewBuilder(256 + len(data))
	cidOff := b.CreateString(cid)
	provOff := b.CreateString(provider)
	srcOff := b.CreateString(sourceName)
	batchOff := b.CreateString(batchID)
	dataOff := b.CreateByteVector(data)
	b.StartObject(5)
	b.PrependUOffsetTSlot(0, cidOff, 0)
	b.PrependUOffsetTSlot(1, provOff, 0)
	b.PrependUOffsetTSlot(2, srcOff, 0)
	b.PrependUOffsetTSlot(3, batchOff, 0)
	b.PrependUOffsetTSlot(4, dataOff, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte(fileID))
	return b.FinishedBytes()
}

// IngestTestRow appends one BuildTestWrapperRow fixture directly to s's
// arena. See BuildTestWrapperRow's doc — test/fixture use only.
func (s *LinkedStore) IngestTestRow(fileID, cid, provider, sourceName, batchID string, data []byte) error {
	if seq := s.ingestRecord(BuildTestWrapperRow(fileID, cid, provider, sourceName, batchID, data)); seq < 0 {
		return fmt.Errorf("flowrt: ingest test row: rc=%d", seq)
	}
	return nil
}

// Query runs a READ-ONLY SQL statement against the flow's linked store and
// returns the result — the data-surface board's search/list/download API over
// the sds_omm/sds_ocm/sds_obd arena tables (cid/provider/source_name/batch_id/
// data). Rejects anything but a single SELECT: this is a query surface, never
// a second write path (records only ever arrive via the in-wasm store node's
// ingest_record/exec_envelope trampolines above). The host does not decode the
// `data` BLOB — callers that need record fields parse the returned bytes with
// the SDS Go bindings.
func (s *LinkedStore) Query(sql string, params ...interface{}) (*flatsqlrt.Result, error) {
	if !isReadOnlySelect(sql) {
		return nil, fmt.Errorf("flowrt: store query must be a single read-only SELECT")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Query(sql, params...)
}

// QueryRecordStream runs a READ-ONLY SELECT whose result cells are record
// BLOBs and returns them as one size-prefixed stream. Unlike the general
// Query path, FlatSQL materializes the whole response artifact in-wasm and
// copies it out once, avoiding per-cell WasmEdge calls for catalog-sized
// board aggregates. The sandboxed engine entry point is used here because it
// deliberately bypasses every response cache: a live OD fire changes the
// generation on each ingest, and retaining successive catalog-sized poll
// results would only hold stale streams in memory.
func (s *LinkedStore) QueryRecordStream(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (*flatsqlrt.RawStream, error) {
	if !isReadOnlySelect(sql) {
		return nil, fmt.Errorf("flowrt: store record-stream query must be a single read-only SELECT")
	}
	if caps.MaxRows == 0 || caps.MaxBytes == 0 || caps.Timeout <= 0 {
		return nil, fmt.Errorf("flowrt: store record-stream query requires finite row, byte, and time limits")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.QuerySandboxedStream(sql, caps, params...)
}

// isReadOnlySelect reports whether sql is (structurally) a single SELECT
// statement: starts with SELECT (case-insensitive, ignoring leading
// whitespace) and carries no statement-separating semicolon other than an
// optional single trailing one. A cheap guard, not a SQL parser — the engine
// itself still enforces real semantics; this only keeps this read surface from
// being repurposed as a write path by an incautious caller.
func isReadOnlySelect(sql string) bool {
	trimmed := strings.TrimSpace(sql)
	trimmed = strings.TrimSuffix(trimmed, ";")
	if strings.Contains(trimmed, ";") {
		return false
	}
	return len(trimmed) >= 6 && strings.EqualFold(trimmed[:6], "select")
}

// Snapshot writes an OPAQUE whole-arena ExportData blob to the snapshot path via
// the fs connector (host never decodes it). Best-effort atomic rename.
func (s *LinkedStore) Snapshot() error {
	if s.snapshotPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := s.db.ExportData()
	if err != nil {
		return fmt.Errorf("flowrt: export flow store snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.snapshotPath), 0o755); err != nil {
		return err
	}
	tmp := s.snapshotPath + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.snapshotPath)
}

// Close snapshots then releases the engine.
func (s *LinkedStore) Close() {
	if err := s.Snapshot(); err != nil {
		log.Warnf("flowrt: flow store snapshot on close failed: %v", err)
	}
	if s.db != nil {
		s.db.Destroy()
	}
	if s.rt != nil {
		s.rt.Close()
	}
}

// parseExecEnvelope decodes [u32le sql_len][sql][u32le param_count] + TLV params
// (u8 tag {4=TEXT,5=BLOB}, u32le size, bytes) — the store node's flatsql_capi
// ParamTag wire format.
func parseExecEnvelope(env []byte) (string, []interface{}, error) {
	off := 0
	rdU32 := func() (uint32, bool) {
		if off+4 > len(env) {
			return 0, false
		}
		v := binary.LittleEndian.Uint32(env[off:])
		off += 4
		return v, true
	}
	sqlLen, ok := rdU32()
	if !ok || off+int(sqlLen) > len(env) {
		return "", nil, fmt.Errorf("bad envelope: sql")
	}
	sql := string(env[off : off+int(sqlLen)])
	off += int(sqlLen)
	pc, ok := rdU32()
	if !ok {
		return "", nil, fmt.Errorf("bad envelope: param_count")
	}
	params := make([]interface{}, 0, pc)
	for i := uint32(0); i < pc; i++ {
		if off+1 > len(env) {
			return "", nil, fmt.Errorf("bad envelope: param tag %d", i)
		}
		tag := env[off]
		off++
		sz, ok := rdU32()
		if !ok || off+int(sz) > len(env) {
			return "", nil, fmt.Errorf("bad envelope: param size %d", i)
		}
		b := env[off : off+int(sz)]
		off += int(sz)
		switch tag {
		case paramTagText:
			params = append(params, string(b))
		case paramTagBlob:
			params = append(params, append([]byte(nil), b...))
		default:
			return "", nil, fmt.Errorf("bad envelope: unknown param tag %d", tag)
		}
	}
	return sql, params, nil
}

// ---------------------------------------------------------------------------
// Retention trampolines (in-wasm keep-latest-K store bound). All three are DUMB:
// the wasm store node owns the policy (which batches to drop); these host funcs
// only run the wasm-authored SQL, tombstone the exact opaque sequences the wasm
// chose, and mechanically reclaim by re-serializing the export minus the
// tombstoned ordinals. No record decoding, SDS-type derivation, or keep-K logic.
// ---------------------------------------------------------------------------

// flatsqlQueryRowsName / flatsqlMarkDeletedBulkName / flatsqlCompactName are the
// three retention symbols the store node imports from module "flatsql".
const (
	flatsqlQueryRowsName       = "query_rows"
	flatsqlMarkDeletedBulkName = "mark_deleted_bulk"
	flatsqlCompactName         = "compact"
)

// queryRows runs the store node's SELECT envelope and serializes the QueryResult
// as [u32 row_count]([u32 col_count]([u8 type]payload)*)* — type 0=NULL, 1=INT
// (i64le), 2=TEXT(u32len+bytes), 3=BLOB(u32len+bytes). It NEVER interprets a
// column. Returns the serialized bytes; the caller returns the FULL length.
func (s *LinkedStore) queryRows(env []byte) ([]byte, bool) {
	sql, params, err := parseExecEnvelope(env)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	res, err := s.db.Query(sql, params...)
	s.mu.Unlock()
	if err != nil {
		return nil, false
	}
	var b bytes.Buffer
	var u32 [4]byte
	var u64 [8]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(len(res.Rows)))
	b.Write(u32[:])
	for _, row := range res.Rows {
		binary.LittleEndian.PutUint32(u32[:], uint32(len(row)))
		b.Write(u32[:])
		for _, cell := range row {
			switch v := cell.(type) {
			case nil:
				b.WriteByte(0)
			case int64:
				b.WriteByte(1)
				binary.LittleEndian.PutUint64(u64[:], uint64(v))
				b.Write(u64[:])
			case string:
				b.WriteByte(2)
				binary.LittleEndian.PutUint32(u32[:], uint32(len(v)))
				b.Write(u32[:])
				b.WriteString(v)
			case []byte:
				b.WriteByte(3)
				binary.LittleEndian.PutUint32(u32[:], uint32(len(v)))
				b.Write(u32[:])
				b.Write(v)
			default:
				b.WriteByte(0)
			}
		}
	}
	return b.Bytes(), true
}

// markDeletedBulk tombstones each opaque sequence the store node passed (loop
// Database.MarkDeleted) and records the sequences for the next compact(). It
// never learns what a batch/record is. Returns the count tombstoned.
func (s *LinkedStore) markDeletedBulk(table string, seqs []uint64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, seq := range seqs {
		_ = s.db.MarkDeleted(table, seq)
		s.pendingTombstones[seq] = true
		count++
	}
	return count
}

// compact mechanically reclaims: export the raw size-prefixed arena stream, drop
// the records whose 1-based ordinal (== global ingest sequence) was tombstoned,
// LoadAndRebuild the survivors into a FRESH Database (reuse-in-place double-
// indexes — the fresh swap is mandatory), swap the binding, and clear tombstones.
// Returns the survivor record count. Zero record-content awareness.
func (s *LinkedStore) compact() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactLocked()
}

// compactLocked is compact() assuming s.mu is HELD (ClearBatch marks + compacts
// under one lock). Same DUMB mechanics: export -> drop tombstoned ordinals ->
// fresh Database -> LoadAndRebuild -> swap -> clear tombstones.
func (s *LinkedStore) compactLocked() int64 {
	if len(s.pendingTombstones) == 0 {
		return 0
	}
	blob, err := s.db.ExportData()
	if err != nil {
		return -3
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(blob)
	if err != nil {
		return -4
	}
	var out bytes.Buffer
	var u32 [4]byte
	var survivors int64
	for i, frame := range frames {
		if s.pendingTombstones[uint64(i+1)] {
			continue
		}
		binary.LittleEndian.PutUint32(u32[:], uint32(len(frame)))
		out.Write(u32[:])
		out.Write(frame)
		survivors++
	}
	fresh, err := s.rt.CreateDatabase(flatsqlStoreSchema, "sdn_flow_store")
	if err != nil {
		return -5
	}
	for _, m := range flatsqlStoreFileIDs {
		if e := fresh.RegisterFileID(m.fileID, m.table); e != nil {
			fresh.Destroy()
			return -6
		}
	}
	if err := fresh.LoadAndRebuild(out.Bytes()); err != nil {
		fresh.Destroy()
		return -7
	}
	old := s.db
	s.db = fresh
	old.Destroy()
	s.pendingTombstones = map[uint64]bool{}
	return survivors
}

// QueryRowsHostFunc / MarkDeletedBulkHostFunc / CompactHostFunc expose the three
// retention trampolines as flatsql.* host funcs (registered alongside
// exec_envelope + ingest_record by NewFlowRuntime).
func (s *LinkedStore) QueryRowsHostFunc() wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }
	return wasmrt.HostFunc{
		Name:    flatsqlQueryRowsName,
		Params:  []*wasmedge.ValType{i32(), i32(), i32(), i32()},
		Returns: []*wasmedge.ValType{i64()},
		Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			envPtr := uint32(params[0].(int32))
			envLen := uint32(params[1].(int32))
			outPtr := uint32(params[2].(int32))
			outCap := uint32(params[3].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			env, err := mem.GetData(uint(envPtr), uint(envLen))
			if err != nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			buf, ok := s.queryRows(env)
			if !ok {
				return []interface{}{int64(-2)}, wasmedge.Result_Success
			}
			if uint32(len(buf)) <= outCap && len(buf) > 0 {
				mem.SetData(buf, uint(outPtr), uint(len(buf)))
			}
			return []interface{}{int64(len(buf))}, wasmedge.Result_Success
		},
	}
}

func (s *LinkedStore) MarkDeletedBulkHostFunc() wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }
	return wasmrt.HostFunc{
		Name:    flatsqlMarkDeletedBulkName,
		Params:  []*wasmedge.ValType{i32(), i32(), i32(), i32()},
		Returns: []*wasmedge.ValType{i64()},
		Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			tblPtr := uint32(params[0].(int32))
			tblLen := uint32(params[1].(int32))
			seqsPtr := uint32(params[2].(int32))
			seqCount := uint32(params[3].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			tblBytes, err := mem.GetData(uint(tblPtr), uint(tblLen))
			if err != nil {
				return []interface{}{int64(-1)}, wasmedge.Result_Success
			}
			seqs := make([]uint64, 0, seqCount)
			if seqCount > 0 {
				raw, e := mem.GetData(uint(seqsPtr), uint(seqCount*8))
				if e != nil {
					return []interface{}{int64(-1)}, wasmedge.Result_Success
				}
				for i := uint32(0); i < seqCount; i++ {
					seqs = append(seqs, binary.LittleEndian.Uint64(raw[i*8:]))
				}
			}
			return []interface{}{s.markDeletedBulk(string(tblBytes), seqs)}, wasmedge.Result_Success
		},
	}
}

func (s *LinkedStore) CompactHostFunc() wasmrt.HostFunc {
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }
	return wasmrt.HostFunc{
		Name:    flatsqlCompactName,
		Returns: []*wasmedge.ValType{i64()},
		Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
			return []interface{}{s.compact()}, wasmedge.Result_Success
		},
	}
}

// cellInt64 coerces a flatsqlrt scalar result cell into int64 (rowid reads).
func cellInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	default:
		return 0
	}
}

// ClearBatch is the store side of the operator "reset a run" primitive: DUMB —
// tombstone every row whose batch_id column equals batchID across the three OD
// write-lane tables, then compact. The caller supplies an opaque batch id; the
// host interprets no record content, derives no SDS type, applies no policy. It
// returns the per-table global sequences it tombstoned (PRE-compact rowids, so
// the mount can prune its fire log by rowid-range overlap before the compact
// renumbers) and the survivor record count.
func (s *LinkedStore) ClearBatch(batchID string) (map[string][]int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tombstoned := map[string][]int64{}
	for _, m := range flatsqlStoreFileIDs {
		res, err := s.db.Query("SELECT rowid FROM "+m.table+" WHERE batch_id = ?", batchID)
		if err != nil {
			return nil, 0, err
		}
		for _, row := range res.Rows {
			if len(row) < 1 {
				continue
			}
			seq := cellInt64(row[0])
			if seq <= 0 {
				continue
			}
			_ = s.db.MarkDeleted(m.table, uint64(seq))
			s.pendingTombstones[uint64(seq)] = true
			tombstoned[m.table] = append(tombstoned[m.table], seq)
		}
	}
	survivors := s.compactLocked()
	return tombstoned, survivors, nil
}
