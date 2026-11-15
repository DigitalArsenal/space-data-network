package sec001

import "testing"

func TestDenylistNamesTheLeakedIdentity(t *testing.T) {
	if !DeniedXPub(" xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa ") {
		t.Fatal("the leaked root xpub is not denied")
	}
	for _, key := range []string{
		"0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c",
		"8E2F6DB0048925CE413512DF5B4261CEE5AD7E04E65588D1A9AF26CD4254FA2B",
		"ac42adf4cf2746567a9cd49ae1c5c57054810fd64186ca7d701fe24acf1e8c0d",
	} {
		if !DeniedEd25519Hex(key) {
			t.Fatalf("leaked key %s is not denied", key)
		}
	}
	if len(Ed25519Keys()) != 3 {
		t.Fatalf("Ed25519Keys() = %v, want the three leaked keys", Ed25519Keys())
	}
	// The demo update root is not a SEC-001 key.
	if DeniedEd25519Hex("8b4715cd8e0809247375d47b1d81ca8043d79975c5875baf162cdf31bfd5e593") || DeniedXPub("") || DeniedEd25519Hex("") {
		t.Fatal("a key outside the leaked identity is denied")
	}
}
