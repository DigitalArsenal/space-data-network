package caps

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
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
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		producer := strings.TrimSpace(fallbackProducer)
		if mod != nil {
			if id := strings.TrimSpace(mod.ID()); id != "" && id != "unknown-module" {
				producer = id
			}
		}
		s := &storageCapAdapter{store: store, producerID: producer, bridge: bridge}
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
