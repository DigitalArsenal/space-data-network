package modulert

// Loop B1 — defensive hardening, FAIL CLOSED.
//
// GAP: the module runtime previously granted exactly the capabilities a
// module's own embedded manifest requested (module.go instantiateWASM) or
// merely checked that the host COULD serve them (capability_provision.go
// ProvisionBridge) — a module was trusted to declare its own privileges,
// with no operator gate.
//
// FIX: an operator-controlled, persisted capability allowlist. Sensitive
// capabilities (see sensitiveCapabilities below) require an explicit
// recorded approval keyed by the module's content hash before the module is
// allowed to load; absent an approval, the ENTIRE module load is refused —
// no partial silent grant. Benign, read-only capabilities (benignCapabilities
// below) remain default-allow with no policy entry required.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------
// Capability tiering
// ---------------------------------------------------------------------

// sensitiveCapabilities lists module-manifest capability names that grant
// access to node secrets, node-controlled disk/network I/O, or unrestricted
// outbound network/process operations. Absent an explicit operator approval
// entry (see CapabilityPolicyStore), a module requesting ANY of these is
// DENIED at load — fail closed.
//
// Tiering rationale:
//
//   - wallet_sign: fetches secrets from the node's own key vault
//     (NodeContext.KeySlots) via the "keyslot.*" hostcall family —
//     capPrefixFromName maps wallet_sign to the "keyslot" hostcall prefix;
//     the manifest schema has no separate "keyslot" capability name (see
//     caps/keyslot.go), so wallet_sign IS the "keyslot (any key operations)"
//     capability referenced in the loop B1 spec.
//   - http, protocol_dial: unrestricted outbound network to arbitrary
//     hosts/peers (SSRF, data exfiltration, abuse-as-relay).
//   - process_exec, host_control: arbitrary code execution / host control.
//     Neither is currently wired to a factory in node.go buildCapRegistry,
//     but both are tiered here pre-emptively so a future factory addition
//     is fail-closed by default rather than silently trusted.
//   - storage_write, storage_ingest: mutate the node's persisted SDS store
//     or write raw provenance-tracked ingest data to disk ("ingest" in the
//     loop B1 spec is storage_ingest — see caps/storage.go
//     handleIngestWithSource).
//   - storage_query, storage_adapter: gated as sensitive alongside
//     storage_write/storage_ingest, NOT merely "read-only benign".
//     caps/storage.go registers ONE handler shared by all four storage_*
//     capabilities (capPrefixFromName maps storage_query/storage_write/
//     storage_adapter/storage_ingest to the same "storage" hostcall
//     prefix). The handler now enforces per-operation HasCapability gates
//     (storage.write/storage.delete require storage_write; the query and
//     flatsql stream ops require storage_query; grants do not imply each
//     other), so approving one storage_* name grants exactly that tier.
//     storage_query stays in the sensitive default-deny set regardless:
//     queries read the node's whole persisted SDS store, and the two-layer
//     posture (policy approval + per-op gate) is deliberate defense in
//     depth; see caps/storage.go for the per-op checks.
//   - ipfs: pins/adds arbitrary content to the local IPFS daemon, reachable
//     by the public swarm — exfiltration and unbounded local-disk risk.
//   - pubsub: broadcasts arbitrary messages under the node's identity to
//     every peer subscribed to a topic — spam/spoofing/DoS risk, the same
//     class of outbound-network abuse as http/protocol_dial.
var sensitiveCapabilities = map[string]bool{
	"wallet_sign":     true,
	"http":            true,
	"process_exec":    true,
	"storage_write":   true,
	"storage_ingest":  true,
	"protocol_dial":   true,
	"host_control":    true,
	"storage_query":   true,
	"storage_adapter": true,
	"ipfs":            true,
	"pubsub":          true,
}

