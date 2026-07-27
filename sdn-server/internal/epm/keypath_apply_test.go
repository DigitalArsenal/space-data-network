package epm

// The APPLY half of §18 — the gap Iris found: the paths were readable and
// proposable but nothing persisted them, so PUT /api/node/epm returned 200 and
// silently dropped the edit.

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

func newPathTestService(t *testing.T) *Service {
	t.Helper()
	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity: %v", err)
	}
	svc := NewService(identity, nil, identity.PeerID, validTestXPub, t.TempDir())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return svc
}

// keyEntry returns the KEY_ADDRESS and PUBLIC_KEY of the first key of a type.
func keyEntry(t *testing.T, epmBytes []byte, want EPM.KeyType, addrType string) (path string, pub string) {
	t.Helper()
	rec := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	key := new(EPM.CryptoKey)
	for i := 0; i < rec.KEYSLength(); i++ {
		if !rec.KEYS(key, i) {
			continue
		}
		if key.KEY_TYPE() != want {
			continue
		}
		if addrType != "" && string(key.ADDRESS_TYPE()) != addrType {
			continue
		}
		return string(key.KEY_ADDRESS()), string(key.PUBLIC_KEY())
	}
	return "", ""
}

// TestEditedSigningPathIsAppliedAndRepublished is the round trip Iris's UI
// needs: save a custom path, and the published record carries it.
func TestEditedSigningPathIsAppliedAndRepublished(t *testing.T) {
	t.Parallel()

	svc := newPathTestService(t)
	before, beforePub := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")
	if before == "" {
		t.Fatal("no secp256k1 signing key in the initial record")
	}

	next, err := NextKeyPath(before, SlotXPubDerivable)
	if err != nil {
		t.Fatalf("NextKeyPath: %v", err)
	}
	if err := svc.UpdateProfile(&Profile{DN: "Path Node", SigningKeyPath: next}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	after, afterPub := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")
	if after != next {
		t.Fatalf("published KEY_ADDRESS = %q, want the edited path %q", after, next)
	}

	// The KEY must move WITH the path. A relabel without re-derivation would
	// publish a path that does not produce the published key, and every
	// verifier following xpub+path would conclude the signature was forged.
	if afterPub == beforePub {
		t.Fatal("the path changed but the published public key did not — the record would advertise a path that does not produce its own key")
	}
	if _, err := hex.DecodeString(afterPub); err != nil || afterPub == "" {
		t.Fatalf("published key is not hex: %q", afterPub)
	}
	// And it must be exactly what deriving at that path yields.
	want, err := deriveXPubPublicKeyAtPath(validTestXPub, next)
	if err != nil {
		t.Fatalf("deriveXPubPublicKeyAtPath: %v", err)
	}
	if afterPub != want {
		t.Fatalf("published key %q is not the key at %q (%q)", afterPub, next, want)
	}

	// The key-slot surface the UI reads must agree with the record.
	slots, err := svc.KeySlots()
	if err != nil {
		t.Fatalf("KeySlots: %v", err)
	}
	for _, s := range slots {
		if s.Slot == KeySlotSigning && s.Path != next {
			t.Fatalf("KeySlots reports %q, record says %q — the widget would disagree with the record", s.Path, next)
		}
	}
}

func TestEditedEncryptionPathIsApplied(t *testing.T) {
	t.Parallel()

	svc := newPathTestService(t)
	before, _ := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeEncryption, "")
	if before == "" {
		t.Fatal("no encryption key in the initial record")
	}
	next, err := NextKeyPath(before, SlotXPubDerivable)
	if err != nil {
		t.Fatalf("NextKeyPath: %v", err)
	}
	if err := svc.UpdateProfile(&Profile{DN: "Path Node", EncryptionKeyPath: next}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	after, _ := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeEncryption, "")
	if after != next {
		t.Fatalf("published encryption KEY_ADDRESS = %q, want %q", after, next)
	}
}

