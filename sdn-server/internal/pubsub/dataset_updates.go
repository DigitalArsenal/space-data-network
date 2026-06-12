package pubsub

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var ErrNoPublisher = errors.New("publisher not set")

const (
	OMMSchema = "OMM.fbs"
	MPESchema = "MPE.fbs"
	CATSchema = "CAT.fbs"
	SPWSchema = "SPW.fbs"
)

var celesTrakDatasetSchemas = []string{OMMSchema, MPESchema, CATSchema, SPWSchema}

const (
	DatasetFeedHeadTopicPrefix = "/space-data-network/feed-heads/1.0.0/"
	DatasetFeedHeadMessageType = "sdn.dataset.feed_head.v1"
)

// SchemaPublisher publishes bytes to an SDN schema pub/sub topic.
type SchemaPublisher interface {
	Publish(schema string, data []byte) error
}

// DatasetUpdateAnnouncement describes where a signed dataset-update PNM should
// be announced.
type DatasetUpdateAnnouncement struct {
	PNM               []byte
	Schemas           []string
	CombinedCelesTrak bool
}

// DatasetFeedHeadAnnouncement is the small mutable feed-head message replicas
// subscribe to before fetching immutable FlatSQL shard CIDs.
type DatasetFeedHeadAnnouncement struct {
	MessageType  string    `json:"message_type"`
	Schema       string    `json:"schema"`
	ProviderID   string    `json:"provider_id,omitempty"`
	SourceName   string    `json:"source_name,omitempty"`
	BatchID      string    `json:"batch_id,omitempty"`
	QueryProfile string    `json:"query_profile"`
	Offset       int       `json:"offset,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	FeedSequence int64     `json:"feed_sequence"`
	PreviousHead string    `json:"previous_head,omitempty"`
	FeedHead     string    `json:"feed_head"`
	RecordCount  int       `json:"record_count,omitempty"`
	ByteCount    int64     `json:"byte_count,omitempty"`
	ShardCID     string    `json:"shard_cid,omitempty"`
	IndexCID     string    `json:"index_cid,omitempty"`
	ManifestCID  string    `json:"manifest_cid,omitempty"`
	PNMCID       string    `json:"pnm_cid,omitempty"`
	PublishedAt  time.Time `json:"published_at,omitempty"`

	// Signature is a hex Ed25519 signature over the canonical announcement
	// payload (all fields above, signature fields cleared), matching the
	// signing scheme used for dataset-publication PNMs.
	Signature        string `json:"signature,omitempty"`
	SignatureType    string `json:"signature_type,omitempty"`
	SigningPublicKey string `json:"signing_public_key,omitempty"`
}

const datasetFeedHeadSignatureDomain = "SDN-FEED-HEAD\x00"

// canonicalSignaturePayload returns the deterministic bytes covered by the
// announcement signature: a domain tag plus the JSON encoding of the
// announcement with its signature fields cleared (struct field order is
// fixed, so the encoding is stable across publishers and verifiers).
func (ann DatasetFeedHeadAnnouncement) canonicalSignaturePayload() ([]byte, error) {
	ann.Signature = ""
	ann.SignatureType = ""
	encoded, err := json.Marshal(ann)
	if err != nil {
		return nil, fmt.Errorf("marshal dataset feed head for signing: %w", err)
	}
	return append([]byte(datasetFeedHeadSignatureDomain), encoded...), nil
}

// SignDatasetFeedHead signs the announcement in place with the publisher's
// Ed25519 signing key (the same key that signs dataset-publication PNMs).
func SignDatasetFeedHead(ann *DatasetFeedHeadAnnouncement, signingKey ed25519.PrivateKey) error {
	if ann == nil {
		return errors.New("dataset feed head is required")
	}
	if len(signingKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 signing key")
	}
	ann.SignatureType = "Ed25519"
	ann.SigningPublicKey = hex.EncodeToString(signingKey.Public().(ed25519.PublicKey))
	payload, err := ann.canonicalSignaturePayload()
	if err != nil {
		return err
	}
	ann.Signature = hex.EncodeToString(ed25519.Sign(signingKey, payload))
	return nil
}

// VerifyDatasetFeedHead verifies the announcement signature. When
// providerPublicKey is non-nil it is the trust anchor (resolved from the
// provider's EPM, like PNM verification); otherwise the embedded
// signing_public_key is used, which proves payload integrity but not
// publisher identity. Returns an error for tampered or malformed signatures.
func VerifyDatasetFeedHead(ann DatasetFeedHeadAnnouncement, providerPublicKey ed25519.PublicKey) error {
	if strings.TrimSpace(ann.Signature) == "" {
		return errors.New("dataset feed head is unsigned")
	}
	if ann.SignatureType != "Ed25519" {
		return fmt.Errorf("unsupported dataset feed head signature type %q", ann.SignatureType)
	}
	publicKey := providerPublicKey
	if publicKey == nil {
		raw, err := hex.DecodeString(strings.TrimSpace(ann.SigningPublicKey))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return errors.New("invalid dataset feed head signing public key")
		}
		publicKey = ed25519.PublicKey(raw)
	}
	signature, err := hex.DecodeString(strings.TrimSpace(ann.Signature))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid dataset feed head signature encoding")
	}
	payload, err := ann.canonicalSignaturePayload()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("invalid dataset feed head signature")
	}
	return nil
}

// PublishDatasetUpdatePNM publishes one signed dataset-update PNM to the PNM
// topic and the affected dataset schema topics. Combined CelesTrak updates are
// announced on OMM, CAT, and SPW so consumers watching any source family can
// discover the same signed manifest CID.
func PublishDatasetUpdatePNM(ctx context.Context, publisher SchemaPublisher, ann DatasetUpdateAnnouncement) error {
	if publisher == nil {
		return ErrNoPublisher
	}
	if !hasSizePrefixedPNMIdentifier(ann.PNM) {
		return ErrInvalidPNM
	}
	if ctx == nil {
		ctx = context.Background()
	}

	schemas, err := datasetUpdateAnnouncementSchemas(ann)
	if err != nil {
		return err
	}

	var publishErrs []error
	for _, schema := range schemas {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := publisher.Publish(schema, ann.PNM); err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("%s: %w", schema, err))
		}
	}
	return errors.Join(publishErrs...)
}

func DatasetFeedHeadTopic(schema string) string {
	return DatasetFeedHeadTopicPrefix + normalizeDatasetUpdateSchema(schema)
}

// PublishSignedDatasetFeedHead signs the announcement with the publisher's
// Ed25519 key before publishing. Use this over PublishDatasetFeedHead
// whenever a signing key is available.
func PublishSignedDatasetFeedHead(ctx context.Context, publisher TopicPublisher, ann DatasetFeedHeadAnnouncement, signingKey ed25519.PrivateKey) error {
	// Normalize before signing so the signature covers exactly the bytes
	// that PublishDatasetFeedHead emits (normalization is idempotent).
	if err := normalizeOutgoingDatasetFeedHead(&ann); err != nil {
		return err
	}
	if err := SignDatasetFeedHead(&ann, signingKey); err != nil {
		return err
	}
	return PublishDatasetFeedHead(ctx, publisher, ann)
}

func normalizeOutgoingDatasetFeedHead(ann *DatasetFeedHeadAnnouncement) error {
	ann.Schema = normalizeDatasetUpdateSchema(ann.Schema)
	if err := sds.ValidateSchemaName(ann.Schema); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	ann.QueryProfile = strings.TrimSpace(ann.QueryProfile)
	if ann.QueryProfile == "" {
		return errors.New("query profile is required")
	}
	ann.FeedHead = strings.TrimSpace(ann.FeedHead)
	if ann.FeedHead == "" {
		return errors.New("feed head is required")
	}
	if ann.FeedSequence <= 0 {
		return errors.New("feed sequence must be positive")
	}
	ann.MessageType = DatasetFeedHeadMessageType
	return nil
}

func PublishDatasetFeedHead(ctx context.Context, publisher TopicPublisher, ann DatasetFeedHeadAnnouncement) error {
	if publisher == nil {
		return ErrNoPublisher
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := normalizeOutgoingDatasetFeedHead(&ann); err != nil {
		return err
	}
	payload, err := json.Marshal(ann)
	if err != nil {
		return fmt.Errorf("marshal dataset feed head: %w", err)
	}
	topic := DatasetFeedHeadTopic(ann.Schema)
	if err := publisher.PublishToTopic(ctx, topic, payload); err != nil {
		return fmt.Errorf("%s: %w", topic, err)
	}
	return nil
}

func ParseDatasetFeedHeadAnnouncement(payload []byte) (DatasetFeedHeadAnnouncement, error) {
	var ann DatasetFeedHeadAnnouncement
	if len(payload) == 0 {
		return ann, errors.New("dataset feed head payload is empty")
	}
	if err := json.Unmarshal(payload, &ann); err != nil {
		return ann, fmt.Errorf("decode dataset feed head: %w", err)
	}
	if err := validateDatasetFeedHeadAnnouncement(&ann); err != nil {
		return DatasetFeedHeadAnnouncement{}, err
	}
	return ann, nil
}

func validateDatasetFeedHeadAnnouncement(ann *DatasetFeedHeadAnnouncement) error {
	if ann == nil {
		return errors.New("dataset feed head is required")
	}
	ann.MessageType = strings.TrimSpace(ann.MessageType)
	if ann.MessageType != DatasetFeedHeadMessageType {
		return fmt.Errorf("unsupported dataset feed head message type %q", ann.MessageType)
	}
	ann.Schema = normalizeDatasetUpdateSchema(ann.Schema)
	if err := sds.ValidateSchemaName(ann.Schema); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	ann.QueryProfile = strings.TrimSpace(ann.QueryProfile)
	if ann.QueryProfile == "" {
		return errors.New("query profile is required")
	}
	ann.FeedHead = strings.TrimSpace(ann.FeedHead)
	if ann.FeedHead == "" {
		return errors.New("feed head is required")
	}
	if ann.FeedSequence <= 0 {
		return errors.New("feed sequence must be positive")
	}
	if ann.Offset < 0 {
		return errors.New("offset must be non-negative")
	}
	if ann.Limit < 0 {
		return errors.New("limit must be non-negative")
	}
	ann.ProviderID = strings.TrimSpace(ann.ProviderID)
	ann.SourceName = strings.TrimSpace(ann.SourceName)
	ann.BatchID = strings.TrimSpace(ann.BatchID)
	ann.PreviousHead = strings.TrimSpace(ann.PreviousHead)
	ann.ShardCID = strings.TrimSpace(ann.ShardCID)
	ann.IndexCID = strings.TrimSpace(ann.IndexCID)
	ann.ManifestCID = strings.TrimSpace(ann.ManifestCID)
	ann.PNMCID = strings.TrimSpace(ann.PNMCID)
	return nil
}

func hasSizePrefixedPNMIdentifier(data []byte) (ok bool) {
	if len(data) < 8 {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return PNM.SizePrefixedPNMBufferHasIdentifier(data)
}

func datasetUpdateAnnouncementSchemas(ann DatasetUpdateAnnouncement) ([]string, error) {
	seen := make(map[string]bool)
	schemas := make([]string, 0, 1+len(ann.Schemas)+len(celesTrakDatasetSchemas))
	add := func(schema string) error {
		normalized := normalizeDatasetUpdateSchema(schema)
		if err := sds.ValidateSchemaName(normalized); err != nil {
			return err
		}
		if !seen[normalized] {
			seen[normalized] = true
			schemas = append(schemas, normalized)
		}
		return nil
	}

	if err := add(pnmSchema); err != nil {
		return nil, err
	}
	for _, schema := range ann.Schemas {
		if err := add(schema); err != nil {
			return nil, err
		}
	}
	if ann.CombinedCelesTrak {
		for _, schema := range celesTrakDatasetSchemas {
			if err := add(schema); err != nil {
				return nil, err
			}
		}
	}
	return schemas, nil
}

func normalizeDatasetUpdateSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" || strings.HasSuffix(schema, ".fbs") {
		return schema
	}
	return schema + ".fbs"
}
