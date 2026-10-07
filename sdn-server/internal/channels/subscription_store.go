package channels

// Durable subscription registry (fbcs program, $DSS sync lane).
//
// The registry itself is in-memory. Once AttachFile names a file, every
// change is written there as JSON, so a restart keeps the operator's
// choices whichever route made them (dashboard, channels API, CLI). At-rest
// JSON is exempt from the no-JSON dashboard wire law: nothing here is served
// to a client.
//
// File shape (version 2):
//
//	{"version": 2,
//	 "defaults": {"CAT": "keep-all"},
//	 "lanes": [{"channel_id": "space-data-network-02-OMM", "subscribed": true},
//	           {"channel_id": "space-data-network-02-CAT", "subscribed": false, "retention": "replace-current"}]}
//
// defaults holds the standard defaults set on this node; a lane's retention
// is its own choice and is absent while the lane follows its standard's
// default. Earlier files are a list: [{"channel_id", "retention"}] or bare
// channel ids, every one subscribed. Their writer saved the rule it applied,
// default or not, and archive-all was never a default, so a listed
// archive-all is kept as a choice and any other word follows the default.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const subscriptionFileVersion = 2

// SubscriptionEntry is one lane in the subscription file.
type SubscriptionEntry struct {
	ChannelID  string `json:"channel_id"`
	Subscribed bool   `json:"subscribed"`
	Retention  string `json:"retention,omitempty"`
}

type subscriptionFileBody struct {
	Version  int                 `json:"version"`
	Defaults map[string]string   `json:"defaults,omitempty"`
	Lanes    []SubscriptionEntry `json:"lanes"`
}

type subscriptionFile struct {
	path string
	logf func(format string, args ...any)
}

// AttachFile loads the subscription file at path (a missing file is not an
// error; a malformed one is) and writes every later change there. logf
// reports a failed write; nil discards it. A file in an earlier shape is
// rewritten in the current one.
func (r *SubscriptionRegistry) AttachFile(path string, logf func(format string, args ...any)) error {
	if r == nil {
		return errors.New("subscription registry is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("subscription file path is required")
	}
	body, legacy, err := readSubscriptionFile(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	for code, word := range body.Defaults {
		code = RetentionStandardCode(code)
		if normalized, ok := NormalizeRetention(word); ok && code != "" && code != NodeDefaultKey {
			r.standardDefaults[code] = normalized
		}
	}
	for _, entry := range body.Lanes {
		parsed, err := ParseChannelID(entry.ChannelID)
		if err != nil {
			continue // ids that no longer parse are dropped, not fatal
		}
		state, ok := r.states[parsed.ChannelID]
		if !ok {
			state = DefaultSubscriptionState(parsed)
		}
		state.Subscribed = entry.Subscribed
		state.Retention = ""
		if word, ok := NormalizeRetention(entry.Retention); ok && (!legacy || word == RetentionArchiveAll) {
			state.Retention = word
		}
		r.states[parsed.ChannelID] = state
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r.file = &subscriptionFile{path: path, logf: logf}
	r.mu.Unlock()
	if legacy {
		r.persist()
	}
	return nil
}

// readSubscriptionFile reads the current shape, else the legacy list.
func readSubscriptionFile(path string) (subscriptionFileBody, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return subscriptionFileBody{}, false, nil
		}
		return subscriptionFileBody{}, false, fmt.Errorf("read subscription file %s: %w", path, err)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return subscriptionFileBody{}, false, nil
	}
	if data[0] == '{' {
		var body subscriptionFileBody
		if err := json.Unmarshal(data, &body); err != nil {
			return subscriptionFileBody{}, false, fmt.Errorf("parse subscription file %s: %w", path, err)
		}
		return body, false, nil
	}
	var entries []SubscriptionEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		var ids []string
		if err := json.Unmarshal(data, &ids); err != nil {
			return subscriptionFileBody{}, false, fmt.Errorf("parse subscription file %s: %w", path, err)
		}
		for _, id := range ids {
			entries = append(entries, SubscriptionEntry{ChannelID: id})
		}
	}
	body := subscriptionFileBody{Lanes: make([]SubscriptionEntry, 0, len(entries))}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ChannelID) == "" {
			continue
		}
		entry.Subscribed = true
		body.Lanes = append(body.Lanes, entry)
	}
	return body, true, nil
}

// persist writes the file when one is attached: atomically (temp file and
// rename), creating its directory when needed.
func (r *SubscriptionRegistry) persist() {
	if r == nil {
		return
	}
	r.mu.RLock()
	file := r.file
	r.mu.RUnlock()
	if file == nil {
		return
	}
	body := subscriptionFileBody{
		Version:  subscriptionFileVersion,
		Defaults: r.standardDefaultsSnapshot(),
		Lanes:    r.lanesSnapshot(),
	}
	if err := writeSubscriptionFile(file.path, body); err != nil {
		file.logf("subscription file %s not saved: %v", file.path, err)
	}
}

func writeSubscriptionFile(path string, body subscriptionFileBody) error {
	data, err := json.MarshalIndent(body, "", "  ")
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
