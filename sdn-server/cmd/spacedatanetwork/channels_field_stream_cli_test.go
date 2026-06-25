package main

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fsm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSM"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestChannelsFieldStreamPrintsVisibilityRowsWithoutSecrets(t *testing.T) {
	t.Parallel()

	messagePath := writeFieldStreamMessageFixture(t)
	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"field-stream", messagePath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels field-stream failed: %v", err)
	}

	body := out.String()
	for _, want := range []string{
		"field_path",
		"state",
		"object_id",
		"Public",
		"position",
		"Encrypted",
		"maneuver_plan",
		"Redacted",
		"covariance_detail",
		"Unavailable",
		"policy-mpe-alpha",
		"epoch-7",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("field-stream table output missing %q:\n%s", want, body)
		}
	}
	assertFieldStreamOutputOmitsSecrets(t, body)
}

func TestChannelsFieldStreamPrintsJSONWithoutSecrets(t *testing.T) {
	t.Parallel()

	messagePath := writeFieldStreamMessageFixture(t)
	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"field-stream", messagePath, "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels field-stream --format json failed: %v", err)
	}
	assertFieldStreamOutputOmitsSecrets(t, out.String())

	var payload searchResult
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("field-stream JSON invalid: %v\n%s", err, out.String())
	}
	if payload.Count != 4 || len(payload.Results) != 4 {
		t.Fatalf("field-stream JSON count = %d len = %d", payload.Count, len(payload.Results))
	}
	row := payload.Results[1]
	if row["field_path"] != "position" ||
		row["state"] != "Encrypted" ||
		row["ciphertext_length"].(float64) != 13 ||
		row["key_id"] != "field-key:alpha:position:epoch-7" {
		t.Fatalf("unexpected encrypted field JSON row: %#v", row)
	}
	for _, forbidden := range []string{"value", "ciphertext", "nonce", "tag", "aad_hash", "provider_signature"} {
		if _, ok := row[forbidden]; ok {
			t.Fatalf("field-stream JSON row exposed %q: %#v", forbidden, row)
		}
	}
}

func TestChannelsFieldStreamPrintsCSVWithoutSecrets(t *testing.T) {
	t.Parallel()

	messagePath := writeFieldStreamMessageFixture(t)
	var out bytes.Buffer
	cmd := newChannelsCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"field-stream", messagePath, "--format", "csv"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("channels field-stream --format csv failed: %v", err)
	}
	assertFieldStreamOutputOmitsSecrets(t, out.String())

	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("field-stream CSV invalid: %v\n%s", err, out.String())
	}
	if len(records) != 5 {
		t.Fatalf("field-stream CSV records len = %d, want 5: %#v", len(records), records)
	}
	if strings.Join(records[0], ",") != "message_id,provider_peer_id,listing_id,stream_id,schema_code,policy_id,policy_version,key_epoch,sequence,subject_id,field_path,state,encoding,key_id,ciphertext_length,value_length,release_tags,decision" {
		t.Fatalf("field-stream CSV header = %#v", records[0])
	}
	if records[2][10] != "position" || records[2][11] != "Encrypted" || records[2][14] != "13" {
		t.Fatalf("field-stream CSV encrypted row = %#v", records[2])
	}
}

func writeFieldStreamMessageFixture(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "message.fsm")
	if err := os.WriteFile(path, buildCLIFieldStreamMessageFixture(), 0o600); err != nil {
		t.Fatalf("write field-stream message fixture: %v", err)
	}
	return path
}

func assertFieldStreamOutputOmitsSecrets(t *testing.T, output string) {
	t.Helper()

	for _, secret := range []string{
		"CLEAR-SECRET",
		base64.StdEncoding.EncodeToString([]byte("CLEAR-SECRET")),
		"CIPHER-SECRET",
		base64.StdEncoding.EncodeToString([]byte("CIPHER-SECRET")),
		base64.StdEncoding.EncodeToString([]byte("NONCE-SECRET")),
		base64.StdEncoding.EncodeToString([]byte("TAG-SECRET")),
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("field-stream output exposed secret %q:\n%s", secret, output)
		}
	}
}

