// Package sigdomain holds the DOMAIN SEPARATION contract for every detached
// Ed25519 signature the node produces over content it did not itself author.
//
// ---------------------------------------------------------------------------
// DELIBERATE DUPLICATE of sdn-server/internal/sigdomain — KEEP IN LOCKSTEP.
//
// The kubo fork is a SEPARATE GO MODULE (kubo/go.mod declares
// github.com/ipfs/kubo and does not require the sdn-server module), so it
// cannot import the original; kubo/sdn/modulert is already a maintained copy of
// sdn-server/internal/modulert for the same reason. Both binaries run on
// host-01 (see sdn-server/internal/node/module_signature_policy.go:53-55), so a
// domain the two copies disagree about is a module that verifies in one binary
// and is reported as invalid_signature by the other — which would poison the
// "module_signature_observe" log the enforcement flip
// (saw-module-signing-enforcement) is gated on draining to empty.
//
// THE REGISTRY AND THE STATEMENT SHAPE MUST BE BYTE-IDENTICAL ACROSS THE TWO
// COPIES. If you add a domain here, add it there in the same commit.
// ---------------------------------------------------------------------------
//
// SEAL COUNCIL 2026-07-30 (Hermes in-node + Hephaestus ship-time, JOINT):
// under the owner's Adversarial-Security bond posture the PUBLISHER KEY IS THE
// NODE KEY (internal/node/module_signature_policy.go:5-31). One bonded key
// therefore signs several unrelated kinds of statement: dataset publications,
// module artifacts, and — next — update manifests (graph/tasks/
// sdn-signed-updater.md). The moment the node exposes a signing ENDPOINT over
// that key, "which statement kind is this signature for?" stops being a
// documentation question and becomes a security boundary.
//
// THE CONCRETE ATTACK THIS PACKAGE CLOSES. internal/storage/manifest.go:815
// signs a dataset-publication manifest as
//
//	ed25519.Sign(nodeKey, sha256(unsignedManifestBytes))   // 32 RAW bytes
//
// with no domain prefix. If the module-signing endpoint also signed a raw
// SHA-256 digest, then any caller allowed to reach it could POST the bytes of
// an unsigned DPM manifest, receive back sig(sha256(thoseBytes)), and staple it
// onto the manifest as a valid dataset publication signed by the bonded node
// key. The endpoint would be a cross-protocol forgery oracle. Prefixing a
// distinct, registered domain to the signed preimage makes the two statement
// kinds live in disjoint message spaces, so a signature minted for one can
// never be presented as the other.
//
// THE STATEMENT SHAPE mirrors the pattern already in the tree —
// datasetPublicationPNMSignaturePayload (internal/storage/manifest.go:161-168)
// builds "SDN-DPM-PNM\x00" || fileID || 0x00 || cid. Statement() is the same
// idea generalized: an ASCII domain label, a NUL that cannot occur inside the
// label, then the fixed-width content hash.
//
// REGISTRATION IS MANDATORY. Statement refuses a domain that is not in the
// registry below. A signer that takes a domain from its caller (an HTTP field,
// a config value) must therefore still land inside a domain some human added
// here, which is what keeps "add a new signed statement kind" a reviewed
// change rather than a request parameter.
package sigdomain

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
)

// ContentHashSize is the width of the content hash every registered domain
// commits to: SHA-256, raw (not hex). Fixed width is part of the contract —
// with a fixed-width tail and a NUL-terminated label there is no length
// ambiguity anywhere in the preimage, so two different (domain, hash) pairs
// can never produce the same statement bytes.
const ContentHashSize = sha256.Size

const (
	// DomainModulePublicationV1 covers a MODULE ARTIFACT: the signature the
	// node's content-bound signing endpoint issues over the SHA-256 of a
	// module's PORTABLE payload — the trailer-stripped bytes the runtime
	// instantiates and the capability policy identifies by ContentHashHex
	// (internal/modulert/publication_signature.go). This is the value that
	// travels in the MBL publication trailer's signature entry as
	// "statementDomain", and internal/modulert requires exactly this domain
	// when verifying a module: an update-manifest signature can therefore
	// never be replayed into a module trailer, and vice versa.
	DomainModulePublicationV1 = "SDN-MODULE-PUBLICATION-V1"

	// DomainUpdateManifestV1 is RESERVED for the SDN-owned signed updater
	// (graph/tasks/sdn-signed-updater.md — the feed runs on
	// sdn.spaceaware.io, signed by this same node key under the same one
	// publisher-key policy, council Q7). It is registered here NOW, ahead of
	// its producer, for exactly one reason: registering it later would be a
	// change to this file made under feed-delivery pressure, and the whole
	// value of a domain registry is that the namespace is decided in advance
	// and in one place. Nothing signs it yet.
	DomainUpdateManifestV1 = "SDN-UPDATE-MANIFEST-V1"
)

// registry is the closed set of statement domains this node will sign or
// verify. Keep the map value a short human description: this table is the
// answer to "what can the bonded node key be made to say?".
var registry = map[string]string{
	DomainModulePublicationV1: "module artifact, portable-payload SHA-256",
	DomainUpdateManifestV1:    "update manifest, canonical manifest SHA-256 (reserved; no producer yet)",
}

// ErrUnregisteredDomain is returned by Statement for any domain not in the
// registry. It is a distinct error so callers can classify it as a
// programming/config fault rather than a bad-input fault.
var ErrUnregisteredDomain = errors.New("signature statement domain is not registered")

// Registered reports whether domain is a known statement domain. Verifiers
// call this before trusting a domain label that arrived on the wire.
func Registered(domain string) bool {
	_, ok := registry[domain]
	return ok
}

// Domains returns the registered domains, sorted — for diagnostics and tests
// that assert the namespace has not silently grown.
func Domains() []string {
	out := make([]string, 0, len(registry))
	for d := range registry {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Describe returns the human description of a registered domain, or "".
func Describe(domain string) string { return registry[domain] }

// Statement builds the exact byte string an Ed25519 signature over
// contentHash must cover for the given domain:
//
//	domain || 0x00 || contentHash        (contentHash is 32 raw bytes)
//
// It is the ONLY way this codebase constructs a signable preimage for a
// registered domain — signer and verifier call the same function, so they
// cannot drift.
//
// Errors (never a partial/best-effort statement): an unregistered domain, or a
// contentHash that is not exactly ContentHashSize bytes. Refusing a wrong-width
// hash matters: a caller that passed, say, hex text instead of raw bytes would
// otherwise get a perfectly valid signature over the wrong preimage.
func Statement(domain string, contentHash []byte) ([]byte, error) {
	if !Registered(domain) {
		return nil, fmt.Errorf("%w: %q (registered: %v)", ErrUnregisteredDomain, domain, Domains())
	}
	if len(contentHash) != ContentHashSize {
		return nil, fmt.Errorf("signature statement content hash must be %d raw bytes, got %d", ContentHashSize, len(contentHash))
	}
	out := make([]byte, 0, len(domain)+1+ContentHashSize)
	out = append(out, domain...)
	out = append(out, 0)
	out = append(out, contentHash...)
	return out, nil
}
