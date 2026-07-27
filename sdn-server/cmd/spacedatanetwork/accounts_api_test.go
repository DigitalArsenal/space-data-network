package main

// Locks the unified ACCOUNTS surface (owner directive 2026-07-27: "we are not
// differentiating between a node running somewhere and an account that will
// login to the site as they are both the same thing").

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func mustDecodePeerID(t *testing.T, s string) peer.ID {
	t.Helper()
	id, err := peer.Decode(s)
	if err != nil {
		t.Fatalf("peer.Decode(%q): %v", s, err)
	}
	return id
}

const (
	accountPeerA = "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	accountPeerB = "12D3KooWSyD1cE1Cb8yFvJmSjSHwnbEwGtxpiUFa5jVDXnJTPFEy"
)

// TestAccountsMergeDedupesOneIdentityIntoOneRow is the heart of the directive:
// an operator whose xpub-derived peer ID equals a live node's peer ID is ONE
// account, not two rows that happen to look alike.
func TestAccountsMergeDedupesOneIdentityIntoOneRow(t *testing.T) {
	t.Parallel()

	shared := mustDecodePeerID(t, accountPeerA)
	registryPeers := []*peers.TrustedPeer{{
		ID:              shared,
		Name:            "Node One",
		TrustLevel:      peers.Standard,
		AddedAt:         time.Unix(1000, 0),
		LastConnected:   time.Unix(2000, 0),
		ConnectionCount: 5,
	}}
	operators := []auth.TrustMatrixEntry{{
		PeerID:          shared.String(),
		XPub:            "xpub-operator-one",
		Name:            "Operator One",
		TrustLevel:      peers.Admin,
		LastConnected:   3000,
		ConnectionCount: 2,
		Source:          "database",
	}}

	got := buildAccounts(registryPeers, operators)
	if len(got) != 1 {
		t.Fatalf("accounts = %d rows, want 1 merged row: %+v", len(got), got)
	}
	row := got[0]
	if row.Kind != accountKindBoth {
		t.Fatalf("kind = %q, want %q", row.Kind, accountKindBoth)
	}
	if row.XPub != "xpub-operator-one" {
		t.Fatalf("merged row lost the operator xpub: %q", row.XPub)
	}
	if !row.CanSignIn {
		t.Fatal("merged row does not report can_sign_in")
	}
	// Higher of the two authorities wins — an admin operator does not lose
	// admin because their node is only a standard peer.
	if row.TrustLevel != peers.Admin {
		t.Fatalf("merged trust = %s, want %s", row.TrustLevel, peers.Admin)
	}
	if row.LastConnected != 3000 {
		t.Fatalf("last_connected = %d, want the most recent (3000)", row.LastConnected)
	}
	if row.ConnectionCount != 7 {
		t.Fatalf("connection_count = %d, want 7 (5 peer + 2 sign-ins)", row.ConnectionCount)
	}
	// The peer's own name is kept when present.
	if row.Name != "Node One" {
		t.Fatalf("name = %q", row.Name)
	}
}

// TestAccountsKeepsDistinctIdentitiesApart locks that the merge is by PEER_ID
// and nothing else.
func TestAccountsKeepsDistinctIdentitiesApart(t *testing.T) {
	t.Parallel()

	peerA := mustDecodePeerID(t, accountPeerA)
	peerB := mustDecodePeerID(t, accountPeerB)

	got := buildAccounts(
		[]*peers.TrustedPeer{{ID: peerA, Name: "Node A", TrustLevel: peers.Standard}},
		[]auth.TrustMatrixEntry{{
			PeerID: peerB.String(), XPub: "xpub-b", Name: "Operator B", TrustLevel: peers.Admin,
		}},
	)
	if len(got) != 2 {
		t.Fatalf("accounts = %d rows, want 2 distinct: %+v", len(got), got)
	}
	kinds := map[string]string{}
	for _, r := range got {
		kinds[r.PeerID] = r.Kind
	}
	if kinds[peerA.String()] != accountKindPeer {
		t.Fatalf("peer A kind = %q", kinds[peerA.String()])
	}
	if kinds[peerB.String()] != accountKindOperator {
		t.Fatalf("operator B kind = %q", kinds[peerB.String()])
	}
}

