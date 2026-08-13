package node

// CONFIGURED PURPOSE KEYS — the "UNLESS they specifically setup another key"
// half of the owner's 2026-08-07 ruling.
//
// The DEFAULT for every server-side purpose key is derive-from-node-root
// (internal/wasm/hdwallet_purpose.go). This file is the opt-in EXCEPTION: an
// operator points a CONFIGURABLE purpose at an external key they supply. The
// record is persisted in the node's encrypted credential keystore
// (internal/credstore — Argon2id + XChaCha20-Poly1305, machine-bound root, the
// same envelope the node's own identity key uses at rest), NEVER as a plaintext
// key file: the ruling's opening sentence exists precisely because ".txt key
// files" are the failure mode.
//
// STRUCTURAL LIMITS, enforced here and asserted by test:
//
//   - The FLEET UPDATE ROOT (identity-signing, the 0' child) is NEVER
//     configurable. One key, one power, isolated by standing owner ruling
//     (graph/tasks/sdn-grant-verifier-key-domain-separation.md).
//   - The encryption purpose is not configurable either: its public half is
//     published in the node EPM and every encrypted record addressed to this
//     node seals to it — swapping it would orphan that ciphertext silently.
//   - A configured key may not COLLIDE with another purpose's key. The same
//     guard that keeps the grant signer distinct from the update root
//     (grantSigningKeyDomainConflict) rules on the operator-supplied seed,
//     fail closed, BEFORE anything is persisted.
//
// A configured key is NOT reproducible from the node mnemonic. The operator
// holds backup responsibility for it, and every surface that reports it says so
// (ManagedKey.Reproducible=false, provenance "external-configured").

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// PurposeKeyConfig is the persisted record for one operator-configured external
// purpose key. It lives INSIDE the encrypted keystore; this struct never
// touches disk in plaintext. JSON keys are lowercase snake_case (node-local
// operational record, not an SDS record).
type PurposeKeyConfig struct {
	Purpose string `json:"purpose"`

	// SeedHex is the 32-byte Ed25519 seed, hex-encoded. Secret material — the
	// struct is only ever serialized INTO the encrypted keystore, and the API
	// layer never returns it.
	SeedHex string `json:"seed_hex"`

	// BondAddresses are the chain addresses whose balances back THIS key under
	// the adversarial-security model, keyed "bitcoin"/"ethereum"/"solana".
	// Optional: an external key with no separately funded addresses is simply
	// unbonded, reported as such, never given an invented share of the root's
	// bond.
	BondAddresses map[string]string `json:"bond_addresses,omitempty"`

	ConfiguredAt string `json:"configured_at"`
	// ConfiguredBy is the configuring admin session's fingerprint (sha256[:12]
	// of the session xpub — the deployment/signing.json convention), never a
	// raw xpub.
	ConfiguredBy string `json:"configured_by,omitempty"`
}

// bondChains is the closed set of chains a bond address may be posted on —
// exactly the set the bond-attestation module can read balances for.
var bondChains = map[string]bool{"bitcoin": true, "ethereum": true, "solana": true}

// ErrPurposeKeyStoreUnavailable wraps a failure to OPEN the encrypted keystore
// at all (no storage path, identity not yet unlocked). Distinct from a bad
// stored record on purpose: a node that never configured anything can be in
// this state, and treating it as a refusal would kill grants fleet-wide for a
// feature nobody used. A record that EXISTS but cannot be read/parsed is NOT
// wrapped in this sentinel and stays fail-closed.
var ErrPurposeKeyStoreUnavailable = errors.New("purpose-key store unavailable")

// PurposeKeyConfigurable reports whether an operator may point this purpose at
// a dedicated external key, and — when they may not — the reason, stated so a
// UI can render the refusal rather than hiding the control silently.
func PurposeKeyConfigurable(purpose wasm.KeyPurpose) (bool, string) {
	if !purpose.Registered() {
		return false, fmt.Sprintf("purpose %d is not registered in the node identity contract (internal/wasm/hdwallet_purpose.go); unregistered purposes cannot hold keys at all", uint32(purpose))
	}
	switch purpose {
	case wasm.PurposeIdentitySigning:
		return false, "this is the FLEET UPDATE ROOT: it stays isolated and non-configurable by standing owner ruling (2026-08-07, graph/tasks/sdn-grant-verifier-key-domain-separation.md)"
	case wasm.PurposeEncryption:
		return false, "the encryption key's public half is published in the node EPM; every encrypted record addressed to this node seals to it, so replacing it would silently orphan that ciphertext"
	default:
		return true, ""
	}
}

