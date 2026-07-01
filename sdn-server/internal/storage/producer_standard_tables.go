package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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

// StoreRoutedByProducer stores a record into the (producer, standard) table for
// its producer (peerID) and schema — the (producer, standard) counterpart of
// Store. It appends the payload to the shared FlatSQL stream and updates the
// record index exactly like Store, but the metadata row lands in the producer's
// own table, keeping each publisher's records separated. Content-addressed
// records are immutable, so a repeat CID is a no-op.
//
// This adds the routed write path without changing the existing per-standard
// Store; readers migrate to the (producer, standard) tables in WS7.3.
func (s *FlatSQLStore) StoreRoutedByProducer(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableName, err := s.ensureProducerStandardTable(peerID, schemaName)
	if err != nil {
		return "", fmt.Errorf("ensure (producer, standard) table: %w", err)
	}

	cid := computeCID(data)
	var existing int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT 1 FROM %s WHERE cid = ?`, tableName), cid).Scan(&existing); err == nil {
		return cid, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("failed to check existing record: %w", err)
	}

	now := time.Now().Unix()
	streamPath, streamOffset, recordLength, err := s.appendFlatSQLStreamRecord(schemaName, data)
	if err != nil {
		return "", fmt.Errorf("failed to append FlatSQL stream record: %w", err)
	}
	if err := insertSchemaMetadata(s.db, tableName, cid, peerID, now, streamPath, streamOffset, recordLength, signature, now); err != nil {
		return "", fmt.Errorf("failed to store data: %w", err)
	}
	if err := s.upsertRecordIndex(schemaName, cid, now, data); err != nil {
		// Do not fail writes if index extraction fails for a record.
		log.Warnf("Failed to index %s record %s: %v", schemaName, cid[:16]+"...", err)
	}
	return cid, nil
}