// TestAccountsListsOperatorsWithoutAPeerID locks that an operator whose
// identifier is not a parseable xpub (a config label, so §14 stores an EMPTY
// peer id rather than fabricating one) is still listed — and is never merged
// into some other row by an empty-string key.
func TestAccountsListsOperatorsWithoutAPeerID(t *testing.T) {
	t.Parallel()

	peerA := mustDecodePeerID(t, accountPeerA)
	got := buildAccounts(
		[]*peers.TrustedPeer{{ID: peerA, Name: "Node A", TrustLevel: peers.Standard}},
		[]auth.TrustMatrixEntry{
			{XPub: "xpub-config-label-one", Name: "Config One", TrustLevel: peers.Admin, Source: "config"},
			{XPub: "xpub-config-label-two", Name: "Config Two", TrustLevel: peers.Standard, Source: "config"},
		},
	)
	if len(got) != 3 {
		t.Fatalf("accounts = %d rows, want 3 (1 peer + 2 unmergeable operators): %+v", len(got), got)
	}
	operators := 0
	for _, r := range got {
		if r.Kind == accountKindOperator {
			operators++
			if r.PeerID != "" {
				t.Fatalf("a config-label operator was given a peer id: %q", r.PeerID)
			}
			if !r.CanSignIn {
				t.Fatal("operator row does not report can_sign_in")
			}
		}
	}
	if operators != 2 {
		t.Fatalf("operator rows = %d, want 2", operators)
	}
}

// TestMergeAccountTrustTakesTheHigherAuthority locks the reconciliation rule
// §16 records, including that the shared 7-value PGP scale needs no conversion.
func TestMergeAccountTrustTakesTheHigherAuthority(t *testing.T) {
	t.Parallel()

	cases := []struct {
		peerTrust, operatorTrust, want peers.TrustLevel
	}{
		{peers.Standard, peers.Admin, peers.Admin},
		{peers.Admin, peers.Standard, peers.Admin},
		{peers.Never, peers.Standard, peers.Standard},
		{peers.Ultimate, peers.Admin, peers.Ultimate},
		{peers.Marginal, peers.Marginal, peers.Marginal},
	}
	for _, tc := range cases {
		if got := mergeAccountTrust(tc.peerTrust, tc.operatorTrust); got != tc.want {
			t.Fatalf("mergeAccountTrust(%s, %s) = %s, want %s",
				tc.peerTrust, tc.operatorTrust, got, tc.want)
		}
	}
}

// TestAccountsSurfaceIsAdminOnly locks the tier gate: the merged list carries
// the same operator/peer detail the PERMISSIONS surfaces did, so it must not
// become readable below Admin just because it moved to a new path.
func TestAccountsSurfaceIsAdminOnly(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/accounts", "/api/accounts/"} {
		if !isAdminOnlyAPIPath(path) {
			t.Fatalf("%q does not require admin trust", path)
		}
		if isPublicAPIPath(path) {
			t.Fatalf("%q is anonymous-reachable", path)
		}
		if isAnyTierAuthenticatedAPIPath(path) {
			t.Fatalf("%q was given the any-tier floor", path)
		}
	}
}

// TestAccountsHandlesEmptyInputs locks that a node with no peers and no
// operators returns an empty list rather than failing.
func TestAccountsHandlesEmptyInputs(t *testing.T) {
	t.Parallel()

	if got := buildAccounts(nil, nil); len(got) != 0 {
		t.Fatalf("empty node produced %d rows", len(got))
	}
	if got := buildAccounts([]*peers.TrustedPeer{nil}, nil); len(got) != 0 {
		t.Fatalf("a nil peer produced %d rows", len(got))
	}
}
