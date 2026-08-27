package caps

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/ingest"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// canonicalStoredSchemaName is the schema name a WRITE persists under.
//
// The capability's own documented shape accepts a bare standard code
// ("OMM") as readily as a file name ("OMM.fbs"), and READERS normalize:
// engineRoutedSchemaFor normalizes before routing into the engine, and
// normalizeDatasetPublicationSchema normalizes a publication request. The
// WRITE path did not, so whatever spelling a module happened to use was
// persisted verbatim into sdn_record_index.schema_name and
// sdn_record_source_tags.schema_name.
//
// That split is not cosmetic; it was measured live on host-02 2026-08-26 and
// it silently severed a whole dataset from the network. The cell-tower ingest
// module stamps "TBS" (its kSchema), so 258,125 $TBS rows landed with
// schema_name "TBS" — visible in /api/v1/catalog, visible to the engine
// (which normalizes), and INVISIBLE to every export: QueryIndexedRecords
// joins `sdn_record_index idx ON idx.schema_name = ?` with the normalized
// "TBS.fbs", matching nothing. Every publish attempt answered "no records
// match export query" while the store held a quarter of a million rows, and
// the control — RFB.fbs, stamped canonically — published in about a second.
// No $TBS batch could ever be exported, announced or replicated, so the
// consumer node stayed at records:0 no matter how much the producer ingested.
//
// Canonicalizing HERE, at the connector boundary, is the fix that cannot be
// forgotten by the next module: one spelling is persisted, and it is the one
// every reader already normalizes to. An input that cannot be a schema name
// at all is passed through unchanged so the store's own validation refuses it
// with its own error, rather than this helper inventing one.
func canonicalStoredSchemaName(schema string) string {
	if canonical := sds.NormalizeSchemaFileName(schema); canonical != "" {
		return canonical
	}
	return schema
}

// NewStorageCapFactory returns a BridgeCapFactory for "storage_query" and
// "storage_write" capabilities. Both capabilities share the same handler —
// the operation prefix distinguishes them ("storage.query" vs
// "storage.write") — but every operation independently re-checks its own
// capability grant against the calling bridge (least privilege / defense in
// depth): storage_query does NOT imply storage_write and vice versa, even
// though both land in this one handler. See the per-case "POLICY" comments
// in handle() below.
//
// Supported operations:
//
//	storage.query  — {"schema":"OMM","day":"2026-04-07","entity_id":"...","norad_cat_id":12345,"limit":100}  (requires storage_query)
//	storage.write  — {"schema":"OMM","data":"base64..."}  (data is raw FlatBuffer bytes) (requires storage_write)
//	storage.delete — {"cid":"sha256hex..."}  (requires storage_write)
func NewStorageCapFactory(store *storage.FlatSQLStore) modulert.BridgeCapFactory {
	return NewStorageCapFactoryWithProducer(store, "")
}

// NewStorageCapFactoryWithProducer is NewStorageCapFactory with an explicit
// fallback producer identity. Writes are attributed to the calling module's
// plugin id (the natural (producer, standard) routing key for module-authored
// records); the fallback covers modules whose manifest is unavailable.
func NewStorageCapFactoryWithProducer(store *storage.FlatSQLStore, fallbackProducer string) modulert.BridgeCapFactory {
	return NewStorageCapFactoryWithOptions(store, StorageCapOptions{FallbackProducer: fallbackProducer})
}

// StorageCapOptions configures host-side resource policy for the storage
// capability adapter. These are resource limits and filesystem roots — never
// request-level decisions (those live in the wasm modules).
type StorageCapOptions struct {
	// FallbackProducer attributes writes when the module manifest is
	// unavailable (compiled flow bundles use the plugin id when present).
	FallbackProducer string

	// NodePeerID is THIS node's libp2p identity, stamped as the
	// producer_peer_id of every record ingested through
	// storage.ingest_with_source.
	//
	// Provenance, not decoration. A record's producer is the NODE that pulled
	// it, and only the host knows that identity — a module is not asked for
	// it and cannot supply it, so it cannot claim to be another node. Without
	// this the flow-ingest path left producer_peer_id empty, the store
	// back-filled it with provider_id (flatsql.go:1984), and the $APPS feed
	// then correctly refused the row as "not a peer" (apps.go:435). The
	// consequence was total: a receiving node could import a producer's shard
	// and STILL show nothing under via:"pubsub", because every imported row
	// carried a provider name where a peer id belonged. Empty is tolerated
	// (the old fallback stands) so an unwired caller degrades, never panics.
	NodePeerID string

	// RawRoot is the raw-archive root directory used by
	// storage.ingest_with_source archive/provenance segments (the same
	// layout the in-daemon ingest runner writes: <raw>/<source>/<day>/<name>
	// and <raw>/provenance/<source>/<ts>.json). Empty disables archiving —
	// archive requests then fail loudly rather than silently dropping.
	RawRoot string

	// MinFreeDiskBytes is the ingest disk guardrail (parity with the ingest
	// runner's requireFreeDisk): storage.ingest_with_source refuses to write
	// when the store or raw-archive filesystem has less free space.
	// <= 0 uses the runner's default (5 GiB).
	MinFreeDiskBytes int64

	// QueryCaps bounds every storage.query_sandboxed execution (gateway
	// loop G.5): statement timeout + row/byte caps, enforced IN-WASM by the
	// engine. Zero fields fall back to the built-in defense defaults
	// (5 s / 200K rows / 128 MiB) — a missing config never means unlimited.
	QueryCaps flatsqlrt.SandboxCaps
}

