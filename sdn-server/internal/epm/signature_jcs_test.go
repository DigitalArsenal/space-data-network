package epm

import "testing"

// These canonical-content vectors are byte-identical to the isomorphic verifiers:
// the C++ wasm module (space-data-network-modules common/epm/tests/epm_content_test.cpp)
// and the wallet (hd-wallet-wasm wasm/test/test_epm_attestation.mjs). If this drifts,
// wallet-signed / module-signed EPMs stop verifying here and vice versa.
func TestMarshalEPMSigningContentMatchesJCSVectors(t *testing.T) {
	// Vector 1 — machine identity (ENTITY_TYPE + one signing key + timestamp).
	vec1 := map[string]interface{}{
		"ENTITY_TYPE":         "Individual",
		"SIGNATURE_TIMESTAMP": int64(1782470000),
		"KEYS": []map[string]interface{}{
			{"PUBLIC_KEY": "aabbcc", "XPUB": "xpubTEST", "ADDRESS_TYPE": "ed25519", "KEY_TYPE": "Signing"},
		},
	}
	want1 := `{"ENTITY_TYPE":"Individual","KEYS":[{"ADDRESS_TYPE":"ed25519","KEY_TYPE":"Signing","PUBLIC_KEY":"aabbcc","XPUB":"xpubTEST"}],"SIGNATURE_TIMESTAMP":1782470000}`
	got1, err := marshalEPMSigningContent(vec1)
	if err != nil {
		t.Fatalf("vector 1: %v", err)
	}
	if string(got1) != want1 {
		t.Fatalf("vector 1 mismatch:\n got: %s\nwant: %s", got1, want1)
	}

	// Vector 2 — raw & < > (NO HTML escaping; the json.Marshal regression) + key sort.
	vec2 := map[string]interface{}{
		"ENTITY_TYPE": "Organization",
		"LEGAL_NAME":  "Acme & Co <Ltd>",
	}
	want2 := `{"ENTITY_TYPE":"Organization","LEGAL_NAME":"Acme & Co <Ltd>"}`
	got2, err := marshalEPMSigningContent(vec2)
	if err != nil {
		t.Fatalf("vector 2: %v", err)
	}
	if string(got2) != want2 {
		t.Fatalf("vector 2 mismatch (HTML escaping not disabled?):\n got: %s\nwant: %s", got2, want2)
	}
}
