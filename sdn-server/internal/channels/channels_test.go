package channels

import "testing"

var testInternalSchemaSuffix = string([]byte{'.', 'f', 'b', 's'})

func TestParseChannelIDParsesFromRight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input        string
		sourceID     string
		standardCode string
		feedUUID     string
	}{
		{
			input:        "celestrak-OMM",
			sourceID:     "celestrak",
			standardCode: "OMM",
		},
		{
			input:        "celestrak-eth-CDM",
			sourceID:     "celestrak-eth",
			standardCode: "CDM",
		},
		{
			input:        "spaceaware-live-OMM-550e8400-e29b-41d4-a716-446655440000",
			sourceID:     "spaceaware-live",
			standardCode: "OMM",
			feedUUID:     "550e8400-e29b-41d4-a716-446655440000",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseChannelID(tc.input)
			if err != nil {
				t.Fatalf("ParseChannelID(%q) returned error: %v", tc.input, err)
			}
			if got.ChannelID != tc.input || got.SourceID != tc.sourceID || got.StandardCode != tc.standardCode || got.FeedUUID != tc.feedUUID {
				t.Fatalf("ParseChannelID(%q) = %+v", tc.input, got)
			}
		})
	}
}

func TestParseChannelIDRejectsInvalidPublicNames(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"",
		"OMM",
		"-OMM",
		"celestrak-",
		"celestrak-omm",
		"celestrak-OMM" + testInternalSchemaSuffix,
		"celestrak-OMM-not-a-uuid",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseChannelID(input); err == nil {
				t.Fatalf("ParseChannelID(%q) succeeded", input)
			}
		})
	}
}

func TestChannelFormattingAndInternalSchemaMapping(t *testing.T) {
	t.Parallel()

	got, err := FormatChannelID(ChannelIDInput{SourceID: "celestrak-eth", StandardCode: "CDM"})
	if err != nil {
		t.Fatalf("FormatChannelID returned error: %v", err)
	}
	if got != "celestrak-eth-CDM" {
		t.Fatalf("FormatChannelID = %q", got)
	}

	got, err = FormatChannelID(ChannelIDInput{
		SourceID:     "spaceaware",
		StandardCode: "OMM",
		FeedUUID:     "550e8400-e29b-41d4-a716-446655440000",
	})
	if err != nil {
		t.Fatalf("FormatChannelID with UUID returned error: %v", err)
	}
	if got != "spaceaware-OMM-550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("FormatChannelID with UUID = %q", got)
	}

	schemaName, err := SchemaNameFromStandardCode("OMM")
	if err != nil {
		t.Fatalf("SchemaNameFromStandardCode returned error: %v", err)
	}
	if schemaName != "OMM"+testInternalSchemaSuffix {
		t.Fatalf("SchemaNameFromStandardCode = %q", schemaName)
	}

	standardCode, err := StandardCodeFromSchemaName("OMM" + testInternalSchemaSuffix)
	if err != nil {
		t.Fatalf("StandardCodeFromSchemaName returned error: %v", err)
	}
	if standardCode != "OMM" {
		t.Fatalf("StandardCodeFromSchemaName = %q", standardCode)
	}
	if DiscoveryTopic("OMM") != "/spacedatanetwork/channels/OMM" {
		t.Fatalf("DiscoveryTopic returned unexpected topic")
	}
}
