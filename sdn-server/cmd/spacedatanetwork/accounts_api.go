package main

// GET /api/accounts — the unified ACCOUNTS surface.
//
// OWNER DIRECTIVE 2026-07-27, verbatim: "remove the entire 'permissions' menu.
// that is all managed on the 'nodes' menu, which should be called 'accounts',
// we are not differentiating between a node running somewhere and an account
// that will login to the site as they are both the same thing."
//
// This is the conceptual completion of §14: a network peer and a login account
// are the same kind of thing — a PRR-shaped identity carrying a trust level.
// §14 already made operator rows a PRR projection keyed by a peer ID derived
// from the account xpub, which is precisely what makes ONE table honest rather
// than a cosmetic join of two unrelated stores.
//
// # Why this is a server endpoint and not a client-side merge
//
// Dedupe is the reason. An operator whose xpub-derived peer ID equals a live
// node's peer ID is ONE account, not two rows that happen to look alike, and
// deciding that on the client would mean shipping the merge rule to every
// surface that ever lists accounts — and getting it subtly different in each.
// The merge key is a server-side fact (§14's PEER_ID binding), the two inputs
// live in two stores the server already holds open, and the trust-tier
// reconciliation below is a policy decision that must not be re-litigated in
// UI code. So the node merges, and the UI renders.
//
// This endpoint is READ-ONLY. Every management action — set trust, add by
// key/vCard, approve — keeps using the existing APIs (§7 for operators, §8 for
// peers); the PERMISSIONS page dies, its API surface does not.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// accountKind records where an account is known from. An identity can be both,
// which is the whole point of the merge.
const (
	accountKindPeer     = "peer"
	accountKindOperator = "operator"
	accountKindBoth     = "both"
)

// accountEntry is one row of the ACCOUNTS table. Field names follow the PRR
// projection internal/peers already uses; these are node-synthesized API keys,
// so they stay lowercase snake_case (an actual serialized SDS record is what
// carries IDL capitalization).
type accountEntry struct {
	// PeerID is PRR.PEER_ID — the merge key. Empty only for an operator whose
	// identifier is not a parseable xpub (a config label), which therefore
	// cannot be deduped against a peer and is listed on its own.
	PeerID string `json:"peer_id,omitempty"`
	// XPub is present when this identity signs in; absent for a pure peer.
	XPub string `json:"xpub,omitempty"`

	Kind         string           `json:"kind"`
	Name         string           `json:"name,omitempty"`
	Organization string           `json:"organization,omitempty"`
	TrustLevel   peers.TrustLevel `json:"trust_level"`
	Groups       []string         `json:"groups,omitempty"`
	Notes        string           `json:"notes,omitempty"`

	AddedAt         int64 `json:"added_at,omitempty"`
	LastConnected   int64 `json:"last_connected,omitempty"`
	ConnectionCount int64 `json:"connection_count"`

	HasEPM   bool `json:"has_epm"`
	HasVCard bool `json:"has_vcard"`

	// CanSignIn is true when this identity has an auth credential bound, i.e.
	// it is reachable through /api/auth/*. A peer that has never signed in is
	// false.
	CanSignIn bool `json:"can_sign_in"`
	// Source is "config" or "database" for operator-derived rows.
	Source string `json:"source,omitempty"`
}

// mergeAccountTrust reconciles the trust levels of the two views of one
// identity.
//
// §14 recorded that the peer registry and the operator store share the SAME
// 7-value PGP ownertrust scale, so no conversion is needed here — but they can
// legitimately DISAGREE (an operator enrolled at admin whose node peer entry is
// standard). The merged row shows the HIGHER of the two, because that is the
// authority the identity actually holds: an admin operator does not lose admin
// because their node is only a standard peer. The lower value is not
// discarded silently — both source rows remain readable through /api/peers and
// /api/auth/users, which are still the surfaces that CHANGE either value.
func mergeAccountTrust(peerTrust, operatorTrust peers.TrustLevel) peers.TrustLevel {
	if operatorTrust > peerTrust {
		return operatorTrust
	}
	return peerTrust
}

