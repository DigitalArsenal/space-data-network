package main

// GET /api/accounts — local login accounts and the node root wallet.
//
// OWNER RULING 2026-11-28 (replaces 2026-07-27 merged-peers view):
// "when I list accounts on sdn.spaceaware.io, it has hundreds of 'accounts'
// listed, this is not supposed to be a list of all the peers, just the
// accounts that can login to the local node and the node root wallet itself."
//
// Network peers live on /api/peers. /api/accounts returns ONLY:
//   1. The node's own root wallet (kind: "root", trust_level: ultimate).
//   2. Every enrolled account that can log in to this node (kind: "operator").

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const (
	accountKindRoot     = "root"
	accountKindOperator = "operator"
)

// accountEntry is one row of GET /api/accounts.
type accountEntry struct {
	PeerID           string           `json:"peer_id,omitempty"`
	XPub             string           `json:"xpub,omitempty"`
	SigningPubKeyHex string           `json:"signing_pubkey_hex,omitempty"`
	Kind             string           `json:"kind"`
	Name             string           `json:"name,omitempty"`
	Organization     string           `json:"organization,omitempty"`
	TrustLevel       peers.TrustLevel `json:"trust_level"`
	Groups           []string         `json:"groups,omitempty"`
	Notes            string           `json:"notes,omitempty"`
	AddedAt          int64            `json:"added_at,omitempty"`
	LastConnected    int64            `json:"last_connected,omitempty"`
	ConnectionCount  int64            `json:"connection_count"`
	HasEPM           bool             `json:"has_epm"`
	HasVCard         bool             `json:"has_vcard"`
	CanSignIn        bool             `json:"can_sign_in"`
	Source           string           `json:"source,omitempty"`
}

// buildAccounts returns the node's own root wallet (when present) followed by
// every enrolled local login account. Network peers are never included.
func buildAccounts(root *accountEntry, operators []auth.TrustMatrixEntry, signingKeyByXPub map[string]string) []accountEntry {
	out := make([]accountEntry, 0, len(operators)+1)
	seenXPub := make(map[string]bool, len(operators)+1)
	seenPeer := make(map[string]bool, len(operators)+1)

	if root != nil {
		entry := *root
		entry.Kind = accountKindRoot
		entry.TrustLevel = peers.Ultimate
		entry.CanSignIn = true
		if entry.Source == "" {
			entry.Source = accountKindRoot
		}
		if strings.TrimSpace(entry.Name) == "" {
			entry.Name = "Node Root"
		}
		if entry.XPub != "" {
			seenXPub[entry.XPub] = true
		}
		if entry.PeerID != "" {
			seenPeer[entry.PeerID] = true
		}
		out = append(out, entry)
	}

	opRows := make([]accountEntry, 0, len(operators))
	for _, op := range operators {
		if op.XPub != "" && seenXPub[op.XPub] {
			continue
		}
		if op.PeerID != "" && seenPeer[op.PeerID] {
			continue
		}
		sigHex := ""
		if signingKeyByXPub != nil {
			sigHex = strings.TrimSpace(signingKeyByXPub[op.XPub])
		}
		entry := accountEntry{
			PeerID:           op.PeerID,
			XPub:             op.XPub,
			SigningPubKeyHex: sigHex,
			Kind:             accountKindOperator,
			Name:             op.Name,
			Organization:     op.Organization,
			TrustLevel:       op.TrustLevel,
			Groups:           append([]string(nil), op.Groups...),
			Notes:            op.Notes,
			AddedAt:          op.AddedAt,
			LastConnected:    op.LastConnected,
			ConnectionCount:  op.ConnectionCount,
			HasEPM:           len(op.EPMData) > 0,
			HasVCard:         strings.TrimSpace(op.VCardData) != "",
			CanSignIn:        true,
			Source:           op.Source,
		}
		if op.XPub != "" {
			seenXPub[op.XPub] = true
		}
		if op.PeerID != "" {
			seenPeer[op.PeerID] = true
		}
		opRows = append(opRows, entry)
	}

	// Highest trust first, then most recently active, then by name.
	sort.SliceStable(opRows, func(i, j int) bool {
		if opRows[i].TrustLevel != opRows[j].TrustLevel {
			return opRows[i].TrustLevel > opRows[j].TrustLevel
		}
		if opRows[i].LastConnected != opRows[j].LastConnected {
			return opRows[i].LastConnected > opRows[j].LastConnected
		}
		return opRows[i].Name < opRows[j].Name
	})

	return append(out, opRows...)
}

