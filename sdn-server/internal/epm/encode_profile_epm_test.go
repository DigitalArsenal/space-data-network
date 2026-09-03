package epm

import "testing"

// EncodeProfileEPM must produce a wire the node's PUT handler decodes back to
// the same profile (the CLI round trip for `identity set` and the wizard).
func TestEncodeProfileEPMRoundTrips(t *testing.T) {
	in := &Profile{DN: "Example Org", LegalName: "Example Org", FamilyName: "Doe", GivenName: "Jane", Email: "ops@example.org"}
	wire, err := EncodeProfileEPM(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeProfileEPM(wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DN != in.DN || out.LegalName != in.LegalName || out.FamilyName != in.FamilyName || out.GivenName != in.GivenName || out.Email != in.Email {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}
