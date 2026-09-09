package sds

import (
	"context"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NCD"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestNCDAdmissionUsesReleasedIdentifierAndMetadata(t *testing.T) {
	v, err := NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.HasSchema("NCD.fbs") || !IsPublicReadSchema("NCD.fbs") {
		t.Fatal("published NCD schema is not admitted for storage and public reads")
	}
	if ident, ok := v.FileIdentifier("NCD.fbs"); !ok || ident != NCD.NCDIdentifier {
		t.Fatalf("NCD identifier = %q, found=%v", ident, ok)
	}
	b := flatbuffers.NewBuilder(256)
	format := b.CreateString("resource-fixture")
	digest := b.CreateString(strings.Repeat("a", 64))
	NCD.NCDStart(b)
	NCD.NCDAddFORMAT(b, NCD.EnumValuesncdContainerFormat["PROVIDER_DEFINED"])
	NCD.NCDAddPROVIDER_DEFINED_FORMAT_NAME(b, format)
	NCD.NCDAddSOURCE_BYTE_LENGTH(b, 42)
	NCD.NCDAddSOURCE_SHA256(b, digest)
	NCD.FinishSizePrefixedNCDBuffer(b, NCD.NCDEnd(b))
	record := b.FinishedBytes()
	if err := v.Validate(context.Background(), "NCD.fbs", record); err != nil {
		t.Fatal(err)
	}
	decoded := NCD.GetSizePrefixedRootAsNCD(record, 0)
	if decoded.SOURCE_BYTE_LENGTH() != 42 || string(decoded.SOURCE_SHA256()) != strings.Repeat("a", 64) {
		t.Fatal("container source identity was lost")
	}
	bad := append([]byte(nil), record...)
	copy(bad[8:12], "$CAT")
	for _, payload := range [][]byte{nil, []byte(`{"SOURCE_BYTE_LENGTH":42}`), bad} {
		if err := v.Validate(context.Background(), "NCD.fbs", payload); err == nil {
			t.Fatal("non-NCD envelope admitted")
		}
	}
}
