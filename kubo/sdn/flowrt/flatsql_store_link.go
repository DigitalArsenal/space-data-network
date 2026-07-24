package flowrt

// This file provides load-time FlatSQL linkage for any artifact carrying the
// generic linked-store descriptor. The FlatSQL engine cannot join the
// WithWASIThreads executor, so it runs on a dedicated thread and the artifact's
// `flatsql` imports resolve to host trampolines over opaque bytes.
//
// The host reads opaque envelopes and record buffers, applies only the embedded
// schema and identifier mappings, and snapshots the opaque arena. Record types,
// columns, retention decisions, and application semantics remain in WASM.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/second-state/WasmEdge-go/wasmedge"
)

// flatsqlExecEnvelopeName is the ONE symbol the store node imports from module
// "flatsql". Signature: int64 exec_envelope(uint32 env_ptr, uint32 env_len).
const flatsqlExecEnvelopeName = "exec_envelope"

// flatsqlIngestRecordName appends a descriptor-mapped FlatBuffer to the
// exportable arena. Signature: int64 ingest_record(uint32, uint32).
const flatsqlIngestRecordName = "ingest_record"

// ParamTag values (must match the store node's flatsql_capi.cpp ParamTag).
const (
	paramTagText = 4
	paramTagBlob = 5
)

// LinkedStore is a per-flow dedicated-thread FlatSQL engine + Database the
// composed flow drives via the exec_envelope trampoline. Safe for concurrent
// use because the engine lock serializes access.
type LinkedStore struct {
	rt           *flatsqlrt.Runtime
	db           *flatsqlrt.Database
	descriptor   *LinkedStoreDescriptor
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
func OpenLinkedStore(aotCacheDir, snapshotPath string, descriptor *LinkedStoreDescriptor) (*LinkedStore, error) {
	if err := descriptor.validate(); err != nil {
		return nil, fmt.Errorf("flowrt: invalid linked-store descriptor: %w", err)
	}
	rt, err := flatsqlrt.New(flatsqlrt.WithAOTCache(aotCacheDir))
	if err != nil {
		return nil, fmt.Errorf("flowrt: open flatsql engine: %w", err)
	}
	db, err := rt.CreateDatabase(descriptor.Schema, descriptor.Database)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("flowrt: create flow store db: %w", err)
	}
	// Materialize the arena vtabs so IngestOne(wrapper) lands in the exported
	// arena (and so a rebuilt snapshot re-registers the same routing). Must run
	// before LoadAndRebuild, mirroring flatsqlrt's export/rebuild round-trip.
	for _, mapping := range descriptor.FileIdentifiers {
		if e := db.RegisterFileID(mapping.ID, mapping.Table); e != nil {
			rt.Close()
			return nil, fmt.Errorf("flowrt: register store file id %s->%s: %w", mapping.ID, mapping.Table, e)
		}
	}
	s := &LinkedStore{rt: rt, db: db, descriptor: descriptor, snapshotPath: snapshotPath, pendingTombstones: map[uint64]bool{}}
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

// IngestRecordHostFunc copies one opaque FlatBuffer from shared guest memory
// into the descriptor-mapped arena. The host does not decode the record.
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

// Query runs a READ-ONLY SQL statement against the flow's linked store and
// returns the result. It rejects anything but a single SELECT; records arrive
// only through the artifact's ingest/exec trampolines.
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
// deliberately bypasses every response cache because each ingest changes the
// database generation.
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

// compactLocked is compact() assuming s.mu is held.
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
	fresh, err := s.rt.CreateDatabase(s.descriptor.Schema, s.descriptor.Database)
	if err != nil {
		return -5
	}
	for _, mapping := range s.descriptor.FileIdentifiers {
		if e := fresh.RegisterFileID(mapping.ID, mapping.Table); e != nil {
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
