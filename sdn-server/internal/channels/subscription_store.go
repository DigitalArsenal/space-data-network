package channels

// Durable subscription registry (fbcs program, $DSS sync lane).
//
// The registry itself is in-memory. The sync lane persists the subscribed
// channels — id + retention rule — as a JSON list next to the store so a
// restart keeps the operator's choices. At-rest JSON is exempt from the
// no-JSON dashboard wire law: nothing here is served to a client.
//
// File shape (current):
//
//	[{"channel_id": "space-data-network-02-OMM", "retention": "replace-current"}]
//
// The earlier shape — a bare list of channel ids — still loads; every id in
// it is subscribed under the registry default rule.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SubscriptionEntry is one persisted subscription.
type SubscriptionEntry struct {
	ChannelID string `json:"channel_id"`
	Retention string `json:"retention"`
}

// Snapshot returns the ids of every subscribed channel, sorted.
func (r *SubscriptionRegistry) Snapshot() []string {
	entries := r.Entries()
	if entries == nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ChannelID)
	}
	return ids
}

// Entries returns every subscribed channel with its retention rule, sorted
// by channel id. The retention word is always populated.
func (r *SubscriptionRegistry) Entries() []SubscriptionEntry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	fallback := r.defaultRetentionLocked()
	entries := make([]SubscriptionEntry, 0, len(r.states))
	for id, state := range r.states {
		if !state.Subscribed {
			continue
		}
		retention := state.Retention
		if retention == "" {
			retention = fallback
		}
		entries = append(entries, SubscriptionEntry{ChannelID: id, Retention: retention})
	}
	r.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].ChannelID < entries[j].ChannelID })
	return entries
}

// LoadFrom marks every channel listed in the JSON file at path as
// subscribed, under the listed retention rule (or the registry default when
// the file predates retention). A missing file is not an error; a malformed
// one is. Ids that no longer parse as channel ids are skipped rather than
// failing the load.
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
	entries, err := parseSubscriptionFile(data)
	if err != nil {
		return fmt.Errorf("parse subscription file %s: %w", path, err)
	}
	for _, entry := range entries {
		parsed, err := ParseChannelID(entry.ChannelID)
		if err != nil {
			continue
		}
		r.set(parsed, true, entry.Retention)
	}
	return nil
}

// parseSubscriptionFile reads the current object-list shape first and falls
// back to the legacy bare id list.
func parseSubscriptionFile(data []byte) ([]SubscriptionEntry, error) {
	var entries []SubscriptionEntry
	if err := json.Unmarshal(data, &entries); err == nil {
		out := make([]SubscriptionEntry, 0, len(entries))
		for _, entry := range entries {
			if strings.TrimSpace(entry.ChannelID) == "" {
				continue
			}
			out = append(out, entry)
		}
		return out, nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	out := make([]SubscriptionEntry, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		out = append(out, SubscriptionEntry{ChannelID: id})
	}
	return out, nil
}

// SaveTo writes the subscribed channels (id + retention) as a JSON list at
// path, atomically (temp file + rename), creating the directory when needed.
func (r *SubscriptionRegistry) SaveTo(path string) error {
	if r == nil {
		return errors.New("subscription registry is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("subscription file path is required")
	}
	entries := r.Entries()
	if entries == nil {
		entries = []SubscriptionEntry{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
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
