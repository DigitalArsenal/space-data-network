package directory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Service normalizes EPM JSON into indexed directory records.
type Service struct {
	store       Store
	localPeerID string
}

// NewService creates a directory service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetLocalPeerID configures the node peer ID that should be hidden from
// directory search results. The local node still keeps its EPM indexed for
// exports and profile management, but it is not a search result.
func (s *Service) SetLocalPeerID(peerID string) {
	if s == nil {
		return
	}
	s.localPeerID = strings.TrimSpace(peerID)
}

// UpsertNodeEPMJSON indexes a node EPM JSON payload.
func (s *Service) UpsertNodeEPMJSON(epmJSON map[string]any, epmCID, source string) error {
	return s.upsertEPMJSON(KindNode, epmJSON, epmCID, source)
}

// UpsertUserEPMJSON indexes a user EPM JSON payload.
func (s *Service) UpsertUserEPMJSON(epmJSON map[string]any, epmCID, source string) error {
	return s.upsertEPMJSON(KindUser, epmJSON, epmCID, source)
}

// SearchNodes returns matching node directory records.
func (s *Service) SearchNodes(search string, limit int) ([]storage.DirectoryRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("directory store is not configured")
	}
	return s.store.QueryDirectory(storage.DirectoryQuery{
		Kind:           KindNode,
		ExcludePeerID:  s.localPeerID,
		ExcludeSources: []string{"peer-connect"},
		Search:         search,
		Limit:          limit,
	})
}

// SearchUsers returns matching user directory records.
func (s *Service) SearchUsers(search string, limit int) ([]storage.DirectoryRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("directory store is not configured")
	}
	return s.store.QueryDirectory(storage.DirectoryQuery{
		Kind:          KindUser,
		ExcludePeerID: s.localPeerID,
		Search:        search,
		Limit:         limit,
	})
}

func (s *Service) upsertEPMJSON(kind string, epmJSON map[string]any, epmCID, source string) error {
	if s == nil || s.store == nil {
		return errors.New("directory store is not configured")
	}
	if epmJSON == nil {
		return errors.New("epm json is required")
	}

	record, err := normalizeRecord(kind, epmJSON, epmCID, source)
	if err != nil {
		return err
	}
	return s.store.UpsertDirectoryRecord(record)
}

func normalizeRecord(kind string, epmJSON map[string]any, epmCID, source string) (storage.DirectoryRecord, error) {
	normalizedKind := strings.TrimSpace(strings.ToLower(kind))
	if normalizedKind == "" {
		normalizedKind = InferKind(epmJSON)
	}
	if normalizedKind == "" {
		normalizedKind = KindUser
	}
	if normalizedKind != KindNode && normalizedKind != KindUser {
		return storage.DirectoryRecord{}, fmt.Errorf("unsupported directory kind %q", normalizedKind)
	}

	peerID := firstString(epmJSON, "peer_id", "peerID", "peerid", "id")
	if peerID == "" {
		return storage.DirectoryRecord{}, fmt.Errorf("peer_id is required for %s directory record", normalizedKind)
	}

	canonical := canonicalDirectoryEPMJSON(normalizedKind, peerID, epmJSON)

	record := storage.DirectoryRecord{
		Kind:           normalizedKind,
		PeerID:         peerID,
		DN:             firstString(epmJSON, "dn", "DN"),
		LegalName:      firstString(epmJSON, "legal_name", "LEGAL_NAME"),
		BitcoinAddress: firstString(epmJSON, "bitcoin_address", "BITCOIN_ADDRESS"),
		EPMCID:         strings.TrimSpace(epmCID),
		Source:         strings.TrimSpace(source),
		UpdatedAt:      time.Now().Unix(),
	}
	if record.Source == "" {
		record.Source = "unknown"
	}

	rawJSON, err := json.Marshal(canonical)
	if err != nil {
		return storage.DirectoryRecord{}, fmt.Errorf("marshal directory record json: %w", err)
	}
	record.EPMJSON = string(rawJSON)

	return record, nil
}

