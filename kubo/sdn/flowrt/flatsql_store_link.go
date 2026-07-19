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
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
//	cid:string (key)  provider:string  source_name:string  batch_id:string  data:[ubyte]
const flatsqlStoreSchema = `
  table sds_omm { cid:string (key); provider:string; source_name:string; batch_id:string; data:[ubyte]; }
  table sds_ocm { cid:string (key); provider:string; source_name:string; batch_id:string; data:[ubyte]; }
  table sds_obd { cid:string (key); provider:string; source_name:string; batch_id:string; data:[ubyte]; }
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
	mu           sync.Mutex
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
	s := &LinkedStore{rt: rt, db: db, snapshotPath: snapshotPath}
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
