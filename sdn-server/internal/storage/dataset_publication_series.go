package storage

// dataset_publication_series.go — what a producer keeps of its own
// publications (graph: sdn-publication-hygiene-20260928).
//
// A SERIES is one complete publication of a lane (schema, provider, source):
// the shard, index and DPM of every window one PublishDatasetUpdate call
// produced, plus the shard-group CAR bundles built over them. Before this file
// the producer kept every series it ever made. Rows were upserted per window,
// so a window whose content changed simply lost its old row, but nothing ever
// released the old window's pins; a batch republished with drifting byte-cut
// windows kept every old row too. host-02's kubo held 45 GB against a 10 GB
// StorageMax — IQC alone had 196 advertised windows of one batch.
//
// Retention keeps the newest N series of each lane (publishing.retention,
// default 2 = the current set and the one before it, which consumers still
// mid-sync may be reading). An older series is released when it is
// SUPERSEDED: a newer series of the same batch scope re-described it (the
// IQC case: one batch republished fifteen times), the store no longer holds
// its batch (a "current" snapshot lane dropped it), or the lane declares its
// batches snapshots (retention.lanes[].snapshot_batches). A series whose
// batch the store still holds is otherwise kept whatever its age: in a lane
// whose batches are parts of one dataset (the cellular chunk lane) every
// part stays published. Released means:
//
//   - kubo pins: every VERIFIED pin-ledger entry of the lane (shard, index,
//     manifest, shard-group CAR) that no kept series or kept publication row
//     references is unpinned and its ledger entry marked "retired". A CID any
//     other ledger entry still needs (another lane, the archive plane, a peer's
//     pin) is never unpinned; only this lane's entry is retired. kubo's own GC
//     (--enable-gc against Datastore.StorageMax) reclaims the blocks.
//   - publication rows: a row no kept series names stops being advertised
//     (deleted, aux-journaled), so a peer is never pointed at content the
//     provider released.
//   - files: the retired rows' shard/index files and the retired manifests'
//     .dpm files are removed. Shard-group CAR bundles live in kubo only: the
//     staging .car file is removed as soon as kubo has pinned it, and a
//     retired bundle leaves as a kubo unpin.
//
// This is the PRODUCER side. The subscription retention rule (owner
// 2026-09-04: ReplaceCurrent by default, ArchiveAll on request) governs what a
// subscriber keeps of what it receives; keep_series: 0 is the producer's
// ArchiveAll and retires nothing. Consumer-side pins (another provider's
// publications) and archive-plane pins are never touched here.
//
// The series ledger is derived bookkeeping in the control database. A store
// that has no series for a lane yet (every store written before this change)
// ADOPTS its existing rows as one series per batch when the lane's first new
// series is recorded, so the first retention pass after an upgrade releases
// the history accumulated before it.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Pin-ledger roles a dataset publication writes.
const (
	PinLedgerRoleShard         = "shard"
	PinLedgerRoleIndex         = "index"
	PinLedgerRoleManifest      = "manifest"
	PinLedgerRoleShardGroupCAR = "shard-group-car"
	// PinLedgerStateRetired marks a publication pin retention released.
	PinLedgerStateRetired = "retired"
)

// publicationRetentionRoles are the roles retention may release. PNM entries
// name store records, not kubo pins, and are left alone.
var publicationRetentionRoles = map[string]bool{
	PinLedgerRoleShard:         true,
	PinLedgerRoleIndex:         true,
	PinLedgerRoleManifest:      true,
	PinLedgerRoleShardGroupCAR: true,
}

// DatasetPublicationLane is the unit retention counts series in.
type DatasetPublicationLane struct {
	SchemaName string
	ProviderID string
	SourceName string
}

func (l DatasetPublicationLane) normalized() DatasetPublicationLane {
	return DatasetPublicationLane{
		SchemaName: strings.TrimSpace(l.SchemaName),
		ProviderID: strings.TrimSpace(l.ProviderID),
		SourceName: strings.TrimSpace(l.SourceName),
	}
}

// DatasetPublicationSeriesMember is one CID a series pinned.
type DatasetPublicationSeriesMember struct {
	CID  string
	Role string
}

// DatasetPublicationSeries is one recorded publication of a lane.
type DatasetPublicationSeries struct {
	SchemaName  string
	ProviderID  string
	SourceName  string
	SeriesID    string
	BatchID     string
	RecordCount int
	ManifestCID string
	// Fingerprint is the set the series exported (DatasetPublicationSetFingerprint
	// taken before the export); auto-publish compares against it.
	Fingerprint string
	PublishedAt time.Time
	Members     []DatasetPublicationSeriesMember
}

func (s DatasetPublicationSeries) lane() DatasetPublicationLane {
	return DatasetPublicationLane{SchemaName: s.SchemaName, ProviderID: s.ProviderID, SourceName: s.SourceName}.normalized()
}