func canonicalDirectoryEPMJSON(kind, peerID string, epmJSON map[string]any) map[string]any {
	canonical := make(map[string]any, len(epmJSON)+3)
	for key, value := range epmJSON {
		normalizedKey := normalizeDirectoryJSONKey(key)
		if normalizedKey == "" || containsPrivateKeyMaterial(normalizedKey) {
			continue
		}
		canonical[normalizedKey] = sanitizeDirectoryJSONValue(value)
	}
	canonical["directory_kind"] = kind
	canonical["entity_type"] = kind
	canonical["peer_id"] = peerID
	if dn := firstString(epmJSON, "dn", "DN"); dn != "" {
		canonical["dn"] = dn
	}
	if legalName := firstString(epmJSON, "legal_name", "LEGAL_NAME"); legalName != "" {
		canonical["legal_name"] = legalName
	}
	if bitcoinAddress := firstString(epmJSON, "bitcoin_address", "BITCOIN_ADDRESS"); bitcoinAddress != "" {
		canonical["bitcoin_address"] = bitcoinAddress
	}
	return canonical
}

func normalizeDirectoryJSONKey(key string) string {
	switch strings.TrimSpace(key) {
	case "":
		return ""
	case "DN":
		return "dn"
	case "LEGAL_NAME":
		return "legal_name"
	case "FAMILY_NAME":
		return "family_name"
	case "GIVEN_NAME":
		return "given_name"
	case "ADDITIONAL_NAME":
		return "additional_name"
	case "HONORIFIC_PREFIX":
		return "honorific_prefix"
	case "HONORIFIC_SUFFIX":
		return "honorific_suffix"
	case "JOB_TITLE":
		return "job_title"
	case "OCCUPATION":
		return "occupation"
	case "EMAIL":
		return "email"
	case "TELEPHONE":
		return "telephone"
	case "BITCOIN_ADDRESS":
		return "bitcoin_address"
	case "PEER_ID", "peerID", "peerId", "peerid", "id":
		return "peer_id"
	case "ENTITY_TYPE":
		return "entity_type"
	default:
		return strings.TrimSpace(key)
	}
}

func sanitizeDirectoryJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalizedKey := normalizeDirectoryJSONKey(key)
			if normalizedKey == "" || containsPrivateKeyMaterial(normalizedKey) {
				continue
			}
			out[normalizedKey] = sanitizeDirectoryJSONValue(nested)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, nested := range typed {
			out = append(out, sanitizeDirectoryJSONValue(nested))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, nested := range typed {
			out = append(out, sanitizeDirectoryJSONValue(nested))
		}
		return out
	default:
		return value
	}
}

func containsPrivateKeyMaterial(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return normalized == "private_key" || normalized == "xpriv"
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			if s, ok := value.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// InferKind resolves the directory record kind from canonical EPM fields.
func InferKind(epmJSON map[string]any) string {
	if epmJSON == nil {
		return ""
	}

	if kind := normalizeKindValue(firstAny(epmJSON, "directory_kind", "kind")); kind != "" {
		return kind
	}
	if kind := normalizeEntityTypeValue(firstAny(epmJSON, "entity_type", "ENTITY_TYPE", "entityType")); kind != "" {
		return kind
	}
	if firstString(epmJSON, "bitcoin_address", "BITCOIN_ADDRESS", "epm_cid", "EPM_CID") != "" {
		return KindNode
	}
	return KindUser
}

func firstAny(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func normalizeKindValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case KindNode:
		return KindNode
	case KindUser:
		return KindUser
	default:
		return ""
	}
}

func normalizeEntityTypeValue(value any) string {
	switch typed := value.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case KindNode, "1":
			return KindNode
		case KindUser, "0":
			return KindUser
		default:
			return ""
		}
	case float64:
		if typed == 1 {
			return KindNode
		}
		if typed == 0 {
			return KindUser
		}
	case int:
		if typed == 1 {
			return KindNode
		}
		if typed == 0 {
			return KindUser
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			if parsed == 1 {
				return KindNode
			}
			if parsed == 0 {
				return KindUser
			}
		}
	}
	return ""
}
