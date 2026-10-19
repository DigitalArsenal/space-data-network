package pubsub

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

var ErrNoPublisher = errors.New("publisher not set")

const (
	OMMSchema = "OMM.fbs"
	MPESchema = "MPE.fbs"
	CATSchema = "CAT.fbs"
	SPWSchema = "SPW.fbs"
)

// DatasetFeedHeadTopicPrefix names the feed-head topics; a topic is the
// prefix + the store-form schema name ("OMM.fbs").
const DatasetFeedHeadTopicPrefix = "/space-data-network/feed-heads/2.0.0/"

// SchemaPublisher publishes bytes to an SDN schema pub/sub topic.
type SchemaPublisher interface {
	Publish(schema string, data []byte) error
}

// TopicPublisher publishes bytes to an explicit SDN pub/sub topic.
type TopicPublisher interface {
	PublishToTopic(ctx context.Context, topic string, data []byte) error
}

// DatasetUpdateAnnouncement describes where a signed dataset-update PNM should
// be announced.
type DatasetUpdateAnnouncement struct {
	PNM     []byte
	Schemas []string
}

// DatasetFeedHeadAnnouncement is the small mutable feed-head message replicas
// subscribe to before fetching immutable FlatSQL shard CIDs: the SEGMENT row
// list-published returns for the publication, signed. On the wire it is one
// $QRP Page frame whose ROWS hold that one row (FeedHeadFrame).
type DatasetFeedHeadAnnouncement = syncwire.Segment

const (
	datasetFeedHeadSignatureDomain = "SDN-FEED-HEAD\x00"
	datasetFeedHeadSignatureType   = "Ed25519"
)

// FeedHeadFrame is THE feed-head message builder: one $QRP Page frame
// carrying ann as its one SEGMENT row. Signing and verifying both build
// through it, so the canonical bytes are the bytes on the wire with the
// SIGNATURE and SIGNATURE_TYPE cells Null.
func FeedHeadFrame(ann DatasetFeedHeadAnnouncement) []byte {
	return syncwire.SegmentPage(syncwire.QRPFields{SchemaName: ann.SchemaName}, []syncwire.Segment{ann})
}

// canonicalSignaturePayload returns the bytes the signature covers: the
// domain tag plus the canonical frame of ann (signature cells Null,
// SIGNING_PUBLIC_KEY present).
func canonicalSignaturePayload(ann DatasetFeedHeadAnnouncement) []byte {
	ann.Signature, ann.SignatureType = nil, ""
	return append([]byte(datasetFeedHeadSignatureDomain), FeedHeadFrame(ann)...)
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
	ann.SigningPublicKey = append([]byte(nil), signingKey.Public().(ed25519.PublicKey)...)
	ann.Signature = ed25519.Sign(signingKey, canonicalSignaturePayload(*ann))
	ann.SignatureType = datasetFeedHeadSignatureType
	return nil
}

// VerifyDatasetFeedHead verifies the announcement signature by rebuilding
// its canonical frame. When providerPublicKey is non-nil it is the trust
// anchor (resolved from the provider's EPM, like PNM verification);
// otherwise the embedded SIGNING_PUBLIC_KEY is used, which proves payload
// integrity but not publisher identity. Returns an error for tampered or
// malformed signatures.
func VerifyDatasetFeedHead(ann DatasetFeedHeadAnnouncement, providerPublicKey ed25519.PublicKey) error {
	if len(ann.Signature) == 0 {
		return errors.New("dataset feed head is unsigned")
	}
	if ann.SignatureType != datasetFeedHeadSignatureType {
		return fmt.Errorf("unsupported dataset feed head signature type %q", ann.SignatureType)
	}
	publicKey := providerPublicKey
	if publicKey == nil {
		if len(ann.SigningPublicKey) != ed25519.PublicKeySize {
			return errors.New("invalid dataset feed head signing public key")
		}
		publicKey = ed25519.PublicKey(ann.SigningPublicKey)
	}
	if len(ann.Signature) != ed25519.SignatureSize {
		return errors.New("invalid dataset feed head signature encoding")
	}
	if !ed25519.Verify(publicKey, canonicalSignaturePayload(ann), ann.Signature) {
		return errors.New("invalid dataset feed head signature")
	}
	return nil
}

// PublishDatasetUpdatePNM publishes one signed dataset-update PNM to the PNM
// topic and every explicitly declared dataset schema topic.
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
	// Normalize before signing so the signature covers exactly the row
	// PublishDatasetFeedHead emits (normalization is idempotent).
	if err := validateDatasetFeedHeadAnnouncement(&ann); err != nil {
		return err
	}
	if err := SignDatasetFeedHead(&ann, signingKey); err != nil {
		return err
	}
	return PublishDatasetFeedHead(ctx, publisher, ann)
}

// PublishDatasetFeedHead publishes one feed-head frame on the schema's
// feed-head topic.
func PublishDatasetFeedHead(ctx context.Context, publisher TopicPublisher, ann DatasetFeedHeadAnnouncement) error {
	if publisher == nil {
		return ErrNoPublisher
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateDatasetFeedHeadAnnouncement(&ann); err != nil {
		return err
	}
	topic := DatasetFeedHeadTopic(ann.SchemaName)
	if err := publisher.PublishToTopic(ctx, topic, FeedHeadFrame(ann)); err != nil {
		return fmt.Errorf("%s: %w", topic, err)
	}
	return nil
}

// ParseDatasetFeedHeadAnnouncement decodes one feed-head frame: a $QRP Page
// with exactly one SEGMENT row.
func ParseDatasetFeedHeadAnnouncement(payload []byte) (DatasetFeedHeadAnnouncement, error) {
	if len(payload) == 0 {
		return DatasetFeedHeadAnnouncement{}, errors.New("dataset feed head payload is empty")
	}
	q, err := syncwire.ParseQRP(payload)
	if err != nil {
		return DatasetFeedHeadAnnouncement{}, fmt.Errorf("decode dataset feed head: %w", err)
	}
	if int8(q.KIND()) != syncwire.QRPKindPage {
		return DatasetFeedHeadAnnouncement{}, errors.New("dataset feed head is not a $QRP Page")
	}
	segs, err := syncwire.Segments(q)
	if err != nil {
		return DatasetFeedHeadAnnouncement{}, fmt.Errorf("decode dataset feed head: %w", err)
	}
	if len(segs) != 1 {
		return DatasetFeedHeadAnnouncement{}, fmt.Errorf("dataset feed head carries %d rows, want 1", len(segs))
	}
	ann := segs[0]
	if err := validateDatasetFeedHeadAnnouncement(&ann); err != nil {
		return DatasetFeedHeadAnnouncement{}, err
	}
	return ann, nil
}

func validateDatasetFeedHeadAnnouncement(ann *DatasetFeedHeadAnnouncement) error {
	if ann == nil {
		return errors.New("dataset feed head is required")
	}
	ann.SchemaName = normalizeDatasetUpdateSchema(ann.SchemaName)
	if err := sds.ValidateSchemaName(ann.SchemaName); err != nil {
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
	schemas := make([]string, 0, 1+len(ann.Schemas))
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
	return schemas, nil
}

func normalizeDatasetUpdateSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" || strings.HasSuffix(schema, ".fbs") {
		return schema
	}
	return schema + ".fbs"
}
