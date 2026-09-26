package storage

import (
	"testing"
	"time"

	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	OEMFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/google/flatbuffers/go"
)

func TestExtractIndexedFieldsOEMAndPre1970MPE(t *testing.T) {
	t.Run("OEM object and start time", func(t *testing.T) {
		fields, err := extractIndexedFields("OEM.fbs", buildIndexedFieldsTestOEM())
		if err != nil {
			t.Fatal(err)
		}
		if fields.entityID != "1998-067A" {
			t.Fatalf("entity_id = %q", fields.entityID)
		}
		if fields.noradCatID == nil || *fields.noradCatID != 25544 {
			t.Fatalf("norad_cat_id = %v", fields.noradCatID)
		}
		wantEpoch := time.Date(2026, 9, 26, 0, 18, 14, 181696000, time.UTC).Unix()
		if fields.epochUnix == nil || *fields.epochUnix != wantEpoch {
			t.Fatalf("epoch_unix = %v, want %d", fields.epochUnix, wantEpoch)
		}
		if fields.epochDay != "2026-09-26" {
			t.Fatalf("epoch_day = %q", fields.epochDay)
		}
	})

	t.Run("fractional pre-1970 MPE epoch", func(t *testing.T) {
		fields, err := extractIndexedFields("MPE.fbs", buildIndexedFieldsTestMPE(-347155200.5))
		if err != nil {
			t.Fatal(err)
		}
		if fields.epochUnix == nil || *fields.epochUnix != -347155201 {
			t.Fatalf("epoch_unix = %v, want -347155201", fields.epochUnix)
		}
		if fields.epochDay != "1958-12-31" {
			t.Fatalf("epoch_day = %q", fields.epochDay)
		}
	})
}

func buildIndexedFieldsTestOEM() []byte {
	b := flatbuffers.NewBuilder(256)
	objectID := b.CreateString("1998-067A")
	startTime := b.CreateString("2026-09-26T00:18:14.181696Z")

	OEMFB.CATStart(b)
	OEMFB.CATAddOBJECT_ID(b, objectID)
	OEMFB.CATAddNORAD_CAT_ID(b, 25544)
	object := OEMFB.CATEnd(b)

	// SDS v1.221.0 generates the nested OEM block type as unexported. Build
	// that generated 21-slot table directly while using its schema slot numbers.
	b.StartObject(21)
	b.PrependUOffsetTSlot(1, object, 0)    // OBJECT
	b.PrependUOffsetTSlot(7, startTime, 0) // START_TIME
	block := b.EndObject()

	OEMFB.OEMStartEPHEMERIS_DATA_BLOCKVector(b, 1)
	b.PrependUOffsetT(block)
	blocks := b.EndVector(1)
	OEMFB.OEMStart(b)
	OEMFB.OEMAddEPHEMERIS_DATA_BLOCK(b, blocks)
	root := OEMFB.OEMEnd(b)
	OEMFB.FinishSizePrefixedOEMBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

func buildIndexedFieldsTestMPE(epoch float64) []byte {
	b := flatbuffers.NewBuilder(64)
	MPEFB.MPEStart(b)
	MPEFB.MPEAddEPOCH(b, epoch)
	root := MPEFB.MPEEnd(b)
	MPEFB.FinishSizePrefixedMPEBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}
