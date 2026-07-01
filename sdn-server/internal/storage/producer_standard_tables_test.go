package storage

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestProducerStandardTableName(t *testing.T) {
	name, err := ProducerStandardTableName("12D3KooWabc", "OMM.fbs")
	if err != nil {
		t.Fatalf("ProducerStandardTableName() error = %v", err)
	}
	if name != "sds_p_12D3KooWabc__OMM" {
		t.Errorf("name = %q, want sds_p_12D3KooWabc__OMM", name)
	}

	// Distinct producers -> distinct tables.
	other, err := ProducerStandardTableName("peerB", "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if other == name {
		t.Error("distinct producers must yield distinct tables")
	}

	// Distinct standards -> distinct tables.
	oem, err := ProducerStandardTableName("12D3KooWabc", "OEM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if oem == name {
		t.Error("distinct standards must yield distinct tables")
	}

	// Non-identifier characters in the producer are sanitized.
	sani, err := ProducerStandardTableName("peer:with/weird.chars", "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if sani != "sds_p_peer_with_weird_chars__OMM" {
		t.Errorf("sanitized = %q, want sds_p_peer_with_weird_chars__OMM", sani)
	}

	// Empty producer -> error.
	if _, err := ProducerStandardTableName("", "OMM.fbs"); err == nil {
		t.Error("expected error for empty producer")
	}
	// Invalid schema name -> error (propagated from SchemaNameToTable).
	if _, err := ProducerStandardTableName("peerA", "not a schema"); err == nil {
		t.Error("expected error for invalid schema name")
	}
}

func TestEnsureProducerStandardTable(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	name, err := store.ensureProducerStandardTable("peerA", "OMM.fbs")
	if err != nil {
		t.Fatalf("ensureProducerStandardTable() error = %v", err)
	}
	if name != "sds_p_peerA__OMM" {
		t.Errorf("name = %q, want sds_p_peerA__OMM", name)
	}
	if exists, err := store.tableExists(name); err != nil || !exists {
		t.Fatalf("table %s should exist after ensure (exists=%v err=%v)", name, exists, err)
	}

	// Idempotent: a second ensure returns the same table without error.
	name2, err := store.ensureProducerStandardTable("peerA", "OMM.fbs")
	if err != nil || name2 != name {
		t.Errorf("idempotent ensure failed: name=%q err=%v", name2, err)
	}

	// A different producer materializes a separate table.
	nameB, err := store.ensureProducerStandardTable("peerB", "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if nameB == name {
		t.Error("distinct producer must create a distinct table")
	}
	if exists, err := store.tableExists(nameB); err != nil || !exists {
		t.Fatalf("peerB table %s should exist (exists=%v err=%v)", nameB, exists, err)
	}
}
