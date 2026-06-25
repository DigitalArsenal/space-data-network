package storefront

import (
	"fmt"

	fsm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSM"
	fsp "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSP"
	flatbuffers "github.com/google/flatbuffers/go"
)

// FieldStreamPolicySummary is the server-facing projection of an SDS FSP policy.
type FieldStreamPolicySummary struct {
	PolicyID          string                   `json:"policy_id"`
	PolicyVersion     uint32                   `json:"policy_version"`
	ProviderPeerID    string                   `json:"provider_peer_id"`
	ListingID         string                   `json:"listing_id"`
	StreamID          string                   `json:"stream_id"`
	SchemaCode        string                   `json:"schema_code"`
	SchemaHash        []byte                   `json:"schema_hash,omitempty"`
	Audiences         []FieldStreamAudience    `json:"audiences"`
	Rules             []FieldStreamRuleSummary `json:"rules"`
	AllowedOperations []string                 `json:"allowed_operations"`
	KeyScope          string                   `json:"key_scope,omitempty"`
	KeyEpoch          string                   `json:"key_epoch,omitempty"`
	ValidFrom         uint64                   `json:"valid_from,omitempty"`
	ExpiresAt         uint64                   `json:"expires_at,omitempty"`
	RevocationStatus  string                   `json:"revocation_status"`
	RevokedAt         uint64                   `json:"revoked_at,omitempty"`
	RevocationReason  string                   `json:"revocation_reason,omitempty"`
	ProviderSignature []byte                   `json:"provider_signature,omitempty"`
}

type FieldStreamAudience struct {
	AudienceType  string `json:"audience_type"`
	SubjectID     string `json:"subject_id,omitempty"`
	SubjectEPMCID string `json:"subject_epm_cid,omitempty"`
	SubjectKeyID  string `json:"subject_key_id,omitempty"`
}

type FieldStreamRuleSummary struct {
	FieldPath          string   `json:"field_path"`
	FieldIDPath        []uint32 `json:"field_id_path,omitempty"`
	Decision           string   `json:"decision"`
	Tags               []string `json:"tags,omitempty"`
	RequiredAttributes []string `json:"required_attributes,omitempty"`
	KeyID              string   `json:"key_id,omitempty"`
}

type FieldStreamMessageSummary struct {
	MessageID           string                    `json:"message_id"`
	ProviderPeerID      string                    `json:"provider_peer_id"`
	ListingID           string                    `json:"listing_id"`
	StreamID            string                    `json:"stream_id"`
	SchemaCode          string                    `json:"schema_code"`
	SchemaHash          []byte                    `json:"schema_hash,omitempty"`
	PolicyID            string                    `json:"policy_id"`
	PolicyVersion       uint32                    `json:"policy_version"`
	KeyEpoch            string                    `json:"key_epoch,omitempty"`
	Sequence            uint64                    `json:"sequence"`
	ProducedAt          uint64                    `json:"produced_at,omitempty"`
	ExpiresAt           uint64                    `json:"expires_at,omitempty"`
	SubjectID           string                    `json:"subject_id,omitempty"`
	Fields              []FieldStreamFieldSummary `json:"fields"`
	PayloadHash         []byte                    `json:"payload_hash,omitempty"`
	PreviousMessageHash []byte                    `json:"previous_message_hash,omitempty"`
	ProviderSignature   []byte                    `json:"provider_signature,omitempty"`
}

type FieldStreamFieldSummary struct {
	FieldPath        string   `json:"field_path"`
	FieldIDPath      []uint32 `json:"field_id_path,omitempty"`
	State            string   `json:"state"`
	Encoding         string   `json:"encoding"`
	Value            []byte   `json:"value,omitempty"`
	Ciphertext       []byte   `json:"ciphertext,omitempty"`
	Nonce            []byte   `json:"nonce,omitempty"`
	Tag              []byte   `json:"tag,omitempty"`
	KeyID            string   `json:"key_id,omitempty"`
	AADHash          []byte   `json:"aad_hash,omitempty"`
	ReleaseTags      []string `json:"release_tags,omitempty"`
	Decision         string   `json:"decision,omitempty"`
	CiphertextLength int      `json:"ciphertext_length,omitempty"`
}

