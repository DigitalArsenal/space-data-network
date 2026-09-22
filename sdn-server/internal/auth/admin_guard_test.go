package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestLastAdminCannotBeRemovedOrDemoted(t *testing.T) {
	store, err := NewUserStore(filepath.Join(t.TempDir(), "auth.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key := func(b string) string { return strings.Repeat(b, 64) }
	if err := store.AddUser("ed25519:a", "Alice", peers.Admin, key("a")); err != nil {
		t.Fatal(err)
	}
	if err := store.AddUser("ed25519:v", "Viewer", peers.Standard, key("b")); err != nil {
		t.Fatal(err)
	}

	if err := store.RemoveUser("ed25519:a"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("remove last admin: %v", err)
	}
	if err := store.UpdateTrust("ed25519:a", peers.Trusted); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
	// Re-asserting admin trust (a rename sends it along) is not a demotion.
	if err := store.UpdateTrust("ed25519:a", peers.Admin); err != nil {
		t.Fatalf("keep last admin at admin: %v", err)
	}
	// Non-admins are unaffected.
	if err := store.RemoveUser("ed25519:v"); err != nil {
		t.Fatalf("remove viewer: %v", err)
	}

	if err := store.AddUser("ed25519:b", "Bob", peers.Admin, key("c")); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTrust("ed25519:a", peers.Trusted); err != nil {
		t.Fatalf("demote with another admin present: %v", err)
	}
	if err := store.RemoveUser("ed25519:b"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("remove the admin that became last: %v", err)
	}
}

func TestConfigAdminCountsTowardTheLastAdminGuard(t *testing.T) {
	store, err := NewUserStore(filepath.Join(t.TempDir(), "auth.db"), []config.UserEntry{
		{XPub: "xpub-config-admin", TrustLevel: "admin", Name: "Config Admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddUser("ed25519:a", "Alice", peers.Admin, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveUser("ed25519:a"); err != nil {
		t.Fatalf("remove with a config admin present: %v", err)
	}
}

func TestUserAPIRefusesRemovingTheLastAdminWithConflict(t *testing.T) {
	h, token, xpub := profileFixture(t, peers.Admin, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/auth/users/"+xpub, nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	h.handleUserByXPub(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"last_admin"`) {
		t.Fatalf("DELETE last admin = %d %s, want 409 last_admin", rec.Code, rec.Body.String())
	}
}
