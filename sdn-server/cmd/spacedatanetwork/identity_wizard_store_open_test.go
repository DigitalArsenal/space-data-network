package main

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestIdentityWizardStoreOpenDoesNotHydrateTheEngine is the regression for
// sdn-host01-flatsql-engine-poison-blocks-epm.
//
// The wizard reads and writes ONLY auxiliary-journal-backed state (the local
// EPM and the node-EPM directory row). A default open additionally brings the
// engine hot window current for every routed standard — work the command does
// not need, on a code path that once trapped in the guest and poisoned the
// engine, so the local-EPM read that followed failed.
//
// The invariants asserted here are exactly the two that matter:
//  1. the wizard's open does NOT hydrate the engine hot window;
//  2. the local EPM — the only thing the wizard actually reads — is served
//     from that store regardless.
func TestIdentityWizardStoreOpenDoesNotHydrateTheEngine(t *testing.T) {
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

	if store.EngineHotWindowHydrated() {
		t.Fatal("identity wizard opened the store with engine hot-window hydration ON: " +
			"it would rebuild every routed window to read one auxiliary row " +
			"(sdn-host01-flatsql-engine-poison-blocks-epm)")
	}

	got, err := store.LoadLocalEPM(peerID)
	if err != nil {
		t.Fatalf("LoadLocalEPM from a deferred store: %v", err)
	}
	if string(got) != string(epmBytes) {
		t.Fatalf("LoadLocalEPM = %q, want %q", got, epmBytes)
	}
}
