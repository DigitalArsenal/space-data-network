package assetpin

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalMetadataAcceptsExactTypedDocument(t *testing.T) {
	sha256 := strings.Repeat("a", 64)
	raw := []byte(`{"attribution":"A < B","candidateKey":"candidate-001","entityId":"vehicle-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceRecordId":"source-001","sourceUrl":"https://example.test/model.glb","vamId":"vam-001"}`)

	metadata, canonical, err := ParseCanonicalMetadata(raw)
	if err != nil {
		t.Fatalf("ParseCanonicalMetadata() error = %v", err)
	}
	if string(canonical) != string(raw) {
		t.Fatalf("canonical metadata = %q, want exact input %q", canonical, raw)
	}
	if metadata.SchemaVersion != 1 ||
		metadata.CandidateKey != "candidate-001" ||
		metadata.EntityID != "vehicle-001" ||
		metadata.SourceRecordID != "source-001" ||
		metadata.SourceURL != "https://example.test/model.glb" ||
		metadata.LicenseName != "CC0-1.0" ||
		metadata.Attribution != "A < B" ||
		metadata.SHA256 != sha256 ||
		metadata.VAMID != "vam-001" {
		t.Fatalf("metadata = %+v, want all typed fields preserved", metadata)
	}
}

func TestCanonicalMetadataRejectsInvalidDocuments(t *testing.T) {
	sha256 := strings.Repeat("b", 64)
	valid := `{"candidateKey":"candidate-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`
	tests := []struct {
		name string
		raw  string
	}{
		{name: "noncanonical whitespace", raw: ` {"candidateKey":"candidate-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "noncanonical key order", raw: `{"schemaVersion":1,"candidateKey":"candidate-001","licenseName":"CC0-1.0","sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "trailing value", raw: valid + `{}`},
		{name: "unknown key", raw: strings.TrimSuffix(valid, "}") + `,"unknown":"value"}`},
		{name: "duplicate key", raw: `{"candidateKey":"candidate-001","candidateKey":"candidate-002","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "schema string", raw: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":"1"`, 1)},
		{name: "schema decimal", raw: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":1.0`, 1)},
		{name: "wrong schema", raw: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{name: "candidate nonstring", raw: strings.Replace(valid, `"candidateKey":"candidate-001"`, `"candidateKey":1`, 1)},
		{name: "candidate surrounding whitespace", raw: strings.Replace(valid, `"candidateKey":"candidate-001"`, `"candidateKey":" candidate-001"`, 1)},
		{name: "missing candidate", raw: `{"licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "empty candidate", raw: strings.Replace(valid, `"candidateKey":"candidate-001"`, `"candidateKey":""`, 1)},
		{name: "missing source", raw: `{"candidateKey":"candidate-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `"}`},
		{name: "empty source", raw: strings.Replace(valid, `"sourceUrl":"https://example.test/model.glb"`, `"sourceUrl":""`, 1)},
		{name: "source surrounding whitespace", raw: strings.Replace(valid, `"sourceUrl":"https://example.test/model.glb"`, `"sourceUrl":"https://example.test/model.glb "`, 1)},
		{name: "missing license", raw: `{"candidateKey":"candidate-001","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "empty license", raw: strings.Replace(valid, `"licenseName":"CC0-1.0"`, `"licenseName":""`, 1)},
		{name: "license surrounding whitespace", raw: strings.Replace(valid, `"licenseName":"CC0-1.0"`, `"licenseName":" CC0-1.0"`, 1)},
		{name: "attribution surrounding whitespace", raw: strings.Replace(valid, `{`, `{"attribution":"Artist ",`, 1)},
		{name: "optional entity surrounding whitespace", raw: strings.Replace(valid, `"licenseName"`, `"entityId":" vehicle-001","licenseName"`, 1)},
		{name: "optional source record surrounding whitespace", raw: strings.Replace(valid, `"sourceUrl"`, `"sourceRecordId":"source-001 ","sourceUrl"`, 1)},
		{name: "optional VAM ID surrounding whitespace", raw: strings.TrimSuffix(valid, "}") + `,"vamId":" vam-001"}`},
		{name: "uppercase SHA", raw: strings.Replace(valid, sha256, strings.ToUpper(sha256), 1)},
		{name: "short SHA", raw: strings.Replace(valid, sha256, sha256[:63], 1)},
		{name: "SHA nonstring", raw: strings.Replace(valid, `"sha256":"`+sha256+`"`, `"sha256":1`, 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := ParseCanonicalMetadata([]byte(test.raw))
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("ParseCanonicalMetadata() error = %v, want ErrInvalidMetadata", err)
			}
		})
	}
}