// TestInvalidPathIsRejectedAndAppliesNothing locks the fail-closed half: a bad
// path must not partially apply. Silently dropping it — the old behaviour — is
// what made the feature look like it worked.
func TestInvalidPathIsRejectedAndAppliesNothing(t *testing.T) {
	t.Parallel()

	svc := newPathTestService(t)
	originalPath, originalPub := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")
	if err := svc.UpdateProfile(&Profile{DN: "Original"}); err != nil {
		t.Fatalf("baseline UpdateProfile: %v", err)
	}

	for name, bad := range map[string]string{
		"hardened below account": "m/44'/0'/0'/0'/0'",
		"garbage":                "not-a-path",
		"too shallow":            "m/44'/0'",
		"non-hardened account":   "m/44/0'/0'/0/0",
	} {
		name, bad := name, bad
		t.Run(name, func(t *testing.T) {
			err := svc.UpdateProfile(&Profile{DN: "Should Not Apply", SigningKeyPath: bad})
			if err == nil {
				t.Fatalf("invalid path %q was accepted", bad)
			}
			if !strings.Contains(err.Error(), "signing_key_path") {
				t.Fatalf("error should name the field, got: %v", err)
			}
			// Nothing may have changed.
			gotPath, gotPub := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")
			if gotPath != originalPath || gotPub != originalPub {
				t.Fatalf("a rejected edit still mutated the record: path %q pub %q", gotPath, gotPub)
			}
			if dn := svc.profile.DN; dn == "Should Not Apply" {
				t.Fatal("a rejected edit still replaced the profile")
			}
		})
	}
}

// TestEmptyPathMeansDefaultNotDelete locks the §6 whole-replace interaction: a
// client that omits the fields must not thereby wipe the node's key layout.
func TestEmptyPathMeansDefaultNotDelete(t *testing.T) {
	t.Parallel()

	svc := newPathTestService(t)
	defaultPath, _ := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")

	if err := svc.UpdateProfile(&Profile{DN: "No Paths Supplied"}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	got, gotPub := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")
	if got != defaultPath {
		t.Fatalf("omitting the field changed the path to %q, want the default %q", got, defaultPath)
	}
	if gotPub == "" {
		t.Fatal("omitting the field removed the published key")
	}
}

// TestRotationSurvivesRoundTripAndKeepsDerivability locks that a rotated path
// stays xpub-derivable — the property the whole paradigm rests on.
func TestRotationSurvivesRoundTripAndKeepsDerivability(t *testing.T) {
	t.Parallel()

	svc := newPathTestService(t)
	path, _ := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")

	for i := 0; i < 3; i++ {
		next, err := NextKeyPath(path, SlotXPubDerivable)
		if err != nil {
			t.Fatalf("NextKeyPath(%q): %v", path, err)
		}
		if err := svc.UpdateProfile(&Profile{DN: "Rotating", SigningKeyPath: next}); err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
		got, gotPub := keyEntry(t, svc.GetNodeEPM(), EPM.KeyTypeSigning, "secp256k1")
		if got != next {
			t.Fatalf("rotation %d published %q, want %q", i, got, next)
		}
		if err := ValidateKeyPath(got, SlotXPubDerivable); err != nil {
			t.Fatalf("rotation %d produced a non-derivable path: %v", i, err)
		}
		want, err := deriveXPubPublicKeyAtPath(validTestXPub, got)
		if err != nil || want != gotPub {
			t.Fatalf("rotation %d: published key is not derivable from xpub+path", i)
		}
		path = next
	}
}

// TestPathValidationErrorsAreOperatorInput locks the API status split: a bad
// path is a 400 with the text verbatim, not a 500. Iris's UI surfaces the
// message directly, and "internal server error" would tell the operator nothing
// about the hardening rule they just broke.
func TestPathValidationErrorsAreOperatorInput(t *testing.T) {
	t.Parallel()

	err := ValidateProfileKeyPaths(&Profile{SigningKeyPath: "m/44'/0'/0'/0'/0'"})
	if err == nil {
		t.Fatal("hardened path accepted")
	}
	if !IsKeyPathValidationError(err) {
		t.Fatalf("path error not classified as operator input: %v", err)
	}
	if !strings.Contains(err.Error(), "hardened") {
		t.Fatalf("message must explain the rule, got: %v", err)
	}

	if IsKeyPathValidationError(nil) {
		t.Fatal("nil classified as a validation error")
	}
	if err := ValidateProfileKeyPaths(&Profile{}); err != nil {
		t.Fatalf("empty paths must validate: %v", err)
	}
	if err := ValidateProfileKeyPaths(nil); err != nil {
		t.Fatalf("nil profile must validate: %v", err)
	}
}