func buildCLIFieldStreamMessageFixture() []byte {
	builder := flatbuffers.NewBuilder(1024)
	publicField := buildCLIFieldStreamValue(
		builder,
		"object_id",
		[]uint32{1},
		0,
		3,
		[]byte("CLEAR-SECRET"),
		nil,
		nil,
		nil,
		"",
		nil,
		[]string{"releasable"},
		"allow-public",
	)
	encryptedField := buildCLIFieldStreamValue(
		builder,
		"position",
		[]uint32{3},
		1,
		2,
		nil,
		[]byte("CIPHER-SECRET"),
		[]byte("NONCE-SECRET"),
		[]byte("TAG-SECRET"),
		"field-key:alpha:position:epoch-7",
		filledCLIBytes(32, 0x23),
		[]string{"restricted", "customer-alpha"},
		"allow-encrypted",
	)
	redactedField := buildCLIFieldStreamValue(
		builder,
		"maneuver_plan",
		[]uint32{7},
		2,
		1,
		nil,
		nil,
		nil,
		nil,
		"",
		nil,
		[]string{"maneuver", "not-granted"},
		"redacted:not-granted",
	)
	unavailableField := buildCLIFieldStreamValue(
		builder,
		"covariance_detail",
		[]uint32{9},
		3,
		1,
		nil,
		nil,
		nil,
		nil,
		"",
		nil,
		[]string{"covariance", "offline"},
		"unavailable:provider-offline",
	)
	fields := offsetCLIVector(builder, []flatbuffers.UOffsetT{publicField, encryptedField, redactedField, unavailableField})

	messageID := builder.CreateByteString([]byte("fsm-mpe-alpha-000001"))
	providerPeerID := builder.CreateByteString([]byte("provider-peer"))
	listingID := builder.CreateByteString([]byte("listing-maneuver-ephemeris"))
	streamID := builder.CreateByteString([]byte("maneuver-ephemeris-live"))
	schemaCode := builder.CreateByteString([]byte("MPE"))
	schemaHash := builder.CreateByteVector(filledCLIBytes(32, 0x61))
	policyID := builder.CreateByteString([]byte("policy-mpe-alpha"))
	keyEpoch := builder.CreateByteString([]byte("epoch-7"))
	subjectID := builder.CreateByteString([]byte("customer-alpha-peer"))
	payloadHash := builder.CreateByteVector(filledCLIBytes(32, 0x31))
	previousHash := builder.CreateByteVector(filledCLIBytes(32, 0x30))
	providerSignature := builder.CreateByteVector(filledCLIBytes(64, 0xa1))

	fsm.FSMStart(builder)
	fsm.FSMAddMESSAGE_ID(builder, messageID)
	fsm.FSMAddPROVIDER_PEER_ID(builder, providerPeerID)
	fsm.FSMAddLISTING_ID(builder, listingID)
	fsm.FSMAddSTREAM_ID(builder, streamID)
	fsm.FSMAddSCHEMA_CODE(builder, schemaCode)
	fsm.FSMAddSCHEMA_HASH(builder, schemaHash)
	fsm.FSMAddPOLICY_ID(builder, policyID)
	fsm.FSMAddPOLICY_VERSION(builder, 3)
	fsm.FSMAddKEY_EPOCH(builder, keyEpoch)
	fsm.FSMAddSEQUENCE(builder, 1)
	fsm.FSMAddPRODUCED_AT(builder, 1_800_000_100_000)
	fsm.FSMAddEXPIRES_AT(builder, 1_800_000_160_000)
	fsm.FSMAddSUBJECT_ID(builder, subjectID)
	fsm.FSMAddFIELDS(builder, fields)
	fsm.FSMAddPAYLOAD_HASH(builder, payloadHash)
	fsm.FSMAddPREVIOUS_MESSAGE_HASH(builder, previousHash)
	fsm.FSMAddPROVIDER_SIGNATURE(builder, providerSignature)
	root := fsm.FSMEnd(builder)
	fsm.FinishFSMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildCLIFieldStreamValue(
	builder *flatbuffers.Builder,
	fieldPath string,
	fieldIDPath []uint32,
	state int,
	encoding int,
	value []byte,
	ciphertext []byte,
	nonce []byte,
	tag []byte,
	keyID string,
	aadHash []byte,
	releaseTags []string,
	decision string,
) flatbuffers.UOffsetT {
	fieldPathOffset := builder.CreateByteString([]byte(fieldPath))
	fieldIDPathOffset := uint32CLIVector(builder, fieldIDPath)
	valueOffset := byteCLIVector(builder, value)
	ciphertextOffset := byteCLIVector(builder, ciphertext)
	nonceOffset := byteCLIVector(builder, nonce)
	tagOffset := byteCLIVector(builder, tag)
	var keyIDOffset flatbuffers.UOffsetT
	if keyID != "" {
		keyIDOffset = builder.CreateByteString([]byte(keyID))
	}
	aadHashOffset := byteCLIVector(builder, aadHash)
	releaseTagsOffset := stringOffsetCLIVector(builder, releaseTags)
	decisionOffset := builder.CreateByteString([]byte(decision))

	fsm.FieldStreamValueStart(builder)
	fsm.FieldStreamValueAddFIELD_PATH(builder, fieldPathOffset)
	fsm.FieldStreamValueAddFIELD_ID_PATH(builder, fieldIDPathOffset)
	builder.PrependInt8Slot(2, int8(state), 3)
	builder.PrependInt8Slot(3, int8(encoding), 0)
	if valueOffset != 0 {
		fsm.FieldStreamValueAddVALUE(builder, valueOffset)
	}
	if ciphertextOffset != 0 {
		fsm.FieldStreamValueAddCIPHERTEXT(builder, ciphertextOffset)
	}
	if nonceOffset != 0 {
		fsm.FieldStreamValueAddNONCE(builder, nonceOffset)
	}
	if tagOffset != 0 {
		fsm.FieldStreamValueAddTAG(builder, tagOffset)
	}
	if keyIDOffset != 0 {
		fsm.FieldStreamValueAddKEY_ID(builder, keyIDOffset)
	}
	if aadHashOffset != 0 {
		fsm.FieldStreamValueAddAAD_HASH(builder, aadHashOffset)
	}
	fsm.FieldStreamValueAddRELEASE_TAGS(builder, releaseTagsOffset)
	fsm.FieldStreamValueAddDECISION(builder, decisionOffset)
	return fsm.FieldStreamValueEnd(builder)
}

func uint32CLIVector(builder *flatbuffers.Builder, values []uint32) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	builder.StartVector(4, len(values), 4)
	for i := len(values) - 1; i >= 0; i-- {
		builder.PrependUint32(values[i])
	}
	return builder.EndVector(len(values))
}

func byteCLIVector(builder *flatbuffers.Builder, bytes []byte) flatbuffers.UOffsetT {
	if len(bytes) == 0 {
		return 0
	}
	return builder.CreateByteVector(bytes)
}

func stringOffsetCLIVector(builder *flatbuffers.Builder, values []string) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, 0, len(values))
	for _, value := range values {
		offsets = append(offsets, builder.CreateByteString([]byte(value)))
	}
	return offsetCLIVector(builder, offsets)
}

func offsetCLIVector(builder *flatbuffers.Builder, offsets []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(offsets) == 0 {
		return 0
	}
	builder.StartVector(4, len(offsets), 4)
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func filledCLIBytes(length int, value byte) []byte {
	bytes := make([]byte, length)
	for i := range bytes {
		bytes[i] = value
	}
	return bytes
}
