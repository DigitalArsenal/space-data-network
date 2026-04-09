package flowrt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstalledFlow represents a flow artifact stored on disk.
type InstalledFlow struct {
	ProgramID string `json:"programId"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Dir       string `json:"-"`
}

// FlowStore manages installed flow artifacts on disk.
// Layout:
//
//	{basePath}/
//	  {programId}/
//	    artifact.json   — compiled artifact metadata
//	    runtime.wasm    — WASM binary
//	    flow.json       — original flow graph (for editor round-trip)
type FlowStore struct {
	basePath string
}

// NewFlowStore creates a FlowStore backed by the given directory.
func NewFlowStore(basePath string) (*FlowStore, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create flows dir: %w", err)
	}
	return &FlowStore{basePath: basePath}, nil
}

// Install writes a flow artifact to disk.
func (s *FlowStore) Install(programID string, wasmBytes []byte, flowJSON []byte, artifactMeta []byte) error {
	dir := filepath.Join(s.basePath, sanitizeID(programID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create flow dir: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "runtime.wasm"), wasmBytes, 0644); err != nil {
		return fmt.Errorf("write wasm: %w", err)
	}
	if len(flowJSON) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "flow.json"), flowJSON, 0644); err != nil {
			return fmt.Errorf("write flow.json: %w", err)
		}
	}
	if len(artifactMeta) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "artifact.json"), artifactMeta, 0644); err != nil {
			return fmt.Errorf("write artifact.json: %w", err)
		}
	}
	return nil
}

// Remove deletes a flow artifact from disk.
func (s *FlowStore) Remove(programID string) error {
	dir := filepath.Join(s.basePath, sanitizeID(programID))
	return os.RemoveAll(dir)
}

// List returns all installed flows.
func (s *FlowStore) List() ([]*InstalledFlow, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var flows []*InstalledFlow
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.basePath, entry.Name())
		wasmPath := filepath.Join(dir, "runtime.wasm")
		if _, err := os.Stat(wasmPath); err != nil {
			continue // not a valid flow dir
		}

		flow := &InstalledFlow{
			ProgramID: entry.Name(),
			Dir:       dir,
		}

		// Try to read flow.json for metadata
		if data, err := os.ReadFile(filepath.Join(dir, "flow.json")); err == nil {
			var prog FlowProgram
			if json.Unmarshal(data, &prog) == nil {
				flow.ProgramID = prog.ProgramID
				flow.Name = prog.Name
				flow.Version = prog.Version
			}
		}

		flows = append(flows, flow)
	}
	return flows, nil
}

// Get returns an installed flow by program ID.
func (s *FlowStore) Get(programID string) (*InstalledFlow, error) {
	dir := filepath.Join(s.basePath, sanitizeID(programID))
	wasmPath := filepath.Join(dir, "runtime.wasm")
	if _, err := os.Stat(wasmPath); err != nil {
		return nil, fmt.Errorf("flow %q not found", programID)
	}

	flow := &InstalledFlow{
		ProgramID: programID,
		Dir:       dir,
	}

	if data, err := os.ReadFile(filepath.Join(dir, "flow.json")); err == nil {
		var prog FlowProgram
		if json.Unmarshal(data, &prog) == nil {
			flow.Name = prog.Name
			flow.Version = prog.Version
		}
	}
	return flow, nil
}

// WASMPath returns the path to the WASM binary for an installed flow.
func (s *FlowStore) WASMPath(programID string) string {
	return filepath.Join(s.basePath, sanitizeID(programID), "runtime.wasm")
}

// FlowJSONPath returns the path to the flow.json for an installed flow.
func (s *FlowStore) FlowJSONPath(programID string) string {
	return filepath.Join(s.basePath, sanitizeID(programID), "flow.json")
}

// sanitizeID replaces path-unsafe characters in a program ID.
func sanitizeID(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "..", "_")
	return r.Replace(id)
}
