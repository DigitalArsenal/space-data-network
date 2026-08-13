package node

// SERVER-MANAGED KEY INVENTORY.
//
// OWNER RULING 2026-08-07: keys are DERIVED FROM THE HD NODE ROOT by default,
// external keys are the opt-in exception, "and we need to be able to show the
// rollup of all value across all keys that are being managed by a server."
//
// This file answers the first half — WHICH keys does this server manage, where did
// each come from, and what can be bonded against it. It deliberately does NOT
// answer the second half: reading balances is chain RPC, which is application
// logic over the generic http hook and belongs in WASM, not in the Go host
// (owner rule, wasm-not-go-host-boundary). The host publishes the inventory and
// the bondable addresses; a module sums the value.
//
// Why an inventory at all, rather than each lane knowing its own key: the
// adversarial-security model prices trust by bonded value on the addresses a key
// controls. That only works if every key the server can sign with is enumerable —
// an unlisted key is an unpriced authority. It is also what lets a UI say "this is
// signing with your node root key" instead of showing an opaque hex string.

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// ManagedKey is one server-managed key as reported to operators and to the
// key-management UI. JSON keys are lowercase snake_case: these are
// node-synthesized API fields, not SDS record fields.
//
// PUBLIC MATERIAL ONLY. Every field here is safe to serve to an authenticated
// admin and, for the public halves, safe to publish at all — the purpose paths are
// fully hardened, so disclosure enables no derivation.
type ManagedKey struct {
	Purpose     string `json:"purpose"`
	Description string `json:"description"`
	Algorithm   string `json:"algorithm"`
	PublicKey   string `json:"public_key"`

	// DerivationPath is set when the key was derived at an HD path. Empty for the
	// legacy-KDF and external-configured provenances.
	DerivationPath string `json:"derivation_path,omitempty"`

	// KDFDomain is set only for the legacy-KDF provenance.
	KDFDomain string `json:"kdf_domain,omitempty"`

	// Provenance is "derived-from-node-root" (the default),
	// "derived-from-node-root-legacy-kdf", or "external-configured".
	Provenance string `json:"provenance"`

	// Reproducible is false when an operator holds backup responsibility for this
	// key — i.e. it cannot be re-derived from the node mnemonic. A UI should say
	// so plainly: a non-reproducible key that is lost is lost.
	Reproducible bool `json:"reproducible"`

	// IsUpdateRoot marks the ONE key carrying fleet code authority. A UI must warn
	// before any action that rotates or replaces it.
	IsUpdateRoot bool `json:"is_update_root"`

	// InUse reports whether a lane is actually signing with this key right now.
	// A key can be derivable and still not in use — for example when the domain
	// separation guard has refused to provision it.
	InUse bool `json:"in_use"`

	// Note carries any operator-relevant qualifier, such as the reason a key is
	// derivable but refused. Empty when there is nothing to say.
	Note string `json:"note,omitempty"`

	// Slot is the module-runtime key-slot id this purpose fills
	// ("provider-signing" for the grant key, "provider-wrapping" for the
	// encryption scalar), empty when the key is not exposed as a runtime slot.
	// It is the join key the key-management UI matches purposes on.
	Slot string `json:"slot,omitempty"`

	// Configurable reports whether an operator may point this purpose at a
	// dedicated external key. Exactly the PurposeKeyConfigurable ruling; false
	// for the update root FOREVER. ConfigurableReason states why when false.
	Configurable       bool   `json:"configurable"`
	ConfigurableReason string `json:"configurable_reason,omitempty"`

	// BondAddresses are the chain addresses whose balances back THIS key under
	// the adversarial-security model ("bitcoin"/"ethereum"/"solana" -> addr).
	// The root identity carries the node's EPM addresses; an external
	// configured key carries the addresses the operator declared for it; a
	// derived hardened child carries none and is correctly UNBONDED.
	BondAddresses map[string]string `json:"bond_addresses,omitempty"`
}

// purposeRuntimeSlot maps a purpose to the module-runtime key slot it fills.
func purposeRuntimeSlot(p wasm.KeyPurpose) string {
	switch p {
	case wasm.PurposeLicensingGrant:
		return providerSigningSlotID
	case wasm.PurposeEncryption:
		return providerWrappingSlotID
	default:
		return ""
	}
}

// annotateManagedKey fills the fields shared by every inventory row.
func (n *Node) annotateManagedKey(entry *ManagedKey, purpose wasm.KeyPurpose) {
	entry.Slot = purposeRuntimeSlot(purpose)
	entry.Configurable, entry.ConfigurableReason = PurposeKeyConfigurable(purpose)
	if purpose == wasm.PurposeIdentitySigning {
		entry.BondAddresses = n.BondableAddresses()
	}
}

