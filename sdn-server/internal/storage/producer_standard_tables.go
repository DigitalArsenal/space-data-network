package storage

import (
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// sanitizeProducerID maps a producer identity (libp2p peer ID or hex public key)
// to a SQL-identifier-safe token. Peer IDs (base58) and public keys (hex) are
// already alphanumeric; any other byte is replaced with '_' so the resulting
// table name is always a valid identifier and never an injection vector.
func sanitizeProducerID(producerID string) string {
	trimmed := strings.TrimSpace(producerID)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ProducerStandardTableName derives the physical table name for records from a
// given producer and standard (schema). Records are stored per (producer,
// standard) rather than per standard alone, so a node keeps each publisher's
// records in a separate table while cross-table SQL can still span producers.
//
// The standard component reuses sds.SchemaNameToTable (validated, e.g.
// "OMM.fbs" -> "OMM"); the producer component is the sanitized peer ID / public
// key. Format:
//
//	sds_p_<producer>__<standard>
func ProducerStandardTableName(producerID, schemaName string) (string, error) {
	standard, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return "", err
	}
	producer := sanitizeProducerID(producerID)
	if producer == "" {
		return "", fmt.Errorf("producer id is required for (producer, standard) table routing")
	}
	return fmt.Sprintf("sds_p_%s__%s", producer, standard), nil
}

// ensureProducerStandardTable computes the (producer, standard) table name and
// creates the table on demand (idempotently) using the canonical schema-record
// layout. A new producer therefore materializes its own tables the first time
// it publishes, with no up-front schema registration.
func (s *FlatSQLStore) ensureProducerStandardTable(producerID, schemaName string) (string, error) {
	tableName, err := ProducerStandardTableName(producerID, schemaName)
	if err != nil {
		return "", err
	}
	exists, err := s.tableExists(tableName)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := s.createSchemaMetadataTable(tableName); err != nil {
			return "", fmt.Errorf("create (producer, standard) table %s: %w", tableName, err)
		}
	}
	return tableName, nil
}
