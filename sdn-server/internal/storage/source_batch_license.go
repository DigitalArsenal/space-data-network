package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SourceBatchLicense is the machine-readable licence a source batch carries.
//
// Licence is a property of the SOURCE BATCH — one (schema, provider, source,
// batch) tuple — not of an individual record: every record parsed out of one
// CC-BY-SA retrieval inherits the same terms. It is therefore stored once per
// batch rather than duplicated across the per-record provenance rows in
// sdn_record_source_tags, which also keeps the deterministic export index
// bytes (which embed SourceTags per record) unchanged for licensed batches.
//
// The DPM carries LICENSE / LICENSE_URL / CITATION per DPMSourceBatch. It has
// NO share-alike field, so ShareAlike is node-side policy state only: it is
// recorded here and rides in the wasm-authored provenance sidecar, and it is
// never written into a published manifest.
type SourceBatchLicense struct {
	SchemaName string `json:"schema_name"`
	ProviderID string `json:"provider_id"`
	SourceName string `json:"source_name"`
	BatchID    string `json:"batch_id"`
	License    string `json:"license,omitempty"`
	LicenseURL string `json:"license_url,omitempty"`
	Citation   string `json:"citation,omitempty"`
	ShareAlike bool   `json:"share_alike,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
}

// IsEmpty reports a licence record that carries no licence statement at all.
// Batches ingested without licence metadata publish exactly as before: no row
// is written and no DPM licence field is emitted.
func (l SourceBatchLicense) IsEmpty() bool {
	return strings.TrimSpace(l.License) == "" &&
		strings.TrimSpace(l.LicenseURL) == "" &&
		strings.TrimSpace(l.Citation) == "" &&
		!l.ShareAlike
}

func normalizeSourceBatchLicense(license SourceBatchLicense) SourceBatchLicense {
	license.SchemaName = strings.TrimSpace(license.SchemaName)
	license.ProviderID = strings.TrimSpace(license.ProviderID)
	license.SourceName = strings.TrimSpace(license.SourceName)
	license.BatchID = strings.TrimSpace(license.BatchID)
	license.License = strings.TrimSpace(license.License)
	license.LicenseURL = strings.TrimSpace(license.LicenseURL)
	license.Citation = strings.TrimSpace(license.Citation)
	return license
}

// sourceBatchLicenseFromTags lifts the licence a writer attached to its
// SourceTags into the batch-keyed licence record. ok is false when the writer
// supplied no licence metadata.
func sourceBatchLicenseFromTags(schemaName string, tags SourceTags) (SourceBatchLicense, bool) {
	license := normalizeSourceBatchLicense(SourceBatchLicense{
		SchemaName: schemaName,
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		License:    tags.License,
		LicenseURL: tags.LicenseURL,
		Citation:   tags.Citation,
		ShareAlike: tags.ShareAlike,
	})
	if license.IsEmpty() {
		return SourceBatchLicense{}, false
	}
	return license, true
}

func (s *FlatSQLStore) initSourceBatchLicenseTable() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_source_batch_license (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			batch_id TEXT NOT NULL DEFAULT '',
			license TEXT NOT NULL DEFAULT '',
			license_url TEXT NOT NULL DEFAULT '',
			citation TEXT NOT NULL DEFAULT '',
			share_alike INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (schema_name, provider_id, source_name, batch_id)
		)
	`); err != nil {
		return fmt.Errorf("failed to create source batch license table: %w", err)
	}
	return nil
}

