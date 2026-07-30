package main

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestIdentityWizardStoreOpenDoesNotHydrateRecordCatalog is the regression for
// sdn-host01-flatsql-engine-poison-blocks-epm.
//
// The wizard reads and writes ONLY auxiliary-journal-backed state (the local
// EPM and the node-EPM directory row). A default open additionally replays the
// compact record catalog, which on sdn.spaceaware.io is 450 MB / 1,344,427
// frames: ~431 s of work the command does not need, and the replay traps in
// the guest and poisons the engine, so the local-EPM read that follows fails.
//
// The invariants asserted here are exactly the two that matter:
//  1. the wizard's open does NOT hydrate the record catalog, so it can never
//     enter the trapping replay;
//  2. the local EPM — the only thing the wizard actually reads — is still
//     served from that un-hydrated store.
func TestIdentityWizardStoreOpenDoesNotHydrateRecordCatalog(t *testing.T) {
	dir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	const peerID = "16Uiu2HAmTestPeerIDForWizardStoreOpenRegression000000"
	epmBytes := []byte("EPM-FIXTURE-BYTES-FOR-WIZARD-STORE-OPEN")

	seed, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.SaveLocalEPM(peerID, epmBytes); err != nil {
		seed.Close()
		t.Fatalf("seed SaveLocalEPM: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	store, err := openIdentityWizardStore(dir, validator)
	if err != nil {
		t.Fatalf("openIdentityWizardStore: %v", err)
	}
	defer store.Close()

	if store.RecordCatalogHydrated() {
		t.Fatal("identity wizard opened the store with record-catalog hydration ON: " +
			"it would replay the whole catalog to read one auxiliary row, and on a " +
			"catalog-scale store that replay traps and poisons the engine " +
			"(sdn-host01-flatsql-engine-poison-blocks-epm)")
	}

	got, err := store.LoadLocalEPM(peerID)
	if err != nil {
		t.Fatalf("LoadLocalEPM from an un-hydrated store: %v", err)
	}
	if string(got) != string(epmBytes) {
		t.Fatalf("LoadLocalEPM = %q, want %q", got, epmBytes)
	}
}