// Built-in defense defaults for QueryCaps (mirrors
// config.DefaultGatewayQuery*).
const (
	defaultQueryTimeout  = 5 * time.Second
	defaultQueryMaxRows  = 200000
	defaultQueryMaxBytes = 128 << 20
)

// effectiveQueryCaps applies the defense defaults to unset fields.
func effectiveQueryCaps(caps flatsqlrt.SandboxCaps) flatsqlrt.SandboxCaps {
	if caps.Timeout <= 0 {
		caps.Timeout = defaultQueryTimeout
	}
	if caps.MaxRows == 0 {
		caps.MaxRows = defaultQueryMaxRows
	}
	if caps.MaxBytes == 0 {
		caps.MaxBytes = defaultQueryMaxBytes
	}
	return caps
}

// NewStorageCapFactoryWithOptions is NewStorageCapFactory with explicit
// host-policy options (loop C.8 flow ingest).
func NewStorageCapFactoryWithOptions(store *storage.FlatSQLStore, opts StorageCapOptions) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		producer := strings.TrimSpace(opts.FallbackProducer)
		if mod != nil {
			if id := strings.TrimSpace(mod.ID()); id != "" && id != "unknown-module" {
				producer = id
			}
		} else if id := strings.TrimSpace(bridge.ProducerID()); id != "" {
			// Flow bundles provision with mod == nil; the bridge carries the
			// flow's program id. Without this a flow-ingested record was
			// stamped with an EMPTY producer ("module:"), which is a
			// provenance defect, not a cosmetic one.
			producer = id
		}
		s := &storageCapAdapter{
			store:            store,
			producerID:       producer,
			nodePeerID:       strings.TrimSpace(opts.NodePeerID),
			bridge:           bridge,
			rawRoot:          strings.TrimSpace(opts.RawRoot),
			minFreeDiskBytes: opts.MinFreeDiskBytes,
			queryCaps:        effectiveQueryCaps(opts.QueryCaps),
		}
		return s.handle
	}
}

type storageCapAdapter struct {
	store      *storage.FlatSQLStore
	producerID string
	// nodePeerID is this node's libp2p identity — the producer of every
	// record ingested through this adapter. See StorageCapOptions.NodePeerID.
	nodePeerID string
	// bridge is the calling instance's hostcall bridge — the registry for
	// "deliver":"ref" body references (loop C.5c). May be nil on legacy
	// provisioning paths; ref delivery then degrades to byte delivery.
	bridge *modulert.HostBridge
	// rawRoot / minFreeDiskBytes: host resource policy for
	// storage.ingest_with_source (see StorageCapOptions).
	rawRoot          string
	minFreeDiskBytes int64
	// queryCaps: per-execution resource caps for storage.query_sandboxed
	// (defaults already applied).
	queryCaps flatsqlrt.SandboxCaps
}

