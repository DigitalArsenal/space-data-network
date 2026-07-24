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

const (
	opaqueStateRoot                 = "/sdn/opaque-state/v1"
	defaultOpaqueStateMaxValueBytes = 512 << 20
	defaultOpaqueStateMaxScopeBytes = 1 << 30
	defaultOpaqueStateMaxScopeKeys  = 1024
)

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
	datastore     ds.Datastore
	maxValueBytes int
	maxScopeBytes int64
	maxScopeKeys  int
	mu            sync.Mutex
}

func NewOpaqueStateStore(datastore ds.Datastore) (*OpaqueStateStore, error) {
	if datastore == nil {
		return nil, errors.New("opaque state datastore is required")
	}
	return &OpaqueStateStore{
		datastore:     datastore,
		maxValueBytes: defaultOpaqueStateMaxValueBytes,
		maxScopeBytes: defaultOpaqueStateMaxScopeBytes,
		maxScopeKeys:  defaultOpaqueStateMaxScopeKeys,
	}, nil
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
	if len(value) > s.maxValueBytes {
		return nil, false, fmt.Errorf("opaque state value exceeds value quota of %d bytes", s.maxValueBytes)
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
	found := err == nil
	if err != nil && !errors.Is(err, ds.ErrNotFound) {
		return err
	}
	if len(current) > s.maxValueBytes || len(data) > s.maxValueBytes-len(current) {
		return fmt.Errorf("opaque state value quota of %d bytes exceeded", s.maxValueBytes)
	}
	if err := s.checkScopeQuotaLocked(ctx, scope, len(current), found, len(current)+len(data)); err != nil {
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
	if len(data) > s.maxValueBytes {
		return fmt.Errorf("opaque state value quota of %d bytes exceeded", s.maxValueBytes)
	}
	current, getErr := s.datastore.Get(ctx, datastoreKey)
	found := getErr == nil
	if getErr != nil && !errors.Is(getErr, ds.ErrNotFound) {
		return getErr
	}
	if err := s.checkScopeQuotaLocked(ctx, scope, len(current), found, len(data)); err != nil {
		return err
	}
	return s.datastore.Put(ctx, datastoreKey, append([]byte(nil), data...))
}

func (s *OpaqueStateStore) checkScopeQuotaLocked(ctx context.Context, scope OpaqueStateScope, currentBytes int, currentFound bool, replacementBytes int) error {
	prefix, err := opaqueStatePrefix(scope)
	if err != nil {
		return err
	}
	results, err := s.datastore.Query(ctx, dsq.Query{Prefix: prefix.String()})
	if err != nil {
		return err
	}
	defer results.Close()
	entries, err := results.Rest()
	if err != nil {
		return err
	}
	totalBytes := int64(0)
	for _, entry := range entries {
		totalBytes += int64(len(entry.Value))
		if totalBytes > s.maxScopeBytes {
			return fmt.Errorf("opaque state scope byte quota of %d bytes exceeded", s.maxScopeBytes)
		}
	}
	projectedKeys := len(entries)
	if !currentFound {
		projectedKeys++
	}
	if projectedKeys > s.maxScopeKeys {
		return fmt.Errorf("opaque state scope key quota of %d entries exceeded", s.maxScopeKeys)
	}
	projectedBytes := totalBytes - int64(currentBytes) + int64(replacementBytes)
	if projectedBytes > s.maxScopeBytes {
		return fmt.Errorf("opaque state scope byte quota of %d bytes exceeded", s.maxScopeBytes)
	}
	return nil
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