func (s *FlatSQLStore) initDatasetPublicationSeriesTables() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_dataset_publication_series (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			series_id TEXT NOT NULL,
			batch_id TEXT NOT NULL DEFAULT '',
			record_count INTEGER NOT NULL DEFAULT 0,
			manifest_cid TEXT NOT NULL DEFAULT '',
			fingerprint TEXT NOT NULL DEFAULT '',
			-- Unix NANOSECONDS: two series of one lane can start within
			-- the same second, and their order is what retention keeps.
			published_at INTEGER NOT NULL,
			PRIMARY KEY (schema_name, provider_id, source_name, series_id)
		)
	`); err != nil {
		return fmt.Errorf("failed to create dataset publication series table: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_dataset_publication_series_cids (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			series_id TEXT NOT NULL,
			cid TEXT NOT NULL,
			role TEXT NOT NULL,
			PRIMARY KEY (schema_name, provider_id, source_name, series_id, cid, role)
		)
	`); err != nil {
		return fmt.Errorf("failed to create dataset publication series members table: %w", err)
	}
	return nil
}

// DatasetPublicationSeriesID is a deterministic id over a series' windows and
// their CIDs: republishing an unchanged set yields the same id.
func DatasetPublicationSeriesID(lane DatasetPublicationLane, batchID string, parts []DatasetShardPublication) string {
	lane = lane.normalized()
	ordered := append([]DatasetShardPublication(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Offset != ordered[j].Offset {
			return ordered[i].Offset < ordered[j].Offset
		}
		return ordered[i].Limit < ordered[j].Limit
	})
	hash := sha256.New()
	fmt.Fprintf(hash, "sdn-dataset-publication-series-v1\x00%s\x00%s\x00%s\x00%s\n", lane.SchemaName, lane.ProviderID, lane.SourceName, strings.TrimSpace(batchID))
	for _, part := range ordered {
		fmt.Fprintf(hash, "%d\x00%d\x00%s\x00%s\x00%s\n", part.Offset, part.Limit, part.ShardCID, part.IndexCID, part.ManifestCID)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// DatasetPublicationSetFingerprint hashes the sorted CIDs a publication of
// (schema, provider, source[, batch]) would export. Equal fingerprints mean
// the same record set. Returns the fingerprint and the record count.
func (s *FlatSQLStore) DatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error) {
	schemaName = strings.TrimSpace(schemaName)
	providerID = strings.TrimSpace(providerID)
	sourceName = strings.TrimSpace(sourceName)
	batchID = strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" {
		return "", 0, errors.New("schema, provider and source are required for a publication fingerprint")
	}
	if s.ps != nil {
		return s.f2PublicationSetFingerprint(schemaName, providerID, sourceName, batchID)
	}
	query := `SELECT DISTINCT cid FROM sdn_record_source_tags WHERE schema_name = ? AND provider_id = ? AND source_name = ?`
	args := []any{schemaName, providerID, sourceName}
	if batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	query += ` ORDER BY cid`

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	defer rows.Close()
	hash := sha256.New()
	fmt.Fprintf(hash, "sdn-dataset-publication-set-v1\x00%s\x00%s\x00%s\x00%s\n", schemaName, providerID, sourceName, batchID)
	count := 0
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return "", 0, err
		}
		hash.Write([]byte(cid))
		hash.Write([]byte{'\n'})
		count++
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), count, nil
}

// LatestDatasetPublicationSeries returns the newest recorded series of a lane
// for one batch scope ("" = the source scope / full catalog).
func (s *FlatSQLStore) LatestDatasetPublicationSeries(lane DatasetPublicationLane, batchID string) (DatasetPublicationSeries, bool, error) {
	lane = lane.normalized()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var series DatasetPublicationSeries
	var publishedAt int64
	err := s.db.QueryRow(`
		SELECT schema_name, provider_id, source_name, series_id, batch_id, record_count, manifest_cid, fingerprint, published_at
		FROM sdn_dataset_publication_series
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
		ORDER BY published_at DESC, rowid DESC
		LIMIT 1`, lane.SchemaName, lane.ProviderID, lane.SourceName, strings.TrimSpace(batchID)).Scan(
		&series.SchemaName, &series.ProviderID, &series.SourceName, &series.SeriesID, &series.BatchID,
		&series.RecordCount, &series.ManifestCID, &series.Fingerprint, &publishedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DatasetPublicationSeries{}, false, nil
		}
		return DatasetPublicationSeries{}, false, fmt.Errorf("latest %s publication series: %w", lane.SchemaName, err)
	}
	series.PublishedAt = time.Unix(0, publishedAt).UTC()
	return series, true, nil
}

// ListDatasetPublicationSeries lists a lane's series newest first, with members.
func (s *FlatSQLStore) ListDatasetPublicationSeries(lane DatasetPublicationLane) ([]DatasetPublicationSeries, error) {
	lane = lane.normalized()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listDatasetPublicationSeriesLocked(lane)
}

