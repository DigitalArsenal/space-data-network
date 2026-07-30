package node

// Module publication-signature trust policy for the sdn-server binary.
//
// SEAL COUNCIL / OWNER RULING 2026-07-30
// (graph/tasks/saw-module-signing-enforcement.md):
//
//	"The publisher key needs to be the Node key. The Node key will have
//	crypto value against it, so the security mechanism is
//	https://github.com/DigitalArsenal/Adversarial-Security, so trust in the
//	signature is based on how much security bond the node keys have."
//
// Trust here is therefore NOT custody isolation — it is economic bonding.
// The node key is an HD identity derived from one 24-word mnemonic
// (identity_bundle.go: IdentityFromMnemonic yields the libp2p identity key,
// the Ed25519 signing key, the encryption key, the chain addresses and the
// xpub from the SAME root). The publisher key named by the ruling is the
// node's Ed25519 SIGNING key — the same key that already signs dataset
// publications (domain "sdn/dataset-publication/v1") and is already
// advertised in the node's EPM directory entry, so a verifier can discover
// it — and the BIP-32/44 chain addresses derived from that same root
// (BTC/ETH/SOL/ATOM, already advertised by the vCard identity contract) are
// where a security bond is posted. A rational attacker drains a compromised
// key immediately, so an aged undrained bond is the trust signal.
//
// Consequence for this file: the node SELF-TRUSTS its own signing key as a
// publisher, and operator-supplied keys (other nodes' publisher keys) are
// ADDITIVE via SDN_TRUSTED_PUBLISHER_KEYS. That collapses the two divergent
// trust roots the council documented — sdnapi.go:378-385 self-trusted the
// node identity while sdnruntime.go:198-206 trusted only an env list — into
// ONE root, resolving it in sdnapi's favor as the ruling directs.
//
// ROLLOUT STATE: this lands in REPORT-ONLY. The policy is attached and
// fully evaluated, every would-be rejection is logged with the greppable
// token "module_signature_observe", and NOTHING is refused. Enforcement is
// a separate, single, reversible step gated on a final owner go/no-go once
// the observe log has been drained to empty by re-signing. See
// ModuleSignaturePolicy.ReportOnly.

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

const (
	// trustedPublisherKeysEnv is deliberately the SAME env var name the kubo
	// sdnruntime plugin reads (kubo/plugin/plugins/sdnruntime/sdnruntime.go:54).
	// Both binaries run on host-01; the council found they disagreed about
	// the trust root partly because only one of them had a knob at all. One
	// name, one meaning, both binaries.
	trustedPublisherKeysEnv = "SDN_TRUSTED_PUBLISHER_KEYS"

	// moduleSignatureEnforceEnv is THE ENFORCE FLIP. Unset/false (the
	// default) = report-only: verify everything, log would-be rejections,
	// admit everything. Set truthy = fail closed.
	//
	// The flip is exactly this one variable plus a restart so that it is one
	// guarded step and its rollback is unsetting it. It defaults to
	// report-only on purpose: an operator who has not made the owner-gated
	// decision gets observability, never a surprise outage.
	moduleSignatureEnforceEnv = "SDN_MODULE_SIGNATURE_ENFORCE"
)

