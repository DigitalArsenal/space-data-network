package modulert

import (
	"encoding/json"
	"strings"
	"testing"
)

// The RecordType union ordinal is NOT a discriminator. These tests are the
// executable form of THEMIS's ruling in sds-recordtype-ordinal-freeze and
// JANUS's ABI ruling in sdn-protected-plugin-hmac-decrypt-failure: module
// signature verification must select the MBL record by `Record.standard`.
//
// Before this change the verifier compared against a hand-copied
// `recRecordTypeMBL byte = 80`. Both directions of that were unsafe, and both
// are covered below.

func mustSignaturePayloadJSON(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"alg":    "ed25519",
		"keyId":  "ordinal-independence-probe",
		"sig":    strings.Repeat("00", 64),
		"digest": strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatalf("marshal signature payload: %v", err)
	}
	return payload
}

// TestFindModuleSignatureEntryIgnoresUnionOrdinal is the BACKWARD half, and the
// dangerous one: a signed artifact whose trailer carries a legacy ordinal must
// still be found. With the old ordinal comparison every pre-2026-07-08 artifact
// (MBL=67) fell through the loop and the module was reported UNSIGNED — a
// signature check that silently answers "no signature" for a signed module.
func TestFindModuleSignatureEntryIgnoresUnionOrdinal(t *testing.T) {
	payloadJSON := mustSignaturePayloadJSON(t)

	generations := []struct {
		name      string
		valueType byte
	}{
		{"current ordinal (SDS 1.183.0 freeze: MBL=80)", recRecordTypeMBLCurrent},
		{"pre-2026-07-08 ordinal (MBL=67) — what is on disk today", recRecordTypeMBLLegacy},
		{"union NONE / unset value_type", 0},
		{"an absurd future renumber", 251},
	}

	var want []byte
	for _, generation := range generations {
		t.Run(generation.name, func(t *testing.T) {
			recBytes := buildRECTrailerWithMBLSignatureAs(t, payloadJSON, generation.valueType, "MBL")
			got, err := findModuleSignatureEntry(recBytes)
			if err != nil {
				t.Fatalf("ordinal %d must not matter, but lookup failed: %v", generation.valueType, err)
			}
			if len(got) == 0 {
				t.Fatalf("ordinal %d: signature entry not found — a signed module would be reported UNSIGNED", generation.valueType)
			}
			if want == nil {
				want = got
			} else if string(got) != string(want) {
				t.Fatalf("ordinal %d produced a different signature payload than the current ordinal", generation.valueType)
			}
		})
	}
}

// TestFindModuleSignatureEntryRejectsForeignStandard is the FORWARD half: a
// record that happens to carry MBL's CURRENT ordinal but declares some other
// standard must NOT be treated as the module bundle. Under the old comparison
// this matched, and the verifier would have read a foreign record's bytes as a
// module bundle — the "matches the WRONG record" failure mode.
func TestFindModuleSignatureEntryRejectsForeignStandard(t *testing.T) {
	payloadJSON := mustSignaturePayloadJSON(t)

	for _, standard := range []string{"PNM", "ENC", "OMM", ""} {
		name := standard
		if name == "" {
			name = "(empty standard)"
		}
		t.Run(name, func(t *testing.T) {
			recBytes := buildRECTrailerWithMBLSignatureAs(t, payloadJSON, recRecordTypeMBLCurrent, standard)
			got, err := findModuleSignatureEntry(recBytes)
			if err != nil {
				// A hard error is an acceptable rejection.
				return
			}
			if len(got) != 0 {
				t.Fatalf("a %q record carrying MBL's ordinal was accepted as a module bundle", standard)
			}
		})
	}
}

// TestFindModuleSignatureEntryToleratesStandardFormatting — publisher lanes are
// not byte-uniform about the standard string, and a signature check must not
// turn cosmetic whitespace or casing into "unsigned".
func TestFindModuleSignatureEntryToleratesStandardFormatting(t *testing.T) {
	payloadJSON := mustSignaturePayloadJSON(t)

	for _, standard := range []string{"MBL", " MBL", "MBL ", "mbl", "Mbl"} {
		t.Run(standard, func(t *testing.T) {
			recBytes := buildRECTrailerWithMBLSignatureAs(t, payloadJSON, recRecordTypeMBLCurrent, standard)
			got, err := findModuleSignatureEntry(recBytes)
			if err != nil {
				t.Fatalf("standard %q: %v", standard, err)
			}
			if len(got) == 0 {
				t.Fatalf("standard %q was not recognised as MBL", standard)
			}
		})
	}
}

// TestDecodeModuleBundleIgnoresUnionOrdinal covers the SECOND verifier that read
// the same literal (publication_signature_bundle.go), so fixing one and not the
// other cannot pass.
//
// This fixture's MBL carries no canonical module hash, so decodeModuleBundle is
// expected to fail LATER, at hash validation. That is precisely the assertion:
// record SELECTION must behave identically for every ordinal, and must never be
// the thing that fails. "publication trailer carries no MBL record" is the
// ordinal-dependent failure this test exists to forbid.
func TestDecodeModuleBundleIgnoresUnionOrdinal(t *testing.T) {
	payloadJSON := mustSignaturePayloadJSON(t)
	const notFound = "carries no MBL record"

	var want string
	for _, valueType := range []byte{recRecordTypeMBLCurrent, recRecordTypeMBLLegacy, 0, 251} {
		recBytes := buildRECTrailerWithMBLSignatureAs(t, payloadJSON, valueType, "MBL")
		_, err := decodeModuleBundle(recBytes, []byte("\x00asm\x01\x00\x00\x00"))

		got := "<nil>"
		if err != nil {
			got = err.Error()
		}
		if strings.Contains(got, notFound) {
			t.Fatalf("ordinal %d: the MBL record was not selected — record selection is still ordinal-dependent", valueType)
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Fatalf("ordinal %d changed the outcome (%q, current ordinal gave %q) — selection must be ordinal-blind", valueType, got, want)
		}
	}

	// And the standard string MUST still be what selects: a foreign standard has
	// to produce exactly the not-found path, or nothing is being discriminated.
	recBytes := buildRECTrailerWithMBLSignatureAs(t, payloadJSON, recRecordTypeMBLCurrent, "PNM")
	if _, err := decodeModuleBundle(recBytes, []byte("\x00asm\x01\x00\x00\x00")); err == nil || !strings.Contains(err.Error(), notFound) {
		t.Fatalf("a PNM record carrying MBL's ordinal was selected as the module bundle: %v", err)
	}
}
