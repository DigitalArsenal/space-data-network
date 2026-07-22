package sdnservices

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// BackupReadSurface is the node-side backup-source read surface the repurposed
// storage_adapter capability exposes (spec A.4). It is a primitive-typed
// interface (JSON in / JSON + bytes out) deliberately, so this package need not
// import sdn/sdnbackup — which would form a cycle (sdnbackup -> sdnmodules ->
// sdnservices). *sdnbackup.BackupSource satisfies it structurally.
type BackupReadSurface interface {
	// BackupUnitsJSON returns a JSON array of {contentHash, kind, size, meta}
	// unit descriptors (bytes omitted). An empty kinds slice means every kind.
	BackupUnitsJSON(ctx context.Context, kinds []string) ([]byte, error)
	// BackupUnitBytes fetches one unit's bytes by content hash, plus its kind
	// and meta (JSON).
	BackupUnitBytes(ctx context.Context, contentHash string) (kind string, metaJSON []byte, data []byte, err error)
}

// OpaqueStateIdentityResolver binds generic persisted bytes to the exact
// independently instantiated artifact and node. The guest never supplies or
// overrides these identity fields.
type OpaqueStateIdentityResolver func(*modulert.Module) (artifactHash, nodeID string, err error)

// storageOpTimeout bounds each storage hostcall's access to the durable
// stores. Cap handlers have no ctx parameter, so a bounded background context
// is derived per operation.
const storageOpTimeout = 30 * time.Second

// NewStorageCapFactory returns a BridgeCapFactory serving the storage_*
// capability family against the kubo-native sdnstore.Store — the Phase-6
// reconnection of the deferred sdn-server storage capability
// (sdn-server/internal/modulert/caps/storage.go) to the new services.
//
// It is deliberately NOT a port of the sdn-server FlatSQLStore/ingest surface:
// it targets sdnstore's (source, 3-letter type) contract directly. A module
// stores a record by (source, type) and reads it back by (source, type); the
// per-source SQL shadow table is exposed for read-only queries.
//
// All four storage_* manifest capabilities (query/write/adapter/ingest) map to
// the single "storage" hostcall prefix (modulert.capPrefixFromName), so this
// one handler serves whichever the module declared. Least-privilege is
// preserved by re-checking the SPECIFIC grant per operation against the calling
// bridge (defense in depth): storage_write does not imply read access and vice
// versa — exactly as the sdn-server handler did. The bridge is only granted a
// capability after modulert's operator capability-policy gate has approved it
// (fail closed, keyed by module content hash), so an unapproved module never
// reaches these operations with a grant.
//
// Supported operations:
//
//	storage.write   — {"source":"...","type":"OMM","data":"<base64 FlatBuffer>"}   (requires storage_write) -> {"cid":"..."}
//	storage.read    — {"source":"...","type":"OMM"}                                (requires storage_query) -> {"records":["<base64>",...]}
//	storage.sources — {"type":"OMM"}                                               (requires storage_query) -> {"sources":["..."]}
//	storage.query   — {"source":"...","type":"OMM","sql":"SELECT ..."}             (requires storage_query) -> {"stream_b64":"...","records":N}
//	storage.ingest_with_source — {"schema":"SPW.fbs","source_name":"...","records":{"$bin":0},...}  (requires storage_ingest) -> {"inserted":N,"cids":[...]}
//	storage.adapter.list_units — {"kinds":["module_wasm",...]}                     (requires storage_adapter) -> {"units":[{"contentHash","kind","size","meta"}]}
//	storage.adapter.get_unit   — {"contentHash":"<hex>"}                           (requires storage_adapter) -> {"contentHash","kind","bytes_b64","meta"}
func NewStorageCapFactory(store *sdnstore.Store, fallbackSource string) modulert.BridgeCapFactory {
	return NewStorageCapFactoryWithSource(store, fallbackSource, nil)
}

