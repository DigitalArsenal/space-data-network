package wasm

// PURPOSE KEYS — the node's derive-child-for-purpose contract.
//
// OWNER RULING 2026-08-07, verbatim:
//
//	"In the absence of a specific distribution key, use the node's root key to
//	 sign / distribute without needing to create separate .txt key files... we do
//	 need it configurable. The adversarial security idea does work on any keys
//	 that the server controls, not just the root node key... the keys should
//	 always be derived from the hd node root key, UNLESS they specifically setup
//	 another key, and we need to be able to show the rollup of all value across
//	 all keys that are being managed by a server."
//
// So: DERIVE-FROM-NODE-ROOT IS THE DEFAULT for every server-side purpose key. An
// operator never has to create a key file to get a working signer. Supplying an
// external key is the opt-in EXCEPTION, and which of the two a key came from must
// be visible — see KeyProvenance.
//
// The one exception to "derive on demand and move on" is the FLEET UPDATE ROOT.
// It stays isolated by owner ruling (graph/tasks/sdn-grant-verifier-key-domain-separation.md):
// one key, one power, with a fail-closed boot guard. Everything else is a sibling.
//
// THE GRAMMAR. A purpose is the CHANGE-LEVEL index of the account:
//
//	m/44'/0'/<account>'/<purpose>'/0'
//
// Fully hardened at every level, which is what makes a purpose key's PUBLIC half
// safe to publish: SLIP-0010 Ed25519 has no public derivation at all, so
// disclosing a child reveals nothing about the parent or any sibling.
//
// ADDING A PURPOSE IS A CONTRACT CHANGE, not an implementation detail. An index
// picked locally by one lane and reused by another silently makes two duties share
// a key — precisely the defect this whole contract exists to prevent. So the set is
// a REGISTRY, DeriveChildForPurpose REFUSES an unregistered purpose, and a new entry
// must land here and in §2.1 of graph/tasks/nst-node-admin-contract.md together.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha512"
	"fmt"
	"io"
	"sort"

	"github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/hkdf"
)

// KeyPurpose is the change-level index in the node's derivation grammar.
type KeyPurpose uint32

const (
	// PurposeIdentitySigning is the node identity Ed25519 key: admit-point
	// challenges, EPM/$PNM records, dataset publications — and, critically,
	// SDN-UPDATE-MANIFEST-V1 and SDN-MODULE-PUBLICATION-V1. It is the FLEET
	// UPDATE / PUBLISHER ROOT. Nothing else may borrow it.
	PurposeIdentitySigning KeyPurpose = 0

	// PurposeEncryption is the X25519 key agreement key.
	PurposeEncryption KeyPurpose = 1

	// PurposeLicensingGrant signs module-delivery and storefront access grants.
	// Its public half is published in KRF.PUBLIC_KEY, stamped into every grant as
	// LGR.GRANT_VERIFIER_PUBKEY, advertised in the node EPM and at
	// /api/module-delivery/provider.
	PurposeLicensingGrant KeyPurpose = 2
)

// purposeLabels is the REGISTRY. A purpose absent from this map cannot be derived.
//
// Labels are stable identifiers: they appear in the key inventory, in the legacy
// KDF domain string, and in operator-facing UI. Renaming one is a breaking change
// for the legacy derivation, so version the label rather than rename it.
//
// RESERVED: indices 3 and up are unallocated. The module-publication /
// distribution lane and any future purpose MUST register here rather than picking
// an index locally.
var purposeLabels = map[KeyPurpose]string{
	PurposeIdentitySigning: "identity-signing",
	PurposeEncryption:      "encryption",
	PurposeLicensingGrant:  "licensing-grant",
}

// purposeDescriptions is operator-facing prose for the key-management surface, so
// a UI never has to invent an explanation of what a key does.
var purposeDescriptions = map[KeyPurpose]string{
	PurposeIdentitySigning: "Node identity signing key. Signs sign-in challenges, EPM and PNM records, dataset publications, update manifests and module publications. THIS IS THE FLEET UPDATE ROOT: whatever it signs, every box in the cluster will install and run.",
	PurposeEncryption:      "Node encryption key. Key agreement for encrypted records and module bundles; signs nothing.",
	PurposeLicensingGrant:  "Licensing grant signing key. Signs every module-delivery and storefront access grant. Its public half is what clients verify grants against.",
}