func nodeRootAccountEntry(n *node.Node, h *auth.Handler) *accountEntry {
	var peerID, xpub, sigHex, name string
	if n != nil {
		if pid := n.PeerID(); pid != "" {
			peerID = pid.String()
		}
		if id := n.Identity(); id != nil && id.SigningPubKey != nil {
			if raw, err := id.SigningPubKey.Raw(); err == nil && len(raw) > 0 {
				sigHex = hex.EncodeToString(raw)
			}
		}
	}
	if h != nil {
		if root := h.NodeRootIdentity(); root != nil {
			if strings.TrimSpace(root.XPub) != "" {
				xpub = strings.TrimSpace(root.XPub)
			}
			if strings.TrimSpace(root.Name) != "" {
				name = strings.TrimSpace(root.Name)
			}
			if sigHex == "" && len(root.SigningKeys) > 0 && len(root.SigningKeys[0]) > 0 {
				sigHex = hex.EncodeToString(root.SigningKeys[0])
			}
		}
	}
	if peerID == "" && xpub == "" && sigHex == "" {
		return nil
	}
	if name == "" {
		name = "Node Root"
	}
	return &accountEntry{
		PeerID:           peerID,
		XPub:             xpub,
		SigningPubKeyHex: sigHex,
		Kind:             accountKindRoot,
		Name:             name,
		TrustLevel:       peers.Ultimate,
		CanSignIn:        true,
		Source:           accountKindRoot,
	}
}

// handleAccounts serves local login accounts plus the node's own root wallet.
func handleAccounts(n *node.Node, resolveAuth func() *auth.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var h *auth.Handler
		if resolveAuth != nil {
			h = resolveAuth()
		}

		var operators []auth.TrustMatrixEntry
		signingKeys := map[string]string{}
		if h != nil {
			if store := h.UserStore(); store != nil {
				entries, err := store.TrustMatrix()
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				operators = entries
				if users, uerr := store.ListUsers(); uerr == nil {
					for _, u := range users {
						if strings.TrimSpace(u.SigningPubKeyHex) != "" {
							signingKeys[u.XPub] = strings.TrimSpace(u.SigningPubKeyHex)
						}
					}
				}
			}
		}

		root := nodeRootAccountEntry(n, h)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"accounts": buildAccounts(root, operators, signingKeys),
		})
	}
}

// handleNodeEPMKeySlots serves the §18 identity key-slot surface.
//
//	GET  /api/node/epm/keys          -> the slots in effect + their GEN KEY proposals
//	POST /api/node/epm/keys {"slot"} -> the GEN KEY proposal for one slot
//
// Both are Admin-only and both return PATHS ONLY. No private key material is
// read, derived, or returned: the corresponding public key is reconstructible by
// anyone from the published xpub plus the path, and the node's seed never leaves
// the process. GEN KEY here PROPOSES; applying the proposal is a separate,
// explicit save through the EPM edit flow, so the button cannot silently
// re-publish the node's identity.
func handleNodeEPMKeySlots(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc := n.EPMService()
		if svc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		switch r.Method {
		case http.MethodGet:
			slots, err := svc.KeySlots()
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"slots": slots})

		case http.MethodPost:
			var req struct {
				Slot string `json:"slot"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 4*1024)).Decode(&req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			current, next, err := svc.ProposeNextKeyPath(epm.KeySlotID(req.Slot))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"slot":         req.Slot,
				"current_path": current,
				"next_path":    next,
			})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