// ConfigurablePurposeKeys enumerates the purposes an operator may configure,
// ascending, driven by the same registry the inventory enumerates from.
func ConfigurablePurposeKeys() []wasm.KeyPurpose {
	var out []wasm.KeyPurpose
	for _, p := range wasm.RegisteredPurposes() {
		if ok, _ := PurposeKeyConfigurable(p); ok {
			out = append(out, p)
		}
	}
	return out
}

// PurposeByLabel resolves a registry label ("licensing-grant") back to its
// purpose. The API layer speaks labels; indices never cross the HTTP boundary.
func PurposeByLabel(label string) (wasm.KeyPurpose, bool) {
	label = strings.TrimSpace(label)
	for _, p := range wasm.RegisteredPurposes() {
		if p.Label() == label {
			return p, true
		}
	}
	return 0, false
}

// purposeKeyLaneID is the credential-keystore lane holding a purpose's
// configured key ("purpose-key-licensing-grant"). Registry labels are already
// lowercase-hyphen, i.e. valid lane charset by construction.
func purposeKeyLaneID(purpose wasm.KeyPurpose) string {
	label := purpose.Label()
	if label == "" {
		return ""
	}
	return "purpose-key-" + label
}

// purposeKeyCache avoids re-deriving the keystore root (Argon2id, deliberately
// expensive) on every inventory read. Keyed by storage path rather than hung
// off Node so the one-daemon-per-box process shape stays the invariant that
// makes it sound; Configure/Clear update it in place.
var purposeKeyCache = struct {
	mu      sync.RWMutex
	byStore map[string]map[string]*PurposeKeyConfig // storagePath -> lane -> cfg (nil = known absent)
}{byStore: make(map[string]map[string]*PurposeKeyConfig)}

func purposeKeyCacheGet(storePath, lane string) (*PurposeKeyConfig, bool) {
	purposeKeyCache.mu.RLock()
	defer purposeKeyCache.mu.RUnlock()
	lanes, ok := purposeKeyCache.byStore[storePath]
	if !ok {
		return nil, false
	}
	cfg, ok := lanes[lane]
	return cfg, ok
}

func purposeKeyCacheSet(storePath, lane string, cfg *PurposeKeyConfig) {
	purposeKeyCache.mu.Lock()
	defer purposeKeyCache.mu.Unlock()
	lanes, ok := purposeKeyCache.byStore[storePath]
	if !ok {
		lanes = make(map[string]*PurposeKeyConfig)
		purposeKeyCache.byStore[storePath] = lanes
	}
	lanes[lane] = cfg
}

// openPurposeKeyStore opens the node's encrypted credential keystore. Fail
// closed: no storage path or no unlocked identity key means no store, never a
// weaker envelope.
func (n *Node) openPurposeKeyStore() (*credstore.Store, string, error) {
	if n == nil || n.config == nil {
		return nil, "", errors.New("node configuration is not available")
	}
	storagePath := strings.TrimSpace(n.config.Storage.Path)
	if storagePath == "" {
		return nil, "", errors.New("node storage path is not configured")
	}
	ikm := n.IdentityKeyMaterial()
	if len(ikm) == 0 {
		return nil, "", errors.New("node identity key material is unavailable (host not started)")
	}
	store, err := credstore.OpenStore(storagePath, ikm)
	if err != nil {
		return nil, "", err
	}
	return store, storagePath, nil
}

// ConfiguredPurposeKey returns the operator-configured external key for a
// purpose, nil when none is configured, and an error when one IS configured but
// cannot be read — which callers must treat as a refusal to provision, never as
// "fall back to the derived default": signing with a different key than the one
// configured is the silent substitution this whole surface exists to prevent.
func (n *Node) ConfiguredPurposeKey(purpose wasm.KeyPurpose) (*PurposeKeyConfig, error) {
	lane := purposeKeyLaneID(purpose)
	if lane == "" {
		return nil, nil
	}
	if ok, _ := PurposeKeyConfigurable(purpose); !ok {
		// Non-configurable purposes have no lane by definition. Answering nil
		// (rather than probing the store) keeps the update root structurally
		// outside this surface.
		return nil, nil
	}
	if n == nil || n.config == nil {
		return nil, nil
	}
	storagePath := strings.TrimSpace(n.config.Storage.Path)
	if cfg, ok := purposeKeyCacheGet(storagePath, lane); ok {
		return cfg, nil
	}

	store, storagePath, err := n.openPurposeKeyStore()
	if err != nil {
		// Cannot know either way — do NOT cache. Wrapped in the sentinel so
		// callers can distinguish "the store itself is unavailable" (a state
		// every never-configured node can be in: no storage path, host not
		// started) from "a configured record is present but bad" (fail closed).
		return nil, fmt.Errorf("%w: %v", ErrPurposeKeyStoreUnavailable, err)
	}
	cred, err := store.Reveal(lane)
	if errors.Is(err, credstore.ErrNotConfigured) {
		purposeKeyCacheSet(storagePath, lane, nil)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read configured %s key: %w", purpose, err)
	}
	cfg, err := parsePurposeKeyConfig(purpose, cred.Secret.Reveal())
	if err != nil {
		return nil, err
	}
	purposeKeyCacheSet(storagePath, lane, cfg)
	return cfg, nil
}