// NewStorageCapFactoryWithSource is NewStorageCapFactory plus the node-side
// backup-source read surface behind the repurposed storage_adapter capability
// (spec A.4): when backupSource is non-nil, storage.adapter.list_units and
// storage.adapter.get_unit enumerate + fetch the node's backup units for a
// backup flow. When nil those ops fail closed (the node has no backup source).
func NewStorageCapFactoryWithSource(store *sdnstore.Store, fallbackSource string, backupSource BackupReadSurface) modulert.BridgeCapFactory {
	return NewStorageCapFactoryWithOpaqueState(store, fallbackSource, backupSource, nil, nil)
}

// NewStorageCapFactoryWithOpaqueState adds schema-blind binary persistence to
// the existing storage_adapter capability. Existing backup adapter operations
// remain unchanged.
func NewStorageCapFactoryWithOpaqueState(store *sdnstore.Store, fallbackSource string, backupSource BackupReadSurface, opaqueState *OpaqueStateStore, resolveIdentity OpaqueStateIdentityResolver) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		source := strings.TrimSpace(fallbackSource)
		if mod != nil {
			if id := strings.TrimSpace(mod.ID()); id != "" && id != "unknown-module" {
				source = id
			}
		}
		s := &storageCapAdapter{
			store:           store,
			fallbackSource:  source,
			bridge:          bridge,
			backupSource:    backupSource,
			opaqueState:     opaqueState,
			module:          mod,
			resolveIdentity: resolveIdentity,
		}
		return s.handle
	}
}

type storageCapAdapter struct {
	store *sdnstore.Store
	// fallbackSource attributes a write whose payload omits "source" — the
	// calling module's plugin id when known (the natural (source, type)
	// routing key for module-authored records).
	fallbackSource string
	// bridge is the calling instance's hostcall bridge; its granted set is the
	// authority for the per-operation capability checks below. May be nil on
	// legacy provisioning paths, in which case every gated op fails closed.
	bridge *modulert.HostBridge
	// backupSource is the node-side backup-source read surface exposed by the
	// repurposed storage_adapter capability. Nil => storage.adapter.* fail closed.
	backupSource BackupReadSurface
	// opaqueState stores bytes without interpreting their schema or contents.
	opaqueState     *OpaqueStateStore
	artifactHash    string
	nodeID          string
	identityErr     error
	module          *modulert.Module
	resolveIdentity OpaqueStateIdentityResolver
}

func (s *storageCapAdapter) has(cap string) bool {
	return s.bridge != nil && s.bridge.HasCapability(cap)
}

