package epm

// Derivation-path validation and rotation for the operator-editable SIGNING
// PATH / ENCRYPTION PATH fields (owner directive 2026-07-27, §18 of
// graph/tasks/nst-node-admin-contract.md).
//
// An operator can hand-edit these paths and can press GEN KEY to rotate them.
// Both actions can silently destroy verifiability if the resulting path is the
// wrong SHAPE, and the two shapes required are exactly inverted — which is why
// this is enforced here rather than trusted to a UI:
//
//   - An XPUB-DERIVABLE slot (the secp256k1 signing and encryption keys) MUST be
//     NON-HARDENED at every component below the account. BIP-32 public
//     derivation (CKDpub) cannot produce a hardened child — the hardened formula
//     hashes the parent PRIVATE key — so a hardened path here would mean no
//     third party could ever derive the key from the published xpub. That
//     silently breaks the owner's whole "xpub + paths, don't ship keys"
//     paradigm, and it breaks it for VERIFIERS, who would have no way to tell
//     the difference between "wrong path" and "bad signature".
//
//   - A SLIP-10 Ed25519 slot MUST be HARDENED at every component. SLIP-10
//     ed25519 has no public derivation at all; every child is hardened by
//     construction. A non-hardened component is not a weaker choice, it is an
//     underivable one.
//
// Neither rule is a policy preference. Both are forced by the key math, and
// getting either backwards produces a record that looks fine and verifies
// nowhere.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// KeyPathSlot selects which derivation rule a path must satisfy.
type KeyPathSlot int

const (
	// SlotXPubDerivable is a secp256k1 key a verifier derives from the
	// published xpub: the EPM signing key and the encryption key. Non-hardened.
	SlotXPubDerivable KeyPathSlot = iota
	// SlotSLIP10Ed25519 is an Ed25519 key derived from the seed via SLIP-10.
	// Fully hardened.
	SlotSLIP10Ed25519
)

func (s KeyPathSlot) String() string {
	switch s {
	case SlotXPubDerivable:
		return "xpub-derivable (secp256k1)"
	case SlotSLIP10Ed25519:
		return "SLIP-10 ed25519"
	default:
		return "unknown"
	}
}

// hardenedOffset is BIP-32's hardened marker (2^31).
const hardenedOffset uint32 = 0x80000000

// maxKeyPathDepth bounds how deep an operator-supplied path may go. BIP-44 uses
// five levels; anything far beyond that is far more likely to be a paste
// accident than an intent, and every extra level is another derivation a
// verifier must perform.
const maxKeyPathDepth = 8

// KeyPathComponent is one level of a parsed derivation path.
type KeyPathComponent struct {
	Index    uint32
	Hardened bool
}

// ParseKeyPath parses a BIP-32 path ("m/44'/0'/0'/0/0") into its components.
// Both "'" and "h"/"H" are accepted as the hardened marker on input; String()
// always re-emits "'".
//
// It is deliberately strict: a path is operator-supplied text that ends up
// signed into a published record, so "close enough" parsing would let a typo
// travel the network.
func ParseKeyPath(path string) ([]KeyPathComponent, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("derivation path is empty")
	}
	parts := strings.Split(trimmed, "/")
	if parts[0] != "m" && parts[0] != "M" {
		return nil, fmt.Errorf("derivation path must start with %q", "m/")
	}
	parts = parts[1:]
	if len(parts) == 0 {
		return nil, fmt.Errorf("derivation path %q has no components below the master key", trimmed)
	}
	if len(parts) > maxKeyPathDepth {
		return nil, fmt.Errorf("derivation path %q is %d levels deep; the maximum is %d", trimmed, len(parts), maxKeyPathDepth)
	}

	components := make([]KeyPathComponent, 0, len(parts))
	for _, raw := range parts {
		// NOT TrimSpace'd per component, deliberately. The whole path is
		// trimmed above, but whitespace INSIDE a path ("m/44' /0'/…") is a
		// mangled paste, not a formatting nicety — and silently normalizing a
		// mangled paste is exactly how a wrong index ends up signed into a
		// published record.
		component := raw
		if component == "" {
			return nil, fmt.Errorf("derivation path %q has an empty component", trimmed)
		}
		hardened := false
		if last := component[len(component)-1]; last == '\'' || last == 'h' || last == 'H' {
			hardened = true
			component = component[:len(component)-1]
		}
		if component == "" {
			return nil, fmt.Errorf("derivation path %q has a component with no index", trimmed)
		}
		// Reject anything that is not plain digits: strconv would accept a
		// leading "+", and a signed value here would be a silent index change.
		for _, r := range component {
			if r < '0' || r > '9' {
				return nil, fmt.Errorf("derivation path %q has a non-numeric component %q", trimmed, raw)
			}
		}
		value, err := strconv.ParseUint(component, 10, 64)
		if err != nil || value >= uint64(hardenedOffset) {
			return nil, fmt.Errorf("derivation path %q component %q is out of range (max %d)", trimmed, raw, hardenedOffset-1)
		}
		components = append(components, KeyPathComponent{Index: uint32(value), Hardened: hardened})
	}
	return components, nil
}

// FormatKeyPath renders components back to canonical text, always using "'".
func FormatKeyPath(components []KeyPathComponent) string {
	var b strings.Builder
	b.WriteString("m")
	for _, c := range components {
		b.WriteString("/")
		b.WriteString(strconv.FormatUint(uint64(c.Index), 10))
		if c.Hardened {
			b.WriteString("'")
		}
	}
	return b.String()
}