// benignCapabilities lists capability names that default-allow with no
// policy entry required.
//
//   - p2p_read: read-only snapshots of the peerstore / SDN-flag-verified-DHT
//     / registry view + stored EPM/PNM data (loop G.2/G.4 — caps/p2p.go).
//     No mutation, no secret material, no outbound side effects beyond
//     what the node is already doing.
//   - node_status_read: read-only snapshot of the HOST's OWN runtime state
//     — uptime, record-store totals, disk headroom, service/mode, libp2p
//     bandwidth totals + a short in-memory rate history (M1 node-status
//     capability — caps/nodestatus.go). Every value is either derived from
//     the node's own already-public operating state (uptime, mode, byte
//     counts) or a local statfs/bandwidth-counter probe; nothing here reads
//     node secrets (contrast wallet_sign/keyslot) or another peer's data
//     (contrast p2p_read), and it has no mutation/outbound side effects.
//   - node_activity_read: read-only, bounded (256-entry) in-memory ring of
//     the HOST's OWN recent activity — peer connect/disconnect, PNM
//     publication, schema record ingest, channel-grant issuance (M2
//     activity capability — caps/nodeactivity.go). Entries carry only
//     already-public libp2p peer IDs and short schema/channel names; no
//     addresses, no key material, no other PII. Same rationale as
//     node_status_read: no secrets, no mutation, no outbound side effects.
//   - crypto_hash, crypto_sign, crypto_verify, crypto_encrypt,
//     crypto_decrypt, crypto_key_agreement, crypto_kdf: stateless
//     functions over caller-SUPPLIED byte material only (see
//     caps/crypto.go — every operation takes its key/data as request
//     parameters). They never read node-held secrets — contrast with
//     wallet_sign/keyslot, which reads NodeContext.KeySlots — and have no
//     I/O side effects. A module could implement identical logic with an
//     in-WASM crypto library; this capability is a convenience/perf
//     passthrough, not a privilege escalation.
//   - clock, random, logging, timers: baseline module-sdk primitives.
//     "clock.now"/"clock.nowIso"/"clock.monotonicNow"/"random.bytes" are
//     served from HostBridge.Dispatch's "Built-in operations (always
//     available)" switch BEFORE any capability/granted check runs, and the
//     SDK log function is wired unconditionally via
//     wasmrt.WithHostModule("sdn"/"env", ...) at instantiation time — so
//     these capability names carry no enforcement today; declaring them is
//     purely documentary. "timers" is likewise informational — CronMethods
//     /InvokeCron read manifest.Timers directly with no capability check.
//     Virtually every module declares these; gating them would create
//     approval churn with no corresponding security benefit.
//   - protocol_handle: Module.Start() registers libp2p stream handlers from
//     manifest.Protocols (AutoInstall entries) unconditionally — it does
//     NOT consult manifest.Capabilities/bridge.granted at all. Like
//     clock/random/logging above, this capability name currently has no
//     enforcement hook to gate; if protocol registration is ever wired to
//     check this capability, revisit its tier then (see host_control/
//     process_exec precedent for how to pre-emptively tier an unwired
//     capability that SHOULD be sensitive once it gains a real effect).
var benignCapabilities = map[string]bool{
	"p2p_read":             true,
	"node_status_read":     true,
	"node_activity_read":   true,
	"crypto_hash":          true,
	"crypto_sign":          true,
	"crypto_verify":        true,
	"crypto_encrypt":       true,
	"crypto_decrypt":       true,
	"crypto_key_agreement": true,
	"crypto_kdf":           true,
	"clock":                true,
	"random":               true,
	"logging":              true,
	"timers":               true,
	"protocol_handle":      true,
}

// IsSensitiveCapability reports whether cap requires an explicit operator
// approval entry before a module may be granted it.
//
// Only capability names listed in sensitiveCapabilities are gated — this is
// an EXPLICIT allowlist-of-what-to-gate, not "deny anything unrecognized".
// The inverse (treat every name absent from benignCapabilities as sensitive)
// was tried and reverted: node.go buildCapRegistry currently wires factories
// for only a subset of the manifest schema's capabilityNames enum (see
// manifest.go); every other declared name (e.g. "database", "tcp", "mqtt",
// "scene_access", ...) already has zero enforcement effect (no factory, no
// hostcall handler gets registered for its prefix — any hostcall using it
// fails with "operation not supported" regardless of this policy). Denying
// module load over an inert, unwired capability name is pure friction with
// no security benefit. When node.go buildCapRegistry gains a real factory
// for a capability not yet classified here, add it to sensitiveCapabilities
// (or benignCapabilities, with a documented reason) as part of that change —
// see the process_exec/host_control comment above for the pattern.
func IsSensitiveCapability(cap string) bool {
	return sensitiveCapabilities[strings.TrimSpace(cap)]
}

// ---------------------------------------------------------------------
// Persisted operator approvals
// ---------------------------------------------------------------------

