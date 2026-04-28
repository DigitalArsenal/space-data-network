package license

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestEntitlementStoreEncryptsSensitiveFieldsAtRest(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "entitlements.db")
	store := openTestEncryptedEntitlementStore(t, dbPath, bytes.Repeat([]byte{0x11}, 32))
	ent := testEntitlement()
	if err := store.UpsertEntitlement(ent); err != nil {
		t.Fatalf("UpsertEntitlement failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	rawDB, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile(db) failed: %v", err)
	}
	for _, leaked := range []string{
		ent.XPub,
		ent.PeerID,
		ent.StripeCustomerID,
		ent.StripeSubscriptionID,
		ent.Plan,
	} {
		if bytes.Contains(rawDB, []byte(leaked)) {
			t.Fatalf("sqlite file leaked %q in plaintext", leaked)
		}
	}
}

func TestEntitlementStoreReadsEncryptedEntitlement(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "entitlements.db")
	key := bytes.Repeat([]byte{0x22}, 32)
	store := openTestEncryptedEntitlementStore(t, dbPath, key)
	ent := testEntitlement()
	if err := store.UpsertEntitlement(ent); err != nil {
		t.Fatalf("UpsertEntitlement failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened := openTestEncryptedEntitlementStore(t, dbPath, key)
	defer reopened.Close()
	got, err := reopened.GetEntitlement(ent.XPub)
	if err != nil {
		t.Fatalf("GetEntitlement failed: %v", err)
	}
	if got == nil {
		t.Fatal("GetEntitlement returned nil")
	}
	assertEntitlementEqual(t, got, ent)

	if _, err := NewEncryptedEntitlementStore(dbPath, bytes.Repeat([]byte{0x23}, 32)); err == nil {
		t.Fatal("NewEncryptedEntitlementStore with wrong key succeeded")
	}
}

func TestEntitlementStoreMigratesPlaintextRows(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "entitlements.db")
	ent := testEntitlement()
	writeLegacyPlaintextEntitlement(t, dbPath, ent)

	store := openTestEncryptedEntitlementStore(t, dbPath, bytes.Repeat([]byte{0x33}, 32))
	got, err := store.GetEntitlement(ent.XPub)
	if err != nil {
		t.Fatalf("GetEntitlement failed after migration: %v", err)
	}
	if got == nil {
		t.Fatal("GetEntitlement returned nil after migration")
	}
	assertEntitlementEqual(t, got, ent)
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	rawDB, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile(db) failed: %v", err)
	}
	for _, leaked := range []string{
		ent.XPub,
		ent.PeerID,
		ent.StripeCustomerID,
		ent.StripeSubscriptionID,
		ent.Plan,
	} {
		if bytes.Contains(rawDB, []byte(leaked)) {
			t.Fatalf("migrated sqlite file leaked %q in plaintext", leaked)
		}
	}
}

func openTestEncryptedEntitlementStore(t *testing.T, path string, key []byte) *EntitlementStore {
	t.Helper()
	store, err := NewEncryptedEntitlementStore(path, key)
	if err != nil {
		t.Fatalf("NewEncryptedEntitlementStore failed: %v", err)
	}
	return store
}

func testEntitlement() *Entitlement {
	return &Entitlement{
		XPub:                 "xpub-sensitive-license-owner",
		PeerID:               "12D3KooWEntitlementPeer",
		Plan:                 "orbpro-enterprise",
		Status:               entitlementStatusActive,
		StripeCustomerID:     "cus_sensitive_123",
		StripeSubscriptionID: "sub_sensitive_456",
		ExpiresAt:            time.Now().Add(24 * time.Hour).Unix(),
		UpdatedAt:            time.Now().Unix(),
	}
}

func assertEntitlementEqual(t *testing.T, got, want *Entitlement) {
	t.Helper()
	if got.XPub != want.XPub ||
		got.PeerID != want.PeerID ||
		got.Plan != want.Plan ||
		got.Status != want.Status ||
		got.StripeCustomerID != want.StripeCustomerID ||
		got.StripeSubscriptionID != want.StripeSubscriptionID ||
		got.ExpiresAt != want.ExpiresAt {
		t.Fatalf("entitlement mismatch\ngot:  %+v\nwant: %+v", got, want)
	}
	if got.UpdatedAt <= 0 {
		t.Fatalf("UpdatedAt was not set: %+v", got)
	}
}

func writeLegacyPlaintextEntitlement(t *testing.T, dbPath string, ent *Entitlement) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE entitlements (
	xpub TEXT PRIMARY KEY,
	peer_id TEXT,
	plan TEXT NOT NULL DEFAULT 'free',
	status TEXT NOT NULL DEFAULT 'active',
	stripe_customer_id TEXT,
	stripe_subscription_id TEXT,
	expires_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
INSERT INTO entitlements (
	xpub, peer_id, plan, status, stripe_customer_id, stripe_subscription_id, expires_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
`, ent.XPub, ent.PeerID, ent.Plan, ent.Status, ent.StripeCustomerID, ent.StripeSubscriptionID, ent.ExpiresAt, ent.UpdatedAt)
	if err != nil {
		t.Fatalf("seed legacy entitlement failed: %v", err)
	}
}
