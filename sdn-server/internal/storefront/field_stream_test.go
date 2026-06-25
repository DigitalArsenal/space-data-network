package storefront

import (
	"testing"

	fsm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSM"
	fsp "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSP"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestDecodeFieldStreamPolicySummary(t *testing.T) {
	policy, err := DecodeFieldStreamPolicySummary(buildFieldStreamPolicyFixture())
	if err != nil {
		t.Fatalf("DecodeFieldStreamPolicySummary failed: %v", err)
	}
	if policy.PolicyID != "policy-mpe-alpha" {
		t.Fatalf("PolicyID = %q, want policy-mpe-alpha", policy.PolicyID)
	}
	if policy.ProviderPeerID != "provider-peer" || policy.ListingID != "listing-maneuver-ephemeris" ||
		policy.StreamID != "maneuver-ephemeris-live" || policy.SchemaCode != "MPE" {
		t.Fatalf("unexpected policy identity: %+v", policy)
	}
	if policy.PolicyVersion != 3 || policy.KeyEpoch != "epoch-7" {
		t.Fatalf("policy version/epoch = %d/%q, want 3/epoch-7", policy.PolicyVersion, policy.KeyEpoch)
	}
	if len(policy.Audiences) != 1 || policy.Audiences[0].SubjectID != "customer-alpha-peer" ||
		policy.Audiences[0].SubjectKeyID != "x25519:alpha:2026-06-25" {
		t.Fatalf("unexpected audiences: %+v", policy.Audiences)
	}
	if got := policy.AllowedOperations; len(got) != 2 || got[0] != "Subscribe" || got[1] != "Decrypt" {
		t.Fatalf("allowed operations = %#v, want Subscribe/Decrypt", got)
	}
	if len(policy.Rules) != 2 {
		t.Fatalf("rules length = %d, want 2", len(policy.Rules))
	}
	if policy.Rules[0].FieldPath != "object_id" || policy.Rules[0].Decision != "AllowPublic" {
		t.Fatalf("public rule = %+v", policy.Rules[0])
	}
	if policy.Rules[1].FieldPath != "position" || policy.Rules[1].Decision != "AllowEncrypted" ||
		policy.Rules[1].KeyID != "field-key:alpha:position:epoch-7" {
		t.Fatalf("encrypted rule = %+v", policy.Rules[1])
	}
	if policy.ProviderSignature == nil || len(policy.ProviderSignature) != 64 {
		t.Fatalf("provider signature length = %d, want 64", len(policy.ProviderSignature))
	}
}

func TestDecodeFieldStreamMessageSummary(t *testing.T) {
	message, err := DecodeFieldStreamMessageSummary(buildFieldStreamMessageFixture())
	if err != nil {
		t.Fatalf("DecodeFieldStreamMessageSummary failed: %v", err)
	}
	if message.MessageID != "fsm-mpe-alpha-000001" || message.PolicyID != "policy-mpe-alpha" {
		t.Fatalf("unexpected message identity: %+v", message)
	}
	if message.Sequence != 1 || message.SubjectID != "customer-alpha-peer" {
		t.Fatalf("message sequence/subject = %d/%q, want 1/customer-alpha-peer", message.Sequence, message.SubjectID)
	}
	if len(message.Fields) != 3 {
		t.Fatalf("fields length = %d, want 3", len(message.Fields))
	}
	if field := message.Fields[0]; field.FieldPath != "object_id" || field.State != "Public" ||
		field.Encoding != "TextUtf8" || string(field.Value) != "SAT-042" {
		t.Fatalf("public field = %+v", field)
	}
	if field := message.Fields[1]; field.FieldPath != "position" || field.State != "Encrypted" ||
		field.Encoding != "FlatBuffer" || field.CiphertextLength != 4 ||
		field.KeyID != "field-key:alpha:position:epoch-7" || field.Decision != "allow-encrypted" {
		t.Fatalf("encrypted field = %+v", field)
	}
	if field := message.Fields[2]; field.FieldPath != "maneuver_plan" || field.State != "Redacted" ||
		field.Decision != "redacted:not-granted" || len(field.Ciphertext) != 0 {
		t.Fatalf("redacted field = %+v", field)
	}
	if len(message.PayloadHash) != 32 || len(message.PreviousMessageHash) != 32 || len(message.ProviderSignature) != 64 {
		t.Fatalf("unexpected hash/signature lengths: payload=%d previous=%d signature=%d",
			len(message.PayloadHash), len(message.PreviousMessageHash), len(message.ProviderSignature))
	}
}

func TestFieldStreamSummaryRejectsWrongIdentifier(t *testing.T) {
	if _, err := DecodeFieldStreamPolicySummary(buildFieldStreamMessageFixture()); err == nil {
		t.Fatal("DecodeFieldStreamPolicySummary should reject an FSM buffer")
	}
	if _, err := DecodeFieldStreamMessageSummary(buildFieldStreamPolicyFixture()); err == nil {
		t.Fatal("DecodeFieldStreamMessageSummary should reject an FSP buffer")
	}
}

func buildFieldStreamPolicyFixture() []byte {
	builder := flatbuffers.NewBuilder(1024)
	audience := buildFieldStreamAudience(builder, "customer-alpha-peer", "bafy-alpha-epm", "x25519:alpha:2026-06-25")
	publicRule := buildFieldStreamRule(builder, "object_id", []uint32{1}, 0, []string{"releasable"}, nil, "")
	encryptedRule := buildFieldStreamRule(
		builder,
		"position",
		[]uint32{3},
		1,
		[]string{"restricted", "orbital-state"},
		[]string{"customer=alpha", "enclave=secret"},
		"field-key:alpha:position:epoch-7",
	)
	audiences := offsetVector(builder, []flatbuffers.UOffsetT{audience})
	rules := offsetVector(builder, []flatbuffers.UOffsetT{publicRule, encryptedRule})
	allowedOps := int8Vector(builder, []int8{0, 1})

	policyID := builder.CreateByteString([]byte("policy-mpe-alpha"))
	providerPeerID := builder.CreateByteString([]byte("provider-peer"))
	listingID := builder.CreateByteString([]byte("listing-maneuver-ephemeris"))
	streamID := builder.CreateByteString([]byte("maneuver-ephemeris-live"))
	schemaCode := builder.CreateByteString([]byte("MPE"))
	schemaHash := builder.CreateByteVector(filledBytes(32, 0x61))
	keyScope := builder.CreateByteString([]byte("stream:listing-maneuver-ephemeris:maneuver-ephemeris-live"))
	keyEpoch := builder.CreateByteString([]byte("epoch-7"))
	providerSignature := builder.CreateByteVector(filledBytes(64, 0xa1))

	fsp.FSPStart(builder)
	fsp.FSPAddPOLICY_ID(builder, policyID)
	fsp.FSPAddPOLICY_VERSION(builder, 3)
	fsp.FSPAddPROVIDER_PEER_ID(builder, providerPeerID)
	fsp.FSPAddLISTING_ID(builder, listingID)
	fsp.FSPAddSTREAM_ID(builder, streamID)
	fsp.FSPAddSCHEMA_CODE(builder, schemaCode)
	fsp.FSPAddSCHEMA_HASH(builder, schemaHash)
	fsp.FSPAddAUDIENCES(builder, audiences)
	fsp.FSPAddRULES(builder, rules)
	fsp.FSPAddALLOWED_OPERATIONS(builder, allowedOps)
	fsp.FSPAddKEY_SCOPE(builder, keyScope)
	fsp.FSPAddKEY_EPOCH(builder, keyEpoch)
	fsp.FSPAddVALID_FROM(builder, 1_800_000_000_000)
	fsp.FSPAddEXPIRES_AT(builder, 1_800_086_400_000)
	fsp.FSPAddPROVIDER_SIGNATURE(builder, providerSignature)
	root := fsp.FSPEnd(builder)
	fsp.FinishFSPBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildFieldStreamMessageFixture() []byte {
	builder := flatbuffers.NewBuilder(1024)
	publicField := buildFieldStreamValue(
		builder,
		"object_id",
		[]uint32{1},
		0,
		3,
		[]byte("SAT-042"),
		nil,
		nil,
		nil,
		"",
		nil,
		[]string{"releasable"},
		"allow-public",
	)
	encryptedField := buildFieldStreamValue(
		builder,
		"position",
		[]uint32{3},
		1,
		2,
		nil,
		[]byte{0xde, 0xad, 0xbe, 0xef},
		filledBytes(12, 0x21),
		filledBytes(16, 0x22),
		"field-key:alpha:position:epoch-7",
		filledBytes(32, 0x23),
		[]string{"restricted", "customer-alpha"},
		"allow-encrypted",
	)
	redactedField := buildFieldStreamValue(
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
	fields := offsetVector(builder, []flatbuffers.UOffsetT{publicField, encryptedField, redactedField})

	messageID := builder.CreateByteString([]byte("fsm-mpe-alpha-000001"))
	providerPeerID := builder.CreateByteString([]byte("provider-peer"))
	listingID := builder.CreateByteString([]byte("listing-maneuver-ephemeris"))
	streamID := builder.CreateByteString([]byte("maneuver-ephemeris-live"))
	schemaCode := builder.CreateByteString([]byte("MPE"))
	schemaHash := builder.CreateByteVector(filledBytes(32, 0x61))
	policyID := builder.CreateByteString([]byte("policy-mpe-alpha"))
	keyEpoch := builder.CreateByteString([]byte("epoch-7"))
	subjectID := builder.CreateByteString([]byte("customer-alpha-peer"))
	payloadHash := builder.CreateByteVector(filledBytes(32, 0x31))
	previousHash := builder.CreateByteVector(filledBytes(32, 0x30))
	providerSignature := builder.CreateByteVector(filledBytes(64, 0xa1))

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

func buildFieldStreamAudience(builder *flatbuffers.Builder, subjectID, epmCID, keyID string) flatbuffers.UOffsetT {
	subjectIDOffset := builder.CreateByteString([]byte(subjectID))
	epmCIDOffset := builder.CreateByteString([]byte(epmCID))
	keyIDOffset := builder.CreateByteString([]byte(keyID))
	fsp.FieldStreamAudienceStart(builder)
	fsp.FieldStreamAudienceAddAUDIENCE_TYPE(builder, 0)
	fsp.FieldStreamAudienceAddSUBJECT_ID(builder, subjectIDOffset)
	fsp.FieldStreamAudienceAddSUBJECT_EPM_CID(builder, epmCIDOffset)
	fsp.FieldStreamAudienceAddSUBJECT_KEY_ID(builder, keyIDOffset)
	return fsp.FieldStreamAudienceEnd(builder)
}

func buildFieldStreamRule(
	builder *flatbuffers.Builder,
	fieldPath string,
	fieldIDPath []uint32,
	decision int,
	tags []string,
	attrs []string,
	keyID string,
) flatbuffers.UOffsetT {
	fieldPathOffset := builder.CreateByteString([]byte(fieldPath))
	fieldIDPathOffset := uint32Vector(builder, fieldIDPath)
	tagsOffset := stringOffsetVector(builder, tags)
	attrsOffset := stringOffsetVector(builder, attrs)
	var keyIDOffset flatbuffers.UOffsetT
	if keyID != "" {
		keyIDOffset = builder.CreateByteString([]byte(keyID))
	}
	fsp.FieldStreamRuleStart(builder)
	fsp.FieldStreamRuleAddFIELD_PATH(builder, fieldPathOffset)
	fsp.FieldStreamRuleAddFIELD_ID_PATH(builder, fieldIDPathOffset)
	builder.PrependInt8Slot(2, int8(decision), 1)
	fsp.FieldStreamRuleAddTAGS(builder, tagsOffset)
	fsp.FieldStreamRuleAddREQUIRED_ATTRIBUTES(builder, attrsOffset)
	if keyIDOffset != 0 {
		fsp.FieldStreamRuleAddKEY_ID(builder, keyIDOffset)
	}
	return fsp.FieldStreamRuleEnd(builder)
}

func buildFieldStreamValue(
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
	fieldIDPathOffset := uint32Vector(builder, fieldIDPath)
	valueOffset := createByteVector(builder, value)
	ciphertextOffset := createByteVector(builder, ciphertext)
	nonceOffset := createByteVector(builder, nonce)
	tagOffset := createByteVector(builder, tag)
	var keyIDOffset flatbuffers.UOffsetT
	if keyID != "" {
		keyIDOffset = builder.CreateByteString([]byte(keyID))
	}
	aadHashOffset := createByteVector(builder, aadHash)
	releaseTagsOffset := stringOffsetVector(builder, releaseTags)
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

func uint32Vector(builder *flatbuffers.Builder, values []uint32) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	builder.StartVector(4, len(values), 4)
	for i := len(values) - 1; i >= 0; i-- {
		builder.PrependUint32(values[i])
	}
	return builder.EndVector(len(values))
}

func int8Vector(builder *flatbuffers.Builder, values []int8) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	builder.StartVector(1, len(values), 1)
	for i := len(values) - 1; i >= 0; i-- {
		builder.PrependInt8(values[i])
	}
	return builder.EndVector(len(values))
}

func stringOffsetVector(builder *flatbuffers.Builder, values []string) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, 0, len(values))
	for _, value := range values {
		offsets = append(offsets, builder.CreateByteString([]byte(value)))
	}
	return offsetVector(builder, offsets)
}

func offsetVector(builder *flatbuffers.Builder, offsets []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(offsets) == 0 {
		return 0
	}
	builder.StartVector(4, len(offsets), 4)
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func filledBytes(length int, value byte) []byte {
	bytes := make([]byte, length)
	for i := range bytes {
		bytes[i] = value
	}
	return bytes
}
