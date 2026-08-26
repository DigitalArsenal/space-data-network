package enginecatalog

import "testing"

// TestDeclaresEncryptedFieldDiscriminates pins the ONE rule that can take a
// standard off the engine for a reason other than its IDL being incomplete.
//
// BOTH DIRECTIONS ARE DANGEROUS. A miss publishes sealed key material on the
// public query surface; a false positive silently un-routes a standard that
// merely mentions encryption — and the embedded IDLs are full of such
// mentions: KMF.fbs DECLARES the attribute (`attribute "encrypted";`),
// RHD.fbs has a field literally NAMED `encrypted`, and a dozen schemas carry
// `/// ... encrypted ...` doc comments.
func TestDeclaresEncryptedFieldDiscriminates(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"the real thing", "table KMF {\n  KEY_ID: string;\n  KEY_BYTES: [ubyte] (encrypted);\n}", true},
		{"with other attributes", "table T {\n  A: string (required, encrypted);\n}", true},
		{"attribute declaration only", "attribute \"encrypted\";\n\ntable T {\n  A: string;\n}", false},
		{"field named encrypted", "table RHD {\n  encrypted: bool = false;\n}", false},
		{"unrelated attribute", "table T {\n  A: string (required);\n}", false},
		{"encryption in prose", "table T {\n  A: string;\n}", false},
	} {
		if got := declaresEncryptedField(stripComments(tc.src)); got != tc.want {
			t.Errorf("%s: declaresEncryptedField = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A DOC COMMENT IS NOT A DECLARATION. Comments are stripped before the
	// scan, and this is the exact line KMF.fbs carries above KEY_BYTES.
	src := "table T {\n  /// This may be field-encrypted (encrypted) when transported.\n  A: string;\n}"
	if declaresEncryptedField(stripComments(src)) {
		t.Error("a doc comment mentioning (encrypted) un-routed a standard")
	}
}
