package auth

import "testing"

func TestDeniedPublishPrincipalRefusesTheLeakedIdentity(t *testing.T) {
	for x := range sec001DeniedXPubs {
		if !DeniedPublishPrincipal(x, "") {
			t.Fatalf("leaked xpub not refused")
		}
	}
	for k := range sec001DeniedEd25519 {
		if !DeniedPublishPrincipal("xpub-some-other-admin", k) {
			t.Fatalf("leaked key not refused")
		}
		if !DeniedPublishPrincipal("", " "+k+" ") {
			t.Fatalf("leaked key with spaces not refused")
		}
	}
	if DeniedPublishPrincipal("xpub-some-other-admin", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff") {
		t.Fatal("an unrelated admin was refused")
	}
	if len(sec001DeniedXPubs) == 0 || len(sec001DeniedEd25519) == 0 {
		t.Fatal("the denylist is empty")
	}
}
