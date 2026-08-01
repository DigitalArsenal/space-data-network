package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// The local-EPM row envelope must stay readable across every password source
// this box could have sealed it under: the daemon and the CLI can resolve
// DIFFERENT sources for the same store (unit files set SDN_KEY_PASSWORD_FILE,
// which the old derivation silently ignored, falling back to the fragile
// hostname composite). These tests pin the candidate-chain behavior that
// repaired host-02's orphaned row on 2026-08-01.

func newLocalEPMKeyTestStore(t *testing.T, dir string) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	store, err := NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

// A row sealed under the hostname-composite fallback (no password env at all —
// what every password-file unit unintentionally used before the fix) must
// still decrypt after SDN_KEY_PASSWORD_FILE starts being honored.
func TestLocalEPMRowSealedUnderFallbackReadableWithPasswordFile(t *testing.T) {
	dir := t.TempDir()
	epmBytes := []byte("epm-payload-fallback-era")

	t.Setenv("SDN_EPM_STORE_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD_FILE", "")
	store := newLocalEPMKeyTestStore(t, dir)
	if err := store.SaveLocalEPM("12D3KooWFallbackEra", epmBytes); err != nil {
		t.Fatalf("SaveLocalEPM under fallback key: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeding store: %v", err)
	}

	passwordPath := filepath.Join(t.TempDir(), "key_password")
	if err := os.WriteFile(passwordPath, []byte("unit-password\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	t.Setenv("SDN_KEY_PASSWORD_FILE", passwordPath)
	reopened := newLocalEPMKeyTestStore(t, dir)
	defer reopened.Close()

	got, err := reopened.LoadLocalEPM("12D3KooWFallbackEra")
	if err != nil {
		t.Fatalf("LoadLocalEPM after adding password file: %v", err)
	}
	if string(got) != string(epmBytes) {
		t.Fatalf("payload mismatch: got %q want %q", got, epmBytes)
	}
}

// New writes must seal under the password file when it is the best available
// source, and must be readable by a process that resolves the same password
// via SDN_KEY_PASSWORD instead (the daemon/CLI asymmetry).
func TestLocalEPMRowSealedUnderPasswordFileReadableWithPasswordEnv(t *testing.T) {
	dir := t.TempDir()
	epmBytes := []byte("epm-payload-password-file-era")

	passwordPath := filepath.Join(t.TempDir(), "key_password")
	if err := os.WriteFile(passwordPath, []byte("unit-password\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	t.Setenv("SDN_EPM_STORE_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD_FILE", passwordPath)
	store := newLocalEPMKeyTestStore(t, dir)
	if err := store.SaveLocalEPM("12D3KooWFileEra", epmBytes); err != nil {
		t.Fatalf("SaveLocalEPM under password-file key: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeding store: %v", err)
	}

	t.Setenv("SDN_KEY_PASSWORD_FILE", "")
	t.Setenv("SDN_KEY_PASSWORD", "unit-password")
	reopened := newLocalEPMKeyTestStore(t, dir)
	defer reopened.Close()

	got, err := reopened.LoadLocalEPM("12D3KooWFileEra")
	if err != nil {
		t.Fatalf("LoadLocalEPM via SDN_KEY_PASSWORD: %v", err)
	}
	if string(got) != string(epmBytes) {
		t.Fatalf("payload mismatch: got %q want %q", got, epmBytes)
	}
}

// A row sealed under inputs this box no longer has must fail with the decrypt
// error the wizard's rebuild fallback keys on — not silently succeed.
func TestLocalEPMRowOrphanedEnvelopeStillFailsClosed(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("SDN_EPM_STORE_PASSWORD", "password-this-box-lost")
	t.Setenv("SDN_KEY_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD_FILE", "")
	store := newLocalEPMKeyTestStore(t, dir)
	if err := store.SaveLocalEPM("12D3KooWOrphaned", []byte("unrecoverable")); err != nil {
		t.Fatalf("SaveLocalEPM: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeding store: %v", err)
	}

	t.Setenv("SDN_EPM_STORE_PASSWORD", "")
	reopened := newLocalEPMKeyTestStore(t, dir)
	defer reopened.Close()

	_, err := reopened.LoadLocalEPM("12D3KooWOrphaned")
	if err == nil {
		t.Fatal("expected decrypt failure for orphaned envelope")
	}
	if !strings.Contains(err.Error(), "decrypt local EPM bytes") {
		t.Fatalf("orphaned envelope must surface the decrypt marker the wizard fallback matches; got: %v", err)
	}
}