// buildAccounts merges the peer registry and the operator trust matrix into one
// deduped list, newest-known first.
func buildAccounts(registryPeers []*peers.TrustedPeer, operators []auth.TrustMatrixEntry) []accountEntry {
	byPeerID := make(map[string]*accountEntry, len(registryPeers)+len(operators))
	var ordered []*accountEntry

	for _, tp := range registryPeers {
		if tp == nil {
			continue
		}
		entry := &accountEntry{
			PeerID:          tp.ID.String(),
			Kind:            accountKindPeer,
			Name:            tp.Name,
			Organization:    tp.Organization,
			TrustLevel:      tp.TrustLevel,
			Groups:          append([]string(nil), tp.Groups...),
			Notes:           tp.Notes,
			ConnectionCount: tp.ConnectionCount,
			HasEPM:          len(tp.EPMData) > 0,
			HasVCard:        strings.TrimSpace(tp.VCardData) != "",
		}
		if !tp.AddedAt.IsZero() {
			entry.AddedAt = tp.AddedAt.Unix()
		}
		if !tp.LastConnected.IsZero() {
			entry.LastConnected = tp.LastConnected.Unix()
		}
		byPeerID[entry.PeerID] = entry
		ordered = append(ordered, entry)
	}

	for _, op := range operators {
		// The merge: same PEER_ID means the SAME account, not two rows.
		if op.PeerID != "" {
			if existing, ok := byPeerID[op.PeerID]; ok {
				existing.Kind = accountKindBoth
				existing.XPub = op.XPub
				existing.CanSignIn = true
				existing.Source = op.Source
				existing.TrustLevel = mergeAccountTrust(existing.TrustLevel, op.TrustLevel)
				if existing.Name == "" {
					existing.Name = op.Name
				}
				if existing.Organization == "" {
					existing.Organization = op.Organization
				}
				if op.LastConnected > existing.LastConnected {
					existing.LastConnected = op.LastConnected
				}
				existing.ConnectionCount += op.ConnectionCount
				existing.HasEPM = existing.HasEPM || len(op.EPMData) > 0
				existing.HasVCard = existing.HasVCard || strings.TrimSpace(op.VCardData) != ""
				continue
			}
		}

		entry := &accountEntry{
			PeerID:          op.PeerID,
			XPub:            op.XPub,
			Kind:            accountKindOperator,
			Name:            op.Name,
			Organization:    op.Organization,
			TrustLevel:      op.TrustLevel,
			Groups:          append([]string(nil), op.Groups...),
			Notes:           op.Notes,
			AddedAt:         op.AddedAt,
			LastConnected:   op.LastConnected,
			ConnectionCount: op.ConnectionCount,
			HasEPM:          len(op.EPMData) > 0,
			HasVCard:        strings.TrimSpace(op.VCardData) != "",
			CanSignIn:       true,
			Source:          op.Source,
		}
		if op.PeerID != "" {
			byPeerID[op.PeerID] = entry
		}
		ordered = append(ordered, entry)
	}

	out := make([]accountEntry, 0, len(ordered))
	for _, e := range ordered {
		out = append(out, *e)
	}
	// Most recently active first, then by name, so the table is stable.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastConnected != out[j].LastConnected {
			return out[i].LastConnected > out[j].LastConnected
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// handleAccounts serves the merged ACCOUNTS list. Admin-only: it is registered
// under the /api/accounts prefix which isAdminOnlyAPIPath covers, and it
// exposes the same operator/peer detail the PERMISSIONS surfaces did.
func handleAccounts(n *node.Node, resolveAuth func() *auth.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var registryPeers []*peers.TrustedPeer
		if registry := n.PeerRegistry(); registry != nil {
			registryPeers = registry.ListPeers()
		}

		var operators []auth.TrustMatrixEntry
		if resolveAuth != nil {
			if h := resolveAuth(); h != nil {
				if store := h.UserStore(); store != nil {
					entries, err := store.TrustMatrix()
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					operators = entries
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"accounts": buildAccounts(registryPeers, operators),
		})
	}
}
