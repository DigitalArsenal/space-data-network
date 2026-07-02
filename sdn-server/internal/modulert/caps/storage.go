package caps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// NewStorageCapFactory returns a CapFactory for "storage_query" and "storage_write"
// capabilities. Both capabilities share the same handler — the operation prefix
// distinguishes them ("storage.query" vs "storage.write").
//
// Supported operations:
//
//	storage.query  — {"schema":"OMM","day":"2026-04-07","entity_id":"...","norad_cat_id":12345,"limit":100}
//	storage.write  — {"schema":"OMM","data":"base64..."}  (data is raw FlatBuffer bytes)
//	storage.delete — {"cid":"sha256hex..."}
func NewStorageCapFactory(store *storage.FlatSQLStore) modulert.CapFactory {
	return NewStorageCapFactoryWithProducer(store, "")
}

// NewStorageCapFactoryWithProducer is NewStorageCapFactory with an explicit
// fallback producer identity. Writes are attributed to the calling module's
// plugin id (the natural (producer, standard) routing key for module-authored
// records); the fallback covers modules whose manifest is unavailable.
func NewStorageCapFactoryWithProducer(store *storage.FlatSQLStore, fallbackProducer string) modulert.CapFactory {
	return func(mod *modulert.Module) modulert.CapHandler {
		producer := strings.TrimSpace(fallbackProducer)
		if mod != nil {
			if id := strings.TrimSpace(mod.ID()); id != "" && id != "unknown-module" {
				producer = id
			}
		}
		s := &storageCapAdapter{store: store, producerID: producer}
		return s.handle
	}
}

type storageCapAdapter struct {
	store      *storage.FlatSQLStore
	producerID string
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

	default:
		return errCapJSON(fmt.Sprintf("unknown storage operation: %s", operation)), nil
	}
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
