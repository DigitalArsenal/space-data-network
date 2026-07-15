package sdnservices

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

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
func NewStorageCapFactory(store *sdnstore.Store, fallbackSource string) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		source := strings.TrimSpace(fallbackSource)
		if mod != nil {
			if id := strings.TrimSpace(mod.ID()); id != "" && id != "unknown-module" {
				source = id
			}
		}
		s := &storageCapAdapter{store: store, fallbackSource: source, bridge: bridge}
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

	default:
		return errCapJSON(fmt.Sprintf("unknown storage operation: %s", operation)), nil
	}
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
// is (source, 3-letter type) records, and that is what the CelesTrak ingest and
// supplemental-OMM OD flows need to land. The meta's archive/provenance/
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
