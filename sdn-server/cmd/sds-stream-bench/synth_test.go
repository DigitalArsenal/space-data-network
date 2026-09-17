package main

import (
	"encoding/hex"
	"os"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func benchTemplates(t *testing.T, recordBytes int) ([]*schemaTemplate, *sds.Validator) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	dir, err := locateSchemaDir(wd)
	if err != nil {
		t.Fatalf("locateSchemaDir(%s) error = %v", wd, err)
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	templates, err := buildTemplates(dir, sds.SupportedSchemas, v, recordBytes)
	if err != nil {
		t.Fatalf("buildTemplates() error = %v", err)
	}
	if len(templates) != len(sds.SupportedSchemas) {
		t.Fatalf("buildTemplates() planned %d templates, want %d", len(templates), len(sds.SupportedSchemas))
	}
	return templates, v
}

// TestEverySchemaSynthesisesAnAdmissibleRecord is the whole premise of the
// benchmark: a synthesised record the store would REFUSE measures nothing.
// VerifyEnvelope is the admission check the write path applies with or without
// the optional flatc WASM module, so passing it for all 236 standards is what
// makes "streamed across all schemas" a true statement rather than a claim.
func TestEverySchemaSynthesisesAnAdmissibleRecord(t *testing.T) {
	templates, v := benchTemplates(t, 1024)
	b := flatbuffers.NewBuilder(8192)
	for _, tpl := range templates {
		rec := tpl.build(b, 12345)
		if err := v.VerifyEnvelope(tpl.schema, rec); err != nil {
			t.Errorf("VerifyEnvelope(%s) error = %v", tpl.schema, err)
			continue
		}
		// The identifier is what the ENGINE routes on, so check the bytes
		// themselves and not only that the validator was satisfied: a record
		// admitted by structure alone would be dropped by the engine without
		// an error (engineIngestablePayload exists because that silent drop
		// once looked like a successful ingest).
		ident, declared := v.FileIdentifier(tpl.schema)
		if !declared {
			continue
		}
		if got := string(rec[4:8]); got != ident {
			t.Errorf("%s record carries identifier %q, want %q", tpl.schema, got, ident)
		}
	}
}

// TestSynthesisedRecordsAreUnique guards the measurement itself. The store
// dedupes on the sha256 CID of the buffer and a repeat CID skips the insert,
// the index row and the engine mirror entirely — so a synthesiser that
// repeated itself would report the throughput of the dedupe path.
func TestSynthesisedRecordsAreUnique(t *testing.T) {
	templates, _ := benchTemplates(t, 1024)
	b := flatbuffers.NewBuilder(8192)
	seen := make(map[string]string)
	for _, tpl := range templates {
		for seq := uint64(1); seq <= 32; seq++ {
			rec := tpl.build(b, seq)
			key := hex.EncodeToString(rec)
			if prev, dup := seen[key]; dup {
				t.Fatalf("%s seq %d produced the same bytes as %s", tpl.schema, seq, prev)
			}
			seen[key] = tpl.schema
		}
	}
}

// TestSynthesisedRecordsHitTheRequestedSize keeps the reported MB/s tied to a
// record population the operator asked for: the filler length is solved per
// standard, and a standard whose solution silently failed would quietly skew
// the bytes-per-record mix the benchmark reports.
func TestSynthesisedRecordsHitTheRequestedSize(t *testing.T) {
	const want = 1024
	templates, _ := benchTemplates(t, want)
	b := flatbuffers.NewBuilder(8192)
	for _, tpl := range templates {
		got := len(tpl.build(b, 7))
		if got < want || got > want+64 {
			t.Errorf("%s record is %d bytes, want %d..%d", tpl.schema, got, want, want+64)
		}
	}
}
