// Package pmm serves this node's $PMM Provider Module Manifest: the signed,
// anonymously-fetchable list of WASM modules this provider offers.
//
// BOUNDARY. This package is a CONNECTOR, not an application. It does exactly
// four things: read a catalog file that DECLARES what the node offers, hash the
// artifact bytes the node actually serves, sign the canonical statement with the
// node key, and serve the result. Every policy decision — which module is CORE,
// which is ENTITLED, which is browsable — arrives as DATA in the catalog file.
// No tier table, no licensing logic and no storefront logic lives in Go.
//
// Trust chain (schema/PMM/main.fbs:36-54), verified by the CLIENT in this order:
//
//	artifact bytes -> CONTENT_HASH -> ARTIFACT_SIGNATURE -> PMM.SIGNATURE
//	-> node key -> domain (DNS proof) -> economic weight (bond)
package pmm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Signer signs a message with the node key. Satisfied by *keys.Manager.
//
// Fail-closed: a nil Signer means the manifest cannot be produced at all. An
// unsigned $PMM is worthless — the whole record exists to be verified — so this
// package never degrades to serving one.
type Signer interface {
	Sign(data []byte) ([]byte, error)
}

// Trust anchor field values. Read from the node's own identity at build time;
// this package never invents one. An anchor field the node cannot prove stays
// EMPTY (owner/oracle ruling 2026-07-30: an absent DNS proof is published as an
// empty DNS_PROOF_TXT, never as a fabricated one).
type TrustAnchor struct {
	ProviderDomain      string       `json:"PROVIDER_DOMAIN"`
	NodePeerID          string       `json:"NODE_PEER_ID"`
	NodeXpub            string       `json:"NODE_XPUB"`
	SigningPublicKey    string       `json:"SIGNING_PUBLIC_KEY"`
	SigningKeyPath      string       `json:"SIGNING_KEY_PATH"`
	SignatureAlgorithm  string       `json:"SIGNATURE_ALGORITHM"`
	EPMCID              string       `json:"EPM_CID"`
	DNSProofRecordName  string       `json:"DNS_PROOF_RECORD_NAME"`
	DNSProofTXT         string       `json:"DNS_PROOF_TXT"`
	DNSProofStatement   string       `json:"DNS_PROOF_STATEMENT"`
	BondAddresses       []ChainProof `json:"BOND_ADDRESSES"`
	BondAttestationURL  string       `json:"BOND_ATTESTATION_URL"`
}

// ChainProof is one bond address derived from the node key.
type ChainProof struct {
	Chain     string `json:"CHAIN"`
	Address   string `json:"ADDRESS"`
	KeyPath   string `json:"KEY_PATH"`
	PublicKey string `json:"PUBLIC_KEY"`
	Signature string `json:"SIGNATURE"`
}

// Entry is one offered module, as declared by the catalog.
type Entry struct {
	ModuleID    string   `json:"MODULE_ID"`
	PluginID    string   `json:"PLUGIN_ID"`
	PLGCID      string   `json:"PLG_CID"`
	Name        string   `json:"NAME"`
	Description string   `json:"DESCRIPTION"`
	Version     string   `json:"VERSION"`
	Epoch       uint64   `json:"EPOCH"`
	ContentHash string   `json:"CONTENT_HASH"`
	SizeBytes   uint64   `json:"ARTIFACT_SIZE_BYTES"`
	ArtifactPath string  `json:"ARTIFACT_PATH"`
	ArtifactCID string   `json:"ARTIFACT_CID"`
	// ArtifactSignature is lowercase hex of a detached signature over the 32 RAW
	// bytes of ContentHash (never the 64 hex characters). It is EMPTY until the
	// Seal Council's domain-separation dissent is resolved by the owner; see
	// SignArtifacts. PMM.SIGNATURE already covers every CONTENT_HASH through the
	// canonical statement, so an empty value costs a bandwidth optimisation, not
	// integrity.
	ArtifactSignature string   `json:"ARTIFACT_SIGNATURE"`
	TrustTier         string   `json:"TRUST_TIER"`
	DefaultEnabled    bool     `json:"DEFAULT_ENABLED"`
	AccessPolicy      string   `json:"ACCESS_POLICY"`
	EntryState        string   `json:"ENTRY_STATE"`
	RuntimeTargets    []string `json:"RUNTIME_TARGETS"`
	RequiredSchemas   []string `json:"REQUIRED_SCHEMAS"`
	MinPermissions    []string `json:"MIN_PERMISSIONS"`
	License           string   `json:"LICENSE"`
	DocumentationURL  string   `json:"DOCUMENTATION_URL"`
	IconURL           string   `json:"ICON_URL"`
	SupersedesHash    string   `json:"SUPERSEDES_CONTENT_HASH"`
	UpdatedAt         string   `json:"UPDATED_AT"`

	// SourceArtifact is the on-disk file whose bytes back this entry. It is
	// catalog INPUT: read from the catalog file, hashed, and then dropped — it
	// is never encoded into the record or the signed statement, because a client
	// must not learn the provider's local layout.
	SourceArtifact string `json:"source_artifact,omitempty"`
}