func (s *storageCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	var p map[string]interface{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &p)
	}
	str := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			return strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), storageOpTimeout)
	defer cancel()

	switch operation {
	case "storage.write":
		// POLICY (least privilege): write-tier gate. storage_query does NOT
		// imply write.
		if !s.has("storage_write") {
			return errCapJSON("storage.write requires the storage_write capability grant"), nil
		}
		sdsType := str("type")
		if sdsType == "" {
			return errCapJSON("missing type"), nil
		}
		source := str("source")
		if source == "" {
			source = s.fallbackSource
		}
		if source == "" {
			return errCapJSON("missing source (no payload source and no module attribution)"), nil
		}
		raw := decodeBase64Cap(str("data"))
		if len(raw) == 0 {
			return errCapJSON("data missing or not valid base64 FlatBuffer bytes"), nil
		}
		c, err := s.store.Store(ctx, source, sdsType, raw)
		if err != nil {
			return errCapJSON("write failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]string{"cid": c.String(), "source": source, "type": strings.ToUpper(sdsType)}), nil

	case "storage.read":
		// POLICY: read-tier gate.
		if !s.has("storage_query") {
			return errCapJSON("storage.read requires the storage_query capability grant"), nil
		}
		sdsType := str("type")
		if sdsType == "" {
			return errCapJSON("missing type"), nil
		}
		source := str("source")
		if source == "" {
			return errCapJSON("missing source"), nil
		}
		recs, err := s.store.ReadBySourceType(ctx, source, sdsType)
		if err != nil {
			return errCapJSON("read failed: " + err.Error()), nil
		}
		out := make([]string, len(recs))
		for i, r := range recs {
			out[i] = encodeBase64Cap(r)
		}
		return okCapJSON(map[string]interface{}{"records": out, "count": len(out)}), nil

	case "storage.sources":
		if !s.has("storage_query") {
			return errCapJSON("storage.sources requires the storage_query capability grant"), nil
		}
		sdsType := str("type")
		if sdsType == "" {
			return errCapJSON("missing type"), nil
		}
		sources, err := s.store.Sources(ctx, sdsType)
		if err != nil {
			return errCapJSON("sources failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"sources": sources}), nil

	case "storage.query":
		if !s.has("storage_query") {
			return errCapJSON("storage.query requires the storage_query capability grant"), nil
		}
		sdsType := str("type")
		source := str("source")
		sqlText := str("sql")
		if sdsType == "" || source == "" || sqlText == "" {
			return errCapJSON("storage.query requires source, type and sql"), nil
		}
		stream, err := s.store.Query(ctx, source, sdsType, sqlText)
		if err != nil {
			return errCapJSON("query failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"stream_b64": encodeBase64Cap(stream.Bytes)}), nil

	case "storage.ingest_with_source":
		// POLICY: dedicated ingest-tier gate (the provenance/batch flow-ingest
		// sink the hostcap/storage-ingest module calls). storage_query/write do
		// NOT imply it.
		if !s.has("storage_ingest") {
			return errCapJSON("storage.ingest_with_source requires the storage_ingest capability grant"), nil
		}
		return s.ingestWithSource(ctx, p)

	case "storage.adapter.list_units":
		// POLICY: the repurposed storage_adapter grant (spec A.4) gates the
		// node-side backup-source read surface. storage_query/write do NOT imply
		// it. Fail closed when no backup source is wired.
		if !s.has("storage_adapter") {
			return errCapJSON("storage.adapter.list_units requires the storage_adapter capability grant"), nil
		}
		return s.adapterListUnits(ctx, payload), nil

	case "storage.adapter.get_unit":
		if !s.has("storage_adapter") {
			return errCapJSON("storage.adapter.get_unit requires the storage_adapter capability grant"), nil
		}
		return s.adapterGetUnit(ctx, str("contentHash")), nil

	case "storage.adapter.opaque.read":
		if denied := s.requireOpaqueState(); denied != nil {
			return denied, nil
		}
		value, found, err := s.opaqueState.Read(ctx, s.opaqueScope(str("namespace")), str("key"))
		if err != nil {
			return errCapJSON("storage.adapter.opaque.read failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"found": found, "bytes_b64": base64.StdEncoding.EncodeToString(value)}), nil

	case "storage.adapter.opaque.append", "storage.adapter.opaque.replace":
		if denied := s.requireOpaqueState(); denied != nil {
			return denied, nil
		}
		encoded, present := p["data"].(string)
		if !present {
			return errCapJSON(operation + " requires base64 data"), nil
		}
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return errCapJSON(operation + " data is not valid base64"), nil
		}
		if operation == "storage.adapter.opaque.append" {
			err = s.opaqueState.Append(ctx, s.opaqueScope(str("namespace")), str("key"), value)
		} else {
			err = s.opaqueState.Replace(ctx, s.opaqueScope(str("namespace")), str("key"), value)
		}
		if err != nil {
			return errCapJSON(operation + " failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"stored_bytes": len(value)}), nil

	case "storage.adapter.opaque.list":
		if denied := s.requireOpaqueState(); denied != nil {
			return denied, nil
		}
		keys, err := s.opaqueState.List(ctx, s.opaqueScope(str("namespace")))
		if err != nil {
			return errCapJSON("storage.adapter.opaque.list failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"keys": keys}), nil

	case "storage.adapter.opaque.sync":
		if denied := s.requireOpaqueState(); denied != nil {
			return denied, nil
		}
		if err := s.opaqueState.Sync(ctx, s.opaqueScope(str("namespace"))); err != nil {
			return errCapJSON("storage.adapter.opaque.sync failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"synced": true}), nil

	case "storage.adapter.opaque.delete":
		if denied := s.requireOpaqueState(); denied != nil {
			return denied, nil
		}
		if err := s.opaqueState.Delete(ctx, s.opaqueScope(str("namespace")), str("key")); err != nil {
			return errCapJSON("storage.adapter.opaque.delete failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{"deleted": true}), nil

	default:
		return errCapJSON(fmt.Sprintf("unknown storage operation: %s", operation)), nil
	}
}

func (s *storageCapAdapter) requireOpaqueState() []byte {
	if !s.has("storage_adapter") {
		return errCapJSON("opaque state requires the storage_adapter capability grant")
	}
	if s.opaqueState == nil {
		return errCapJSON("opaque state is not configured")
	}
	if s.resolveIdentity == nil {
		s.identityErr = fmt.Errorf("opaque state identity resolver is not configured")
		s.artifactHash, s.nodeID = "", ""
	} else {
		// Resolve only after Module.Load has published immutable identity. This
		// handler runs under the module invocation lock, so the resolver must use
		// Module.BoundIdentity rather than the locking dashboard accessors.
		s.artifactHash, s.nodeID, s.identityErr = s.resolveIdentity(s.module)
	}
	if s.identityErr != nil || s.artifactHash == "" || s.nodeID == "" {
		message := "opaque state identity is unavailable"
		if s.identityErr != nil {
			message += ": " + s.identityErr.Error()
		}
		return errCapJSON(message)
	}
	return nil
}

func (s *storageCapAdapter) opaqueScope(namespace string) OpaqueStateScope {
	return OpaqueStateScope{ArtifactHash: s.artifactHash, NodeID: s.nodeID, Namespace: namespace}
}

// adapterListUnits enumerates the node's backup units (modules, flows, records,
// on-disk config) for a backup flow — the READ half of the repurposed
// storage_adapter capability (spec A.4). Bytes are omitted here (fetched per
// unit via get_unit); only {contentHash, kind, size, meta} descriptors return.
func (s *storageCapAdapter) adapterListUnits(ctx context.Context, payload []byte) []byte {
	if s.backupSource == nil {
		return errCapJSON("storage.adapter.list_units: this node has no backup source configured")
	}
	var req struct {
		Kinds []string `json:"kinds"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &req)
	}
	unitsJSON, err := s.backupSource.BackupUnitsJSON(ctx, req.Kinds)
	if err != nil {
		return errCapJSON("storage.adapter.list_units failed: " + err.Error())
	}
	return okCapJSON(map[string]interface{}{"units": json.RawMessage(unitsJSON)})
}

// adapterGetUnit fetches one backup unit's bytes by content hash — the FETCH
// half of the repurposed storage_adapter capability (spec A.4).
func (s *storageCapAdapter) adapterGetUnit(ctx context.Context, contentHash string) []byte {
	if s.backupSource == nil {
		return errCapJSON("storage.adapter.get_unit: this node has no backup source configured")
	}
	if strings.TrimSpace(contentHash) == "" {
		return errCapJSON("storage.adapter.get_unit requires a contentHash")
	}
	kind, metaJSON, data, err := s.backupSource.BackupUnitBytes(ctx, contentHash)
	if err != nil {
		return errCapJSON("storage.adapter.get_unit failed: " + err.Error())
	}
	return okCapJSON(map[string]interface{}{
		"contentHash": contentHash,
		"kind":        kind,
		"bytes_b64":   encodeBase64Cap(data),
		"meta":        json.RawMessage(metaJSON),
	})
}

// ingestWithSource persists a size-prefixed FlatBuffer record stream authored
// by a parser node, keyed by (source, 3-letter type). It is the kubo-native
// core of the sdn-server storage.ingest_with_source cap
// (sdn-server/internal/modulert/caps/storage.go): it takes the ingest DECISIONS
// the wasm parser already made — schema, source_name attribution, batch id —
// and lands each record in sdnstore. Records land by (source, type); the
// content-addressed store dedups a replayed batch by construction, so no
// reconcile pass is needed.
//
// The provenance/raw-archive/reconcile-mode richness of the sdn-server
// FlatSQLStore ingest surface is deliberately NOT ported: sdnstore's contract
// is (source, 3-letter type) records. The meta's archive/provenance/
// reconcile fields are accepted and ignored (documented divergence).
//
// The payload here is the hostcall envelope's meta JSON with binary segments
// already inlined by the bridge (attachHostcallBinaryRefs): the "records" field
// is the base64 of segment 0 (the size-prefixed record stream), and an optional
// "archive.raw" is the base64 of segment 1 (ignored — no raw archive in kubo).
func (s *storageCapAdapter) ingestWithSource(ctx context.Context, meta map[string]interface{}) ([]byte, error) {
	get := func(key string) string {
		if v, ok := meta[key]; ok {
			return strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		return ""
	}
	schema := get("schema")
	sdsType := sdsTypeFromSchema(schema)
	if sdsType == "" {
		return errCapJSON("storage.ingest_with_source: meta missing a usable schema (want e.g. \"SPW.fbs\")"), nil
	}
	source := get("source_name")
	if source == "" {
		source = get("source")
	}
	if source == "" {
		source = s.fallbackSource
	}
	if source == "" {
		return errCapJSON("storage.ingest_with_source: meta missing source_name"), nil
	}
	recordsB64, _ := meta["records"].(string)
	stream := decodeBase64Cap(recordsB64)
	if len(stream) == 0 {
		return errCapJSON("storage.ingest_with_source: meta carries no records stream"), nil
	}
	records, err := splitSizePrefixedStream(stream)
	if err != nil {
		return errCapJSON("storage.ingest_with_source: " + err.Error()), nil
	}
	if len(records) == 0 {
		return errCapJSON("storage.ingest_with_source: record stream decoded to zero records"), nil
	}

	cids := make([]string, 0, len(records))
	for i, rec := range records {
		c, err := s.store.Store(ctx, source, sdsType, rec)
		if err != nil {
			return errCapJSON(fmt.Sprintf("storage.ingest_with_source: store record %d/%d: %v", i+1, len(records), err)), nil
		}
		cids = append(cids, c.String())
	}
	return okCapJSON(map[string]interface{}{
		"ok":       true,
		"schema":   schema,
		"type":     sdsType,
		"source":   source,
		"batch_id": get("batch_id"),
		"inserted": len(cids),
		"cids":     cids,
	}), nil
}

// sdsTypeFromSchema maps a parser schema name ("SPW.fbs", "OMM.fbs", or a bare
// "SPW") to the 3-letter SDS type sdnstore keys records by. Returns "" when the
// schema does not yield exactly 3 leading letters.
func sdsTypeFromSchema(schema string) string {
	s := strings.ToUpper(strings.TrimSpace(schema))
	s = strings.TrimSuffix(s, ".FBS")
	if len(s) < 3 {
		return ""
	}
	s = s[:3]
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return ""
		}
	}
	return s
}

// splitSizePrefixedStream splits a [u32 len][record bytes]... stream (the frame
// the wasm parser emits via append_size_prefixed) into individual FlatBuffer
// records. Each record is the raw FlatBuffer WITHOUT a size prefix — exactly
// what sdnstore.Store content-addresses.
func splitSizePrefixedStream(stream []byte) ([][]byte, error) {
	var out [][]byte
	off := 0
	for off < len(stream) {
		if off+4 > len(stream) {
			return nil, fmt.Errorf("record stream truncated in length prefix at offset %d", off)
		}
		n := int(binary.LittleEndian.Uint32(stream[off:]))
		off += 4
		if n <= 0 || off+n > len(stream) {
			return nil, fmt.Errorf("record stream length %d at offset %d exceeds bounds (%d)", n, off-4, len(stream))
		}
		rec := make([]byte, n)
		copy(rec, stream[off:off+n])
		out = append(out, rec)
		off += n
	}
	return out, nil
}
