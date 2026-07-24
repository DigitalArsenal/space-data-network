package sdnservices

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	ds "github.com/ipfs/go-datastore"
	dsq "github.com/ipfs/go-datastore/query"
)

const opaqueStateRoot = "/sdn/opaque-state/v1"

// OpaqueStateScope isolates state by the exact signed artifact, independently
// instantiated node, and node-selected namespace. Values have no host-visible
// schema.
type OpaqueStateScope struct {
	ArtifactHash string
	NodeID       string
	Namespace    string
}

// OpaqueStateStore is a binary key/value adapter over the node datastore.
type OpaqueStateStore struct {
	datastore ds.Datastore
	mu        sync.Mutex
}

func NewOpaqueStateStore(datastore ds.Datastore) (*OpaqueStateStore, error) {
	if datastore == nil {
		return nil, errors.New("opaque state datastore is required")
	}
	return &OpaqueStateStore{datastore: datastore}, nil
}

func (s *OpaqueStateStore) Read(ctx context.Context, scope OpaqueStateScope, key string) ([]byte, bool, error) {
	datastoreKey, err := opaqueStateKey(scope, key)
	if err != nil {
		return nil, false, err
	}
	value, err := s.datastore.Get(ctx, datastoreKey)
	if errors.Is(err, ds.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), value...), true, nil
}

func (s *OpaqueStateStore) Append(ctx context.Context, scope OpaqueStateScope, key string, data []byte) error {
	datastoreKey, err := opaqueStateKey(scope, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.datastore.Get(ctx, datastoreKey)
	if err != nil && !errors.Is(err, ds.ErrNotFound) {
		return err
	}
	combined := make([]byte, 0, len(current)+len(data))
	combined = append(combined, current...)
	combined = append(combined, data...)
	return s.datastore.Put(ctx, datastoreKey, combined)
}

func (s *OpaqueStateStore) Replace(ctx context.Context, scope OpaqueStateScope, key string, data []byte) error {
	datastoreKey, err := opaqueStateKey(scope, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.datastore.Put(ctx, datastoreKey, append([]byte(nil), data...))
}

func (s *OpaqueStateStore) List(ctx context.Context, scope OpaqueStateScope) ([]string, error) {
	prefix, err := opaqueStatePrefix(scope)
	if err != nil {
		return nil, err
	}
	results, err := s.datastore.Query(ctx, dsq.Query{Prefix: prefix.String(), KeysOnly: true})
	if err != nil {
		return nil, err
	}
	defer results.Close()
	entries, err := results.Rest()
	if err != nil {
		return nil, err
	}
	base := prefix.String() + "/"
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Key, base) {
			keys = append(keys, strings.TrimPrefix(entry.Key, base))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *OpaqueStateStore) Delete(ctx context.Context, scope OpaqueStateScope, key string) error {
	datastoreKey, err := opaqueStateKey(scope, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.datastore.Delete(ctx, datastoreKey)
}

func (s *OpaqueStateStore) Sync(ctx context.Context, scope OpaqueStateScope) error {
	prefix, err := opaqueStatePrefix(scope)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.datastore.Sync(ctx, prefix)
}

func opaqueStateKey(scope OpaqueStateScope, key string) (ds.Key, error) {
	prefix, err := opaqueStatePrefix(scope)
	if err != nil {
		return ds.Key{}, err
	}
	if err := validateOpaqueSegment("key", key); err != nil {
		return ds.Key{}, err
	}
	return prefix.ChildString(key), nil
}

func opaqueStatePrefix(scope OpaqueStateScope) (ds.Key, error) {
	if len(scope.ArtifactHash) != 64 || !isHex(scope.ArtifactHash) {
		return ds.Key{}, errors.New("opaque state artifact hash must be 64 hexadecimal characters")
	}
	if err := validateOpaqueSegment("node id", scope.NodeID); err != nil {
		return ds.Key{}, err
	}
	if err := validateOpaqueSegment("namespace", scope.Namespace); err != nil {
		return ds.Key{}, err
	}
	return ds.NewKey(opaqueStateRoot).
		ChildString(strings.ToLower(scope.ArtifactHash)).
		ChildString(scope.NodeID).
		ChildString(scope.Namespace), nil
}

func validateOpaqueSegment(label, value string) error {
	if value == "" || value == "." || value == ".." || len(value) > 255 {
		return fmt.Errorf("opaque state %s is invalid", label)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("opaque state %s contains an unsafe character", label)
	}
	return nil
}

func isHex(value string) bool {
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