func (s *FlatSQLStore) listDatasetPublicationSeriesLocked(lane DatasetPublicationLane) ([]DatasetPublicationSeries, error) {
	rows, err := s.db.Query(`
		SELECT series_id, batch_id, record_count, manifest_cid, fingerprint, published_at
		FROM sdn_dataset_publication_series
		WHERE schema_name = ? AND provider_id = ? AND source_name = ?
		ORDER BY published_at DESC, rowid DESC`, lane.SchemaName, lane.ProviderID, lane.SourceName)
	if err != nil {
		return nil, fmt.Errorf("list %s publication series: %w", lane.SchemaName, err)
	}
	var out []DatasetPublicationSeries
	for rows.Next() {
		series := DatasetPublicationSeries{SchemaName: lane.SchemaName, ProviderID: lane.ProviderID, SourceName: lane.SourceName}
		var publishedAt int64
		if err := rows.Scan(&series.SeriesID, &series.BatchID, &series.RecordCount, &series.ManifestCID, &series.Fingerprint, &publishedAt); err != nil {
			rows.Close()
			return nil, err
		}
		series.PublishedAt = time.Unix(0, publishedAt).UTC()
		out = append(out, series)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		members, err := s.db.Query(`
			SELECT cid, role FROM sdn_dataset_publication_series_cids
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND series_id = ?
			ORDER BY role, cid`, lane.SchemaName, lane.ProviderID, lane.SourceName, out[i].SeriesID)
		if err != nil {
			return nil, fmt.Errorf("list %s publication series members: %w", lane.SchemaName, err)
		}
		for members.Next() {
			var member DatasetPublicationSeriesMember
			if err := members.Scan(&member.CID, &member.Role); err != nil {
				members.Close()
				return nil, err
			}
			out[i].Members = append(out[i].Members, member)
		}
		err = members.Err()
		members.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RecordDatasetPublicationSeries records one completed series. Publication
// rows of the lane that no recorded series names yet (every row written before
// series existed, or by a path that records none) are first adopted as one
// legacy series per batch, so retention always knows what each advertised row
// belongs to. The windows this series published are never adopted.
func (s *FlatSQLStore) RecordDatasetPublicationSeries(series DatasetPublicationSeries, providerPeerID string) error {
	if err := s.requireWritable("record dataset publication series"); err != nil {
		return err
	}
	lane := series.lane()
	series.SchemaName, series.ProviderID, series.SourceName = lane.SchemaName, lane.ProviderID, lane.SourceName
	series.BatchID = strings.TrimSpace(series.BatchID)
	if lane.SchemaName == "" || strings.TrimSpace(series.SeriesID) == "" {
		return errors.New("series schema and id are required")
	}
	if series.PublishedAt.IsZero() {
		series.PublishedAt = time.Now().UTC()
	}

	existing, err := s.ListDatasetPublicationSeries(lane)
	if err != nil {
		return err
	}
	adopt, err := s.unrecordedDatasetPublicationSeries(lane, providerPeerID, existing, series)
	if err != nil {
		return err
	}
	return s.writeDatasetPublicationSeries(lane, append(adopt, series))
}

// AdoptDatasetPublicationSeries records the lane's unrecorded publication rows
// as legacy series (one per batch) without publishing anything, so a
// retention pass can run on a lane that has not published since series
// existed. Returns how many legacy series it wrote.
func (s *FlatSQLStore) AdoptDatasetPublicationSeries(lane DatasetPublicationLane, providerPeerID string) (int, error) {
	if err := s.requireWritable("adopt dataset publication series"); err != nil {
		return 0, err
	}
	lane = lane.normalized()
	if lane.SchemaName == "" {
		return 0, errors.New("schema is required")
	}
	existing, err := s.ListDatasetPublicationSeries(lane)
	if err != nil {
		return 0, err
	}
	adopt, err := s.unrecordedDatasetPublicationSeries(lane, providerPeerID, existing, DatasetPublicationSeries{})
	if err != nil || len(adopt) == 0 {
		return 0, err
	}
	return len(adopt), s.writeDatasetPublicationSeries(lane, adopt)
}

func (s *FlatSQLStore) writeDatasetPublicationSeries(lane DatasetPublicationLane, all []DatasetPublicationSeries) error {
	defer s.lockWrite("RecordDatasetPublicationSeries")()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin record publication series: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, each := range all {
		if _, err := tx.Exec(`
			INSERT INTO sdn_dataset_publication_series (
				schema_name, provider_id, source_name, series_id, batch_id, record_count, manifest_cid, fingerprint, published_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(schema_name, provider_id, source_name, series_id) DO UPDATE SET
				batch_id = excluded.batch_id,
				record_count = excluded.record_count,
				manifest_cid = excluded.manifest_cid,
				fingerprint = excluded.fingerprint,
				published_at = excluded.published_at`,
			lane.SchemaName, lane.ProviderID, lane.SourceName, each.SeriesID, each.BatchID, each.RecordCount,
			strings.TrimSpace(each.ManifestCID), strings.TrimSpace(each.Fingerprint), each.PublishedAt.UnixNano()); err != nil {
			return fmt.Errorf("record publication series %s: %w", each.SeriesID, err)
		}
		for _, member := range each.Members {
			cid := strings.TrimSpace(member.CID)
			if cid == "" {
				continue
			}
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO sdn_dataset_publication_series_cids (
					schema_name, provider_id, source_name, series_id, cid, role
				) VALUES (?, ?, ?, ?, ?, ?)`,
				lane.SchemaName, lane.ProviderID, lane.SourceName, each.SeriesID, cid, strings.TrimSpace(member.Role)); err != nil {
				return fmt.Errorf("record publication series member %s: %w", cid, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publication series: %w", err)
	}
	committed = true
	return nil
}

// legacySeriesID names the series that adopts a batch's unrecorded rows.
func legacySeriesID(batchID string) string {
	return "legacy:" + strings.TrimSpace(batchID)
}

// unrecordedDatasetPublicationSeries groups the lane's rows that no recorded
// series (nor the series being recorded) names by batch, merging into an
// existing legacy series of that batch.
func (s *FlatSQLStore) unrecordedDatasetPublicationSeries(lane DatasetPublicationLane, providerPeerID string, existing []DatasetPublicationSeries, current DatasetPublicationSeries) ([]DatasetPublicationSeries, error) {
	rows, err := s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   lane.SchemaName,
		ProviderID:   lane.ProviderID,
		SourceName:   lane.SourceName,
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]bool)
	legacyAt := make(map[string]time.Time)
	for _, each := range existing {
		for _, member := range each.Members {
			recorded[member.CID] = true
		}
		if strings.HasPrefix(each.SeriesID, "legacy:") {
			legacyAt[each.SeriesID] = each.PublishedAt
		}
	}
	for _, member := range current.Members {
		recorded[strings.TrimSpace(member.CID)] = true
	}
	byBatch := map[string]*DatasetPublicationSeries{}
	var order []string
	for _, row := range rows {
		if recorded[row.ShardCID] || row.ProviderID != lane.ProviderID || row.SourceName != lane.SourceName {
			continue
		}
		legacy := byBatch[row.BatchID]
		if legacy == nil {
			legacy = &DatasetPublicationSeries{
				SchemaName: lane.SchemaName, ProviderID: lane.ProviderID, SourceName: lane.SourceName,
				SeriesID: legacySeriesID(row.BatchID), BatchID: row.BatchID, ManifestCID: row.ManifestCID,
				PublishedAt: row.PublishedAt,
			}
			if at, ok := legacyAt[legacy.SeriesID]; ok && at.After(legacy.PublishedAt) {
				legacy.PublishedAt = at
			}
			byBatch[row.BatchID] = legacy
			order = append(order, row.BatchID)
		}
		legacy.RecordCount += row.RecordCount
		if row.PublishedAt.After(legacy.PublishedAt) {
			legacy.PublishedAt = row.PublishedAt
		}
		legacy.Members = append(legacy.Members,
			DatasetPublicationSeriesMember{CID: row.ShardCID, Role: PinLedgerRoleShard},
			DatasetPublicationSeriesMember{CID: row.IndexCID, Role: PinLedgerRoleIndex},
			DatasetPublicationSeriesMember{CID: row.ManifestCID, Role: PinLedgerRoleManifest})
	}
	out := make([]DatasetPublicationSeries, 0, len(order))
	for _, batchID := range order {
		legacy := byBatch[batchID]
		cars, err := s.ListPinLedgerEntries(PinLedgerQuery{
			SchemaName:        lane.SchemaName,
			ProviderPeerID:    strings.TrimSpace(providerPeerID),
			ProviderID:        lane.ProviderID,
			SourceName:        lane.SourceName,
			BatchID:           batchID,
			QueryProfile:      DatasetPublicationQueryProfile,
			Role:              PinLedgerRoleShardGroupCAR,
			VerificationState: "verified",
		})
		if err != nil {
			return nil, err
		}
		for _, car := range cars {
			if !recorded[car.CID] {
				legacy.Members = append(legacy.Members, DatasetPublicationSeriesMember{CID: car.CID, Role: PinLedgerRoleShardGroupCAR})
			}
		}
		out = append(out, *legacy)
	}
	return out, nil
}

// laneBatchHoldsRecords reports whether the store still holds any record of
// the lane under batchID.
func (s *FlatSQLStore) laneBatchHoldsRecords(lane DatasetPublicationLane, batchID string) (bool, error) {
	if s.ps != nil {
		batches, err := s.f2DistinctBatches(lane.SchemaName, lane.ProviderID, lane.SourceName)
		if err != nil {
			return false, fmt.Errorf("probe %s batch %s: %w", lane.SchemaName, batchID, err)
		}
		for _, b := range batches {
			if b == strings.TrimSpace(batchID) {
				return true, nil
			}
		}
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var one int
	err := s.db.QueryRow(`
		SELECT 1 FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
		LIMIT 1`, lane.SchemaName, lane.ProviderID, lane.SourceName, strings.TrimSpace(batchID)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe %s batch %s: %w", lane.SchemaName, batchID, err)
	}
	return true, nil
}

// DatasetPublicationRetentionPolicy is the retention rule of one lane.
type DatasetPublicationRetentionPolicy struct {
	// KeepSeries is how many of the newest series stay regardless. <= 0
	// retains everything.
	KeepSeries int
	// SnapshotBatches declares every batch of the lane a complete snapshot of
	// it, so a series beyond the newest KeepSeries is superseded even while
	// the store still holds its batch (OMM elset history, a daily SatNOGS
	// pull). Without it such a series is retired only when a newer series of
	// the SAME scope replaced it or the store no longer holds its batch: a
	// lane whose batches are PARTS of one dataset (the cellular chunk lane)
	// must never lose a part.
	SnapshotBatches bool
}

// DatasetPublicationRetentionPlan is what one retention pass would release.
type DatasetPublicationRetentionPlan struct {
	Lane          DatasetPublicationLane
	Keep          int
	KeptSeries    []string
	RetiredSeries []string
	// Unpin holds one ledger entry per CID to unpin from kubo.
	Unpin []PinLedgerEntry
	// Shared holds CIDs another ledger entry still needs: this lane's entry is
	// retired, the kubo pin stays.
	Shared []PinLedgerEntry
	// RetireRows are publication rows no kept series names.
	RetireRows []DatasetShardPublication
}

// PlanDatasetPublicationRetention computes a retention pass for one lane of
// this node's own publications. A lane with no recorded series plans nothing:
// nothing is known to be current, and retention never releases blind.
//
// Series are walked newest first. The newest policy.KeepSeries stay. An older
// series is RETIRED when it is superseded: a newer series of the same batch
// scope re-described it, the store no longer holds its batch, or the lane
// declares snapshot batches. Otherwise it stays (see
// DatasetPublicationRetentionPolicy). Every pin-ledger entry of the lane that
// no remaining series or advertised row names — windows a later publication
// overwrote or pruned — is released too.
func (s *FlatSQLStore) PlanDatasetPublicationRetention(lane DatasetPublicationLane, providerPeerID string, policy DatasetPublicationRetentionPolicy) (DatasetPublicationRetentionPlan, error) {
	lane = lane.normalized()
	providerPeerID = strings.TrimSpace(providerPeerID)
	series, err := s.ListDatasetPublicationSeries(lane)
	if err != nil {
		return DatasetPublicationRetentionPlan{Lane: lane, Keep: policy.KeepSeries}, err
	}
	return s.planDatasetPublicationRetention(lane, providerPeerID, policy, series)
}

func (s *FlatSQLStore) planDatasetPublicationRetention(lane DatasetPublicationLane, providerPeerID string, policy DatasetPublicationRetentionPolicy, series []DatasetPublicationSeries) (DatasetPublicationRetentionPlan, error) {
	plan := DatasetPublicationRetentionPlan{Lane: lane, Keep: policy.KeepSeries}
	if policy.KeepSeries <= 0 || lane.SchemaName == "" || len(series) == 0 {
		return plan, nil
	}
	var err error
	protected := map[string]bool{}
	protect := func(each DatasetPublicationSeries) {
		for _, member := range each.Members {
			protected[member.CID] = true
		}
	}
	newerScope := map[string]bool{}
	for i, each := range series {
		superseded := newerScope[each.BatchID]
		newerScope[each.BatchID] = true
		if i < policy.KeepSeries {
			plan.KeptSeries = append(plan.KeptSeries, each.SeriesID)
			protect(each)
			continue
		}
		if !superseded && !policy.SnapshotBatches {
			held := true
			if each.BatchID != "" {
				if held, err = s.laneBatchHoldsRecords(lane, each.BatchID); err != nil {
					return plan, err
				}
			}
			if held {
				plan.KeptSeries = append(plan.KeptSeries, each.SeriesID)
				protect(each)
				continue
			}
		}
		plan.RetiredSeries = append(plan.RetiredSeries, each.SeriesID)
	}
	// Anything written after the newest series began is a publication still
	// in flight on another path; this pass never retires it.
	inFlightAfter := series[0].PublishedAt

	rows, err := s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   lane.SchemaName,
		ProviderID:   lane.ProviderID,
		SourceName:   lane.SourceName,
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		return plan, err
	}
	for _, row := range rows {
		if row.ProviderID != lane.ProviderID || row.SourceName != lane.SourceName {
			continue
		}
		if protected[row.ShardCID] || row.PublishedAt.After(inFlightAfter) {
			protected[row.ShardCID] = true
			protected[row.IndexCID] = true
			protected[row.ManifestCID] = true
			continue
		}
		plan.RetireRows = append(plan.RetireRows, row)
	}

	entries, err := s.ListPinLedgerEntries(PinLedgerQuery{
		SchemaName:        lane.SchemaName,
		ProviderPeerID:    providerPeerID,
		ProviderID:        lane.ProviderID,
		SourceName:        lane.SourceName,
		QueryProfile:      DatasetPublicationQueryProfile,
		VerificationState: "verified",
	})
	if err != nil {
		return plan, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !publicationRetentionRoles[entry.Role] || entry.CID == "" || protected[entry.CID] || seen[entry.CID] {
			continue
		}
		if entry.VerifiedAt.After(inFlightAfter) {
			continue
		}
		seen[entry.CID] = true
		shared, err := s.pinLedgerCIDNeededOutsideLane(entry.CID, lane, providerPeerID)
		if err != nil {
			return plan, err
		}
		if shared {
			plan.Shared = append(plan.Shared, entry)
			continue
		}
		plan.Unpin = append(plan.Unpin, entry)
	}
	return plan, nil
}

// PlanDatasetPublicationRetentionWithAdoption plans as if the lane's
// unrecorded rows had been adopted as legacy series, writing nothing — the
// dry run of a pass on a lane that has not published since series existed.
func (s *FlatSQLStore) PlanDatasetPublicationRetentionWithAdoption(lane DatasetPublicationLane, providerPeerID string, policy DatasetPublicationRetentionPolicy) (DatasetPublicationRetentionPlan, error) {
	lane = lane.normalized()
	existing, err := s.ListDatasetPublicationSeries(lane)
	if err != nil {
		return DatasetPublicationRetentionPlan{Lane: lane}, err
	}
	adopt, err := s.unrecordedDatasetPublicationSeries(lane, providerPeerID, existing, DatasetPublicationSeries{})
	if err != nil {
		return DatasetPublicationRetentionPlan{Lane: lane}, err
	}
	merged := mergeDatasetPublicationSeries(existing, adopt)
	return s.planDatasetPublicationRetention(lane, providerPeerID, policy, merged)
}

// mergeDatasetPublicationSeries overlays adopted legacy series on the recorded
// ones (same id: members unioned, newest time), newest first.
func mergeDatasetPublicationSeries(existing, adopt []DatasetPublicationSeries) []DatasetPublicationSeries {
	byID := make(map[string]int, len(existing))
	out := append([]DatasetPublicationSeries(nil), existing...)
	for i, each := range out {
		byID[each.SeriesID] = i
	}
	for _, each := range adopt {
		if i, ok := byID[each.SeriesID]; ok {
			out[i].Members = append(out[i].Members, each.Members...)
			if each.PublishedAt.After(out[i].PublishedAt) {
				out[i].PublishedAt = each.PublishedAt
			}
			continue
		}
		byID[each.SeriesID] = len(out)
		out = append(out, each)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PublishedAt.After(out[j].PublishedAt) })
	return out
}

// pinLedgerCIDNeededOutsideLane reports whether any verified ledger entry
// other than this lane's own publication entries references cid: another
// lane, another provider peer, the archive plane, or a non-publication role.
func (s *FlatSQLStore) pinLedgerCIDNeededOutsideLane(cid string, lane DatasetPublicationLane, providerPeerID string) (bool, error) {
	entries, err := s.ListPinLedgerEntries(PinLedgerQuery{CID: cid, VerificationState: "verified"})
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		ours := entry.SchemaName == lane.SchemaName &&
			entry.ProviderPeerID == providerPeerID &&
			entry.ProviderID == lane.ProviderID &&
			entry.SourceName == lane.SourceName &&
			entry.QueryProfile == DatasetPublicationQueryProfile &&
			publicationRetentionRoles[entry.Role]
		if !ours {
			return true, nil
		}
	}
	return false, nil
}

// DatasetPublicationRetentionResult reports one applied retention pass.
type DatasetPublicationRetentionResult struct {
	Lane           DatasetPublicationLane
	KeptSeries     int
	RetiredSeries  int
	Unpinned       int
	UnpinFailed    int
	SharedRetained int
	RowsDeleted    int
	FilesRemoved   int
	BytesRemoved   int64
	UnpinnedBytes  int64
}

// ApplyDatasetPublicationRetention records a retention pass whose kubo unpins
// the caller has attempted: unpinned names the CIDs kubo released. A CID whose
// unpin failed keeps its verified entry and is retried by the next pass.
// outputDir is the publication output directory (the service's).
func (s *FlatSQLStore) ApplyDatasetPublicationRetention(plan DatasetPublicationRetentionPlan, providerPeerID string, unpinned map[string]bool, outputDir string, now time.Time) (DatasetPublicationRetentionResult, error) {
	lane := plan.Lane.normalized()
	result := DatasetPublicationRetentionResult{Lane: lane, KeptSeries: len(plan.KeptSeries), RetiredSeries: len(plan.RetiredSeries)}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	retire := make([]PinLedgerEntry, 0, len(plan.Unpin)+len(plan.Shared))
	for _, entry := range plan.Unpin {
		if unpinned[entry.CID] {
			result.Unpinned++
			result.UnpinnedBytes += entry.ByteCount
			retire = append(retire, entry)
		} else {
			result.UnpinFailed++
		}
	}
	for _, entry := range plan.Shared {
		result.SharedRetained++
		retire = append(retire, entry)
	}
	for _, entry := range retire {
		laneEntries, err := s.ListPinLedgerEntries(PinLedgerQuery{
			CID:               entry.CID,
			SchemaName:        lane.SchemaName,
			ProviderPeerID:    strings.TrimSpace(providerPeerID),
			ProviderID:        lane.ProviderID,
			SourceName:        lane.SourceName,
			QueryProfile:      DatasetPublicationQueryProfile,
			VerificationState: "verified",
		})
		if err != nil {
			return result, err
		}
		for _, laneEntry := range laneEntries {
			if !publicationRetentionRoles[laneEntry.Role] {
				continue
			}
			laneEntry.VerificationState = PinLedgerStateRetired
			laneEntry.UpdatedAt = now
			if err := s.UpsertPinLedgerEntry(laneEntry); err != nil {
				return result, fmt.Errorf("retire pin ledger entry %s: %w", laneEntry.CID, err)
			}
		}
	}

	for _, row := range plan.RetireRows {
		deleted, err := s.DeleteDatasetShardPublication(row)
		if err != nil {
			return result, err
		}
		if deleted {
			result.RowsDeleted++
		}
	}

	// Files: the deleted rows' shard/index files, unless a surviving row of
	// the schema maps to the same file; the released manifests and bundles.
	if strings.TrimSpace(outputDir) != "" {
		live, err := s.liveDatasetPublicationFiles(lane.SchemaName)
		if err != nil {
			return result, err
		}
		schemaDir := filepath.Join(outputDir, datasetPublicationPathComponent(lane.SchemaName))
		for _, row := range plan.RetireRows {
			for _, path := range datasetPublicationRowFiles(schemaDir, row) {
				if live.referenced(path) {
					continue
				}
				result.removeFile(path)
			}
		}
		for _, entry := range retire {
			if len(entry.ByteHash) < 16 || !unpinned[entry.CID] {
				continue
			}
			switch entry.Role {
			case PinLedgerRoleManifest:
				matches, _ := filepath.Glob(filepath.Join(outputDir, "manifests", "*-"+entry.ByteHash[:16]+".dpm"))
				for _, match := range matches {
					result.removeFile(match)
				}
			case PinLedgerRoleShardGroupCAR:
				result.removeFile(filepath.Join(schemaDir, "car", "shard-group-"+entry.ByteHash[:16]+".car"))
			}
		}
	}

	if len(plan.RetiredSeries) > 0 {
		defer s.lockWrite("ApplyDatasetPublicationRetention")()
		for _, seriesID := range plan.RetiredSeries {
			if _, err := s.db.Exec(`DELETE FROM sdn_dataset_publication_series_cids WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND series_id = ?`,
				lane.SchemaName, lane.ProviderID, lane.SourceName, seriesID); err != nil {
				return result, fmt.Errorf("delete retired series members: %w", err)
			}
			if _, err := s.db.Exec(`DELETE FROM sdn_dataset_publication_series WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND series_id = ?`,
				lane.SchemaName, lane.ProviderID, lane.SourceName, seriesID); err != nil {
				return result, fmt.Errorf("delete retired series: %w", err)
			}
		}
	}
	return result, nil
}

func (r *DatasetPublicationRetentionResult) removeFile(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if err := os.Remove(path); err != nil {
		log.Warnf("publication retention: remove %s: %v", path, err)
		return
	}
	r.FilesRemoved++
	r.BytesRemoved += info.Size()
}

// datasetPublicationRowFiles names a row's shard and index files under the
// schema's publication directory.
func datasetPublicationRowFiles(schemaDir string, row DatasetShardPublication) []string {
	var out []string
	if len(row.QuerySHA256) >= 16 && len(row.ShardSHA256) >= 16 {
		out = append(out, filepath.Join(schemaDir, "shards", row.QuerySHA256[:16]+"-"+row.ShardSHA256[:16]+".fbshard"))
	}
	if len(row.QuerySHA256) >= 16 && len(row.IndexSHA256) >= 16 {
		out = append(out, filepath.Join(schemaDir, "indexes", row.QuerySHA256[:16]+"-"+row.IndexSHA256[:16]+".index.json"))
	}
	return out
}

// RemoveDatasetPublicationRowFiles removes the shard/index files of rows that
// were just deleted, unless a remaining row of the schema serves the same
// content. Returns the files and bytes removed.
func (s *FlatSQLStore) RemoveDatasetPublicationRowFiles(outputDir, schemaName string, rows []DatasetShardPublication) (int, int64, error) {
	if strings.TrimSpace(outputDir) == "" || len(rows) == 0 {
		return 0, 0, nil
	}
	live, err := s.liveDatasetPublicationFiles(schemaName)
	if err != nil {
		return 0, 0, err
	}
	schemaDir := filepath.Join(outputDir, datasetPublicationPathComponent(schemaName))
	result := DatasetPublicationRetentionResult{}
	for _, row := range rows {
		for _, path := range datasetPublicationRowFiles(schemaDir, row) {
			if live.referenced(path) {
				continue
			}
			result.removeFile(path)
		}
	}
	return result.FilesRemoved, result.BytesRemoved, nil
}

// livePublicationFiles is the set of shard/index content hashes (16-hex
// prefixes) the schema's remaining publication rows serve. Matching by the
// content half of the name also covers legacy file names whose query half
// differs (datasetPublicationShardPathForRepair).
type livePublicationFiles struct {
	shards  map[string]bool
	indexes map[string]bool
}

func (l livePublicationFiles) referenced(path string) bool {
	name := filepath.Base(path)
	switch {
	case strings.HasSuffix(name, ".fbshard"):
		return l.shards[contentHalf(strings.TrimSuffix(name, ".fbshard"))]
	case strings.HasSuffix(name, ".index.json"):
		return l.indexes[contentHalf(strings.TrimSuffix(name, ".index.json"))]
	}
	return true
}

func contentHalf(stem string) string {
	if i := strings.LastIndex(stem, "-"); i >= 0 {
		return stem[i+1:]
	}
	return stem
}

func (s *FlatSQLStore) liveDatasetPublicationFiles(schemaName string) (livePublicationFiles, error) {
	live := livePublicationFiles{shards: map[string]bool{}, indexes: map[string]bool{}}
	rows, err := s.ListDatasetShardPublicationsForProfile(DatasetPublicationQueryProfile, schemaName)
	if err != nil {
		return live, err
	}
	for _, row := range rows {
		if len(row.ShardSHA256) >= 16 {
			live.shards[row.ShardSHA256[:16]] = true
		}
		if len(row.IndexSHA256) >= 16 {
			live.indexes[row.IndexSHA256[:16]] = true
		}
	}
	return live, nil
}

// DatasetPublicationFileSweep reports a sweep of one schema's publication
// directory.
type DatasetPublicationFileSweep struct {
	SchemaName   string
	FilesRemoved int
	BytesRemoved int64
}

// SweepDatasetPublicationFiles removes files in outputDir/<schema> that no
// longer serve anything and are older than minAge (the age guard keeps an
// export still in flight, whose row is not written yet, untouched):
//
//   - car/shard-group-*.car: every bundle is pinned in kubo when it is built;
//     the local file is a staging copy.
//   - shards/*.fbshard, indexes/*.index.json: files whose content hash no
//     remaining publication row of the schema names (windows overwritten by a
//     later series and rows retention retired).
func (s *FlatSQLStore) SweepDatasetPublicationFiles(outputDir, schemaName string, minAge time.Duration, now time.Time) (DatasetPublicationFileSweep, error) {
	sweep := DatasetPublicationFileSweep{SchemaName: strings.TrimSpace(schemaName)}
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" || sweep.SchemaName == "" {
		return sweep, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	live, err := s.liveDatasetPublicationFiles(sweep.SchemaName)
	if err != nil {
		return sweep, err
	}
	schemaDir := filepath.Join(outputDir, datasetPublicationPathComponent(sweep.SchemaName))
	result := DatasetPublicationRetentionResult{}
	for _, sub := range []string{"car", "shards", "indexes"} {
		entries, err := os.ReadDir(filepath.Join(schemaDir, sub))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return sweep, fmt.Errorf("read %s: %w", filepath.Join(schemaDir, sub), err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			path := filepath.Join(schemaDir, sub, name)
			switch sub {
			case "car":
				if !strings.HasPrefix(name, "shard-group-") || filepath.Ext(name) != ".car" {
					continue
				}
			case "shards":
				if !strings.HasSuffix(name, ".fbshard") || live.referenced(path) {
					continue
				}
			case "indexes":
				if !strings.HasSuffix(name, ".index.json") || live.referenced(path) {
					continue
				}
			}
			info, err := entry.Info()
			if err != nil || now.Sub(info.ModTime()) < minAge {
				continue
			}
			result.removeFile(path)
		}
	}
	sweep.FilesRemoved = result.FilesRemoved
	sweep.BytesRemoved = result.BytesRemoved
	return sweep, nil
}
