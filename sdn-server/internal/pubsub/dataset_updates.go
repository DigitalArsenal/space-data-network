package pubsub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var ErrNoPublisher = errors.New("publisher not set")

const (
	OMMSchema = "OMM.fbs"
	CATSchema = "CAT.fbs"
	SPWSchema = "SPW.fbs"
)

var celesTrakDatasetSchemas = []string{OMMSchema, CATSchema, SPWSchema}

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
