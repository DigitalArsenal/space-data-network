package main

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Regression for the 2026-08-01 host-02 lockout: a stored local-EPM row whose
// envelope no candidate key can open (sealed under a hostname/home-dir/store
// layout this box no longer has) must NOT abort the wizard. The identity keys
// hold everything needed to rebuild the profile (owner ruling 2026-07-31), so
// the wizard falls back to a fresh profile and its save overwrites the
// orphaned row. Before the fix this path returned "load local EPM profile:
// decrypt local EPM bytes: cipher: message authentication failed" and the node
// could never be renamed.
func TestIdentityWizardProfileSourceRecoversFromUndecryptableLocalEPM(t *testing.T) {
	dir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	const peerIDStr = "16Uiu2HAmTestPeerIDForOrphanedEPMRegression0000000000"

	// Seed a row under a password this box then "loses".
	t.Setenv("SDN_EPM_STORE_PASSWORD", "password-this-box-lost")
	t.Setenv("SDN_KEY_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD_FILE", "")
	seed, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.SaveLocalEPM(peerIDStr, []byte("EPM-BYTES-SEALED-UNDER-LOST-KEY")); err != nil {
		seed.Close()
		t.Fatalf("seed SaveLocalEPM: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	t.Setenv("SDN_EPM_STORE_PASSWORD", "")
	store, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	source, err := loadIdentityWizardStoredProfileSource(
		identityWizardStoreFromHandle(store), peer.ID(peerIDStr), t.TempDir())
	if err != nil {
		t.Fatalf("wizard must recover from an undecryptable local EPM row, got: %v", err)
	}
	if source.Profile == nil {
		t.Fatal("recovered profile source has no profile")
	}
	if len(source.SourceEPM) != 0 {
		t.Fatal("recovered profile source must not carry undecryptable source EPM bytes")
	}
}