func (s *storageCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	var p map[string]interface{}
	if len(payload) > 0 {
		json.Unmarshal(payload, &p) //nolint:errcheck
	}
	str := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	switch operation {
	case "storage.query":
		// POLICY (least privilege, defense-in-depth): all four storage_*
		// manifest capabilities (query/write/adapter/ingest) share this one
		// hostcall handler under the "storage" prefix (capPrefixFromName in
		// module.go) — the handler itself is the ONLY place that can tell
		// them apart. storage_write/storage_ingest do NOT imply read access:
		// a module approved only for writing must not silently gain query
		// access, so this checks storage_query specifically rather than
		// "any storage_* grant".
		if s.bridge == nil || !s.bridge.HasCapability("storage_query") {
			return refuseCapJSON("storage.query", "requires the storage_query capability grant"), nil
		}
		schema := str("schema")
		if schema == "" {
			return errCapJSON("missing schema"), nil
		}
		day := str("day")
		entityID := str("entity_id")

		var noradCatID *uint32
		if ncid := str("norad_cat_id"); ncid != "" {
			var v uint32
			if _, err := fmt.Sscanf(ncid, "%d", &v); err == nil {
				noradCatID = &v
			}
		}

		limit := 100
		if lv := str("limit"); lv != "" {
			var lvi int
			if _, err := fmt.Sscanf(lv, "%d", &lvi); err == nil && lvi > 0 {
				limit = lvi
			}
		}

		records, err := s.store.QueryByIndexedFields(schema, day, noradCatID, entityID, limit)
		if err != nil {
			return errCapJSON("query failed: " + err.Error()), nil
		}
		result, _ := json.Marshal(records)
		return okCapJSON(json.RawMessage(result)), nil

	case "storage.write":
		// POLICY: write-tier gate. storage_query does NOT imply write —
		// see the storage.query case above for why the four storage_*
		// capabilities must be checked individually despite sharing one
		// handler.
		if s.bridge == nil || !s.bridge.HasCapability("storage_write") {
			return refuseCapJSON("storage.write", "requires the storage_write capability grant"), nil
		}
		schema := canonicalStoredSchemaName(str("schema"))
		if schema == "" {
			return errCapJSON("missing schema"), nil
		}
		data := str("data")
		if data == "" {
			return errCapJSON("missing data"), nil
		}
		// data is expected to be a hex CID or raw bytes — store as FlatBuffer record
		// The store's PublishRecord takes raw FlatBuffer bytes.
		// For now accept base64-encoded bytes via the "data" field.
		rawBytes := decodeBase64Cap(data)
		if len(rawBytes) == 0 {
			return errCapJSON("data could not be decoded"), nil
		}
		// ADMIT-POINT CHECK: storage.write takes ONE record. A whole
		// size-prefixed SHARD reaching here would sail past
		// engineRecordPayload (storage/engine_records.go:137 only strips a
		// prefix that accounts for the WHOLE buffer) and be handed to
		// IngestOne unsplit — one malformed row instead of N records, with
		// no error anywhere. Refuse it and name the capability that exists
		// for exactly this payload: storage.ingest_with_source demuxes the
		// stream before storing (see handleIngestWithSource). The browser
		// runtime refuses the same shape at the same boundary
		// (sdn-js FlatSQLEngineRecordStore.store → storeStream, 2.0.18).
		if frames, isStream := multiFrameSizePrefixedStream(rawBytes); isStream {
			return errCapJSON(fmt.Sprintf(
				"storage.write admits ONE record, but data is an aligned size-prefixed stream of %d frames (%d bytes); use storage.ingest_with_source, which splits the stream before storing",
				frames, len(rawBytes))), nil
		}
		cid, err := s.store.Store(schema, rawBytes, s.producerID, nil)
		if err != nil {
			return errCapJSON("write failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]string{"cid": cid}), nil

	case "storage.delete":
		// POLICY: destructive op — same write-tier gate as storage.write.
		// There is no separate "storage_delete" manifest capability (see
		// manifest.go / capability_policy.go sensitiveCapabilities);
		// storage_write is the write/delete tier.
		if s.bridge == nil || !s.bridge.HasCapability("storage_write") {
			return refuseCapJSON("storage.delete", "requires the storage_write capability grant"), nil
		}
		schema := str("schema")
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		if err := s.store.Delete(schema, cid); err != nil {
			return errCapJSON("delete failed: " + err.Error()), nil
		}
		return okCapJSON(true), nil

	case "storage.ingest_with_source":
		return logIngestRefusal(s.handleIngestWithSource(p, str)), nil

	// Engine-native ops (loop C.1): results are ALIGNED size-prefixed
	// FlatBuffer streams delivered as raw binary envelope segments (the
	// handler returns a PRE-ENCODED envelope — no base64/JSON round-trip
	// anywhere on the host side, loop C.5).
	case "storage.flatsql_query_stream":
		// POLICY: read-tier gate, same rationale as storage.query above.
		if s.bridge == nil || !s.bridge.HasCapability("storage_query") {
			return refuseCapJSON("storage.flatsql_query_stream", "requires the storage_query capability grant"), nil
		}
		sqlText := str("sql")
		if sqlText == "" {
			return errCapJSON("missing sql"), nil
		}
		params, err := decodeTaggedParams(p["params"])
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		stream, err := s.store.QueryRawStream(sqlText, params...)
		if err != nil {
			return errCapJSON("flatsql query failed: " + err.Error()), nil
		}
		return s.streamResult(stream, str("deliver")), nil

	case "storage.flatsql_epoch_stream":
		// POLICY: read-tier gate, same rationale as storage.query above.
		if s.bridge == nil || !s.bridge.HasCapability("storage_query") {
			return refuseCapJSON("storage.flatsql_epoch_stream", "requires the storage_query capability grant"), nil
		}
		schema := str("schema")
		if schema == "" {
			schema = "OMM.fbs"
		}
		profile := str("profile")
		if profile == "" {
			profile = "nearest"
		}
		var epoch float64
		if v, ok := p["epoch"].(float64); ok {
			epoch = v
		}
		limit := 50000
		if v, ok := p["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}
		stream, err := s.store.QueryEpochRawStream(schema, str("source"), profile, epoch, limit)
		if err != nil {
			return errCapJSON("epoch stream failed: " + err.Error()), nil
		}
		return s.streamResult(stream, str("deliver")), nil

	// Sandboxed public query (gateway loop G.5): the /api/v1/query flow's
	// ONLY data path. Policy-mediated read-only — the engine enforces the
	// sandbox IN-WASM (authorizer over record tables/shadows/views,
	// single-statement SELECT, statement timeout, row/byte caps); the host
	// contributes exactly two things: the storage_query grant check and the
	// configured caps. Sandbox rejections come back as
	// {"ok":false,"error":{"message","sandbox":"<code>"}} so the wasm flow
	// maps them to HTTP statuses.
	case "storage.query_sandboxed":
		return s.handleQuerySandboxed(p, str), nil

	case "storage.query_surface":
		return s.handleQuerySurface(), nil

	case "storage.flatsql_cache_key":
		sqlText := str("sql")
		if sqlText == "" {
			return errCapJSON("missing sql"), nil
		}
		params, err := decodeTaggedParams(p["params"])
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		var projection []string
		if raw, ok := p["projection"].([]interface{}); ok {
			for _, entry := range raw {
				projection = append(projection, fmt.Sprintf("%v", entry))
			}
		}
		key, err := s.store.ResponseArtifactCacheKey(str("schema_name"), str("schema_version"), sqlText,
			flatsqlrt.ResponseArtifactKeyOptions{
				Format:          str("format"),
				PublishEventKey: str("publish_event_key"),
				Projection:      projection,
				Params:          params,
			})
		if err != nil {
			return errCapJSON("cache key failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]string{"key": key}), nil

	default:
		return errCapJSON(fmt.Sprintf("unknown storage operation: %s", operation)), nil
	}
}

// errSandboxCapJSON is errCapJSON plus the typed sandbox rejection code —
// the wasm flow maps codes to HTTP statuses (never string-matches messages).
func errSandboxCapJSON(code, msg string) []byte {
	r, _ := json.Marshal(map[string]interface{}{
		"ok":    false,
		"error": map[string]string{"message": msg, "sandbox": code},
	})
	return r
}

// handleQuerySandboxed executes one untrusted SELECT under the engine
// sandbox. Payload:
//
//	{
//	  "sql": "SELECT ...",                      (required)
//	  "params": [{"t":"i64|f64|str|bool|null|bytes","v":...}],
//	  "want": "stream" | "rows" | "auto",       (default "stream")
//	  "deliver": "ref"                          (stream results only)
//	}
//
// want=stream: all result cells must be BLOB — the aligned frame stream is
// delivered like storage.flatsql_query_stream (binary segment or body ref).
// want=rows: the engine assembles bare-array JSON IN-WASM (column names
// verbatim — schema-exact capitalization); delivered as a binary segment.
// want=auto: stream first, falling back to rows when the projection is not
// BLOB-only (one extra bounded execution; every execution wears the caps).
func (s *storageCapAdapter) handleQuerySandboxed(p map[string]interface{}, str func(string) string) []byte {
	if s.bridge == nil || !s.bridge.HasCapability("storage_query") {
		return refuseCapJSON("storage.query_sandboxed", "requires the storage_query capability grant")
	}
	sqlText := str("sql")
	if sqlText == "" {
		return errCapJSON("missing sql")
	}
	params, err := decodeTaggedParams(p["params"])
	if err != nil {
		return errCapJSON(err.Error())
	}
	want := str("want")
	if want == "" {
		want = "stream"
	}

	failed := func(err error) []byte {
		if se, ok := flatsqlrt.AsSandboxError(err); ok {
			return errSandboxCapJSON(se.Code, se.Message)
		}
		return errCapJSON("sandboxed query failed: " + err.Error())
	}

	switch want {
	case "stream", "auto":
		stream, err := s.store.QuerySandboxedStream(sqlText, s.queryCaps, params...)
		if err != nil {
			if se, ok := flatsqlrt.AsSandboxError(err); ok &&
				want == "auto" && se.Code == flatsqlrt.SandboxCodeNotRecordStream {
				break // projection result — fall through to rows
			}
			return failed(err)
		}
		return s.streamResult(stream, str("deliver"))
	case "rows":
		// handled below
	default:
		return errCapJSON("unknown want: " + want + " (stream | rows | auto)")
	}

	payload, rows, cols, err := s.store.QuerySandboxedJSON(sqlText, s.queryCaps, params...)
	if err != nil {
		return failed(err)
	}
	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"kind":    "rows",
			"rows":    rows,
			"columns": cols,
			"json":    map[string]interface{}{"$bin": 0},
		},
	}, [][]byte{payload})
}

// handleQuerySurface reports the queryable public surface (tables / views /
// columns straight from the live engine — no hand-maintained list) plus the
// effective caps, so /api/v1/query is self-documenting.
func (s *storageCapAdapter) handleQuerySurface() []byte {
	if s.bridge == nil || !s.bridge.HasCapability("storage_query") {
		return refuseCapJSON("storage.query_surface", "requires the storage_query capability grant")
	}
	surface, err := s.store.PublicQuerySurface()
	if err != nil {
		return errCapJSON("query surface failed: " + err.Error())
	}
	return okCapJSON(map[string]interface{}{
		"tables": surface,
		"caps": map[string]interface{}{
			"timeout_ms": int64(s.queryCaps.Timeout / time.Millisecond),
			"max_rows":   s.queryCaps.MaxRows,
			"max_bytes":  s.queryCaps.MaxBytes,
		},
	})
}

// defaultIngestMinFreeDiskBytes mirrors the ingest runner's disk guardrail
// default (internal/ingest defaultMinFreeDiskBytes).
const defaultIngestMinFreeDiskBytes = 5 * 1024 * 1024 * 1024

// capBool reads an optional boolean from a cap payload. JSON booleans arrive
// as bool; a module that spells the flag as a string still gets the obvious
// reading rather than a silent false.
func capBool(p map[string]interface{}, key string) bool {
	if p == nil {
		return false
	}
	switch v := p[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

// handleIngestWithSource is the provenance/batch-capable flow ingest op
// (loop C.8a): the WASM side (provider parser nodes) delivers a size-prefixed
// aligned FlatBuffer record stream plus full SourceTags attribution; the host
// performs ONLY resource-guarded persistence — disk guardrail, batch store
// with source tags, optional source-batch reconcile, optional raw-payload +
// provenance archiving under the ingest raw root. No parsing, no fetch, no
// scheduling decisions live here.
//
// POLICY: this op requires the dedicated "storage_ingest" capability grant
// (checked against the calling bridge), not just any storage_* capability —
// provider attribution is a heavier privilege than storage.write.
//
// Payload meta (records/raw/provenance are binary envelope segments,
// delivered by the bridge as base64 at their {"$bin":N} references):
//
//	{
//	  "schema": "OMM.fbs",
//	  "provider_id": "space-data-network-02",     (required)
//	  "source_name": "provider-gp",              (required)
//	  "source_url":  "https://...",               (optional)
//	  "batch_id":    "<sha256 of source payload>",(required)
//	  "content_key_id": "public",                 (optional)
//	  "license":     "CC-BY-SA-4.0",              (optional, SPDX id)
//	  "license_url": "https://...",               (optional)
//	  "citation":    "SatNOGS DB contributors",   (optional)
//	  "share_alike": true,                        (optional)
//	  "source_peer": "source:provider",          (optional)
//	  "records": {"$bin":0},                      (size-prefixed record stream)
//	  "reconcile": "none"|"duplicates"|"current", (default "duplicates")
//	  "archive": {"source":"provider","name":"catalog.csv","raw":{"$bin":1}},
//	  "provenance": {"source":"provider-gp","json":{"$bin":2}}
//	}
// logIngestRefusal writes a REFUSED batch down.
//
// The guarded-persistence answer travels back INTO the guest, and every node of
// a linked-direct flow runs inside the guest, so a refusal here used to leave
// the host with nothing to say beyond "the run landed no batch" (graph:
// sdn-cellular-ingest-lands-no-batch, where a refusal at exactly this boundary
// was invisible until the flow runtime started reading node status). The host
// already computed the reason; not writing it down was the whole defect. This
// logs the connector's own verdict and nothing about what the records mean.
func logIngestRefusal(response []byte) []byte {
	var envelope struct {
		OK    *bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return response
	}
	if envelope.OK != nil && !*envelope.OK && envelope.Error.Message != "" {
		log.Warnf("storage.ingest_with_source refused a batch: %s", envelope.Error.Message)
	}
	return response
}

func (s *storageCapAdapter) handleIngestWithSource(p map[string]interface{}, str func(string) string) []byte {
	if s.bridge == nil || !s.bridge.HasCapability("storage_ingest") {
		return refuseCapJSON("storage.ingest_with_source",
			"requires the storage_ingest capability grant")
	}

	schema := canonicalStoredSchemaName(str("schema"))
	if schema == "" {
		return errCapJSON("missing schema")
	}
	tags := storage.SourceTags{
		ProviderID:   str("provider_id"),
		SourceName:   str("source_name"),
		SourceURL:    str("source_url"),
		BatchID:      str("batch_id"),
		ContentKeyID: str("content_key_id"),
		// Licence terms of the retrieved source, as declared by the parser
		// node that fetched it. This is the ONLY authority for licence: the
		// host never infers it from config, because only the module knows
		// which upstream document it actually pulled. Carried through to
		// DPMSourceBatch LICENSE / LICENSE_URL / CITATION on publication.
		License:    str("license"),
		LicenseURL: str("license_url"),
		Citation:   str("citation"),
		ShareAlike: capBool(p, "share_alike"),
		// The HOST supplies the producer identity. It is deliberately not
		// read from the payload: a module must not be able to attribute its
		// writes to another node.
		ProducerPeerID: s.nodePeerID,
	}
	if strings.TrimSpace(tags.ProviderID) == "" || strings.TrimSpace(tags.SourceName) == "" || strings.TrimSpace(tags.BatchID) == "" {
		return errCapJSON("provider_id, source_name and batch_id are required for provenance attribution")
	}
	sourcePeer := str("source_peer")
	if sourcePeer == "" {
		sourcePeer = "module:" + s.producerID
	}

	recordsB64 := str("records")
	if recordsB64 == "" {
		return errCapJSON("missing records stream")
	}
	stream := decodeBase64Cap(recordsB64)
	records, err := splitSizePrefixedStream(stream)
	if err != nil {
		return errCapJSON("invalid records stream: " + err.Error())
	}
	if len(records) == 0 {
		return errCapJSON("records stream contains no records")
	}

	// Disk guardrail (parity with ingest.Runner.requireFreeDisk): refuse the
	// whole batch when the store or raw filesystem is under the floor.
	minFree := s.minFreeDiskBytes
	if minFree <= 0 {
		minFree = defaultIngestMinFreeDiskBytes
	}
	guardPaths := []string{filepath.Dir(s.store.Path())}
	if s.rawRoot != "" {
		guardPaths = append(guardPaths, s.rawRoot)
	}
	for _, path := range guardPaths {
		free, err := ingest.AvailableDiskBytes(existingDirOrParent(path))
		if err != nil {
			return refuseCapJSON("storage.ingest_with_source",
				"free-disk check failed for "+path+": "+err.Error())
		}
		if int64(free) < minFree {
			// This refusal discards a batch the node already paid a publisher
			// to fetch, so it is reported in the units an operator acts on.
			return refuseCapJSON("storage.ingest_with_source", fmt.Sprintf(
				"ingest requires at least %d free bytes at %s; only %d available "+
					"(%.1f GiB floor, %.1f GiB free) — schema %s, source %s/%s: the whole batch is being dropped",
				minFree, path, free,
				float64(minFree)/(1024*1024*1024), float64(free)/(1024*1024*1024),
				schema, tags.ProviderID, tags.SourceName))
		}
	}

	reconcile := str("reconcile")
	if reconcile == "" {
		reconcile = "duplicates"
	}
	switch reconcile {
	case "none", "duplicates", "current":
	default:
		return errCapJSON("unknown reconcile mode " + reconcile + " (none|duplicates|current)")
	}

	// Pre-ingest duplicate reconcile: replaying the SAME source batch (same
	// batch_id) must not double records — mirror the runner's
	// reconcile-before-ingest step.
	if reconcile != "none" {
		if _, err := s.store.ReconcileSourceBatchIndexedDuplicates(schema, tags.ProviderID, tags.SourceName, tags.BatchID, true); err != nil {
			return errCapJSON("pre-ingest reconcile failed: " + err.Error())
		}
	}

	inserted, err := s.store.StoreBatchWithSourceTags(schema, records, sourcePeer, nil, tags)
	if err != nil {
		return errCapJSON("ingest failed: " + err.Error())
	}

	// pullBytes is filled in by the optional raw-archive block below; when the
	// flow archives nothing, the ledger falls back to the byte count the egress
	// connector recorded for this source_url.
	var pullBytes int64

	result := map[string]interface{}{
		"schema":   schema,
		"inserted": inserted,
		"batch_id": tags.BatchID,
	}

	if reconcile == "current" {
		// Drop records from older batches when the module declares current-snapshot
		// semantics for this provider/source.
		batchResult, err := s.store.ReconcileSourceBatch(schema, tags.ProviderID, tags.SourceName, tags.BatchID, true)
		if err != nil {
			return errCapJSON("post-ingest current-batch reconcile failed: " + err.Error())
		}
		result["reconciled_old_batches"] = batchResult.Deleted
	}
	if reconcile != "none" {
		dupResult, err := s.store.ReconcileSourceBatchIndexedDuplicates(schema, tags.ProviderID, tags.SourceName, tags.BatchID, true)
		if err != nil {
			return errCapJSON("post-ingest duplicate reconcile failed: " + err.Error())
		}
		result["reconciled_duplicates"] = dupResult.Deleted
	}
	// THE SUMMARY IS BOOKKEEPING, NOT RECONCILIATION, so it is refreshed for
	// EVERY mode — including "none".
	//
	// It used to be nested inside the reconcile branch above, which meant a lane
	// that asked the host to touch nothing (the correct mode for a chunked
	// append: cellular worldwide ingest, one chunk per tick) stored its records
	// and then reported zero records and zero bytes for that source forever.
	// `apps list` showed the flow with no source lines while the rows were
	// sitting in the store — an ingest that worked and looked exactly like one
	// that never ran (graph: sdn-cellular-ingest-lands-no-batch).
	if err := s.store.RefreshSourceBatchSummary(schema, tags.ProviderID, tags.SourceName, tags.BatchID); err != nil {
		return errCapJSON("refresh source batch summary failed: " + err.Error())
	}

	// Optional raw-payload archive (runner archiveRaw layout:
	// <raw>/<source>/<yyyy-mm-dd>/<name>).
	if archive, ok := p["archive"].(map[string]interface{}); ok {
		source := sanitizePathComponent(fmt.Sprintf("%v", archive["source"]))
		name := sanitizePathComponent(fmt.Sprintf("%v", archive["name"]))
		rawB64, _ := archive["raw"].(string)
		if s.rawRoot == "" {
			return errCapJSON("archive requested but the host has no raw-archive root configured")
		}
		if source == "" || name == "" || rawB64 == "" {
			return errCapJSON("archive requires source, name and raw bytes")
		}
		raw := decodeBase64Cap(rawB64)
		// The raw archive IS the retrieved payload, so its length is the
		// honest "last pull size" for this source's operational ledger.
		pullBytes = int64(len(raw))
		day := time.Now().UTC().Format("2006-01-02")
		dir := filepath.Join(s.rawRoot, source, day)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return errCapJSON("create raw archive dir: " + err.Error())
		}
		archivePath := filepath.Join(dir, name)
		if err := os.WriteFile(archivePath, raw, 0644); err != nil {
			return errCapJSON("write raw archive: " + err.Error())
		}
		result["archived"] = archivePath
	}

	// Optional provenance record (runner recordIngestBatchProvenance layout:
	// <raw>/provenance/<source>/<ts>.json). The JSON body is authored by the
	// wasm side — the host only places it.
	if prov, ok := p["provenance"].(map[string]interface{}); ok {
		source := sanitizePathComponent(fmt.Sprintf("%v", prov["source"]))
		jsonB64, _ := prov["json"].(string)
		if s.rawRoot == "" {
			return errCapJSON("provenance requested but the host has no raw-archive root configured")
		}
		if source == "" || jsonB64 == "" {
			return errCapJSON("provenance requires source and json bytes")
		}
		body := decodeBase64Cap(jsonB64)
		if !json.Valid(body) {
			return errCapJSON("provenance json is not valid JSON")
		}
		dir := filepath.Join(s.rawRoot, "provenance", source)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return errCapJSON("create provenance dir: " + err.Error())
		}
		filename := time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
		provPath := filepath.Join(dir, filename)
		if err := os.WriteFile(provPath, body, 0644); err != nil {
			return errCapJSON("write provenance: " + err.Error())
		}
		result["provenance"] = provPath
	}

	// Book the batch in the node's operational ledger (internal/sourcemetrics).
	// Observation only: the host records THAT a provenance-tagged batch landed
	// for this source, never what it meant.
	observeIngest(IngestObservation{
		ProducerID: s.producerID,
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		SourceURL:  tags.SourceURL,
		Schema:     schema,
		BatchID:    tags.BatchID,
		PullBytes:  pullBytes,
		Records:    len(records),
		Inserted:   inserted,
	})

	return okCapJSON(result)
}

// IngestObservation is one provenance-tagged batch as seen by the host's
// storage connector, for the operational ledger.
type IngestObservation struct {
	// ProducerID is the module/flow instance that drove the ingest — a runtime
	// identity the host already tracks, which is what lets the $APPS feed
	// attribute a source to a running app WITHOUT the host knowing anything
	// about what that app does.
	ProducerID string
	ProviderID string
	SourceName string
	SourceURL  string
	Schema     string
	BatchID    string
	PullBytes  int64
	Records    int
	Inserted   int
}

// IngestObserver books an ingest batch in the operational ledger. Nil disables
// it; it must never fail the ingest.
type IngestObserver func(IngestObservation)

var (
	ingestObserverMu     sync.RWMutex
	ingestObserver       IngestObserver
	extraIngestObservers map[uint64]IngestObserver
	nextIngestObserverID uint64
)

// SetIngestObserver installs the process-wide ingest ledger hook — the node's
// operational metrics tap. Pass nil to disable. It replaces only the ledger
// slot; taps registered with AddIngestObserver are untouched.
func SetIngestObserver(observer IngestObserver) {
	ingestObserverMu.Lock()
	ingestObserver = observer
	ingestObserverMu.Unlock()
}

// AddIngestObserver registers an ADDITIONAL tap on the same event and returns
// the function that removes it.
//
// The ledger hook above is a single slot because there is exactly one
// operational ledger. Other host connectors legitimately need the same signal —
// the dataset auto-publisher (api.AutoPublisher) is one — and must not have to
// displace the ledger to get it. Every observer runs on the INGESTING
// goroutine, so a tap that does real work must hand it off to its own worker;
// none may block, and none may fail the ingest.
func AddIngestObserver(observer IngestObserver) func() {
	if observer == nil {
		return func() {}
	}
	ingestObserverMu.Lock()
	id := nextIngestObserverID
	nextIngestObserverID++
	if extraIngestObservers == nil {
		extraIngestObservers = make(map[uint64]IngestObserver)
	}
	extraIngestObservers[id] = observer
	ingestObserverMu.Unlock()

	return func() {
		ingestObserverMu.Lock()
		delete(extraIngestObservers, id)
		ingestObserverMu.Unlock()
	}
}

func observeIngest(obs IngestObservation) {
	ingestObserverMu.RLock()
	observer := ingestObserver
	extras := make([]IngestObserver, 0, len(extraIngestObservers))
	for _, extra := range extraIngestObservers {
		extras = append(extras, extra)
	}
	ingestObserverMu.RUnlock()
	if observer != nil {
		observer(obs)
	}
	for _, extra := range extras {
		extra(obs)
	}
}

// splitSizePrefixedStream parses a [u32le len][bytes]... record stream into
// unprefixed record byte slices (the store's batch input shape).
func splitSizePrefixedStream(stream []byte) ([][]byte, error) {
	var records [][]byte
	off := 0
	for off < len(stream) {
		if off+4 > len(stream) {
			return nil, fmt.Errorf("truncated size prefix at offset %d", off)
		}
		n := int(binary.LittleEndian.Uint32(stream[off:]))
		off += 4
		if n <= 0 || off+n > len(stream) {
			return nil, fmt.Errorf("record at offset %d has invalid length %d", off-4, n)
		}
		records = append(records, stream[off:off+n])
		off += n
	}
	return records, nil
}

// multiFrameSizePrefixedStream reports whether a payload is an aligned
// [u32le len][frame]... stream of TWO OR MORE frames — the SDS shard shape,
// as opposed to one record (bare, or size-prefixed with the prefix accounting
// for the whole buffer, which is what engineRecordPayload strips).
//
// Deliberately strict, because it gates a refusal: the frame lengths must
// tile the buffer EXACTLY, and every frame must carry a printable 4-byte
// FlatBuffer file identifier at frame+4. Opaque record bytes cannot satisfy
// both by accident, so a legitimate single record is never refused.
func multiFrameSizePrefixedStream(data []byte) (int, bool) {
	if len(data) < 24 {
		return 0, false
	}
	frames := 0
	off := 0
	for off < len(data) {
		if off+4 > len(data) {
			return 0, false
		}
		n := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		if n < 8 || off+n > len(data) {
			return 0, false
		}
		if !hasPrintableFileIdentifier(data[off:]) {
			return 0, false
		}
		off += n
		frames++
	}
	return frames, frames >= 2
}

// hasPrintableFileIdentifier reports whether a bare FlatBuffer carries a
// printable-ASCII 4-byte file identifier at bytes 4..8.
func hasPrintableFileIdentifier(frame []byte) bool {
	if len(frame) < 8 {
		return false
	}
	for _, c := range frame[4:8] {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// sanitizePathComponent keeps archive names strictly single path components.
func sanitizePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "<nil>" || value == "." || value == ".." {
		return ""
	}
	if strings.ContainsAny(value, "/\\") {
		return ""
	}
	return value
}

// existingDirOrParent walks up from path to the nearest existing directory
// (the runner's existingDiskPath behavior) so statfs works before the raw
// dir is first created.
func existingDirOrParent(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "."
	}
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// streamResult shapes an aligned FlatBuffer stream as a PRE-ENCODED hostcall
// envelope. Two delivery modes, elected by the GUEST (never here):
//
//   - byte delivery (default): the stream bytes travel as a raw binary
//     segment referenced by {"$bin":0} — never base64/JSON (loop C.5
//     hostcall-bridge copy elimination).
//   - reference delivery (deliver == "ref", loop C.5c): the bytes stay
//     host-side, registered on the calling instance's hostcall bridge; the
//     guest receives only result.ref = {token, size, frames, fnv1a64} and
//     forwards it to the egress, where $HTR BODY_REF_TOKEN resolves back to
//     this exact buffer — the stream never enters the flow's linear memory.
//     The fnv1a64/frames metadata come precomputed from the flatsqlrt mirror
//     (computed once per materialization, free on warm requests).
func (s *storageCapAdapter) streamResult(stream *flatsqlrt.RawStream, deliver string) []byte {
	if deliver == "ref" && s.bridge != nil {
		token := s.bridge.PutBodyRef(stream.Bytes)
		ref := map[string]interface{}{
			"token":   token,
			"size":    len(stream.Bytes),
			"fnv1a64": fmt.Sprintf("%016x", stream.FNV1a64),
		}
		if stream.FrameCount >= 0 {
			ref["frames"] = stream.FrameCount
		}
		return modulert.PreEncodedEnvelope(map[string]interface{}{
			"ok": true,
			"result": map[string]interface{}{
				"rows":    stream.Rows,
				"columns": stream.Columns,
				"ref":     ref,
			},
		}, nil)
	}
	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"rows":    stream.Rows,
			"columns": stream.Columns,
			"stream":  map[string]interface{}{"$bin": 0},
		},
	}, [][]byte{stream.Bytes})
}

