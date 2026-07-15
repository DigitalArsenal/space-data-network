package sdnservices

import (
	"context"
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

	default:
		return errCapJSON(fmt.Sprintf("unknown storage operation: %s", operation)), nil
	}
}