// Manifest is the whole record.
type Manifest struct {
	ProviderDomain  string      `json:"PROVIDER_DOMAIN"`
	ProviderName    string      `json:"PROVIDER_NAME"`
	Description     string      `json:"DESCRIPTION"`
	Epoch           uint64      `json:"EPOCH"`
	Trust           TrustAnchor `json:"TRUST"`
	Modules         []Entry     `json:"MODULES"`
	CanonicalURL    string      `json:"CANONICAL_URL"`
	CreatedAt       string      `json:"CREATED_AT"`
	UpdatedAt       string      `json:"UPDATED_AT"`
	ExpiresAt       string      `json:"EXPIRES_AT"`
	Signature       string      `json:"SIGNATURE"`
	SignedStatement string      `json:"SIGNED_STATEMENT"`
}

// Enum symbol sets. The canonical statement prints enums as their IDL SYMBOL
// NAMES, so an out-of-vocabulary value would produce a statement no verifier can
// rebuild. Reject at build time rather than serve an unverifiable manifest.
var (
	validTiers   = map[string]bool{"UNSPECIFIED": true, "CORE": true, "RECOMMENDED": true, "OPTIONAL": true}
	validAccess  = map[string]bool{"ANONYMOUS": true, "AUTHENTICATED": true, "ENTITLED": true}
	validStates  = map[string]bool{"ACTIVE": true, "DEPRECATED": true, "WITHDRAWN": true, "REVOKED": true}
)

// ErrNoSigner is returned when a manifest is requested with no node key.
var ErrNoSigner = errors.New("pmm: no signer; refusing to produce an unsigned manifest")

// SortModules orders entries bytewise ascending by MODULE_ID, which the
// canonical statement requires and the FlatBuffers `key` attribute assumes.
func SortModules(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ModuleID < entries[j].ModuleID })
}

// Validate enforces every invariant that would otherwise produce a record a
// client must reject: duplicate keys, bad enums, malformed hashes.
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.ProviderDomain) == "" {
		return errors.New("pmm: PROVIDER_DOMAIN is required")
	}
	if m.Trust.ProviderDomain != m.ProviderDomain {
		return fmt.Errorf("pmm: TRUST.PROVIDER_DOMAIN %q must equal PROVIDER_DOMAIN %q",
			m.Trust.ProviderDomain, m.ProviderDomain)
	}
	seen := make(map[string]struct{}, len(m.Modules))
	for i := range m.Modules {
		e := &m.Modules[i]
		if strings.TrimSpace(e.ModuleID) == "" {
			return fmt.Errorf("pmm: entry %d has empty MODULE_ID", i)
		}
		if _, dup := seen[e.ModuleID]; dup {
			// MODULE_ID is `(required, key)` — unique within the manifest.
			return fmt.Errorf("pmm: duplicate MODULE_ID %q", e.ModuleID)
		}
		seen[e.ModuleID] = struct{}{}
		if !validTiers[e.TrustTier] {
			return fmt.Errorf("pmm: %s has invalid TRUST_TIER %q", e.ModuleID, e.TrustTier)
		}
		if !validAccess[e.AccessPolicy] {
			return fmt.Errorf("pmm: %s has invalid ACCESS_POLICY %q", e.ModuleID, e.AccessPolicy)
		}
		if !validStates[e.EntryState] {
			return fmt.Errorf("pmm: %s has invalid ENTRY_STATE %q", e.ModuleID, e.EntryState)
		}
		if len(e.ContentHash) != 64 {
			return fmt.Errorf("pmm: %s CONTENT_HASH must be 64 hex chars, got %d", e.ModuleID, len(e.ContentHash))
		}
		if e.ContentHash != strings.ToLower(e.ContentHash) {
			return fmt.Errorf("pmm: %s CONTENT_HASH must be lowercase", e.ModuleID)
		}
		if _, err := hex.DecodeString(e.ContentHash); err != nil {
			return fmt.Errorf("pmm: %s CONTENT_HASH is not hex: %w", e.ModuleID, err)
		}
		// An anonymously-loadable entry with no path is unfetchable; an ENTITLED
		// entry with a path would leak closed bytes to anonymous clients.
		if e.AccessPolicy == "ANONYMOUS" && strings.TrimSpace(e.ArtifactPath) == "" {
			return fmt.Errorf("pmm: %s is ANONYMOUS but has no ARTIFACT_PATH", e.ModuleID)
		}
		if e.AccessPolicy == "ENTITLED" && strings.TrimSpace(e.ArtifactPath) != "" {
			return fmt.Errorf("pmm: %s is ENTITLED and must not publish an anonymous ARTIFACT_PATH", e.ModuleID)
		}
	}
	return nil
}

