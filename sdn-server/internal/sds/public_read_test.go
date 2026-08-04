package sds

import "testing"

// The anonymous data plane is an ALLOW list. These tests pin both halves of
// that: what it admits, and — more importantly — that it stays closed by
// default (sdn-rfb-public-read-allowlist).

func TestPublicReadSchemaAdmitsThePublishedStandards(t *testing.T) {
	for _, schema := range []string{
		"OMM.fbs", "CAT.fbs", "MPE.fbs", "SPW.fbs",
		"RFB.fbs", "LKS.fbs",
		"PNM.fbs", "DPM.fbs", "EPM.fbs", "APP.fbs",
	} {
		if !IsPublicReadSchema(schema) {
			t.Errorf("%s must be on the anonymous data plane", schema)
		}
		if PublicReadSchemaReason(schema) == "" {
			t.Errorf("%s is public but carries no recorded reason", schema)
		}
	}
}

// TestPublicReadSchemaAcceptsCodeAndFileName: a caller holding "rfb" from a URL
// path and a caller holding "RFB.fbs" from the registry must get the same
// answer. The 401-before-CORS defect was a string-shape mismatch at heart.
func TestPublicReadSchemaAcceptsCodeAndFileName(t *testing.T) {
	for _, form := range []string{"rfb", "RFB", "rfb.fbs", "RFB.fbs", " RFB.fbs "} {
		if !IsPublicReadSchema(form) {
			t.Errorf("IsPublicReadSchema(%q) = false, want true", form)
		}
	}
}

// TestPublicReadSchemaRefusesSensitiveFamilies is the fail-closed guarantee.
// These standards carry key material, access grants and node-internal ledgers:
// a per-schema data plane that admitted them would be a worse defect than the
// literal list it replaced.
func TestPublicReadSchemaRefusesSensitiveFamilies(t *testing.T) {
	for _, schema := range []string{
		"KMF.fbs", // key material
		"ENC.fbs", // encrypted envelopes
		"ACL.fbs", // access control lists
		"LGR.fbs", // grants ledger
		"PGR.fbs", // SDN-internal
		"PLOG.fbs",
		"PLHD.fbs",
		"RHD.fbs",
	} {
		if IsPublicReadSchema(schema) {
			t.Errorf("%s must NOT be anonymously readable", schema)
		}
	}
}

func TestPublicReadSchemaRefusesUnknownAndMalformed(t *testing.T) {
	for _, schema := range []string{
		"",
		"   ",
		"NOPE",
		"NOPE.fbs",
		"../KMF",           // traversal
		"../../etc/passwd", //
		"schemas/KMF.fbs",  // path separator
		"..",               //
		".fbs",             //
	} {
		if IsPublicReadSchema(schema) {
			t.Errorf("IsPublicReadSchema(%q) = true, want false", schema)
		}
	}
}

// TestPublicReadSchemasAreEmbedded: every standard on the anonymous data plane
// must actually be a schema this node holds. A typo here would open a door to
// nothing, and hide the fact that the real schema is still closed.
func TestPublicReadSchemasAreEmbedded(t *testing.T) {
	supported := make(map[string]struct{}, len(SupportedSchemas))
	for _, schema := range SupportedSchemas {
		supported[schema] = struct{}{}
	}
	for _, schema := range PublicReadSchemas() {
		if _, ok := supported[schema]; !ok {
			t.Errorf("%s is on the anonymous data plane but is not a supported schema", schema)
		}
		if _, err := schemasFS.ReadFile(embeddedSchemaPath(schema)); err != nil {
			t.Errorf("%s is on the anonymous data plane but is not embedded: %v", schema, err)
		}
	}
}

// TestPublicReadIsAnAllowList: the great majority of standards must be closed.
// If a future change inverts the default, this fails loudly rather than
// silently publishing the store.
func TestPublicReadIsAnAllowList(t *testing.T) {
	open := 0
	for _, schema := range SupportedSchemas {
		if IsPublicReadSchema(schema) {
			open++
		}
	}
	if open != len(PublicReadSchemas()) {
		t.Fatalf("anonymous schemas = %d, declared = %d", open, len(PublicReadSchemas()))
	}
	if open >= len(SupportedSchemas)/2 {
		t.Fatalf("%d of %d schemas are anonymous — the data plane is no longer an allow list", open, len(SupportedSchemas))
	}
}
