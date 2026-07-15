package sdnflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// registryFileName is the installed-flows registry file under <repo>/sdn/flows.
const registryFileName = "installed-flows.json"

// InstalledEntry is one persisted installed-flows registry record. It captures
// enough to re-load and re-register the flow on a later boot: the bundle
// reference plus the per-install interval overrides and node config.
type InstalledEntry struct {
	// ID is the flow's programId (its scheduler id). Display/id only.
	ID string `json:"id"`
	// Ref is the bundle reference (directory holding runtime.wasm + flow.json)
	// the bytes re-load from on a later boot.
	Ref string `json:"ref"`
	// Name / Version are display fields captured at install time.
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// Intervals are per-trigger interval overrides (triggerId -> duration).
	Intervals map[string]string `json:"intervals,omitempty"`
	// Config is the per-flow node CONFIG served via plugin.getConfig.
	Config map[string]interface{} `json:"config,omitempty"`
	// Enabled gates boot re-registration.
	Enabled bool `json:"enabled"`
	// Source is a provenance tag: "admin", "boot-set", "direct".
	Source string `json:"source,omitempty"`
	// InstalledAt is the RFC3339 UTC time the entry was first recorded.
	InstalledAt string `json:"installed_at,omitempty"`
}

type registryFile struct {
	Flows []InstalledEntry `json:"flows"`
}

// Registry persists the installed-flows set as one JSON file
// (<repo>/sdn/flows/installed-flows.json, 0600, atomic writes). An empty dir
// puts it in no-persistence mode: Put/Remove are no-ops and List returns
// nothing, so a node with no repo path still runs (installs just do not survive
// a restart).
type Registry struct {
	dir string
	mu  sync.Mutex
}

// NewRegistry opens (creating if needed) the flow registry directory. An empty
// dir selects no-persistence mode.
func NewRegistry(dir string) (*Registry, error) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("sdnflows: create registry dir %q: %w", dir, err)
		}
	}
	return &Registry{dir: dir}, nil
}

// Dir returns the registry directory ("" in no-persistence mode).
func (r *Registry) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// List returns every persisted entry, sorted by id.
func (r *Registry) List() ([]InstalledEntry, error) {
	if r == nil || r.dir == "" {
		return []InstalledEntry{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadLocked()
}

// Get returns the entry for id and whether it exists.
func (r *Registry) Get(id string) (InstalledEntry, bool, error) {
	entries, err := r.List()
	if err != nil {
		return InstalledEntry{}, false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true, nil
		}
	}
	return InstalledEntry{}, false, nil
}

// Put inserts or replaces the entry for e.ID, preserving the original
// InstalledAt on replace. No-op in no-persistence mode.
func (r *Registry) Put(e InstalledEntry) error {
	if r == nil || r.dir == "" {
		return nil
	}
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("sdnflows: registry entry has empty id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := r.loadLocked()
	if err != nil {
		return err
	}
	if e.InstalledAt == "" {
		e.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == e.ID {
			if entries[i].InstalledAt != "" {
				e.InstalledAt = entries[i].InstalledAt
			}
			entries[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, e)
	}
	return r.saveLocked(entries)
}

// SetEnabled flips an entry's enabled flag. Unknown id is a no-op.
func (r *Registry) SetEnabled(id string, enabled bool) error {
	if r == nil || r.dir == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := r.loadLocked()
	if err != nil {
		return err
	}
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Enabled = enabled
			return r.saveLocked(entries)
		}
	}
	return nil
}

// Remove deletes an entry. Unknown id is a no-op.
func (r *Registry) Remove(id string) error {
	if r == nil || r.dir == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := r.loadLocked()
	if err != nil {
		return err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return r.saveLocked(out)
}

func (r *Registry) loadLocked() ([]InstalledEntry, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, registryFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return []InstalledEntry{}, nil
		}
		return nil, fmt.Errorf("sdnflows: read registry: %w", err)
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("sdnflows: parse registry: %w", err)
	}
	if rf.Flows == nil {
		rf.Flows = []InstalledEntry{}
	}
	sort.Slice(rf.Flows, func(i, j int) bool { return rf.Flows[i].ID < rf.Flows[j].ID })
	return rf.Flows, nil
}

func (r *Registry) saveLocked(entries []InstalledEntry) error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return fmt.Errorf("sdnflows: create registry dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	data, err := json.MarshalIndent(registryFile{Flows: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("sdnflows: encode registry: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(r.dir, "."+registryFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("sdnflows: temp registry: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("sdnflows: write registry: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("sdnflows: chmod registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sdnflows: close registry: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(r.dir, registryFileName)); err != nil {
		return fmt.Errorf("sdnflows: commit registry: %w", err)
	}
	return nil
}