// decodeTaggedParams decodes the typed query-parameter array used across the
// stack ({"t":"null|bool|i64|f64|str|bytes","v":...}, bytes base64).
func decodeTaggedParams(raw interface{}) ([]interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	entries, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("params must be an array of tagged values")
	}
	out := make([]interface{}, 0, len(entries))
	for i, e := range entries {
		m, ok := e.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("param %d: expected {t,v} object", i)
		}
		tag, _ := m["t"].(string)
		switch tag {
		case "null":
			out = append(out, nil)
		case "bool":
			v, _ := m["v"].(bool)
			out = append(out, v)
		case "i64":
			v, ok := m["v"].(float64)
			if !ok {
				return nil, fmt.Errorf("param %d: i64 value missing", i)
			}
			out = append(out, int64(v))
		case "f64":
			v, ok := m["v"].(float64)
			if !ok {
				return nil, fmt.Errorf("param %d: f64 value missing", i)
			}
			out = append(out, v)
		case "str":
			v, ok := m["v"].(string)
			if !ok {
				return nil, fmt.Errorf("param %d: str value missing", i)
			}
			out = append(out, v)
		case "bytes":
			v, ok := m["v"].(string)
			if !ok {
				return nil, fmt.Errorf("param %d: bytes value missing", i)
			}
			decoded, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return nil, fmt.Errorf("param %d: bytes not base64: %w", i, err)
			}
			out = append(out, decoded)
		default:
			return nil, fmt.Errorf("param %d: unknown tag %q", i, tag)
		}
	}
	return out, nil
}

// decodeBase64Cap decodes a standard base64 string (with or without padding).
func decodeBase64Cap(s string) []byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	lookup := [256]byte{}
	for i := range lookup {
		lookup[i] = 0xFF
	}
	for i, c := range alphabet {
		lookup[c] = byte(i)
	}

	// Strip padding
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}

	result := make([]byte, 0, len(s)*3/4)
	buf := uint(0)
	bits := uint(0)
	for i := 0; i < len(s); i++ {
		v := lookup[s[i]]
		if v == 0xFF {
			continue
		}
		buf = (buf << 6) | uint(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			result = append(result, byte(buf>>bits))
			buf &= (1 << bits) - 1
		}
	}
	return result
}