func DecodeFieldStreamPolicySummary(bytes []byte) (*FieldStreamPolicySummary, error) {
	if len(bytes) == 0 {
		return nil, fmt.Errorf("field stream policy bytes are empty")
	}
	if !fsp.FSPBufferHasIdentifier(bytes) {
		return nil, fmt.Errorf("field stream policy identifier mismatch")
	}
	root := fsp.GetRootAsFSP(bytes, 0)
	if string(root.POLICY_ID()) == "" {
		return nil, fmt.Errorf("field stream policy id is required")
	}
	summary := &FieldStreamPolicySummary{
		PolicyID:          string(root.POLICY_ID()),
		PolicyVersion:     root.POLICY_VERSION(),
		ProviderPeerID:    string(root.PROVIDER_PEER_ID()),
		ListingID:         string(root.LISTING_ID()),
		StreamID:          string(root.STREAM_ID()),
		SchemaCode:        string(root.SCHEMA_CODE()),
		SchemaHash:        cloneBytes(root.SCHEMA_HASHBytes()),
		AllowedOperations: make([]string, 0, root.ALLOWED_OPERATIONSLength()),
		KeyScope:          string(root.KEY_SCOPE()),
		KeyEpoch:          string(root.KEY_EPOCH()),
		ValidFrom:         root.VALID_FROM(),
		ExpiresAt:         root.EXPIRES_AT(),
		RevocationStatus:  root.REVOCATION_STATUS().String(),
		RevokedAt:         root.REVOKED_AT(),
		RevocationReason:  string(root.REVOCATION_REASON()),
		ProviderSignature: cloneBytes(root.PROVIDER_SIGNATUREBytes()),
	}
	for i := 0; i < root.AUDIENCESLength(); i++ {
		var audience fsp.FieldStreamAudience
		if root.AUDIENCES(&audience, i) {
			summary.Audiences = append(summary.Audiences, FieldStreamAudience{
				AudienceType:  audience.AUDIENCE_TYPE().String(),
				SubjectID:     string(audience.SUBJECT_ID()),
				SubjectEPMCID: string(audience.SUBJECT_EPM_CID()),
				SubjectKeyID:  string(audience.SUBJECT_KEY_ID()),
			})
		}
	}
	for i := 0; i < root.RULESLength(); i++ {
		var rule fsp.FieldStreamRule
		if root.RULES(&rule, i) {
			summary.Rules = append(summary.Rules, summarizeFieldStreamRule(&rule))
		}
	}
	for i := 0; i < root.ALLOWED_OPERATIONSLength(); i++ {
		summary.AllowedOperations = append(summary.AllowedOperations, root.ALLOWED_OPERATIONS(i).String())
	}
	return summary, nil
}

