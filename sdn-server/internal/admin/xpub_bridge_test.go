package admin

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// newTestXPub returns a syntactically valid (per auth.IsValidXPub), but
// otherwise meaningless, placeholder xpub string for tests. It is never a
// real hd-wallet derived key — just random bytes shaped like one — matching
// the convention already used by internal/auth/tofu_test.go's "xpub-bound"
// style placeholders.
func newTestXPub(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	xpub := "xpub" + hex.EncodeToString(raw)
	if !auth.IsValidXPub(xpub) {
		t.Fatalf("test helper produced an invalid xpub: %q", xpub)
	}
	return xpub
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "sdn-admin-xpub-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestBindXPub_RejectsInvalidXPub(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateAdmin("testadmin", "TestPassword123"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, err := m.Authenticate("testadmin", "TestPassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	session, err := m.ValidateSession(token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	for _, bad := range []string{"", "not-an-xpub", "xpub", "   "} {
		if err := m.BindXPub(session.AdminID, bad); err != ErrInvalidXPub {
			t.Errorf("BindXPub(%q) error = %v, want ErrInvalidXPub", bad, err)
		}
	}
}

func TestBindXPub_RoundTrip(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateAdmin("testadmin", "TestPassword123"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, err := m.Authenticate("testadmin", "TestPassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	session, err := m.ValidateSession(token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	// Before binding, the admin resolves with no xpub identity.
	adm, err := m.GetAdmin(session.AdminID)
	if err != nil {
		t.Fatalf("GetAdmin: %v", err)
	}
	if adm.XPub != "" {
		t.Fatalf("XPub should be empty before binding, got %q", adm.XPub)
	}

	xpub := newTestXPub(t)
	if err := m.BindXPub(session.AdminID, xpub); err != nil {
		t.Fatalf("BindXPub: %v", err)
	}

	adm, err = m.GetAdmin(session.AdminID)
	if err != nil {
		t.Fatalf("GetAdmin after bind: %v", err)
	}
	if adm.XPub != xpub {
		t.Errorf("GetAdmin().XPub = %q, want %q", adm.XPub, xpub)
	}

	byXPub, err := m.GetAdminByXPub(xpub)
	if err != nil {
		t.Fatalf("GetAdminByXPub: %v", err)
	}
	if byXPub == nil {
		t.Fatal("GetAdminByXPub returned nil, want the bound admin")
	}
	if byXPub.ID != session.AdminID || byXPub.Username != "testadmin" {
		t.Errorf("GetAdminByXPub returned %+v, want admin ID %d (testadmin)", byXPub, session.AdminID)
	}

	// An unbound/unknown xpub must resolve to nil, nil — mirroring
	// auth.UserStore.GetUser's not-found convention.
	unknown, err := m.GetAdminByXPub(newTestXPub(t))
	if err != nil {
		t.Fatalf("GetAdminByXPub(unknown): unexpected error %v", err)
	}
	if unknown != nil {
		t.Errorf("GetAdminByXPub(unknown) = %+v, want nil", unknown)
	}
}

func TestBindXPub_RejectsDuplicateAcrossAdmins(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateAdmin("admin-one", "TestPassword123"); err != nil {
		t.Fatalf("CreateAdmin(admin-one): %v", err)
	}
	if err := m.CreateAdmin("admin-two", "TestPassword123"); err != nil {
		t.Fatalf("CreateAdmin(admin-two): %v", err)
	}

	tok1, err := m.Authenticate("admin-one", "TestPassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Fatalf("Authenticate(admin-one): %v", err)
	}
	s1, err := m.ValidateSession(tok1)
	if err != nil {
		t.Fatalf("ValidateSession(admin-one): %v", err)
	}

	tok2, err := m.Authenticate("admin-two", "TestPassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Fatalf("Authenticate(admin-two): %v", err)
	}
	s2, err := m.ValidateSession(tok2)
	if err != nil {
		t.Fatalf("ValidateSession(admin-two): %v", err)
	}

	xpub := newTestXPub(t)
	if err := m.BindXPub(s1.AdminID, xpub); err != nil {
		t.Fatalf("BindXPub(admin-one): %v", err)
	}
	if err := m.BindXPub(s2.AdminID, xpub); err != ErrXPubInUse {
		t.Errorf("BindXPub(admin-two, same xpub) error = %v, want ErrXPubInUse", err)
	}

	// Re-binding the same admin to the same xpub it already owns must be a
	// no-op success, not a conflict with itself.
	if err := m.BindXPub(s1.AdminID, xpub); err != nil {
		t.Errorf("re-binding admin-one to its own xpub should succeed, got %v", err)
	}
}

// TestAdminXPubResolvesToUserStoreIdentity is the F1 cross-package
// regression test: a user registered by xpub in internal/auth.UserStore (the
// identity authority) and an admin account bound to that same xpub via
// Manager.BindXPub must resolve to the SAME identity — same xpub, same
// name/trust semantics — rather than the admin auth path minting or
// representing a second, divergent identity.
func TestAdminXPubResolvesToUserStoreIdentity(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateAdmin("bridge-admin", "TestPassword123"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, err := m.Authenticate("bridge-admin", "TestPassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	session, err := m.ValidateSession(token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	adm, err := m.GetAdmin(session.AdminID)
	if err != nil {
		t.Fatalf("GetAdmin: %v", err)
	}

	xpub := newTestXPub(t)
	if err := m.BindXPub(adm.ID, xpub); err != nil {
		t.Fatalf("BindXPub: %v", err)
	}

	// The UserStore is the identity authority: register the same xpub there
	// with Admin trust, as the HTTP bridge documented on BindXPub would do
	// the first time an admin account is bound.
	userDBPath := t.TempDir() + "/users.db"

	userStore, err := auth.NewUserStore(userDBPath, nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	if err := userStore.AddUser(xpub, adm.Username, peers.Admin, ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	// Resolve through both stores by the same xpub and assert they describe
	// one identity, not two.
	fromAdmin, err := m.GetAdminByXPub(xpub)
	if err != nil {
		t.Fatalf("GetAdminByXPub: %v", err)
	}
	if fromAdmin == nil {
		t.Fatal("GetAdminByXPub returned nil after binding")
	}

	fromUserStore, err := userStore.GetUser(xpub)
	if err != nil {
		t.Fatalf("userStore.GetUser: %v", err)
	}
	if fromUserStore == nil {
		t.Fatal("userStore.GetUser returned nil after AddUser")
	}

	if fromUserStore.XPub != fromAdmin.XPub {
		t.Errorf("identity divergence: admin bridge xpub %q != userstore xpub %q", fromAdmin.XPub, fromUserStore.XPub)
	}
	if fromUserStore.Name != fromAdmin.Username {
		t.Errorf("identity divergence: userstore name %q != admin username %q", fromUserStore.Name, fromAdmin.Username)
	}
	if fromUserStore.TrustLevel != peers.Admin {
		t.Errorf("bridged identity trust level = %v, want peers.Admin", fromUserStore.TrustLevel)
	}
}