// parsePurposeKeyConfig validates a stored record. A corrupt record is an
// error, not an absent key: fail closed.
func parsePurposeKeyConfig(purpose wasm.KeyPurpose, raw string) (*PurposeKeyConfig, error) {
	var cfg PurposeKeyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("configured %s key record is not valid JSON: %w", purpose, err)
	}
	if cfg.Purpose != purpose.Label() {
		return nil, fmt.Errorf("configured key record names purpose %q but is stored under %q", cfg.Purpose, purpose.Label())
	}
	if _, err := decodePurposeKeySeed(cfg.SeedHex); err != nil {
		return nil, fmt.Errorf("configured %s key: %w", purpose, err)
	}
	return &cfg, nil
}

// decodePurposeKeySeed validates and decodes an operator-supplied seed.
func decodePurposeKeySeed(seedHex string) ([]byte, error) {
	seedHex = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(seedHex)), "0x")
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, fmt.Errorf("seed is not valid hex: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("seed must be %d bytes (%d hex characters), got %d bytes", ed25519.SeedSize, ed25519.SeedSize*2, len(seed))
	}
	return seed, nil
}

// validateBondAddresses admits only the chains the attestation module can
// read, with plausibly-shaped values. It does NOT verify ownership — the bond
// model never needs it to: balances are read from the chain, and claiming a
// stranger's funded address buys an attacker nothing they can sign with.
func validateBondAddresses(addrs map[string]string) (map[string]string, error) {
	if len(addrs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(addrs))
	for chain, addr := range addrs {
		chain = strings.ToLower(strings.TrimSpace(chain))
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if !bondChains[chain] {
			keys := make([]string, 0, len(bondChains))
			for k := range bondChains {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return nil, fmt.Errorf("unknown bond chain %q: the attestation module reads %s", chain, strings.Join(keys, ", "))
		}
		if len(addr) > 128 {
			return nil, fmt.Errorf("%s bond address is implausibly long (%d chars)", chain, len(addr))
		}
		for _, r := range addr {
			if r <= 0x20 || r > 0x7e {
				return nil, fmt.Errorf("%s bond address contains a non-printable or non-ASCII character", chain)
			}
		}
		out[chain] = addr
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// configuredPurposeKeyCollision refuses an operator-supplied seed that would
// make two duties share a key. Same shape as grantSigningKeyDomainConflict —
// constant-time comparisons, public-key equality checked independently of seed
// equality — extended to every OTHER purpose key this node manages.
func (n *Node) configuredPurposeKeyCollision(purpose wasm.KeyPurpose, seed []byte) string {
	if len(seed) != ed25519.SeedSize {
		return "seed is not a 32-byte Ed25519 seed"
	}
	// The update root. grantSigningKeyDomainConflict already states this
	// refusal in operator terms; every configurable purpose reuses it verbatim
	// because the property is identical: fleet code authority must not be
	// reachable through a sibling lane.
	if reason := grantSigningKeyDomainConflict(seed, n.updateRootSigningSeed()); reason != "" {
		return reason
	}
	// The encryption scalar. An Ed25519 seed equal to the X25519 scalar would
	// drive one secret through two algorithms — the exact cross-protocol reuse
	// the keyslot oracle refuses at runtime; refuse it at configure time too.
	if n != nil && n.identity != nil && len(n.identity.EncryptionKey) == ed25519.SeedSize {
		if subtle.ConstantTimeCompare(seed, n.identity.EncryptionKey[:ed25519.SeedSize]) == 1 {
			return "the supplied seed is byte-identical to the node's encryption key scalar; one secret must not serve two algorithms"
		}
	}
	// Every OTHER purpose's key (today: none beyond the two above are
	// derivable), so a future purpose registered at 3'+ is covered without a
	// second edit here.
	for _, other := range wasm.RegisteredPurposes() {
		if other == purpose || other == wasm.PurposeIdentitySigning || other == wasm.PurposeEncryption {
			continue
		}
		otherSeed := n.derivedPurposeSeed(other)
		if len(otherSeed) != ed25519.SeedSize {
			continue
		}
		if subtle.ConstantTimeCompare(seed, otherSeed) == 1 {
			return fmt.Sprintf("the supplied seed is byte-identical to the node's derived %s key; two purposes must not share a key", other)
		}
	}
	return ""
}

// derivedPurposeSeed returns the node's DERIVED (default) seed for a purpose,
// or nil. Only used by the collision guard above.
func (n *Node) derivedPurposeSeed(purpose wasm.KeyPurpose) []byte {
	if n == nil {
		return nil
	}
	switch purpose {
	case wasm.PurposeLicensingGrant:
		if n.identity != nil {
			if raw, err := n.identity.RawGrantSigningKey(); err == nil && len(raw) == ed25519.SeedSize {
				return raw
			}
			return nil
		}
		if identity, err := n.loadLegacyServerIdentity(); err == nil && identity != nil &&
			identity.SigningKey != nil && len(identity.SigningKey.PrivateKey) >= 32 {
			return legacyGrantSigningSeed(identity.SigningKey.PrivateKey[:32])
		}
	}
	return nil
}

// ConfigurePurposeKey validates and persists a dedicated external key for a
// configurable purpose. Nothing is persisted unless every check passes; the
// running licensing runtime keeps its boot-provisioned key until the daemon is
// restarted, which the caller must surface (restart_required).
func (n *Node) ConfigurePurposeKey(purpose wasm.KeyPurpose, seedHex string, bondAddrs map[string]string, configuredBy string) (*PurposeKeyConfig, error) {
	if ok, reason := PurposeKeyConfigurable(purpose); !ok {
		return nil, fmt.Errorf("purpose %s is not configurable: %s", purpose, reason)
	}
	seed, err := decodePurposeKeySeed(seedHex)
	if err != nil {
		return nil, err
	}
	// A seed that yields no usable verifier key (all-zero public key included)
	// is refused by the same derivation the signer uses.
	if _, err := ed25519PublicFromSeed(seed); err != nil {
		return nil, err
	}
	if reason := n.configuredPurposeKeyCollision(purpose, seed); reason != "" {
		return nil, fmt.Errorf("REFUSED: %s", reason)
	}
	cleanAddrs, err := validateBondAddresses(bondAddrs)
	if err != nil {
		return nil, err
	}

	cfg := &PurposeKeyConfig{
		Purpose:       purpose.Label(),
		SeedHex:       hex.EncodeToString(seed),
		BondAddresses: cleanAddrs,
		ConfiguredAt:  time.Now().UTC().Format(time.RFC3339),
		ConfiguredBy:  strings.TrimSpace(configuredBy),
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode purpose key record: %w", err)
	}

	store, storagePath, err := n.openPurposeKeyStore()
	if err != nil {
		return nil, err
	}
	lane := purposeKeyLaneID(purpose)
	if err := store.Put(lane, "purpose:"+purpose.Label(), string(payload)); err != nil {
		return nil, fmt.Errorf("persist configured %s key: %w", purpose, err)
	}
	purposeKeyCacheSet(storagePath, lane, cfg)
	log.Infof(
		"Purpose key CONFIGURED: %s now has a dedicated external key (verifier %x, provenance external-configured, configured_by=%s). It is NOT derivable from the node mnemonic — the operator holds backup responsibility. The running runtime keeps its boot key until restart.",
		purpose, mustPurposePub(seed), cfg.ConfiguredBy,
	)
	return cfg, nil
}

// ClearPurposeKey removes a configured external key, returning the purpose to
// the ruled default: derive from the node root. Idempotent.
func (n *Node) ClearPurposeKey(purpose wasm.KeyPurpose) error {
	if ok, reason := PurposeKeyConfigurable(purpose); !ok {
		return fmt.Errorf("purpose %s is not configurable: %s", purpose, reason)
	}
	store, storagePath, err := n.openPurposeKeyStore()
	if err != nil {
		return err
	}
	lane := purposeKeyLaneID(purpose)
	if err := store.Clear(lane); err != nil {
		return fmt.Errorf("clear configured %s key: %w", purpose, err)
	}
	purposeKeyCacheSet(storagePath, lane, nil)
	log.Infof("Purpose key CLEARED: %s returns to the ruled default (derived from the node root at its contract path) at next restart.", purpose)
	return nil
}

// mustPurposePub renders the public half for a log line; the seed was already
// validated by the caller.
func mustPurposePub(seed []byte) []byte {
	pub, err := ed25519PublicFromSeed(seed)
	if err != nil {
		return nil
	}
	return pub
}
