package storage

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func mustNewFlatSQLStore(t *testing.T) *FlatSQLStore {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "flatsql-directory-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

func TestFlatSQLStore_UpsertAndQueryDirectoryRecord(t *testing.T) {
	store := mustNewFlatSQLStore(t)

	record := DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HAmExample",
		DN:             "SDN Node Example",
		BitcoinAddress: "bc1qexample",
		EPMCID:         "bafyexample",
		Source:         "local",
		EPMJSON:        `{"bitcoin_address":"bc1qexample","dn":"SDN Node Example","peer_id":"16Uiu2HAmExample"}`,
		UpdatedAt:      1700000000,
	}

	if err := store.UpsertDirectoryRecord(record); err != nil {
		t.Fatalf("UpsertDirectoryRecord failed: %v", err)
	}

	updated := record
	updated.DN = "SDN Node Example v2"
	updated.BitcoinAddress = "bc1qupdated"
	updated.EPMCID = "bafyupdated"
	updated.Source = "remote"
	updated.EPMJSON = `{"bitcoin_address":"bc1qupdated","dn":"SDN Node Example v2","peer_id":"16Uiu2HAmExample"}`
	updated.UpdatedAt = 1700000100

	if err := store.UpsertDirectoryRecord(updated); err != nil {
		t.Fatalf("second UpsertDirectoryRecord failed: %v", err)
	}

	results, err := store.QueryDirectory(DirectoryQuery{
		Kind:   "node",
		Search: "bc1qupdated",
	})
	if err != nil {
		t.Fatalf("QueryDirectory failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("QueryDirectory returned %d records, want 1", len(results))
	}
	got := results[0]
	if got.PeerID != updated.PeerID || got.EPMCID != updated.EPMCID || got.Source != updated.Source {
		t.Fatalf("unexpected record: %+v", got)
	}
	if got.DN != updated.DN {
		t.Fatalf("DN = %q, want %q", got.DN, updated.DN)
	}
	if got.BitcoinAddress != updated.BitcoinAddress {
		t.Fatalf("BitcoinAddress = %q, want %q", got.BitcoinAddress, updated.BitcoinAddress)
	}
	if got.UpdatedAt != updated.UpdatedAt {
		t.Fatalf("UpdatedAt = %d, want %d", got.UpdatedAt, updated.UpdatedAt)
	}
}

func TestFlatSQLStore_LocalEPMRecordsAreEncryptedAtRest(t *testing.T) {
	store := mustNewFlatSQLStore(t)

	peerID := "16Uiu2HAmLocalProfile"
	epmBytes := []byte("$EPM binary bytes containing jane@example.com")

	if err := store.SaveLocalEPM(peerID, epmBytes); err != nil {
		t.Fatalf("SaveLocalEPM failed: %v", err)
	}

	gotBytes, err := store.LoadLocalEPM(peerID)
	if err != nil {
		t.Fatalf("LoadLocalEPM failed: %v", err)
	}
	if !bytes.Equal(gotBytes, epmBytes) {
		t.Fatalf("EPM bytes = %q, want %q", gotBytes, epmBytes)
	}

	record, err := store.GetLocalEPMRecord(peerID)
	if err != nil {
		t.Fatalf("GetLocalEPMRecord failed: %v", err)
	}
	if !bytes.Equal(record.EPMBytes, epmBytes) {
		t.Fatalf("EPM bytes = %q, want %q", record.EPMBytes, epmBytes)
	}
	columns := localEPMColumns(t, store)
	for _, forbidden := range []string{"encrypted_profile_json", "encrypted_epm_json"} {
		if columns[forbidden] {
			t.Fatalf("sdn_local_epms stores JSON projection column %q; local EPM source of truth must be encrypted EPM.fbs bytes only", forbidden)
		}
	}

	// The statement journal is the at-rest surface for control-table state
	// now (the engine is in-memory; sdn.db no longer exists). Encrypted EPM
	// bytes flow through journaled INSERT params, so plaintext must never
	// appear there. Note: journal params are JSON-encoded (blobs as base64),
	// so also forbid the base64 of the plaintext.
	rawJournal, err := os.ReadFile(filepath.Join(store.basePath, "control.sdnj"))
	if err != nil {
		t.Fatalf("read control journal failed: %v", err)
	}
	forbidden := [][]byte{
		epmBytes,
		[]byte("Jane Example"),
		[]byte("jane@example.com"),
		[]byte("$EPM binary bytes"),
		[]byte(base64.StdEncoding.EncodeToString(epmBytes)),
	}
	for _, f := range forbidden {
		if bytes.Contains(rawJournal, f) {
			t.Fatalf("local EPM store leaked %q into the control journal", f)
		}
	}
}

func localEPMColumns(t *testing.T, store *FlatSQLStore) map[string]bool {
	t.Helper()

	rows, err := store.db.Query(`PRAGMA table_info(sdn_local_epms)`)
	if err != nil {
		t.Fatalf("inspect sdn_local_epms columns: %v", err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan column metadata: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate column metadata: %v", err)
	}
	return columns
}