// Label returns the registered label, or "" when the purpose is unregistered.
func (p KeyPurpose) Label() string { return purposeLabels[p] }

// Description returns operator-facing prose for the purpose.
func (p KeyPurpose) Description() string { return purposeDescriptions[p] }

// Registered reports whether this purpose is part of the contract.
func (p KeyPurpose) Registered() bool {
	_, ok := purposeLabels[p]
	return ok
}

func (p KeyPurpose) String() string {
	if label := p.Label(); label != "" {
		return label
	}
	return fmt.Sprintf("unregistered-purpose-%d", uint32(p))
}

// RegisteredPurposes returns every purpose in the contract, ascending. The key
// inventory and the key-management UI enumerate from here so a newly registered
// purpose appears everywhere at once.
func RegisteredPurposes() []KeyPurpose {
	out := make([]KeyPurpose, 0, len(purposeLabels))
	for purpose := range purposeLabels {
		out = append(out, purpose)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PurposeKeyPath renders the derivation path for a purpose and account.
func PurposeKeyPath(purpose KeyPurpose, account uint32) string {
	return fmt.Sprintf("m/44'/0'/%d'/%d'/0'", account, uint32(purpose))
}

// KeyProvenance says where a server-managed key came from. The owner requires this
// to be visible: a UI must be able to annotate "signing with the node root key"
// versus "signing with a key you configured", and the bond rollup has to attribute
// value to the right key.
type KeyProvenance string

const (
	// ProvenanceDerivedFromNodeRoot — the DEFAULT. A hardened SLIP-0010 child of
	// the node's HD root at PurposeKeyPath. No key file exists, nothing was
	// persisted, and the key is reproducible from the node mnemonic alone.
	ProvenanceDerivedFromNodeRoot KeyProvenance = "derived-from-node-root"

	// ProvenanceDerivedFromNodeRootLegacyKDF — same intent, for a node whose
	// identity predates the HD scheme and therefore has no seed to derive at a
	// path. HKDF-SHA512 over the identity signing key under a versioned,
	// purpose-labelled domain. Still derived, still nothing on disk.
	ProvenanceDerivedFromNodeRootLegacyKDF KeyProvenance = "derived-from-node-root-legacy-kdf"

	// ProvenanceExternalConfigured — the opt-in EXCEPTION: an operator supplied
	// this key explicitly. It is NOT reproducible from the node mnemonic, so it
	// must be backed up separately and it survives no rebuild on its own.
	ProvenanceExternalConfigured KeyProvenance = "external-configured"
)

// Reproducible reports whether the key can be re-derived from the node mnemonic
// alone. False means an operator holds backup responsibility for it.
func (p KeyProvenance) Reproducible() bool {
	return p == ProvenanceDerivedFromNodeRoot || p == ProvenanceDerivedFromNodeRootLegacyKDF
}

// PurposeKey is one server-managed key, with everything the key-management surface
// and the bond rollup need to describe it. The PRIVATE half is deliberately absent
// from this struct: it is a description, and descriptions get logged and serialized.
type PurposeKey struct {
	Purpose KeyPurpose

	// Path is the BIP-32/SLIP-10 derivation path, or "" when the key was not
	// derived at a path (legacy KDF, or externally configured).
	Path string

	// KDFDomain is the HKDF label, set only for the legacy-KDF provenance.
	KDFDomain string

	// PublicKey is the raw public key. Ed25519 = 32 bytes, X25519 = 32 bytes.
	PublicKey []byte

	// Algorithm is "ed25519" or "x25519".
	Algorithm string

	Provenance KeyProvenance

	// IsUpdateRoot marks the ONE key that carries fleet code authority. Exactly
	// one server-managed key may have this set; a key-management UI must warn
	// loudly before any action that would rotate or replace it.
	IsUpdateRoot bool
}

// LegacyPurposeKDFDomain is the versioned HKDF label for a purpose on nodes with
// no HD seed. Versioned so the derivation can never be silently changed under a
// fleet that has already published the corresponding public key.
func LegacyPurposeKDFDomain(purpose KeyPurpose) string {
	label := purpose.Label()
	if label == "" {
		return ""
	}
	return "SDN-PURPOSE-" + label + "-V1"
}

// DeriveLegacyPurposeSeed derives a purpose key seed for a node with no HD seed,
// from the parent identity signing key. Deterministic, one-way, purpose-separated,
// never persisted.
//
// It returns nil for an unregistered purpose or a short parent: a caller that
// cannot get a key must fail closed, never fall back to the parent.
func DeriveLegacyPurposeSeed(parent []byte, purpose KeyPurpose) []byte {
	domain := LegacyPurposeKDFDomain(purpose)
	if domain == "" || len(parent) < ed25519.SeedSize {
		return nil
	}
	reader := hkdf.New(sha512.New, parent[:ed25519.SeedSize], nil, []byte(domain))
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(reader, seed); err != nil {
		return nil
	}
	return seed
}

// DeriveChildForPurpose derives the Ed25519 key for a registered purpose from an
// HD seed. This is THE contract every server-side lane uses to get a purpose key:
// the licensing grant lane, the module-publication/distribution lane, and anything
// added later.
//
// It REFUSES an unregistered purpose. An index picked locally by one lane and
// reused by another is exactly how two duties end up sharing a key, and that is
// invisible until someone decodes a key_id by hand.
func (hw *HDWalletModule) DeriveChildForPurpose(ctx context.Context, seed []byte, account uint32, purpose KeyPurpose) (crypto.PrivKey, crypto.PubKey, error) {
	if !purpose.Registered() {
		return nil, nil, fmt.Errorf(
			"refusing to derive key purpose %d: it is not registered in the node identity contract; register it in internal/wasm/hdwallet_purpose.go and in §2.1 of graph/tasks/nst-node-admin-contract.md before using it",
			uint32(purpose),
		)
	}
	path := PurposeKeyPath(purpose, account)
	derived, err := hw.DeriveEd25519Key(ctx, seed, path)
	if err != nil {
		return nil, nil, fmt.Errorf("derive %s key at %s: %w", purpose, path, err)
	}
	priv, pub, err := crypto.GenerateEd25519Key(bytes.NewReader(derived.PrivateKey))
	if err != nil {
		return nil, nil, fmt.Errorf("build libp2p key for %s: %w", purpose, err)
	}
	return priv, pub, nil
}

// PurposeKeys describes every key this identity manages, for the key inventory and
// the bond rollup. Descriptions only — no private material.
//
// Every entry an HD identity produces is ProvenanceDerivedFromNodeRoot: that is the
// owner's default and this type has no way to express an external key, because an
// external key by definition did not come from this identity. The node assembles
// the full inventory, mixing these with any operator-configured keys.
func (id *DerivedIdentity) PurposeKeys() []PurposeKey {
	if id == nil {
		return nil
	}
	var keys []PurposeKey

	if id.SigningPubKey != nil {
		if raw, err := id.SigningPubKey.Raw(); err == nil {
			keys = append(keys, PurposeKey{
				Purpose:    PurposeIdentitySigning,
				Path:       id.SigningKeyPath,
				PublicKey:  raw,
				Algorithm:  "ed25519",
				Provenance: ProvenanceDerivedFromNodeRoot,
				// The node identity signing key IS the fleet update root.
				IsUpdateRoot: true,
			})
		}
	}
	if len(id.EncryptionPub) > 0 {
		keys = append(keys, PurposeKey{
			Purpose:    PurposeEncryption,
			Path:       id.EncryptionKeyPath,
			PublicKey:  append([]byte(nil), id.EncryptionPub...),
			Algorithm:  "x25519",
			Provenance: ProvenanceDerivedFromNodeRoot,
		})
	}
	if id.GrantSigningPubKey != nil {
		if raw, err := id.GrantSigningPubKey.Raw(); err == nil {
			keys = append(keys, PurposeKey{
				Purpose:    PurposeLicensingGrant,
				Path:       id.GrantSigningKeyPath,
				PublicKey:  raw,
				Algorithm:  "ed25519",
				Provenance: ProvenanceDerivedFromNodeRoot,
			})
		}
	}
	return keys
}