// HashArtifact returns the SHA-256 of the portable, pre-AOT bytes the node
// actually serves for this entry — the identity capability and signature
// policies key on.
func HashArtifact(path string) (string, uint64, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", 0, fmt.Errorf("pmm: read artifact %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), uint64(len(data)), nil
}

// CanonicalStatement rebuilds the exact bytes PMM.SIGNATURE covers.
//
// From schema/PMM/main.fbs:36-54: UTF-8, LF endings, no CR, no trailing
// whitespace, EVERY line LF-terminated including the last. Enums as IDL symbol
// names. Booleans as 1 or 0. One module: line per entry, sorted bytewise
// ascending by MODULE_ID, single ASCII space between fields.
//
// A verifier MUST rebuild this from the record's own fields, so this function is
// the single source of truth on both the signing and the verifying side.
func CanonicalStatement(m *Manifest) string {
	entries := make([]Entry, len(m.Modules))
	copy(entries, m.Modules)
	SortModules(entries)

	var b strings.Builder
	b.WriteString("SDN-MODULE-MANIFEST-V1\n")
	b.WriteString("domain:" + m.ProviderDomain + "\n")
	b.WriteString("peer:" + m.Trust.NodePeerID + "\n")
	b.WriteString("key:" + m.Trust.SigningPublicKey + "\n")
	fmt.Fprintf(&b, "epoch:%d\n", m.Epoch)
	b.WriteString("expires:" + m.ExpiresAt + "\n")
	for _, e := range entries {
		enabled := "0"
		if e.DefaultEnabled {
			enabled = "1"
		}
		b.WriteString("module:" + e.ModuleID + " " + e.Version + " " + e.ContentHash + " " +
			e.TrustTier + " " + e.AccessPolicy + " " + enabled + " " + e.EntryState + "\n")
	}
	return b.String()
}

// Sign fills SIGNED_STATEMENT and SIGNATURE using the node key.
//
// The statement is domain-separated by construction — it opens with the literal
// SDN-MODULE-MANIFEST-V1 — so signing it carries no cross-protocol replay
// hazard. That is NOT true of a bare 32-byte artifact digest, which is why
// ARTIFACT_SIGNATURE is deliberately left unset here.
func Sign(m *Manifest, signer Signer) error {
	if signer == nil {
		return ErrNoSigner
	}
	if err := m.Validate(); err != nil {
		return err
	}
	SortModules(m.Modules)
	statement := CanonicalStatement(m)
	sig, err := signer.Sign([]byte(statement))
	if err != nil {
		return fmt.Errorf("pmm: sign manifest: %w", err)
	}
	m.SignedStatement = statement
	m.Signature = hex.EncodeToString(sig)
	return nil
}

// VerifyStatement rebuilds the canonical statement and reports whether the
// carried copy agrees. The schema requires a verifier to reject any difference,
// so the carried copy can never become a second source of truth.
func VerifyStatement(m *Manifest) error {
	rebuilt := CanonicalStatement(m)
	if m.SignedStatement != "" && m.SignedStatement != rebuilt {
		return errors.New("pmm: SIGNED_STATEMENT does not match the record's own fields")
	}
	return nil
}
