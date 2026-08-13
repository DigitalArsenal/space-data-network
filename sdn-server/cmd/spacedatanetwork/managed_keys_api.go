package main

// THE MANAGED-KEY REGISTRY SURFACE (graph task sdn-managed-key-registry-api).
//
// Owner ruling 2026-08-07: every key the server controls is enumerable with its
// purpose, its derivation provenance and its bond; deriving from the node root
// is the DEFAULT and an operator-configured external key is the opt-in
// exception; the value rollup covers all keys the server manages; and the whole
// thing runs with "a strong security, auditing, and distribution mindset".
//
// Routes (both under the Admin-classified /api/node/epm prefix):
//
//	GET  /api/node/epm/keys        the registry: rotatable identity slots (§18)
//	                               + purpose slots with source/provenance +
//	                               the full managed-key inventory + bond addrs
//	POST /api/node/epm/keys        {"slot"} — the §18 GEN KEY proposal (unchanged)
//	                               {"action":"configure",...} — point a
//	                               configurable purpose at a dedicated key
//	                               {"action":"clear",...} — back to the default
//	GET  /api/node/epm/keys/audit  per-key signing events, read from the
//	                               append-only modulesign/updatesign audit logs
//
// The GET returns PUBLIC material only: paths, provenance labels, public keys,
// chain addresses. The POST accepts secret material (a seed) and never returns
// it; persistence is the encrypted credential keystore, never a key file.
//
// The audit view READS the two existing append-only logs — it does not invent a
// third log, and it cannot be used to rewrite either (GET only, file opened
// read-only).

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/modulesign"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// managedSlotRow is one row of the key-slot table the key-management UI
// consumes (nodekeys.js derivationState): the two §18 xpub-derivable slots plus
// one row per module-runtime purpose slot. `source` is "root" | "external";
// `provenance` carries the precise KeyProvenance label when known.
type managedSlotRow struct {
	Slot          string `json:"slot"`
	Purpose       string `json:"purpose,omitempty"`
	Path          string `json:"path,omitempty"`
	NextPath      string `json:"next_path,omitempty"`
	Rotatable     bool   `json:"rotatable"`
	Reason        string `json:"reason,omitempty"`
	XPubDerivable bool   `json:"xpub_derivable"`
	Source        string `json:"source"`
	Provenance    string `json:"provenance,omitempty"`
	Algorithm     string `json:"algorithm,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Configurable  bool   `json:"configurable"`
	Note          string `json:"note,omitempty"`
}

// slotSourceFromProvenance folds the three KeyProvenance values into the
// two-state source the UI renders. Anything not explicitly external is root:
// both derived provenances ARE the node root, by definition.
func slotSourceFromProvenance(provenance string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(provenance)), "external") {
		return "external"
	}
	return epm.KeySlotSourceRoot
}

// buildManagedSlotRows merges the §18 identity slots with the purpose-key
// inventory into the one slots[] table. Pure — testable without a node.
func buildManagedSlotRows(identitySlots []epm.KeySlot, managed []node.ManagedKey) []managedSlotRow {
	rows := make([]managedSlotRow, 0, len(identitySlots)+len(managed))
	for _, s := range identitySlots {
		source := s.Source
		if source == "" {
			source = epm.KeySlotSourceRoot
		}
		rows = append(rows, managedSlotRow{
			Slot:          string(s.Slot),
			Path:          s.Path,
			NextPath:      s.NextPath,
			Rotatable:     s.Rotatable,
			Reason:        s.Reason,
			XPubDerivable: s.XPubDerivable,
			Source:        source,
			Configurable:  false,
		})
	}
	for _, key := range managed {
		if key.Slot == "" {
			// Not exposed as a module-runtime slot (the identity root itself);
			// it is still in managed_keys, just not a slot row.
			continue
		}
		rows = append(rows, managedSlotRow{
			Slot:          key.Slot,
			Purpose:       key.Purpose,
			Path:          key.DerivationPath,
			Rotatable:     false,
			Reason:        "purpose keys rotate by configuring a dedicated key (or clearing it back to the derived default), not by advancing a path index",
			XPubDerivable: false,
			Source:        slotSourceFromProvenance(key.Provenance),
			Provenance:    key.Provenance,
			Algorithm:     key.Algorithm,
			PublicKey:     key.PublicKey,
			Configurable:  key.Configurable,
			Note:          key.Note,
		})
	}
	return rows
}

// handleNodeManagedKeys serves the managed-key registry at /api/node/epm/keys.
// It subsumes the §18 key-slot surface (GET slots + POST GEN KEY proposal) and
// adds the registry GET fields and the configure/clear POST actions.
//
// Every response is PATHS + PUBLIC KEYS + provenance labels; no private key
// material is ever read back out of the node.
func handleNodeManagedKeys(n *node.Node) http.HandlerFunc {
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
			managed := n.ServerManagedKeys()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"slots":        buildManagedSlotRows(slots, managed),
				"managed_keys": managed,
				// The node's own bondable addresses (the ROOT's). Addresses
				// only: balances are chain RPC and belong to the WASM
				// attestation module (wasm-not-go-host-boundary law).
				"bond_addresses": n.BondableAddresses(),
				// Per-key address sets — the shape the bond attestation
				// actually runs over, so a UI can show WHICH addresses back
				// WHICH key without re-deriving the mapping.
				"purpose_bond_addresses": n.PurposeBondAddresses(),
				"signing_audit": map[string]string{
					"module_signing_log": modulesign.DefaultAuditPath(),
					"update_signing_log": updatesign.DefaultAuditPath(),
				},
			})

		case http.MethodPost:
			var req struct {
				Action        string            `json:"action"`
				Slot          string            `json:"slot"`
				Purpose       string            `json:"purpose"`
				SeedHex       string            `json:"seed_hex"`
				BondAddresses map[string]string `json:"bond_addresses"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			switch strings.TrimSpace(req.Action) {
			case "", "propose":
				// The §18 GEN KEY proposal, byte-compatible with the shipped
				// UI: propose only, never apply.
				if strings.TrimSpace(req.Slot) == "" {
					http.Error(w, "either a §18 {\"slot\"} or an {\"action\":\"configure\"|\"clear\"} request is required", http.StatusBadRequest)
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

			case "configure":
				purpose, ok := node.PurposeByLabel(req.Purpose)
				if !ok {
					http.Error(w, "unknown purpose "+strings.TrimSpace(req.Purpose)+": purposes are the registered labels served in managed_keys[]", http.StatusBadRequest)
					return
				}
				requester := ""
				if session := auth.SessionFromContext(r.Context()); session != nil {
					requester = modulesign.FingerprintPrincipal(session.XPub)
				}
				cfg, err := n.ConfigurePurposeKey(purpose, req.SeedHex, req.BondAddresses, requester)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				log.Infof("managed-key registry: %s configured with a dedicated external key by admin session %s", purpose, requester)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"purpose":        purpose.String(),
					"configured":     true,
					"provenance":     string(wasm.ProvenanceExternalConfigured),
					"bond_addresses": cfg.BondAddresses,
					"configured_at":  cfg.ConfiguredAt,
					"managed_keys":   n.ServerManagedKeys(),
					// The licensing runtime is provisioned at boot; the
					// configured key signs after the next daemon restart.
					"restart_required": true,
					"sentence":         "The dedicated key is stored encrypted at rest and is NOT derivable from the node mnemonic — back it up separately. It takes effect at the next daemon restart.",
				})

			case "clear":
				purpose, ok := node.PurposeByLabel(req.Purpose)
				if !ok {
					http.Error(w, "unknown purpose "+strings.TrimSpace(req.Purpose), http.StatusBadRequest)
					return
				}
				requester := ""
				if session := auth.SessionFromContext(r.Context()); session != nil {
					requester = modulesign.FingerprintPrincipal(session.XPub)
				}
				if err := n.ClearPurposeKey(purpose); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				log.Infof("managed-key registry: %s cleared back to the derived default by admin session %s", purpose, requester)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"purpose":          purpose.String(),
					"configured":       false,
					"managed_keys":     n.ServerManagedKeys(),
					"restart_required": true,
					"sentence":         "This purpose returns to the ruled default — derived from the node root at its contract path — at the next daemon restart.",
				})

			default:
				http.Error(w, "unknown action "+req.Action+": expected propose, configure, or clear", http.StatusBadRequest)
			}

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// ── the per-key signing audit view ──────────────────────────────────────────