// ServerManagedKeys returns every key this node can sign or agree with, with its
// provenance. The enumeration is driven by wasm.RegisteredPurposes so a newly
// registered purpose appears here — and therefore in the UI and the bond rollup —
// without a second edit.
//
// Ordering is stable (registry order) so a UI diffing two responses sees real
// changes rather than map iteration noise.
func (n *Node) ServerManagedKeys() []ManagedKey {
	if n == nil {
		return nil
	}

	grantSeed, _, _ := n.moduleRuntimeKeySlots()
	grantRefusal := ""
	if len(grantSeed) == ed25519.SeedSize {
		grantRefusal = grantSigningKeyDomainConflict(grantSeed, n.updateRootSigningSeed())
	}

	// The operator-configured external grant key, when there is one.
	// moduleRuntimeKeySlots already resolved it into grantSeed (or into a
	// refusal); this read is what lets the inventory SAY so rather than
	// mislabeling an external key as derived.
	extGrant, extGrantRefusal := n.configuredGrantSeed()
	extGrantCfg, _ := n.ConfiguredPurposeKey(wasm.PurposeLicensingGrant)

	var out []ManagedKey

	if n.identity != nil {
		for _, key := range n.identity.PurposeKeys() {
			entry := ManagedKey{
				Purpose:        key.Purpose.String(),
				Description:    key.Purpose.Description(),
				Algorithm:      key.Algorithm,
				PublicKey:      hex.EncodeToString(key.PublicKey),
				DerivationPath: key.Path,
				KDFDomain:      key.KDFDomain,
				Provenance:     string(key.Provenance),
				Reproducible:   key.Provenance.Reproducible(),
				IsUpdateRoot:   key.IsUpdateRoot,
				InUse:          true,
			}
			if key.Purpose == wasm.PurposeLicensingGrant {
				switch {
				case extGrantRefusal != "":
					// Configured, unreadable: the slot is NOT provisioned
					// (fail closed) and the row must carry the reason, not a
					// derived key the node is deliberately not signing with.
					entry.Provenance = string(wasm.ProvenanceExternalConfigured)
					entry.Reproducible = false
					entry.PublicKey = ""
					entry.DerivationPath = ""
					entry.InUse = false
					entry.Note = "REFUSED: an operator-configured external key exists but cannot be used — " + extGrantRefusal
				case len(extGrant) == ed25519.SeedSize:
					// Configured and readable: the external key IS the grant
					// key. Report ITS public half and its provenance; the
					// operator holds backup responsibility for it.
					entry.Provenance = string(wasm.ProvenanceExternalConfigured)
					entry.Reproducible = false
					entry.DerivationPath = ""
					if pub, perr := ed25519PublicFromSeed(extGrant); perr == nil {
						entry.PublicKey = hex.EncodeToString(pub)
					}
					if extGrantCfg != nil {
						entry.BondAddresses = extGrantCfg.BondAddresses
					}
					entry.Note = "operator-configured external key (set " + configuredAtNote(extGrantCfg) + "); the licensing runtime loads it at boot — restart the daemon if it was configured after the last boot"
					if grantRefusal != "" {
						entry.InUse = false
						entry.Note = "REFUSED at boot: " + grantRefusal
					}
				case grantRefusal != "":
					// A grant key can be perfectly derivable and still not be
					// signing anything, because the domain separation guard
					// refused to provision it. Reporting it as in-use would be
					// a lie an operator would act on.
					entry.InUse = false
					entry.Note = "REFUSED at boot: " + grantRefusal
				case len(grantSeed) != ed25519.SeedSize:
					entry.InUse = false
					entry.Note = "no grant signing seed is provisioned; this node issues no grants"
				}
			}
			n.annotateManagedKey(&entry, key.Purpose)
			out = append(out, entry)
		}
		return out
	}

	// Legacy on-disk identity: no HD root, so the purpose keys are KDF children
	// rather than path children. Same contract, different derivation, and the
	// provenance says which — that distinction is exactly what the owner asked to
	// be queryable.
	identity, err := n.loadLegacyServerIdentity()
	if err != nil || identity == nil {
		return nil
	}
	if identity.SigningKey != nil && len(identity.SigningKey.PublicKey) > 0 {
		entry := ManagedKey{
			Purpose:      wasm.PurposeIdentitySigning.String(),
			Description:  wasm.PurposeIdentitySigning.Description(),
			Algorithm:    "ed25519",
			PublicKey:    hex.EncodeToString(identity.SigningKey.PublicKey),
			Provenance:   string(wasm.ProvenanceExternalConfigured),
			Reproducible: false,
			IsUpdateRoot: true,
			InUse:        true,
			Note:         "legacy on-disk identity: generated from crypto/rand and stored, not derived from a mnemonic. It cannot be recovered from a seed phrase.",
		}
		n.annotateManagedKey(&entry, wasm.PurposeIdentitySigning)
		out = append(out, entry)
	}
	switch {
	case extGrantRefusal != "":
		entry := ManagedKey{
			Purpose:      wasm.PurposeLicensingGrant.String(),
			Description:  wasm.PurposeLicensingGrant.Description(),
			Algorithm:    "ed25519",
			Provenance:   string(wasm.ProvenanceExternalConfigured),
			Reproducible: false,
			InUse:        false,
			Note:         "REFUSED: an operator-configured external key exists but cannot be used — " + extGrantRefusal,
		}
		n.annotateManagedKey(&entry, wasm.PurposeLicensingGrant)
		out = append(out, entry)
	case len(grantSeed) == ed25519.SeedSize:
		entry := ManagedKey{
			Purpose:      wasm.PurposeLicensingGrant.String(),
			Description:  wasm.PurposeLicensingGrant.Description(),
			Algorithm:    "ed25519",
			Provenance:   string(wasm.ProvenanceDerivedFromNodeRootLegacyKDF),
			KDFDomain:    legacyGrantSigningDomain,
			Reproducible: true,
			InUse:        grantRefusal == "",
		}
		if len(extGrant) == ed25519.SeedSize {
			entry.Provenance = string(wasm.ProvenanceExternalConfigured)
			entry.KDFDomain = ""
			entry.Reproducible = false
			if extGrantCfg != nil {
				entry.BondAddresses = extGrantCfg.BondAddresses
			}
			entry.Note = "operator-configured external key (set " + configuredAtNote(extGrantCfg) + "); the licensing runtime loads it at boot — restart the daemon if it was configured after the last boot"
		}
		if pub, perr := ed25519PublicFromSeed(grantSeed); perr == nil {
			entry.PublicKey = hex.EncodeToString(pub)
		}
		if grantRefusal != "" {
			entry.InUse = false
			entry.Note = "REFUSED at boot: " + grantRefusal
		}
		n.annotateManagedKey(&entry, wasm.PurposeLicensingGrant)
		out = append(out, entry)
	}
	return out
}

