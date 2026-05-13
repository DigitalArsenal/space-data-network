package pubsub

import (
	"context"
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

func PublishDatasetFeedHead(ctx context.Context, publisher TopicPublisher, ann DatasetFeedHeadAnnouncement) error {
	if publisher == nil {
		return ErrNoPublisher
	}
	if ctx == nil {
		ctx = context.Background()
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
	ann.MessageType = DatasetFeedHeadMessageType
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
