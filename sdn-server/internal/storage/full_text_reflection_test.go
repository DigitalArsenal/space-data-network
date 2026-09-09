package storage

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NCD"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestEveryRoutedStandardHasACompleteSearchSchema(t *testing.T) {
	store := newEngineRecordsStoreWithOptions(t, t.TempDir())
	defer store.Close()
	for _, name := range sds.SupportedSchemas {
		_, table, fileID, routed := EngineRelationSchemaText(name)
		if !routed {
			continue
		}
		binarySchema, ok := sds.SearchSchema(name)
		if !ok {
			t.Errorf("routed standard lacks complete search metadata: %s", name)
			continue
		}
		builder := flatbuffers.NewBuilder(32)
		builder.StartObject(0)
		builder.FinishWithFileIdentifier(builder.EndObject(), []byte(fileID))
		var text string
		err := store.db.QueryRow(`SELECT flatsql_record_text(?,?,?)`, table, builder.FinishedBytes(), binarySchema).Scan(&text)
		// Empty records cannot satisfy required fields, but every routed
		// standard must have valid, supported metadata before record validation.
		if err != nil && !strings.Contains(err.Error(), "Invalid FlatBuffer search record") {
			t.Errorf("unsupported complete schema %s: %v", name, err)
		}
	}
}

func TestFullTextNativeDescriptorIncludesLateAndNestedFields(t *testing.T) {
	directory := t.TempDir()
	store := newEngineRecordsStoreWithOptions(t, directory, WithEngineGenericHotWindow(1))
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	b := flatbuffers.NewBuilder(256)
	format := b.CreateString("sample ephemeris")
	comment := b.CreateString("historicalcomment")
	NCD.NCDStartCOMMENT_AREAVector(b, 1)
	b.PrependUOffsetT(comment)
	comments := b.EndVector(1)
	segmentName := b.CreateString("segmentneedle")
	NCD.NCDSegmentDescriptorStart(b)
	NCD.NCDSegmentDescriptorAddNAME(b, segmentName)
	segment := NCD.NCDSegmentDescriptorEnd(b)
	NCD.NCDStartSEGMENTSVector(b, 1)
	b.PrependUOffsetT(segment)
	segments := b.EndVector(1)
	hash := b.CreateString("abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	NCD.NCDStart(b)
	NCD.NCDAddFORMAT(b, NCD.EnumValuesncdContainerFormat["PROVIDER_DEFINED"])
	NCD.NCDAddPROVIDER_DEFINED_FORMAT_NAME(b, format)
	NCD.NCDAddCOMMENT_AREA(b, comments)
	NCD.NCDAddSEGMENTS(b, segments)
	NCD.NCDAddSOURCE_SHA256(b, hash)
	NCD.NCDAddSOURCE_BYTE_LENGTH(b, 31750)
	NCD.FinishSizePrefixedNCDBuffer(b, NCD.NCDEnd(b))
	// The provider builds a size-prefixed descriptor, then the ingestion
	// boundary removes the stream prefix. Its 64-bit fields still use that
	// original alignment origin, as in the live Intelsat descriptor.
	want := append([]byte(nil), b.FinishedBytes()[4:]...)
	cid, err := store.StoreWithSourceTags("NCD.fbs", want, "peer", nil, SourceTags{ProviderID: "provider", SourceName: "native"})
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		deadline := time.Now().Add(60 * time.Second)
		for {
			err := store.CheckFullTextSearch("NCD.fbs", "segmentneedle")
			if err == nil {
				break
			}
			if !errors.Is(err, ErrSearchIndexBuilding) || time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		for _, term := range []string{"historicalcomment", "segmentneedle", "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"} {
			query := RawRecordQuery{SchemaName: "NCD.fbs", ProviderID: "provider", SourceName: "native", Search: term}
			count, err := store.CountRawRecords(query)
			if err != nil || count != 1 {
				t.Fatalf("complete-record search %q: %d %v", term, count, err)
			}
			query.SourceName = "another-source"
			if count, err := store.CountRawRecords(query); err != nil || count != 0 {
				t.Fatalf("search crossed source scope: %d %v", count, err)
			}
		}
		got, err := store.GetRecord("NCD.fbs", cid)
		if err != nil || !bytes.Equal(got.Data, want) {
			t.Fatalf("search changed canonical record bytes: %v", err)
		}
	}
	check()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store = newEngineRecordsStoreWithOptions(t, directory, WithEngineGenericHotWindow(1))
	check()
}