// configuredAtNote renders when/by whom an external key was configured, for the
// inventory note. Public metadata only.
func configuredAtNote(cfg *PurposeKeyConfig) string {
	if cfg == nil {
		return "at an unrecorded time"
	}
	note := cfg.ConfiguredAt
	if note == "" {
		note = "at an unrecorded time"
	}
	if cfg.ConfiguredBy != "" {
		note += " by session " + cfg.ConfiguredBy
	}
	return note
}

// PurposeBondAddresses is the per-key address map the bond attestation runs
// over: purpose label -> chain -> address, one entry per managed key that HAS
// bondable addresses. Derived hardened children have none and are therefore
// absent — correctly UNBONDED, never given an invented share.
//
// Built FROM the inventory rather than beside it, so the attestation and the
// key registry can never disagree about which addresses belong to which key.
func (n *Node) PurposeBondAddresses() map[string]map[string]string {
	keys := n.ServerManagedKeys()
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(keys))
	for _, key := range keys {
		if len(key.BondAddresses) == 0 {
			continue
		}
		addrs := make(map[string]string, len(key.BondAddresses))
		for chain, addr := range key.BondAddresses {
			if strings.TrimSpace(addr) == "" {
				continue
			}
			addrs[chain] = addr
		}
		if len(addrs) == 0 {
			continue
		}
		out[key.Purpose] = addrs
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BondableAddresses returns the chain addresses derived from the node identity,
// which is where value is bonded under the adversarial-security model.
//
// It reports ADDRESSES ONLY. Balances, aging and the value rollup are chain RPC —
// application logic over the generic http hook, and therefore WASM's job, not the
// host's. The host's contribution is naming the addresses authoritatively so a
// module is never guessing which addresses belong to this server.
//
// A hardened Ed25519 purpose child (the grant key, and any future purpose key)
// derives no standard chain address and is therefore UNBONDED. That is correct and
// deliberate: a grant is worth one module download and must not borrow the fleet's
// economic weight. See deployment/signing.json gaps[] "grant-key-is-unbonded".
func (n *Node) BondableAddresses() map[string]string {
	if n == nil || n.identity == nil || n.identity.Addresses == nil {
		return nil
	}
	out := make(map[string]string, 3)
	addrs := n.identity.Addresses
	if addrs.Bitcoin != nil && strings.TrimSpace(addrs.Bitcoin.Address) != "" {
		out["bitcoin"] = addrs.Bitcoin.Address
	}
	if addrs.Ethereum != nil && strings.TrimSpace(addrs.Ethereum.Address) != "" {
		out["ethereum"] = addrs.Ethereum.Address
	}
	if addrs.Solana != nil && strings.TrimSpace(addrs.Solana.Address) != "" {
		out["solana"] = addrs.Solana.Address
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
