package caps

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/ingest"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// NewStorageCapFactory returns a BridgeCapFactory for "storage_query" and
// "storage_write" capabilities. Both capabilities share the same handler —
// the operation prefix distinguishes them ("storage.query" vs
// "storage.write").
//
// Supported operations:
//
//	storage.query  — {"schema":"OMM","day":"2026-04-07","entity_id":"...","norad_cat_id":12345,"limit":100}
//	storage.write  — {"schema":"OMM","data":"base64..."}  (data is raw FlatBuffer bytes)
//	storage.delete — {"cid":"sha256hex..."}
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
		}
		s := &storageCapAdapter{
			store:            store,
			producerID:       producer,
			bridge:           bridge,
			rawRoot:          strings.TrimSpace(opts.RawRoot),
			minFreeDiskBytes: opts.MinFreeDiskBytes,
		}
		return s.handle
	}
}

type storageCapAdapter struct {
	store      *storage.FlatSQLStore
	producerID string
	// bridge is the calling instance's hostcall bridge — the registry for
	// "deliver":"ref" body references (loop C.5c). May be nil on legacy
	// provisioning paths; ref delivery then degrades to byte delivery.
	bridge *modulert.HostBridge
	// rawRoot / minFreeDiskBytes: host resource policy for
	// storage.ingest_with_source (see StorageCapOptions).
	rawRoot          string
	minFreeDiskBytes int64
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
		schema := str("schema")
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
		cid, err := s.store.Store(schema, rawBytes, s.producerID, nil)
		if err != nil {
			return errCapJSON("write failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]string{"cid": cid}), nil

	case "storage.delete":
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
		return s.handleIngestWithSource(p, str), nil

	// Engine-native ops (loop C.1): results are ALIGNED size-prefixed
	// FlatBuffer streams delivered as raw binary envelope segments (the
	// handler returns a PRE-ENCODED envelope — no base64/JSON round-trip
	// anywhere on the host side, loop C.5).
	case "storage.flatsql_query_stream":
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

// defaultIngestMinFreeDiskBytes mirrors the ingest runner's disk guardrail
// default (internal/ingest defaultMinFreeDiskBytes).
const defaultIngestMinFreeDiskBytes = 5 * 1024 * 1024 * 1024

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
//	  "source_name": "celestrak-gp",              (required)
//	  "source_url":  "https://...",               (optional)
//	  "batch_id":    "<sha256 of source payload>",(required)
//	  "content_key_id": "public",                 (optional)
//	  "source_peer": "source:celestrak",          (optional)
//	  "records": {"$bin":0},                      (size-prefixed record stream)
//	  "reconcile": "none"|"duplicates"|"current", (default "duplicates")
//	  "archive": {"source":"celestrak","name":"catalog.csv","raw":{"$bin":1}},
//	  "provenance": {"source":"celestrak-gp","json":{"$bin":2}}
//	}
func (s *storageCapAdapter) handleIngestWithSource(p map[string]interface{}, str func(string) string) []byte {
	if s.bridge == nil || !s.bridge.HasCapability("storage_ingest") {
		return errCapJSON("storage.ingest_with_source requires the storage_ingest capability grant")
	}

	schema := str("schema")
	if schema == "" {
		return errCapJSON("missing schema")
	}
	tags := storage.SourceTags{
		ProviderID:   str("provider_id"),
		SourceName:   str("source_name"),
		SourceURL:    str("source_url"),
		BatchID:      str("batch_id"),
		ContentKeyID: str("content_key_id"),
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
			return errCapJSON("free-disk check failed for " + path + ": " + err.Error())
		}
		if int64(free) < minFree {
			return errCapJSON(fmt.Sprintf("ingest requires at least %d free bytes at %s; only %d available", minFree, path, free))
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

	result := map[string]interface{}{
		"schema":   schema,
		"inserted": inserted,
		"batch_id": tags.BatchID,
	}

	if reconcile == "current" {
		// Drop records from OLD batches of this provider/source (the runner's
		// reconcileCelestrakCurrentSourceBatch, used for snapshot sources like
		// SATCAT where only the newest batch is meaningful).
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
		if err := s.store.RefreshSourceBatchSummary(schema, tags.ProviderID, tags.SourceName, tags.BatchID); err != nil {
			return errCapJSON("refresh source batch summary failed: " + err.Error())
		}
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

	return okCapJSON(result)
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