// buildModuleSignaturePolicy assembles the node's publication-signature trust
// policy: the node's own Ed25519 signing key (the publisher key named by the
// owner ruling) plus any additional operator-trusted publisher keys, in
// report-only mode unless the operator has explicitly opted into enforcement.
//
// It never returns nil. A nil policy would mean "no verification at all",
// which is precisely the inert state the council found on this binary and the
// state this function exists to end — with a policy attached the node can
// always answer "what would enforcement break?", which is the whole point of
// the observe stage.
func (n *Node) buildModuleSignaturePolicy() *modulert.ModuleSignaturePolicy {
	enforce := envTruthy(os.Getenv(moduleSignatureEnforceEnv))
	policy := &modulert.ModuleSignaturePolicy{
		AllowUnsignedByContentHash: make(map[string]bool),
		ReportOnly:                 !enforce,
	}

	// Trust root 1 (canonical): the node's own signing key.
	selfHex := ""
	if pub := n.signingPublicKey(); len(pub) == ed25519.PublicKeySize {
		policy.TrustedSigners = append(policy.TrustedSigners, pub)
		selfHex = hex.EncodeToString(pub)
	}

	// Trust root 2 (additive): other nodes' publisher keys, from the operator.
	operatorKeys, err := parseTrustedPublisherKeys(os.Getenv(trustedPublisherKeysEnv))
	if err != nil {
		// A malformed operator knob must not silently widen or narrow trust.
		// In report-only this is merely noisy; under enforcement the node
		// still refuses everything not signed by its own key, which is the
		// safe direction.
		log.Warnf("Module signature policy: %s is malformed (%v); ignoring operator-supplied publisher keys", trustedPublisherKeysEnv, err)
	} else {
		for _, k := range operatorKeys {
			if hex.EncodeToString(k) == selfHex {
				continue // already trusted as self
			}
			policy.TrustedSigners = append(policy.TrustedSigners, k)
		}
	}

	mode := "REPORT-ONLY (verifying and logging, refusing nothing)"
	if enforce {
		mode = "ENFORCING (fail closed)"
	}
	switch {
	case selfHex != "":
		log.Infof("Module signature policy %s: trusting node publisher key %s… + %d operator key(s)", mode, selfHex[:12], len(policy.TrustedSigners)-1)
	case len(policy.TrustedSigners) > 0:
		log.Warnf("Module signature policy %s: node signing key unavailable; trusting %d operator-supplied key(s) only", mode, len(policy.TrustedSigners))
	default:
		log.Warnf("Module signature policy %s: NO trusted publisher keys (node signing key unavailable and %s empty) — every artifact will be reported as untrusted", mode, trustedPublisherKeysEnv)
	}
	return policy
}

// signingPublicKey returns the raw Ed25519 public half of the node's signing
// key — the publisher key of record under the 2026-07-30 ruling — or nil.
//
// This is the public half of the same key SigningKey() exposes privately and
// the same key logservice and dataset publication sign with; deriving the
// trust root from it (rather than from a separately configured key) is what
// makes "the publisher key is the node key" true by construction instead of
// by operator convention.
func (n *Node) signingPublicKey() ed25519.PublicKey {
	if n == nil || n.identity == nil || n.identity.SigningPrivKey == nil {
		return nil
	}
	raw, err := n.identity.SigningPrivKey.GetPublic().Raw()
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(append([]byte(nil), raw...))
}

// parseTrustedPublisherKeys decodes a list of hex Ed25519 public keys.
//
// The accepted syntax is byte-for-byte the kubo plugin's
// (moduleSignaturePolicyFromText, sdnruntime.go:334-354): comma / semicolon /
// whitespace separated, case-insensitive, duplicates collapsed. Kept
// identical on purpose — an operator must be able to put the SAME
// SDN_TRUSTED_PUBLISHER_KEYS value in front of both binaries and get the same
// trust set, which is the divergence the council flagged.
func parseTrustedPublisherKeys(raw string) ([]ed25519.PublicKey, error) {
	var out []ed25519.PublicKey
	seen := make(map[string]bool)
	for _, encoded := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		encoded = strings.ToLower(strings.TrimSpace(encoded))
		if encoded == "" || seen[encoded] {
			continue
		}
		decoded, err := hex.DecodeString(encoded)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("trusted publisher key %q must be exactly %d hexadecimal Ed25519 public-key bytes", encoded, ed25519.PublicKeySize)
		}
		seen[encoded] = true
		out = append(out, ed25519.PublicKey(append([]byte(nil), decoded...)))
	}
	return out, nil
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enforce":
		return true
	}
	return false
}
