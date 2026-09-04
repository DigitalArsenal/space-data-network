package channels

// Durable subscription registry (fbcs program, $DSS sync lane).
//
// The registry itself is in-memory. The sync lane persists the set of
// subscribed channel ids as a JSON list next to the store so a restart keeps
// the operator's choices. At-rest JSON is exempt from the no-JSON dashboard
// wire law: nothing here is served to a client.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot returns the ids of every subscribed channel, sorted.
func (r *SubscriptionRegistry) Snapshot() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	ids := make([]string, 0, len(r.states))
	for id, state := range r.states {
		if state.Subscribed {
			ids = append(ids, id)
		}
	}
	r.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

// LoadFrom marks every channel id listed in the JSON file at path as
// subscribed. A missing file is not an error; a malformed one is. Ids that no
// longer parse as channel ids are skipped rather than failing the load.
func (r *SubscriptionRegistry) LoadFrom(path string) error {
	if r == nil {
		return errors.New("subscription registry is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("subscription file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read subscription file %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return fmt.Errorf("parse subscription file %s: %w", path, err)
	}
	for _, id := range ids {
		parsed, err := ParseChannelID(id)
		if err != nil {
			continue
		}
		r.set(parsed, true)
	}
	return nil
}

// SaveTo writes the subscribed channel ids as a JSON list at path,
// atomically (temp file + rename), creating the directory when needed.
func (r *SubscriptionRegistry) SaveTo(path string) error {
	if r == nil {
		return errors.New("subscription registry is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("subscription file path is required")
	}
	ids := r.Snapshot()
	if ids == nil {
		ids = []string{}
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return fmt.Errorf("encode subscriptions: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create subscription directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create subscription temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write subscription file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close subscription file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit subscription file: %w", err)
	}
	return nil
}
