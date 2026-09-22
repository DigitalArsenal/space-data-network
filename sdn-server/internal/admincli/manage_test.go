package admincli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func seed(t *testing.T, n node, rowKey, name string, trust peers.TrustLevel, keyByte string) {
	t.Helper()
	if err := n.store.AddUser(context.Background(), rowKey, name, trust, strings.Repeat(keyByte, 64)); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	accounts := []Account{
		{RowKey: "ed25519:aa", Name: "Alice", SigningPubKeyHex: strings.Repeat("a", 64)},
		{RowKey: "xpub-b", Name: "Ops", SigningPubKeyHex: strings.Repeat("b", 64)},
		{RowKey: "xpub-c", Name: "ops", SigningPubKeyHex: strings.Repeat("c", 64)},
	}
	for target, want := range map[string]string{
		"alice":                 "ed25519:aa",
		"xpub-b":                "xpub-b",
		strings.Repeat("C", 64): "xpub-c",
		"  ed25519:aa ":         "ed25519:aa",
	} {
		got, err := Resolve(accounts, target)
		if err != nil || got.RowKey != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", target, got.RowKey, err, want)
		}
	}
	if _, err := Resolve(accounts, "ops"); err == nil || !strings.Contains(err.Error(), "2 accounts") {
		t.Errorf("ambiguous name: %v", err)
	}
	if _, err := Resolve(accounts, "nobody"); err == nil {
		t.Error("resolved an unknown name")
	}
}

func TestManageAccounts(t *testing.T) {
	n := newNode(t)
	ctx := context.Background()
	seed(t, n, "ed25519:a", "Alice", peers.Admin, "a")
	seed(t, n, "ed25519:v", "Viewer", peers.Standard, "b")

	term, out := terminal("", false)
	accounts, err := n.store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	PrintAccounts(out, accounts)
	listing := out.String()
	if strings.Index(listing, "Alice") > strings.Index(listing, "Viewer") {
		t.Fatalf("admins are not listed first:\n%s", listing)
	}

	// The last admin is protected, whichever command reaches for it.
	if err := Remove(ctx, term, n.store, "alice", true); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("remove last admin: %v", err)
	}
	if err := SetTrust(ctx, term, n.store, "alice", "full", true); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}

	// Promote the viewer; then the first admin can step down and be removed.
	if err := SetTrust(ctx, term, n.store, "viewer", "admin", true); err != nil {
		t.Fatal(err)
	}
	if err := SetTrust(ctx, term, n.store, strings.Repeat("a", 64), "standard", true); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, term, n.store, "Alice", true); err != nil {
		t.Fatal(err)
	}
	accounts, _ = n.store.List(ctx)
	if len(accounts) != 1 || accounts[0].Name != "Viewer" || accounts[0].Trust != peers.Admin {
		t.Fatalf("accounts after changes: %+v", accounts)
	}

	for _, level := range []string{"ultimate", "never", "root"} {
		if err := SetTrust(ctx, term, n.store, "viewer", level, true); err == nil {
			t.Errorf("assigned trust %q", level)
		}
	}

	// Declining the confirmation changes nothing.
	seed(t, n, "ed25519:z", "Zed", peers.Standard, "c")
	term, _ = terminal("n\n", true)
	if err := Remove(ctx, term, n.store, "zed", false); err == nil {
		t.Fatal("removed after the operator answered no")
	}
	if accounts, _ = n.store.List(ctx); len(accounts) != 2 {
		t.Fatalf("a declined remove changed the list: %+v", accounts)
	}
}
