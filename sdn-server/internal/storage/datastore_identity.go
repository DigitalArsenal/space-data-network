package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const datastoreNamespaceDirName = "datastores"

const (
	datastoreIdentityMetadataKey = "sdn.datastore.identity"
	datastoreKeyMetadataKey      = "sdn.datastore.key"
	datastoreRegistryFileName    = "registry.json"
)

// DatastoreIdentity is the SDN-owned identity for an isolated FlatSQL
// namespace. FlatSQL only sees records for this namespace; SDN keeps source,
// provider, cursor, and artifact identity outside the canonical schema.
type DatastoreIdentity struct {
	SchemaName      string            `json:"schema_name"`
	SourcePeerID    string            `json:"source_peer_id,omitempty"`
	SourcePublicKey string            `json:"source_public_key,omitempty"`
	ProviderID      string            `json:"provider_id,omitempty"`
	SourceName      string            `json:"source_name,omitempty"`
	BatchHead       string            `json:"batch_head,omitempty"`
	QueryProfile    string            `json:"query_profile,omitempty"`
	CanonicalParams map[string]string `json:"canonical_params,omitempty"`
	SnapshotID      string            `json:"snapshot_id,omitempty"`
	HighWaterMark   string            `json:"high_water_mark,omitempty"`
	ArtifactHash    string            `json:"artifact_hash,omitempty"`
}

type canonicalDatastoreIdentity struct {
	SchemaName      string                    `json:"schema_name"`
	SourcePeerID    string                    `json:"source_peer_id,omitempty"`
	SourcePublicKey string                    `json:"source_public_key,omitempty"`
	ProviderID      string                    `json:"provider_id,omitempty"`
	SourceName      string                    `json:"source_name,omitempty"`
	BatchHead       string                    `json:"batch_head,omitempty"`
	QueryProfile    string                    `json:"query_profile,omitempty"`
	CanonicalParams []canonicalDatastoreParam `json:"canonical_params,omitempty"`
	SnapshotID      string                    `json:"snapshot_id,omitempty"`
	HighWaterMark   string                    `json:"high_water_mark,omitempty"`
	ArtifactHash    string                    `json:"artifact_hash,omitempty"`
}

type canonicalDatastoreParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// DatastoreRegistryEntry records one SDN-managed FlatSQL namespace.
type DatastoreRegistryEntry struct {
	Key       string            `json:"key"`
	Path      string            `json:"path"`
	Identity  DatastoreIdentity `json:"identity"`
	UpdatedAt int64             `json:"updated_at"`
}

type datastoreRegistryFile struct {
	Version int                      `json:"version"`
	Entries []DatastoreRegistryEntry `json:"entries"`
}

// Key returns a stable filesystem-safe key for this SDN datastore identity.
func (identity DatastoreIdentity) Key() (string, error) {
	payload, err := identity.canonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sdn-ds-v1-" + hex.EncodeToString(sum[:16]), nil
}

// DatastoreIdentityPath returns the directory for an isolated SDN datastore
// identity under the supplied SDN storage root.
func DatastoreIdentityPath(basePath string, identity DatastoreIdentity) (string, error) {
	key, err := identity.Key()
	if err != nil {
		return "", err
	}
	return filepath.Join(basePath, datastoreNamespaceDirName, key), nil
}

// NewFlatSQLStoreForIdentity opens the FlatSQL store isolated to one SDN
// datastore identity.
func NewFlatSQLStoreForIdentity(basePath string, validator *sds.Validator, identity DatastoreIdentity) (*FlatSQLStore, error) {
	path, err := DatastoreIdentityPath(basePath, identity)
	if err != nil {
		return nil, err
	}
	store, err := NewFlatSQLStore(path, validator)
	if err != nil {
		return nil, err
	}
	if err := store.recordDatastoreIdentity(identity); err != nil {
		store.Close()
		return nil, err
	}
	if err := registerDatastoreIdentity(basePath, identity); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// ListDatastoreIdentities lists SDN-managed FlatSQL namespaces registered under
// the supplied SDN storage root.
func ListDatastoreIdentities(basePath string) ([]DatastoreRegistryEntry, error) {
	registry, err := readDatastoreRegistry(basePath)
	if err != nil {
		return nil, err
	}
	entries := append([]DatastoreRegistryEntry(nil), registry.Entries...)
	sortDatastoreRegistryEntries(entries)
	return entries, nil
}

// ListDatastoreIdentities lists SDN-managed FlatSQL namespaces registered next
// to this root store.
func (s *FlatSQLStore) ListDatastoreIdentities() ([]DatastoreRegistryEntry, error) {
	return ListDatastoreIdentities(s.basePath)
}

// OpenRegisteredDatastore opens a registered identity namespace by key.
func (s *FlatSQLStore) OpenRegisteredDatastore(key string) (*FlatSQLStore, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("datastore key is required")
	}
	entries, err := ListDatastoreIdentities(s.basePath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Key == key {
			return NewFlatSQLStoreForIdentity(s.basePath, s.validator, entry.Identity)
		}
	}
	return nil, fmt.Errorf("datastore key not found: %s", key)
}