// ValidateKeyPath reports whether path is well-formed AND has the hardening
// shape its slot requires. See the package comment for why the two slots demand
// opposite things.
//
// The BIP-44 account prefix (m/44'/coin'/account') is hardened in both cases;
// for an xpub-derivable slot the non-hardened requirement applies BELOW the
// account, because that is the part a verifier walks from the published account
// xpub.
func ValidateKeyPath(path string, slot KeyPathSlot) error {
	components, err := ParseKeyPath(path)
	if err != nil {
		return err
	}

	switch slot {
	case SlotSLIP10Ed25519:
		for i, c := range components {
			if !c.Hardened {
				return fmt.Errorf(
					"derivation path %q is invalid for a %s key: component %d (%d) is not hardened, and SLIP-10 ed25519 has no public derivation — every component must be hardened",
					path, slot, i+1, c.Index)
			}
		}
	case SlotXPubDerivable:
		// The account prefix is hardened; everything the verifier walks from the
		// account xpub must not be.
		const accountDepth = 3
		if len(components) <= accountDepth {
			return fmt.Errorf(
				"derivation path %q is invalid for a %s key: it must descend below the BIP-44 account level (m/44'/coin'/account'/…) so a verifier has something to derive from the published xpub",
				path, slot)
		}
		for i := 0; i < accountDepth; i++ {
			if !components[i].Hardened {
				return fmt.Errorf(
					"derivation path %q is invalid for a %s key: the BIP-44 account prefix component %d (%d) must be hardened",
					path, slot, i+1, components[i].Index)
			}
		}
		for i := accountDepth; i < len(components); i++ {
			if components[i].Hardened {
				return fmt.Errorf(
					"derivation path %q is invalid for a %s key: component %d (%d') is hardened, so it cannot be derived from the published xpub — every component below the account must be non-hardened",
					path, slot, i+1, components[i].Index)
			}
		}
	default:
		return fmt.Errorf("unknown key path slot %d", slot)
	}
	return nil
}

// NextKeyPath returns the path for the NEXT key in the same slot — what the
// GEN KEY button derives. It increments the final component and preserves that
// component's hardening, so a rotated path always satisfies the same slot rule
// the original did.
//
// Rotation only ever moves sideways within a slot. It never changes the depth
// or the hardening shape, because either would change which slot the path
// belongs to and could make the key underivable.
func NextKeyPath(path string, slot KeyPathSlot) (string, error) {
	if err := ValidateKeyPath(path, slot); err != nil {
		return "", err
	}
	components, err := ParseKeyPath(path)
	if err != nil {
		return "", err
	}
	last := len(components) - 1
	if components[last].Index >= hardenedOffset-1 {
		return "", fmt.Errorf("derivation path %q cannot be rotated: the final index is already at the maximum (%d)", path, hardenedOffset-1)
	}
	components[last].Index++
	next := FormatKeyPath(components)
	// Belt and braces: a rotation that produced an invalid path would be a bug
	// in this function, and it would ship a broken record.
	if err := ValidateKeyPath(next, slot); err != nil {
		return "", fmt.Errorf("rotated path %q failed validation: %w", next, err)
	}
	return next, nil
}

// EffectiveKeyPaths resolves the derivation paths actually in effect for a
// node: the operator's edited paths when set, otherwise the node's defaults.
//
// Empty means UNSET, never "delete". PUT /api/node/epm is a whole-profile
// replace (§6), so a client that omits these fields must not thereby wipe the
// node's key layout.
func EffectiveKeyPaths(profile *Profile, account uint32) (signing string, encryption string) {
	signing = xpubSigningKeyPath(account)
	encryption = xpubEncryptionKeyPath(account)
	if profile == nil {
		return signing, encryption
	}
	if custom := strings.TrimSpace(profile.SigningKeyPath); custom != "" {
		signing = custom
	}
	if custom := strings.TrimSpace(profile.EncryptionKeyPath); custom != "" {
		encryption = custom
	}
	return signing, encryption
}

// ValidateProfileKeyPaths checks the operator-editable paths on a profile.
//
// Returned errors are surfaced VERBATIM to the operator (the API and the CLI
// both pass them straight through) because they explain the hardening rule,
// which is not guessable from a form field.
func ValidateProfileKeyPaths(profile *Profile) error {
	if profile == nil {
		return nil
	}
	if p := strings.TrimSpace(profile.SigningKeyPath); p != "" {
		if err := ValidateKeyPath(p, SlotXPubDerivable); err != nil {
			return keyPathValidationError{fmt.Errorf("signing_key_path: %w", err)}
		}
	}
	if p := strings.TrimSpace(profile.EncryptionKeyPath); p != "" {
		if err := ValidateKeyPath(p, SlotXPubDerivable); err != nil {
			return keyPathValidationError{fmt.Errorf("encryption_key_path: %w", err)}
		}
	}
	return nil
}

// keyPathValidationError marks an error as caused by operator-supplied path
// input rather than by a server fault, so the API can answer 400 with the text
// verbatim instead of 500 with a generic message.
type keyPathValidationError struct{ err error }

func (e keyPathValidationError) Error() string { return e.err.Error() }
func (e keyPathValidationError) Unwrap() error { return e.err }

// IsKeyPathValidationError reports whether err came from validating an
// operator-supplied derivation path.
func IsKeyPathValidationError(err error) bool {
	var target keyPathValidationError
	return errors.As(err, &target)
}