// UpsertSourceBatchLicense records the licence governing one source batch.
// It is durable through the auxiliary metadata journal, like every other
// non-record table.
func (s *FlatSQLStore) UpsertSourceBatchLicense(license SourceBatchLicense) error {
	if err := s.requireWritable("upsert source batch license"); err != nil {
		return err
	}
	license = normalizeSourceBatchLicense(license)
	if license.SchemaName == "" {
		return errors.New("schema name is required")
	}
	if license.ProviderID == "" {
		return errors.New("provider id is required")
	}
	if license.SourceName == "" {
		return errors.New("source name is required")
	}
	if license.UpdatedAt <= 0 {
		license.UpdatedAt = time.Now().UTC().Unix()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applySourceBatchLicenseUpsert(license); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{
		Kind:               auxiliaryEventSourceBatchLicenseUpsert,
		SourceBatchLicense: &license,
	}); err != nil {
		return fmt.Errorf("append source batch license metadata: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) applySourceBatchLicenseUpsert(license SourceBatchLicense) error {
	license = normalizeSourceBatchLicense(license)
	shareAlike := 0
	if license.ShareAlike {
		shareAlike = 1
	}
	if _, err := s.auxWrite().Exec(`
		INSERT INTO sdn_source_batch_license (
			schema_name, provider_id, source_name, batch_id,
			license, license_url, citation, share_alike, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, provider_id, source_name, batch_id)
		DO UPDATE SET
			license = excluded.license,
			license_url = excluded.license_url,
			citation = excluded.citation,
			share_alike = excluded.share_alike,
			updated_at = excluded.updated_at
	`, license.SchemaName, license.ProviderID, license.SourceName, license.BatchID,
		license.License, license.LicenseURL, license.Citation, shareAlike, license.UpdatedAt); err != nil {
		return fmt.Errorf("upsert source batch license: %w", err)
	}
	return nil
}

// SourceBatchLicenseFor loads the licence recorded for one source batch.
func (s *FlatSQLStore) SourceBatchLicenseFor(schemaName, providerID, sourceName, batchID string) (SourceBatchLicense, bool, error) {
	lookup := normalizeSourceBatchLicense(SourceBatchLicense{
		SchemaName: schemaName,
		ProviderID: providerID,
		SourceName: sourceName,
		BatchID:    batchID,
	})
	if lookup.SchemaName == "" {
		return SourceBatchLicense{}, false, errors.New("schema name is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var shareAlike int
	license := lookup
	err := s.db.QueryRow(`
		SELECT license, license_url, citation, share_alike, updated_at
		FROM sdn_source_batch_license
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`, lookup.SchemaName, lookup.ProviderID, lookup.SourceName, lookup.BatchID).
		Scan(&license.License, &license.LicenseURL, &license.Citation, &shareAlike, &license.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SourceBatchLicense{}, false, nil
		}
		return SourceBatchLicense{}, false, fmt.Errorf("load source batch license: %w", err)
	}
	license.ShareAlike = shareAlike != 0
	return license, true, nil
}

// recordSourceBatchLicense persists the licence a writer attached to its
// SourceTags. It is a no-op for the (overwhelmingly common) batch that carries
// no licence metadata, so untagged ingest is byte-for-byte unchanged.
func (s *FlatSQLStore) recordSourceBatchLicense(schemaName string, tags SourceTags) error {
	license, ok := sourceBatchLicenseFromTags(schemaName, tags)
	if !ok {
		return nil
	}
	return s.UpsertSourceBatchLicense(license)
}

// attachSourceBatchLicenses fills the licence trio on an export's source-batch
// summaries so BuildSignedDatasetPublicationManifest can bind them into the
// DPM. Batches with no recorded licence are left untouched (empty), which is
// exactly the pre-licence publication.
//
// The export's DatasetExportSourceBatch.SourceSHA256 IS the batch id (the
// sha256 of the retrieved source payload) — see summarizeExportSourceBatches.
func (s *FlatSQLStore) attachSourceBatchLicenses(export *DatasetExport) error {
	if s == nil || export == nil || len(export.SourceBatches) == 0 {
		return nil
	}
	for i := range export.SourceBatches {
		batch := export.SourceBatches[i]
		license, found, err := s.SourceBatchLicenseFor(export.SchemaName, batch.ProviderID, batch.SourceName, batch.SourceSHA256)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		export.SourceBatches[i].License = license.License
		export.SourceBatches[i].LicenseURL = license.LicenseURL
		export.SourceBatches[i].Citation = license.Citation
		export.SourceBatches[i].ShareAlike = license.ShareAlike
	}
	return nil
}