// DatastoreIdentity returns the SDN datastore identity recorded in this store.
func (s *FlatSQLStore) DatastoreIdentity() (DatastoreIdentity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var encoded string
	err := s.db.QueryRow(`SELECT value FROM sdn_metadata WHERE key = ?`, datastoreIdentityMetadataKey).Scan(&encoded)
	if err != nil {
		if err == sql.ErrNoRows {
			return DatastoreIdentity{}, false, nil
		}
		return DatastoreIdentity{}, false, fmt.Errorf("read datastore identity metadata: %w", err)
	}
	var identity DatastoreIdentity
	if err := json.Unmarshal([]byte(encoded), &identity); err != nil {
		return DatastoreIdentity{}, false, fmt.Errorf("parse datastore identity metadata: %w", err)
	}
	return identity, true, nil
}

func (s *FlatSQLStore) recordDatastoreIdentity(identity DatastoreIdentity) error {
	normalized, err := identity.normalizedIdentity()
	if err != nil {
		return err
	}
	key, err := normalized.Key()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode datastore identity metadata: %w", err)
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range []struct {
		key   string
		value string
	}{
		{datastoreIdentityMetadataKey, string(encoded)},
		{datastoreKeyMetadataKey, key},
	} {
		if _, err := s.db.Exec(`
			INSERT INTO sdn_metadata (key, value, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		`, entry.key, entry.value, now); err != nil {
			return fmt.Errorf("record datastore identity metadata: %w", err)
		}
	}
	return nil
}

func registerDatastoreIdentity(basePath string, identity DatastoreIdentity) error {
	normalized, err := identity.normalizedIdentity()
	if err != nil {
		return err
	}
	key, err := normalized.Key()
	if err != nil {
		return err
	}
	path, err := DatastoreIdentityPath(basePath, normalized)
	if err != nil {
		return err
	}
	registry, err := readDatastoreRegistry(basePath)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	replaced := false
	for index := range registry.Entries {
		if registry.Entries[index].Key != key {
			continue
		}
		registry.Entries[index] = DatastoreRegistryEntry{
			Key:       key,
			Path:      path,
			Identity:  normalized,
			UpdatedAt: now,
		}
		replaced = true
		break
	}
	if !replaced {
		registry.Entries = append(registry.Entries, DatastoreRegistryEntry{
			Key:       key,
			Path:      path,
			Identity:  normalized,
			UpdatedAt: now,
		})
	}
	sortDatastoreRegistryEntries(registry.Entries)
	return writeDatastoreRegistry(basePath, registry)
}

func readDatastoreRegistry(basePath string) (datastoreRegistryFile, error) {
	path := datastoreRegistryPath(basePath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return datastoreRegistryFile{Version: 1}, nil
		}
		return datastoreRegistryFile{}, fmt.Errorf("read datastore registry: %w", err)
	}
	if len(data) == 0 {
		return datastoreRegistryFile{Version: 1}, nil
	}
	var registry datastoreRegistryFile
	if err := json.Unmarshal(data, &registry); err != nil {
		return datastoreRegistryFile{}, fmt.Errorf("parse datastore registry: %w", err)
	}
	if registry.Version == 0 {
		registry.Version = 1
	}
	sortDatastoreRegistryEntries(registry.Entries)
	return registry, nil
}