// keySigningEvent is one signing event as the UI renders it: which KEY
// (purpose) signed WHAT (domain + content hash), WHEN, for WHOM. Folded from
// the two append-only audit logs; nothing here is a new record of authority.
type keySigningEvent struct {
	TS      string `json:"ts"`
	Event   string `json:"event"`
	Lane    string `json:"lane"`
	Purpose string `json:"purpose"`
	Slot    string `json:"slot,omitempty"`
	Domain  string `json:"domain,omitempty"`

	ContentHash  string `json:"content_hash,omitempty"`
	SignerPubKey string `json:"signer_pubkey_hex,omitempty"`
	Requester    string `json:"requester,omitempty"`
	Reason       string `json:"reason,omitempty"`

	// Update-lane release facts, present only on update-signing events.
	UpdateID string `json:"update_id,omitempty"`
	Version  string `json:"version,omitempty"`
	Channel  string `json:"channel,omitempty"`
	Target   string `json:"target,omitempty"`
}

// auditSourceStatus reports each log file's read outcome so an empty events
// list is distinguishable from an unreadable one.
type auditSourceStatus struct {
	Lane        string `json:"lane"`
	Path        string `json:"path"`
	Entries     int    `json:"entries"`
	ParseErrors int    `json:"parse_errors,omitempty"`
	Error       string `json:"error,omitempty"`
}