// CapabilityApproval records one operator-approved (module, capability)
// pair.
type CapabilityApproval struct {
	// ModuleHash is the lowercase hex SHA-256 digest of the module's raw
	// WASM artifact — the canonical, non-spoofable identity this entry is
	// keyed by (see ContentHashHex).
	ModuleHash string `json:"module_hash"`
	Capability string `json:"capability"`

	// PluginID and SignerPubKeyHex are display/audit metadata only, taken
	// from the manifest and (when known) the signed catalog entry. They
	// are NEVER consulted for the approve/deny decision — ModuleHash is
	// the only trusted identity.
	PluginID        string `json:"plugin_id,omitempty"`
	SignerPubKeyHex string `json:"signer_pubkey_hex,omitempty"`

	ApprovedAt time.Time `json:"approved_at"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	Note       string    `json:"note,omitempty"`
}

const capabilityPolicyFileVersion = 1

type capabilityPolicyFile struct {
	Version   int                  `json:"version"`
	Approvals []CapabilityApproval `json:"approvals"`
}

// CapabilityPolicyStore persists operator-approved module+capability pairs
// as a JSON file, keyed by module content hash. This mirrors the
// lightweight JSON-file persistence pattern used elsewhere in this codebase
// for operator-controlled install state (e.g. flowrt.FlowStore's
// artifact.json / flow.json) rather than coupling capability policy to the
// FlatSQL record store — a small operator allowlist has no need for the
// record/CID/provenance machinery internal/storage provides.
//
// A store with an empty path is in-memory only (used by tests); approvals
// still enforce for the process lifetime but do not survive restart.
type CapabilityPolicyStore struct {
	mu   sync.RWMutex
	path string
	// entries[moduleHash][capability] = approval
	entries map[string]map[string]CapabilityApproval
}

// NewCapabilityPolicyStore opens (or, if absent, prepares to create) a
// capability policy store backed by the JSON file at path. A missing file
// is NOT an error — it is a fresh node with an empty policy, which is the
// correct starting state for default-deny (loop B1 acceptance: "Default-deny
// when policy store is absent/empty").
func NewCapabilityPolicyStore(path string) (*CapabilityPolicyStore, error) {
	s := &CapabilityPolicyStore{
		path:    strings.TrimSpace(path),
		entries: make(map[string]map[string]CapabilityApproval),
	}
	if s.path == "" {
		return s, nil
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload re-reads the policy file from disk, replacing the in-memory
// approval set. Used by tests to verify approvals persist across a store
// reload, and safe to call from operators wanting to pick up out-of-band
// edits to the policy file.
func (s *CapabilityPolicyStore) Reload() error {
	if s == nil {
		return errors.New("capability policy: store is nil")
	}
	entries := make(map[string]map[string]CapabilityApproval)
	if s.path != "" {
		data, err := os.ReadFile(s.path)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("capability policy: read %s: %w", s.path, err)
			}
			// Fresh node: no file yet. Empty policy — default deny.
		} else if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			var file capabilityPolicyFile
			if err := json.Unmarshal(data, &file); err != nil {
				return fmt.Errorf("capability policy: parse %s: %w", s.path, err)
			}
			for _, approval := range file.Approvals {
				hash := strings.ToLower(strings.TrimSpace(approval.ModuleHash))
				cap := strings.TrimSpace(approval.Capability)
				if hash == "" || cap == "" {
					continue
				}
				if entries[hash] == nil {
					entries[hash] = make(map[string]CapabilityApproval)
				}
				approval.ModuleHash = hash
				approval.Capability = cap
				entries[hash][cap] = approval
			}
		}
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	return nil
}

// IsApproved reports whether the operator has recorded an approval for
// (moduleHash, capability). A nil store, an empty store, or a blank
// identity/capability all report false — default deny.
func (s *CapabilityPolicyStore) IsApproved(moduleHash, capability string) bool {
	if s == nil {
		return false
	}
	moduleHash = strings.ToLower(strings.TrimSpace(moduleHash))
	capability = strings.TrimSpace(capability)
	if moduleHash == "" || capability == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entries[moduleHash][capability]
	return ok
}

// Approve records (and persists) an operator approval for one
// (module hash, capability) pair. Returns the stored entry (ApprovedAt
// filled in when the caller left it zero).
func (s *CapabilityPolicyStore) Approve(approval CapabilityApproval) (CapabilityApproval, error) {
	if s == nil {
		return CapabilityApproval{}, errors.New("capability policy: store is nil")
	}
	approval.ModuleHash = strings.ToLower(strings.TrimSpace(approval.ModuleHash))
	approval.Capability = strings.TrimSpace(approval.Capability)
	if approval.ModuleHash == "" {
		return CapabilityApproval{}, errors.New("capability policy: module_hash is required")
	}
	if approval.Capability == "" {
		return CapabilityApproval{}, errors.New("capability policy: capability is required")
	}
	if approval.ApprovedAt.IsZero() {
		approval.ApprovedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[approval.ModuleHash] == nil {
		s.entries[approval.ModuleHash] = make(map[string]CapabilityApproval)
	}
	s.entries[approval.ModuleHash][approval.Capability] = approval
	if err := s.saveLocked(); err != nil {
		return CapabilityApproval{}, err
	}
	return approval, nil
}

// Revoke removes a recorded approval, persisting the change. Revoking an
// entry that does not exist is a no-op (not an error).
func (s *CapabilityPolicyStore) Revoke(moduleHash, capability string) error {
	if s == nil {
		return errors.New("capability policy: store is nil")
	}
	moduleHash = strings.ToLower(strings.TrimSpace(moduleHash))
	capability = strings.TrimSpace(capability)

	s.mu.Lock()
	defer s.mu.Unlock()
	byCap := s.entries[moduleHash]
	if byCap == nil {
		return nil
	}
	if _, ok := byCap[capability]; !ok {
		return nil
	}
	delete(byCap, capability)
	if len(byCap) == 0 {
		delete(s.entries, moduleHash)
	}
	return s.saveLocked()
}

// List returns every recorded approval, sorted by (module hash, capability).
func (s *CapabilityPolicyStore) List() []CapabilityApproval {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CapabilityApproval, 0)
	for _, byCap := range s.entries {
		for _, approval := range byCap {
			out = append(out, approval)
		}
	}
	sortApprovals(out)
	return out
}

// ForModule returns the approvals recorded for one module hash, sorted by
// capability.
func (s *CapabilityPolicyStore) ForModule(moduleHash string) []CapabilityApproval {
	moduleHash = strings.ToLower(strings.TrimSpace(moduleHash))
	s.mu.RLock()
	defer s.mu.RUnlock()
	byCap := s.entries[moduleHash]
	out := make([]CapabilityApproval, 0, len(byCap))
	for _, approval := range byCap {
		out = append(out, approval)
	}
	sortApprovals(out)
	return out
}

func sortApprovals(approvals []CapabilityApproval) {
	sort.Slice(approvals, func(i, j int) bool {
		if approvals[i].ModuleHash != approvals[j].ModuleHash {
			return approvals[i].ModuleHash < approvals[j].ModuleHash
		}
		return approvals[i].Capability < approvals[j].Capability
	})
}

// saveLocked serializes the current entries and atomically replaces the
// policy file (write to a temp file, then rename). Callers must hold s.mu.
func (s *CapabilityPolicyStore) saveLocked() error {
	if s.path == "" {
		return nil // in-memory only (tests) — nothing to persist
	}
	approvals := make([]CapabilityApproval, 0)
	for _, byCap := range s.entries {
		for _, approval := range byCap {
			approvals = append(approvals, approval)
		}
	}
	sortApprovals(approvals)

	file := capabilityPolicyFile{Version: capabilityPolicyFileVersion, Approvals: approvals}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("capability policy: marshal: %w", err)
	}
	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("capability policy: mkdir %s: %w", dir, err)
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("capability policy: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("capability policy: rename %s -> %s: %w", tmp, s.path, err)
	}
	return nil
}

// DefaultCapabilityPolicyPath returns the default on-disk location for the
// module capability policy file, given a node's base storage/data path
// (mirrors license.DefaultPluginRoot's baseDataPath-relative convention).
func DefaultCapabilityPolicyPath(baseDataPath string) string {
	return filepath.Join(baseDataPath, "modules", "capability_policy.json")
}

// ---------------------------------------------------------------------
// Module identity
// ---------------------------------------------------------------------

// ContentHashHex returns the canonical module identity used by the
// capability policy: the lowercase hex SHA-256 digest of the raw WASM
// artifact bytes. Preferred over a manifest-declared plugin id (spoofable —
// any module can self-report any PluginID string in its own embedded
// manifest) and stable across restarts for the exact same artifact bytes.
func ContentHashHex(wasmBytes []byte) string {
	sum := sha256.Sum256(wasmBytes)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------
// Enforcement
// ---------------------------------------------------------------------

// checkCapabilityPolicy enforces the operator capability allowlist against a
// module's manifest-declared capabilities. It returns a non-nil error
// naming every requested sensitive capability that lacks a recorded
// approval; callers MUST refuse to load/provision the module on error (fail
// closed, no partial silent grant). A nil policy is equivalent to an empty
// one — every sensitive capability is denied.
func checkCapabilityPolicy(policy *CapabilityPolicyStore, moduleHash, pluginID string, capabilities []string) error {
	var denied []string
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" || !IsSensitiveCapability(capability) {
			continue
		}
		if policy != nil && policy.IsApproved(moduleHash, capability) {
			continue
		}
		denied = append(denied, capability)
	}
	if len(denied) == 0 {
		return nil
	}
	sort.Strings(denied)

	label := strings.TrimSpace(pluginID)
	if label == "" {
		label = "unknown"
	}
	displayHash := moduleHash
	if len(displayHash) > 16 {
		displayHash = displayHash[:16]
	}
	suffix := "y"
	if len(denied) != 1 {
		suffix = "ies"
	}
	return fmt.Errorf(
		"module capability policy: module %q (hash %s...) requests unapproved sensitive capabilit%s [%s] — record an operator approval for module_hash=%s before loading (fail closed)",
		label, displayHash, suffix, strings.Join(denied, ", "), moduleHash,
	)
}