func writeDatastoreRegistry(basePath string, registry datastoreRegistryFile) error {
	registry.Version = 1
	dir := filepath.Join(basePath, datastoreNamespaceDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create datastore registry dir: %w", err)
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode datastore registry: %w", err)
	}
	path := datastoreRegistryPath(basePath)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write datastore registry temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace datastore registry: %w", err)
	}
	return nil
}

func datastoreRegistryPath(basePath string) string {
	return filepath.Join(basePath, datastoreNamespaceDirName, datastoreRegistryFileName)
}

func sortDatastoreRegistryEntries(entries []DatastoreRegistryEntry) {
	sort.Slice(entries, func(i, j int) bool {
		left := entries[i].Identity
		right := entries[j].Identity
		for _, pair := range [][2]string{
			{left.ProviderID, right.ProviderID},
			{left.SourceName, right.SourceName},
			{left.SchemaName, right.SchemaName},
			{left.QueryProfile, right.QueryProfile},
			{left.BatchHead, right.BatchHead},
			{entries[i].Key, entries[j].Key},
		} {
			if pair[0] == pair[1] {
				continue
			}
			return pair[0] < pair[1]
		}
		return false
	})
}

func (identity DatastoreIdentity) canonicalJSON() ([]byte, error) {
	normalized, err := identity.normalize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func (identity DatastoreIdentity) normalizedIdentity() (DatastoreIdentity, error) {
	normalized, err := identity.normalize()
	if err != nil {
		return DatastoreIdentity{}, err
	}
	params := make(map[string]string, len(normalized.CanonicalParams))
	for _, param := range normalized.CanonicalParams {
		params[param.Key] = param.Value
	}
	if len(params) == 0 {
		params = nil
	}
	return DatastoreIdentity{
		SchemaName:      normalized.SchemaName,
		SourcePeerID:    normalized.SourcePeerID,
		SourcePublicKey: normalized.SourcePublicKey,
		ProviderID:      normalized.ProviderID,
		SourceName:      normalized.SourceName,
		BatchHead:       normalized.BatchHead,
		QueryProfile:    normalized.QueryProfile,
		CanonicalParams: params,
		SnapshotID:      normalized.SnapshotID,
		HighWaterMark:   normalized.HighWaterMark,
		ArtifactHash:    normalized.ArtifactHash,
	}, nil
}

func (identity DatastoreIdentity) normalize() (canonicalDatastoreIdentity, error) {
	schemaName, err := normalizeDatastoreIdentitySchema(identity.SchemaName)
	if err != nil {
		return canonicalDatastoreIdentity{}, err
	}
	params := make([]canonicalDatastoreParam, 0, len(identity.CanonicalParams))
	for key, value := range identity.CanonicalParams {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		params = append(params, canonicalDatastoreParam{Key: key, Value: strings.TrimSpace(value)})
	}
	sort.Slice(params, func(i, j int) bool {
		return params[i].Key < params[j].Key
	})
	return canonicalDatastoreIdentity{
		SchemaName:      schemaName,
		SourcePeerID:    strings.TrimSpace(identity.SourcePeerID),
		SourcePublicKey: strings.TrimSpace(identity.SourcePublicKey),
		ProviderID:      strings.TrimSpace(identity.ProviderID),
		SourceName:      strings.TrimSpace(identity.SourceName),
		BatchHead:       strings.TrimSpace(identity.BatchHead),
		QueryProfile:    strings.TrimSpace(identity.QueryProfile),
		CanonicalParams: params,
		SnapshotID:      strings.TrimSpace(identity.SnapshotID),
		HighWaterMark:   strings.TrimSpace(identity.HighWaterMark),
		ArtifactHash:    strings.TrimSpace(identity.ArtifactHash),
	}, nil
}

func normalizeDatastoreIdentitySchema(schemaName string) (string, error) {
	value := strings.TrimSpace(schemaName)
	if value == "" {
		return "", fmt.Errorf("datastore identity schema name is required")
	}
	if strings.HasSuffix(strings.ToLower(value), ".fbs") {
		value = value[:len(value)-len(".fbs")]
	}
	value = strings.ToUpper(value) + ".fbs"
	if err := sds.ValidateSchemaName(value); err != nil {
		return "", fmt.Errorf("invalid datastore identity schema name: %w", err)
	}
	return value, nil
}
