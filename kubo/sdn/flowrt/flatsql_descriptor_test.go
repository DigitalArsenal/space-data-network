package flowrt

import (
	"bytes"
	"encoding/json"
	"testing"
)

func encodeWasmU32ForTest(value uint32) []byte {
	var out []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if value == 0 {
			return out
		}
	}
}

func wasmWithLinkedStoreSections(payloads ...[]byte) []byte {
	wasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	for _, payload := range payloads {
		name := []byte(linkedStoreSectionName)
		body := append(encodeWasmU32ForTest(uint32(len(name))), name...)
		body = append(body, payload...)
		wasm = append(wasm, 0)
		wasm = append(wasm, encodeWasmU32ForTest(uint32(len(body)))...)
		wasm = append(wasm, body...)
	}
	return wasm
}

func neutralDescriptorWithView() *LinkedStoreDescriptor {
	descriptor := neutralLinkedStoreDescriptor()
	descriptor.RecordViews = []LinkedStoreRecordView{{
		ID:             "records",
		FileIdentifier: "TREC",
		Table:          "fixture_records",
		RecordColumn:   "data",
		Filters: []LinkedStoreRecordViewFilter{{
			Parameter: "label",
			Column:    "label",
			Type:      "text",
		}},
		LatestOrderBy: "key",
	}}
	return descriptor
}

func TestReadLinkedStoreDescriptorCanonical(t *testing.T) {
	descriptor := neutralDescriptorWithView()
	canonical, err := canonicalLinkedStoreDescriptor(descriptor)
	if err != nil {
		t.Fatalf("canonicalLinkedStoreDescriptor: %v", err)
	}
	want := `{"database":"fixture_records","engine":"flatsql","fileIdentifiers":[{"id":"TREC","table":"fixture_records"}],"recordViews":[{"fileIdentifier":"TREC","filters":[{"column":"label","parameter":"label","type":"text"}],"id":"records","latestOrderBy":"key","recordColumn":"data","table":"fixture_records"}],"schema":"table fixture_records { key:string (key); label:string; data:[ubyte]; }","version":1}`
	if string(canonical) != want {
		t.Fatalf("canonical descriptor mismatch:\n got: %s\nwant: %s", canonical, want)
	}

	got, err := ReadLinkedStoreDescriptor(wasmWithLinkedStoreSections(canonical))
	if err != nil {
		t.Fatalf("ReadLinkedStoreDescriptor: %v", err)
	}
	if got == nil || got.Database != descriptor.Database || len(got.RecordViews) != 1 {
		t.Fatalf("decoded descriptor = %#v", got)
	}
}

func TestReadLinkedStoreDescriptorAbsent(t *testing.T) {
	got, err := ReadLinkedStoreDescriptor([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	if err != nil || got != nil {
		t.Fatalf("absent descriptor = %#v, err=%v", got, err)
	}
}

func TestReadLinkedStoreDescriptorRejectsNonCanonicalAndDuplicateSections(t *testing.T) {
	descriptor := neutralDescriptorWithView()
	pretty, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLinkedStoreDescriptor(wasmWithLinkedStoreSections(pretty)); err == nil {
		t.Fatal("accepted non-canonical descriptor JSON")
	}

	canonical, err := canonicalLinkedStoreDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLinkedStoreDescriptor(wasmWithLinkedStoreSections(canonical, canonical)); err == nil {
		t.Fatal("accepted duplicate linked-store descriptor sections")
	}
}

func TestReadLinkedStoreDescriptorRejectsMissingFilterArray(t *testing.T) {
	canonical, err := canonicalLinkedStoreDescriptor(neutralDescriptorWithView())
	if err != nil {
		t.Fatal(err)
	}
	missing := bytes.Replace(canonical, []byte(`"filters":[{"column":"label","parameter":"label","type":"text"}]`), []byte(`"filters":null`), 1)
	if bytes.Equal(missing, canonical) {
		t.Fatal("test fixture did not remove filter array")
	}
	if _, err := ReadLinkedStoreDescriptor(wasmWithLinkedStoreSections(missing)); err == nil {
		t.Fatal("accepted record view without a filter array")
	}
}
