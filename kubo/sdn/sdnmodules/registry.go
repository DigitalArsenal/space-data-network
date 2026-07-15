// Package sdnmodules is the SDN node WASM-module install + register pipeline: it
// turns a real module-sdk WASM artifact into a running, cron-scheduled
// modulert.Module on a kubo-based SDN node, and persists which modules are
// installed so the set re-registers on the next boot.
//
// It is the missing seam flagged by the cron worker: sdnservices.BuildServices
// wires the store, channels, capability registry and an (empty) cron Scheduler,
// but nothing actually installed a WASM module and registered it as a running
// *modulert.Module in that scheduler — the cron demo used a NATIVE heartbeat
// stub. This package closes that gap with a single Installer that, given a
// module's portable WASM bytes (resolved from the blockstore by CONTENT_HASH via
// appmanifest, or handed in directly for a drop-in / embedded set):
//
//  1. verifies the operator capability policy FAIL CLOSED (this is inherent in
//     Services.LoadModule → modulert.NewModule → checkCapabilityPolicy, keyed by
//     the module's content hash, NOT its self-declared plugin id) — an
//     unapproved sensitive capability refuses the whole install, no partial
//     grant;
//  2. LoadModules the WASM into a live *modulert.Module wired to the node's
//     services (storage_*/pubsub/schedule_cron capabilities);
//  3. registers that real module with the cron Scheduler under its manifest
//     Timers (overridden by the per-module home-dir config), so the scheduler
//     fires the module's real InvokeCron on its effective cadence and
//     GET /sdn/v1/modules lists it;
//  4. records the install in a persisted installed-modules registry so a later
//     boot re-resolves the bytes by content hash and re-registers it.
//
// # Home-directory layout
//
// Everything lives under the node's SDN module config root, <repo>/sdn/modules:
//
//	installed.json         the installed-modules registry (this file) — one
//	                       entry per installed module: id, content_hash,
//	                       name/version, enabled, source, installed_at.
//	<moduleId>.json        the per-module cron + opaque-input config
//	                       (sdncron.ConfigStore — unchanged).
//	install/*.wasm         optional operator drop-in directory: any *.wasm here
//	                       is installed at boot (still fail-closed — a drop-in
//	                       module declaring a sensitive capability installs only
//	                       if an operator has approved that capability for its
//	                       content hash).
package sdnmodules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// registryFileName is the installed-modules registry file, kept alongside the
// per-module <moduleId>.json config files under <repo>/sdn/modules.
const registryFileName = "installed.json"

// InstalledEntry is one persisted installed-modules registry record. It is the
// durable "this module is installed" fact: the content hash re-resolves the
// exact WASM bytes from the blockstore on the next boot, and enabled gates
// whether that boot re-registers it with the scheduler.
type InstalledEntry struct {
	// ID is the module's manifest PluginID (its scheduler id + config file base
	// name). Display/id only — trust is keyed on ContentHash, never this.
	ID string `json:"id"`
	// ContentHash is the lowercase hex SHA-256 of the portable WASM artifact —
	// the capability-policy identity and the blockstore address the bytes
	// re-resolve from on a later boot.
	ContentHash string `json:"content_hash"`
	// Name / Version are the manifest display fields captured at install time so
	// the registry is human-readable without re-loading the WASM.
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// Enabled gates boot re-registration. A disabled entry stays recorded (its
	// bytes stay resolvable) but is not registered with the scheduler at boot.
	Enabled bool `json:"enabled"`
	// Source is a provenance tag: "admin" (loopback admin route), "dropin:<file>"
	// (install/ directory), or "direct" (an in-process InstallBytes call).
	Source string `json:"source,omitempty"`
	// InstalledAt is the RFC3339 UTC time the entry was first recorded.
	InstalledAt string `json:"installed_at,omitempty"`
}

// registryFile is the on-disk JSON envelope: a single object with a "modules"
// array, so the file can gain sibling keys later without a format break.
type registryFile struct {
	Modules []InstalledEntry `json:"modules"`
}

// Registry persists the installed-modules set as one JSON file
// (<repo>/sdn/modules/installed.json, 0600, atomic writes). An empty dir puts it
// in no-persistence mode: Put/Remove are no-ops and Load returns nothing, so a
// node with no repo path still runs (installs just do not survive a restart),
// mirroring sdncron.ConfigStore.
type Registry struct {
	dir string
	mu  sync.Mutex
}

// NewRegistry opens (creating if needed) the module registry directory. An empty
// dir selects no-persistence mode.
func NewRegistry(dir string) (*Registry, error) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("sdnmodules: create registry dir %q: %w", dir, err)
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

// Path returns the on-disk registry file path ("" in no-persistence mode).
func (r *Registry) Path() string {
	if r == nil || r.dir == "" {
		return ""
	}
	return filepath.Join(r.dir, registryFileName)
}

// List returns every persisted entry, sorted by id. Empty (never nil) in
// no-persistence mode or when nothing is installed.
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
// InstalledAt on replace. No-op (returns nil) in no-persistence mode.
func (r *Registry) Put(e InstalledEntry) error {
	if r == nil || r.dir == "" {
		return nil
	}
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("sdnmodules: registry entry has empty id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := r.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == e.ID {
			if e.InstalledAt == "" {
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
	changed := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Enabled = enabled
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return r.saveLocked(entries)
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

// loadLocked reads and decodes the registry file. A missing file yields an empty
// list. Caller holds r.mu.
func (r *Registry) loadLocked() ([]InstalledEntry, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, registryFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return []InstalledEntry{}, nil
		}
		return nil, fmt.Errorf("sdnmodules: read registry: %w", err)
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("sdnmodules: parse registry: %w", err)
	}
	if rf.Modules == nil {
		rf.Modules = []InstalledEntry{}
	}
	sort.Slice(rf.Modules, func(i, j int) bool { return rf.Modules[i].ID < rf.Modules[j].ID })
	return rf.Modules, nil
}

// saveLocked writes the registry atomically (temp + rename) at 0600. Caller
// holds r.mu.
func (r *Registry) saveLocked(entries []InstalledEntry) error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return fmt.Errorf("sdnmodules: create registry dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	data, err := json.MarshalIndent(registryFile{Modules: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("sdnmodules: encode registry: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(r.dir, "."+registryFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("sdnmodules: temp registry: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("sdnmodules: write registry: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("sdnmodules: chmod registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sdnmodules: close registry: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(r.dir, registryFileName)); err != nil {
		return fmt.Errorf("sdnmodules: commit registry: %w", err)
	}
	return nil
}
