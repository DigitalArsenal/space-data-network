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
	store Store
}

// NewService creates a directory service.
func NewService(store Store) *Service {
	return &Service{store: store}
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
		Kind:   KindNode,
		Search: search,
		Limit:  limit,
	})
}

// SearchUsers returns matching user directory records.
func (s *Service) SearchUsers(search string, limit int) ([]storage.DirectoryRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("directory store is not configured")
	}
	return s.store.QueryDirectory(storage.DirectoryQuery{
		Kind:   KindUser,
		Search: search,
		Limit:  limit,
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
		return storage.DirectoryRecord{}, fmt.Errorf("directory kind is required")
	}

	peerID := firstString(epmJSON, "peer_id", "peerID", "peerid", "id")
	if peerID == "" {
		return storage.DirectoryRecord{}, fmt.Errorf("peer_id is required for %s directory record", normalizedKind)
	}

	canonical := map[string]any{
		"directory_kind": normalizedKind,
		"peer_id":        peerID,
	}
	if dn := firstString(epmJSON, "dn", "DN"); dn != "" {
		canonical["dn"] = dn
	}
	if legalName := firstString(epmJSON, "legal_name", "LEGAL_NAME"); legalName != "" {
		canonical["legal_name"] = legalName
	}
	if bitcoinAddress := firstString(epmJSON, "bitcoin_address", "BITCOIN_ADDRESS"); bitcoinAddress != "" {
		canonical["bitcoin_address"] = bitcoinAddress
	}

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