// rawAuditLine is the superset of the two audit entry shapes; both logs are
// lowercase snake_case JSONL with compatible field names.
type rawAuditLine struct {
	TS              string `json:"ts"`
	Event           string `json:"event"`
	ContentHash     string `json:"content_hash"`
	StatementDomain string `json:"statement_domain"`
	SignerPubKeyHex string `json:"signer_pubkey_hex"`
	Requester       string `json:"requester"`
	Reason          string `json:"reason"`
	UpdateID        string `json:"update_id"`
	Version         string `json:"version"`
	Channel         string `json:"channel"`
	Target          string `json:"target"`
}

// readSigningAuditLog reads one append-only JSONL audit file. Tolerant of a
// torn final line (counted, never fatal): the log is written under O_APPEND by
// a live daemon.
func readSigningAuditLog(lane, path string) ([]keySigningEvent, auditSourceStatus) {
	status := auditSourceStatus{Lane: lane, Path: path}
	if strings.TrimSpace(path) == "" {
		status.Error = "no audit log path could be resolved"
		return nil, status
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		// An absent file is an EMPTY log, not an error: the file is created
		// lazily on the first signature.
		return nil, status
	}
	if err != nil {
		status.Error = err.Error()
		return nil, status
	}
	defer f.Close()

	var events []keySigningEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw rawAuditLine
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			status.ParseErrors++
			continue
		}
		status.Entries++
		events = append(events, keySigningEvent{
			TS:           raw.TS,
			Event:        raw.Event,
			Lane:         lane,
			Domain:       raw.StatementDomain,
			ContentHash:  raw.ContentHash,
			SignerPubKey: strings.ToLower(raw.SignerPubKeyHex),
			Requester:    raw.Requester,
			Reason:       raw.Reason,
			UpdateID:     raw.UpdateID,
			Version:      raw.Version,
			Channel:      raw.Channel,
			Target:       raw.Target,
		})
	}
	if err := scanner.Err(); err != nil {
		status.Error = err.Error()
	}
	return events, status
}

// resolveAuditEventKeys attributes each event to the managed key whose public
// half signed it. An event whose signer is not in the inventory says
// "unrecorded-key" loudly rather than being silently dropped — an unlisted
// signer is exactly the thing an auditor needs to see.
func resolveAuditEventKeys(events []keySigningEvent, managed []node.ManagedKey) {
	byPub := make(map[string]node.ManagedKey, len(managed))
	for _, key := range managed {
		if key.PublicKey != "" {
			byPub[strings.ToLower(key.PublicKey)] = key
		}
	}
	for i := range events {
		if events[i].SignerPubKey == "" {
			// Refusals are logged before a signer is chosen; attribute them by
			// lane (both lanes sign with the update/publisher root).
			events[i].Purpose = wasm.PurposeIdentitySigning.String()
			continue
		}
		if key, ok := byPub[events[i].SignerPubKey]; ok {
			events[i].Purpose = key.Purpose
			events[i].Slot = key.Slot
			continue
		}
		events[i].Purpose = "unrecorded-key"
	}
}

// mergeKeySigningEvents merges, attributes, sorts (newest first) and bounds the
// events from both lanes. Pure — testable with fixture files.
func mergeKeySigningEvents(moduleEvents, updateEvents []keySigningEvent, managed []node.ManagedKey, limit int) []keySigningEvent {
	events := make([]keySigningEvent, 0, len(moduleEvents)+len(updateEvents))
	events = append(events, moduleEvents...)
	events = append(events, updateEvents...)
	resolveAuditEventKeys(events, managed)
	// RFC3339 timestamps sort lexicographically within a uniform offset; both
	// writers stamp UTC. A stable sort keeps same-instant lines in file order.
	sort.SliceStable(events, func(i, j int) bool { return events[i].TS > events[j].TS })
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events
}

const (
	signingAuditDefaultLimit = 200
	signingAuditMaxLimit     = 1000
)

// handleNodeKeySigningAudit serves GET /api/node/epm/keys/audit — the per-key
// signing event view. Admin-classified by the /api/node/epm prefix. Read-only
// by construction: the audit files are opened O_RDONLY and the append-only
// writers live elsewhere.
func handleNodeKeySigningAudit(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := signingAuditDefaultLimit
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			parsed := 0
			for _, c := range v {
				if c < '0' || c > '9' {
					parsed = -1
					break
				}
				parsed = parsed*10 + int(c-'0')
				if parsed > signingAuditMaxLimit {
					parsed = signingAuditMaxLimit
					break
				}
			}
			if parsed > 0 {
				limit = parsed
			}
		}

		moduleEvents, moduleStatus := readSigningAuditLog("module-signing", modulesign.DefaultAuditPath())
		updateEvents, updateStatus := readSigningAuditLog("update-signing", updatesign.DefaultAuditPath())
		managed := n.ServerManagedKeys()

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"events":  mergeKeySigningEvents(moduleEvents, updateEvents, managed, limit),
			"sources": []auditSourceStatus{moduleStatus, updateStatus},
			"limit":   limit,
		})
	}
}
