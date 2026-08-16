package channels

import "testing"

// EGP (Entity Group) rides the standard per-publisher channel convention with
// NO registration step: AssertStandardCode is format-only (^[A-Z]{3}$) and
// deliberately SDS-type-neutral, so a newly ratified three-letter code is
// admissible the moment it exists. This test pins that contract for $EGP so a
// future "registry" refactor cannot silently strand the group channel.
func TestEGPWireTopicNeedsNoRegistration(t *testing.T) {
	if _, err := AssertStandardCode("EGP"); err != nil {
		t.Fatalf("AssertStandardCode(EGP) = %v, want admitted", err)
	}
	got, err := WireTopic("spaceaware", "EGP")
	if err != nil {
		t.Fatalf("WireTopic: %v", err)
	}
	if want := "/spacedatanetwork/channels/EGP/spaceaware"; got != want {
		t.Fatalf("WireTopic = %q, want %q", got, want)
	}
	prefix, err := StandardTopicPrefix("EGP")
	if err != nil {
		t.Fatalf("StandardTopicPrefix: %v", err)
	}
	if want := "/spacedatanetwork/channels/EGP/"; prefix != want {
		t.Fatalf("StandardTopicPrefix = %q, want %q", prefix, want)
	}
}
