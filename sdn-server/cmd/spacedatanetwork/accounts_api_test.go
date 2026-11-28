package main

// Locks the local-login ACCOUNTS surface (owner ruling 2026-11-28: /api/accounts
// returns ONLY the node's own root wallet and accounts that can log in to this
// node, never the full peer registry).

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const (
	accountPeerA = "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	accountPeerB = "12D3KooWSyD1cE1Cb8yFvJmSjSHwnbEwGtxpiUFa5jVDXnJTPFEy"
)

func TestAccountsListsRootWalletAndLoginOperatorsOnly(t *testing.T) {
	t.Parallel()

	root := &accountEntry{
		PeerID:           accountPeerA,
		XPub:             "xpub-root",
		SigningPubKeyHex: "1111111111111111111111111111111111111111111111111111111111111111",
		Name:             "Node Root",
	}
	operators := []auth.TrustMatrixEntry{
		{
			PeerID:        accountPeerB,
			XPub:          "xpub-standard",
			Name:          "Standard Operator",
			TrustLevel:    peers.Standard,
			LastConnected: 5000,
			Source:        "database",
		},
		{
			XPub:          "xpub-admin",
			Name:          "Admin Operator",
			TrustLevel:    peers.Admin,
			LastConnected: 1000,
			Source:        "config",
		},
	}
	sigKeys := map[string]string{
		"xpub-admin":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"xpub-standard": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	got := buildAccounts(root, operators, sigKeys)
	if len(got) != 3 {
		t.Fatalf("accounts = %d rows, want 3 (root + 2 operators): %+v", len(got), got)
	}
	if got[0].Kind != accountKindRoot || got[0].TrustLevel != peers.Ultimate || !got[0].CanSignIn {
		t.Fatalf("row 0 = %+v, want root wallet with ultimate trust", got[0])
	}
	if got[1].XPub != "xpub-admin" || got[1].Kind != accountKindOperator || got[1].SigningPubKeyHex != sigKeys["xpub-admin"] {
		t.Fatalf("row 1 = %+v, want admin operator sorted before standard", got[1])
	}
	if got[2].XPub != "xpub-standard" || got[2].Kind != accountKindOperator || got[2].SigningPubKeyHex != sigKeys["xpub-standard"] {
		t.Fatalf("row 2 = %+v, want standard operator last", got[2])
	}
}

func TestAccountsDedupesOperatorMatchingRootWallet(t *testing.T) {
	t.Parallel()

	root := &accountEntry{
		PeerID: accountPeerA,
		XPub:   "xpub-root",
		Name:   "Node Root",
	}
	operators := []auth.TrustMatrixEntry{
		{PeerID: accountPeerA, XPub: "xpub-root", Name: "Duplicate Root", TrustLevel: peers.Admin},
		{XPub: "xpub-other", Name: "Other Admin", TrustLevel: peers.Admin},
	}

	got := buildAccounts(root, operators, nil)
	if len(got) != 2 {
		t.Fatalf("accounts = %d rows, want 2 (root deduped): %+v", len(got), got)
	}
	if got[0].Kind != accountKindRoot || got[1].XPub != "xpub-other" {
		t.Fatalf("unexpected rows: %+v", got)
	}
}

func TestAccountsListsOperatorsWithoutAPeerID(t *testing.T) {
	t.Parallel()

	got := buildAccounts(nil, []auth.TrustMatrixEntry{
		{XPub: "config-label-a", Name: "Config A", TrustLevel: peers.Admin, Source: "config"},
		{XPub: "config-label-b", Name: "Config B", TrustLevel: peers.Standard, Source: "config"},
	}, nil)
	if len(got) != 2 {
		t.Fatalf("accounts = %d rows, want 2 distinct config operators: %+v", len(got), got)
	}
}

func TestAccountsHandlesEmptyInputs(t *testing.T) {
	t.Parallel()

	if got := buildAccounts(nil, nil, nil); len(got) != 0 {
		t.Fatalf("empty inputs yielded %d rows", len(got))
	}
}