func DecodeFieldStreamMessageSummary(bytes []byte) (*FieldStreamMessageSummary, error) {
	if len(bytes) == 0 {
		return nil, fmt.Errorf("field stream message bytes are empty")
	}
	if !fsm.FSMBufferHasIdentifier(bytes) {
		return nil, fmt.Errorf("field stream message identifier mismatch")
	}
	root := fsm.GetRootAsFSM(bytes, 0)
	if string(root.MESSAGE_ID()) == "" {
		return nil, fmt.Errorf("field stream message id is required")
	}
	summary := &FieldStreamMessageSummary{
		MessageID:           string(root.MESSAGE_ID()),
		ProviderPeerID:      string(root.PROVIDER_PEER_ID()),
		ListingID:           string(root.LISTING_ID()),
		StreamID:            string(root.STREAM_ID()),
		SchemaCode:          string(root.SCHEMA_CODE()),
		SchemaHash:          cloneBytes(root.SCHEMA_HASHBytes()),
		PolicyID:            string(root.POLICY_ID()),
		PolicyVersion:       root.POLICY_VERSION(),
		KeyEpoch:            string(root.KEY_EPOCH()),
		Sequence:            root.SEQUENCE(),
		ProducedAt:          root.PRODUCED_AT(),
		ExpiresAt:           root.EXPIRES_AT(),
		SubjectID:           string(root.SUBJECT_ID()),
		PayloadHash:         cloneBytes(root.PAYLOAD_HASHBytes()),
		PreviousMessageHash: cloneBytes(root.PREVIOUS_MESSAGE_HASHBytes()),
		ProviderSignature:   cloneBytes(root.PROVIDER_SIGNATUREBytes()),
	}
	for i := 0; i < root.FIELDSLength(); i++ {
		var field fsm.FieldStreamValue
		if root.FIELDS(&field, i) {
			summary.Fields = append(summary.Fields, summarizeFieldStreamField(&field))
		}
	}
	return summary, nil
}

func summarizeFieldStreamRule(rule *fsp.FieldStreamRule) FieldStreamRuleSummary {
	fieldIDPath := make([]uint32, 0, rule.FIELD_ID_PATHLength())
	for i := 0; i < rule.FIELD_ID_PATHLength(); i++ {
		fieldIDPath = append(fieldIDPath, rule.FIELD_ID_PATH(i))
	}
	return FieldStreamRuleSummary{
		FieldPath:          string(rule.FIELD_PATH()),
		FieldIDPath:        fieldIDPath,
		Decision:           rule.DECISION().String(),
		Tags:               stringVector(rule.TAGSLength(), rule.TAGS),
		RequiredAttributes: stringVector(rule.REQUIRED_ATTRIBUTESLength(), rule.REQUIRED_ATTRIBUTES),
		KeyID:              string(rule.KEY_ID()),
	}
}

func summarizeFieldStreamField(field *fsm.FieldStreamValue) FieldStreamFieldSummary {
	fieldIDPath := make([]uint32, 0, field.FIELD_ID_PATHLength())
	for i := 0; i < field.FIELD_ID_PATHLength(); i++ {
		fieldIDPath = append(fieldIDPath, field.FIELD_ID_PATH(i))
	}
	ciphertext := cloneBytes(field.CIPHERTEXTBytes())
	return FieldStreamFieldSummary{
		FieldPath:        string(field.FIELD_PATH()),
		FieldIDPath:      fieldIDPath,
		State:            field.STATE().String(),
		Encoding:         field.ENCODING().String(),
		Value:            cloneBytes(field.VALUEBytes()),
		Ciphertext:       ciphertext,
		Nonce:            cloneBytes(field.NONCEBytes()),
		Tag:              cloneBytes(field.TAGBytes()),
		KeyID:            string(field.KEY_ID()),
		AADHash:          cloneBytes(field.AAD_HASHBytes()),
		ReleaseTags:      stringVector(field.RELEASE_TAGSLength(), field.RELEASE_TAGS),
		Decision:         string(field.DECISION()),
		CiphertextLength: len(ciphertext),
	}
}

func stringVector(length int, getter func(int) []byte) []string {
	if length == 0 {
		return nil
	}
	values := make([]string, 0, length)
	for i := 0; i < length; i++ {
		values = append(values, string(getter(i)))
	}
	return values
}

func cloneBytes(bytes []byte) []byte {
	if len(bytes) == 0 {
		return nil
	}
	return append([]byte(nil), bytes...)
}

func createByteVector(builder *flatbuffers.Builder, bytes []byte) flatbuffers.UOffsetT {
	if len(bytes) == 0 {
		return 0
	}
	return builder.CreateByteVector(bytes)
}
