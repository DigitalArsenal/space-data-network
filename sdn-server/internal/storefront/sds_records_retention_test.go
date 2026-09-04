package storefront

import (
	"bytes"
	"testing"

	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
	flatbuffers "github.com/google/flatbuffers/go"
)

// recommendedRetentionSlot is the $STF vtable offset of RECOMMENDED_RETENTION
// (field 29: 4 + 2*29), the slot STFAddRECOMMENDED_RETENTION writes.
const recommendedRetentionSlot = flatbuffers.VOffsetT(4 + 2*29)

// vtableLength reads the size word of a root table's vtable: the number of
// bytes the vtable spans, which is how many slots it can name.
func vtableLength(stfBytes []byte) flatbuffers.VOffsetT {
	tab := sdsstf.GetRootAsSTF(stfBytes, 0).Table()
	vtable := flatbuffers.UOffsetT(int64(tab.Pos) - int64(tab.GetSOffsetT(tab.Pos)))
	return tab.GetVOffsetT(vtable)
}

func retentionFixture(rule string) Listing {
	return Listing{
		ListingID:            "l-retention",
		ListingKind:          ListingKindDataStream,
		ProviderPeerID:       "12D3KooWRetentionProvider",
		Title:                "Catalogue feed",
		DataTypes:            []string{"OMM"},
		DeliveryMethods:      []string{"PubSub"},
		Pricing:              []PricingTier{{Name: "Open", PriceCurrency: "SDN"}},
		AcceptedPayments:     []PaymentMethod{PaymentMethodFree},
		Active:               true,
		Version:              1,
		RecommendedRetention: rule,
	}
}

// TestListingRecordRecommendedRetention: the publisher's recommended rule is
// stated once on the $STF, and the default rule costs no bytes — a listing
// signed before the field existed re-encodes byte-identically, so its
// FlatBuffer signature keeps verifying.
func TestListingRecordRecommendedRetention(t *testing.T) {
	t.Parallel()
	join := resolver(nil)

	archive := retentionFixture("ArchiveAll")
	archiveBytes := encodeListingRecord(&archive, join)
	if got := sdsstf.GetRootAsSTF(archiveBytes, 0).RECOMMENDED_RETENTION().String(); got != "ArchiveAll" {
		t.Fatalf("RECOMMENDED_RETENTION = %q, want ArchiveAll", got)
	}
	if got := vtableLength(archiveBytes); got <= recommendedRetentionSlot {
		t.Fatalf("ArchiveAll vtable spans %d bytes, slot %d must be named", got, recommendedRetentionSlot)
	}

	blank := retentionFixture("")
	blankBytes := encodeListingRecord(&blank, join)
	replace := retentionFixture("ReplaceCurrent")
	replaceBytes := encodeListingRecord(&replace, join)
	if !bytes.Equal(blankBytes, replaceBytes) {
		t.Fatal("\"\" and ReplaceCurrent encode to different bytes")
	}
	// The default scalar is skipped by the builder (PrependInt8Slot x == d),
	// so the vtable never reaches slot 29: exactly the bytes the encoder
	// produced before the field existed.
	if got := vtableLength(blankBytes); got > recommendedRetentionSlot {
		t.Fatalf("default-rule vtable spans %d bytes, names slot %d: default scalar was written", got, recommendedRetentionSlot)
	}
	blankTable := sdsstf.GetRootAsSTF(blankBytes, 0).Table()
	if blankTable.Offset(recommendedRetentionSlot) != 0 {
		t.Fatal("default-rule STF carries a RECOMMENDED_RETENTION slot")
	}
	if got := sdsstf.GetRootAsSTF(blankBytes, 0).RECOMMENDED_RETENTION().String(); got != "ReplaceCurrent" {
		t.Fatalf("absent slot reads %q, want ReplaceCurrent", got)
	}
	if bytes.Equal(blankBytes, archiveBytes) {
		t.Fatal("ArchiveAll encodes the same bytes as the default rule")
	}

	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"ArchiveAll", archiveBytes, "ArchiveAll"},
		{"default", blankBytes, "ReplaceCurrent"},
	} {
		decoded, err := decodeListingRecord(tc.data)
		if err != nil {
			t.Fatalf("%s: decodeListingRecord: %v", tc.name, err)
		}
		if decoded.RecommendedRetention != tc.want {
			t.Fatalf("%s: decoded RecommendedRetention = %q, want %q", tc.name, decoded.RecommendedRetention, tc.want)
		}
		if again := encodeListingRecord(decoded, join); !bytes.Equal(again, tc.data) {
			t.Fatalf("%s: decode → encode is not byte-identical", tc.name)
		}
	}
}

// TestRetentionPolicyWord: "" and unknown words read as the default rule.
func TestRetentionPolicyWord(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"": "ReplaceCurrent", "ReplaceCurrent": "ReplaceCurrent", "ArchiveAll": "ArchiveAll",
		" ArchiveAll ": "ArchiveAll", "archiveall": "ReplaceCurrent", "stfRetentionPolicy(7)": "ReplaceCurrent",
	} {
		if got := retentionPolicyWord(in); got != want {
			t.Errorf("retentionPolicyWord(%q) = %q, want %q", in, got, want)
		}
	}
}
