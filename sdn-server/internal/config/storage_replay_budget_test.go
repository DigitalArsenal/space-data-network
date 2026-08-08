package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// The auxiliary-journal replay byte bound shipped defaulted (b6c21e87) with no
// way for an operator to move it. These tests lock the operator knob to the two
// properties the wire in node.go depends on: the YAML key parses to the field,
// and an ABSENT key stays zero so the store keeps its built-in default. If the
// absent case ever resolved to something non-zero, every existing config on the
// fleet would silently change its replay budget on the next boot.

func TestStorageAuxiliaryReplayChunkBytesParsesFromYAML(t *testing.T) {
	var cfg StorageConfig
	if err := yaml.Unmarshal([]byte("path: /var/lib/sdn\nauxiliary_replay_chunk_bytes: 4194304\n"), &cfg); err != nil {
		t.Fatalf("unmarshal storage config: %v", err)
	}
	if cfg.AuxiliaryReplayChunkBytes != 4<<20 {
		t.Fatalf("auxiliary_replay_chunk_bytes parsed as %d, want %d", cfg.AuxiliaryReplayChunkBytes, 4<<20)
	}
}

func TestStorageAuxiliaryReplayChunkBytesAbsentKeepsStoreDefault(t *testing.T) {
	var cfg StorageConfig
	if err := yaml.Unmarshal([]byte("path: /var/lib/sdn\n"), &cfg); err != nil {
		t.Fatalf("unmarshal storage config: %v", err)
	}
	if cfg.AuxiliaryReplayChunkBytes != 0 {
		t.Fatalf("absent auxiliary_replay_chunk_bytes resolved to %d, want 0 (store default)", cfg.AuxiliaryReplayChunkBytes)
	}
	// Defaults must not mint a value either: Default() is what a fresh install
	// writes, and a written-out number would freeze today's default into every
	// config file on disk.
	if def := Default(); def.Storage.AuxiliaryReplayChunkBytes != 0 {
		t.Fatalf("Default() set auxiliary_replay_chunk_bytes to %d, want 0", def.Storage.AuxiliaryReplayChunkBytes)
	}
}
