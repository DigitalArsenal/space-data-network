// Package sourcemetrics is the node's OPERATIONAL ledger for data-source
// retrieval: what the host's own connectors did, when, and how much they moved.
//
// BOUNDARY. This package records connector facts ONLY — an outbound HTTP fetch
// completed with status S and N bytes; a provenance-tagged ingest batch B landed
// for source X. It never parses a payload, never decides what to fetch, never
// schedules anything. Those decisions live in the wasm ingest flows
// (space-data-network-modules flows/celestrak-*-ingest). The host contributes
// timers, egress, guarded persistence — and this bookkeeping of its own I/O.
//
// SEPARATE DATABASE. Operational metrics live in their own sqlite file
// (source-metrics.db) beside the record store, never inside it — the same
// safety pattern as auth.db / audit.db / storefront.db. A record-store rebuild
// must not take the operator's retrieval history with it, and metric writes
// must never contend with the single-writer standards store.
//
// SOURCE ID. Rows are keyed by "<provider_id>/<source_name>" — the exact
// provenance pair the wasm parser stamps onto every record through
// storage.ingest_with_source. That makes the ledger joinable to the records it
// describes (storage.SourceTags) and to their publications
// (storage.DatasetShardPublication) without inventing a second identity space.
package sourcemetrics

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

var log = logging.Logger("sourcemetrics")

// DBFileName is the operational metrics database, opened beside the record
// store directory (never inside it).
const DBFileName = "source-metrics.db"

// DefaultDebounceHours is the retrieval debounce the CelesTrak fetch policy
// binds this node to: the same published payload is not re-pulled inside a 3 h
// window. It is reported per source so an operator (and the $APPS widget) can
// see the window the node is actually honouring.
const DefaultDebounceHours = 3.0

// SourceID composes the ledger key from the provenance pair the wasm parser
// stamps on every record. Empty components are tolerated so a malformed ingest
// still records SOMETHING rather than being silently dropped.
func SourceID(providerID, sourceName string) string {
	providerID = strings.TrimSpace(providerID)
	sourceName = strings.TrimSpace(sourceName)
	switch {
	case providerID == "" && sourceName == "":
		return ""
	case providerID == "":
		return sourceName
	case sourceName == "":
		return providerID
	}
	return providerID + "/" + sourceName
}

// Fetch is one completed outbound retrieval attempt, as observed by the host's
// HTTP egress connector. The connector knows the URL, not the source — sources
// are attributed later, when the ingest cap reports the same source_url.
type Fetch struct {
	URL        string
	Status     int
	Bytes      int64
	DurationMs int64
	Err        string
	At         time.Time
}

// Ingest is one provenance-tagged batch that reached the store through
// storage.ingest_with_source.
type Ingest struct {
	// AppID is the module/flow instance that drove the ingest. It attributes a
	// source to a running app without the host having to know what the app is
	// for.
	AppID      string
	ProviderID string
	SourceName string
	SourceURL  string
	Schema     string
	BatchID    string
	// PullBytes is the size of the RETRIEVED payload (the raw archive segment
	// the parser hands back), not the size of the normalized records.
	PullBytes int64
	Records   int
	Inserted  int
	At        time.Time
}

// PNM is the latest publication notification observed for a source.
type PNM struct {
	ID          string
	CID         string
	Schema      string
	FeedHead    string
	RecordCount int
	PublishedAt time.Time
}

// Source is one ledger row: the $APPS retrieval feed for a single data source.
type Source struct {
	SourceID   string `json:"source_id"`
	AppID      string `json:"app_id,omitempty"`
	ProviderID string `json:"provider_id"`
	SourceName string `json:"source_name"`
	SourceURL  string `json:"source_url,omitempty"`

	// Retrieved-not-derived. Every row this package writes describes a payload
	// this node PULLED from a publisher; nothing here is a fit, a propagation,
	// or any other derived product.
	Origin string `json:"origin"`

	LastRetrievedAt   *time.Time `json:"-"`
	DebounceHours     float64    `json:"debounce_hours"`
	LastPullSizeBytes int64      `json:"last_pull_size_bytes"`

	LastStatus     int    `json:"last_status,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	LastDurationMs int64  `json:"last_duration_ms,omitempty"`

	LastBatchID string `json:"last_batch_id,omitempty"`
	// LastBatchRepeated is true when the most recent pull carried the SAME
	// content hash as the previous one — the source republished nothing. This
	// is the honest "debounce hit" signal: the fetch happened, the data did not
	// change, and the store deduplicated it.
	LastBatchRepeated bool `json:"last_batch_repeated"`

	// LastSchemas / LastRecords / LastInserted describe the LAST PULL as a
	// whole, summed across every schema that one retrieved payload produced.
	LastSchemas  []string `json:"last_schemas,omitempty"`
	LastRecords  int      `json:"last_records,omitempty"`
	LastInserted int      `json:"last_inserted,omitempty"`

	FetchCount  int64 `json:"fetch_count"`
	IngestCount int64 `json:"ingest_count"`

	LastPNM *PNM `json:"-"`
}

// Store is the operational metrics database handle. All methods are safe for
// concurrent use and NEVER return an error to their connector callers — a
// bookkeeping failure must not fail a retrieval. Failures are logged.
type Store struct {
	db     *sql.DB
	closer func() error

	mu sync.Mutex
	// now is injectable for tests.
	now func() time.Time
}

// Open creates or opens the operational metrics database beside the record
// store directory. storeDir is the directory holding the record store (the
// caller passes filepath.Dir(store.Path())).
func Open(storeDir string) (*Store, error) {
	if strings.TrimSpace(storeDir) == "" {
		return nil, errors.New("sourcemetrics: store directory is required")
	}
	path := filepath.Join(storeDir, DBFileName)
	db, closer, err := flatsqldrv.OpenStandalone(path)
	if err != nil {
		return nil, fmt.Errorf("sourcemetrics: open %s: %w", path, err)
	}
	s := &Store{db: db, closer: closer, now: time.Now}
	if err := s.initTables(); err != nil {
		closer()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer()
}

func (s *Store) initTables() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS source_metrics (
			source_id            TEXT PRIMARY KEY,
			app_id               TEXT NOT NULL DEFAULT '',
			provider_id          TEXT NOT NULL DEFAULT '',
			source_name          TEXT NOT NULL DEFAULT '',
			source_url           TEXT NOT NULL DEFAULT '',
			last_retrieved_at    INTEGER NOT NULL DEFAULT 0,
			debounce_hours       REAL    NOT NULL DEFAULT 0,
			last_pull_size_bytes INTEGER NOT NULL DEFAULT 0,
			last_status          INTEGER NOT NULL DEFAULT 0,
			last_error           TEXT    NOT NULL DEFAULT '',
			last_duration_ms     INTEGER NOT NULL DEFAULT 0,
			last_batch_id        TEXT    NOT NULL DEFAULT '',
			last_batch_repeated  INTEGER NOT NULL DEFAULT 0,
			-- last_fetch_seq is the fetch counter for this source's URL at the
			-- moment of the last ingest. It is what separates "a new pull" from
			-- "the same pull delivering a second schema": one GP payload lands
			-- as both OMM and MPE under ONE batch id, and that must not be
			-- mistaken for the publisher republishing identical content.
			last_fetch_seq       INTEGER NOT NULL DEFAULT 0,
			-- last_schemas is every schema the LAST PULL produced, comma
			-- separated. One payload commonly lands as several schemas (a GP
			-- catalog becomes both OMM and MPE), and reporting only the last
			-- one made a 500-record pull look like a 2-record pull.
			last_schemas         TEXT    NOT NULL DEFAULT '',
			last_records         INTEGER NOT NULL DEFAULT 0,
			last_inserted        INTEGER NOT NULL DEFAULT 0,
			fetch_count          INTEGER NOT NULL DEFAULT 0,
			ingest_count         INTEGER NOT NULL DEFAULT 0,
			last_pnm_id          TEXT    NOT NULL DEFAULT '',
			last_pnm_cid         TEXT    NOT NULL DEFAULT '',
			last_pnm_schema      TEXT    NOT NULL DEFAULT '',
			last_pnm_feed_head   TEXT    NOT NULL DEFAULT '',
			last_pnm_records     INTEGER NOT NULL DEFAULT 0,
			last_pnm_at          INTEGER NOT NULL DEFAULT 0,
			updated_at           INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return fmt.Errorf("sourcemetrics: create source_metrics: %w", err)
	}

	// Fetch events are keyed by URL because that is ALL the HTTP egress
	// connector knows. RecordIngest joins them to a source by source_url.
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS fetch_events (
			url              TEXT PRIMARY KEY,
			last_status      INTEGER NOT NULL DEFAULT 0,
			last_bytes       INTEGER NOT NULL DEFAULT 0,
			last_duration_ms INTEGER NOT NULL DEFAULT 0,
			last_error       TEXT    NOT NULL DEFAULT '',
			last_at          INTEGER NOT NULL DEFAULT 0,
			fetch_count      INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return fmt.Errorf("sourcemetrics: create fetch_events: %w", err)
	}
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_source_metrics_retrieved ON source_metrics(last_retrieved_at DESC)`); err != nil {
		return fmt.Errorf("sourcemetrics: create source_metrics index: %w", err)
	}

	// Attempt log, keyed by APP. A source row only exists once an ingest has
	// SUCCEEDED, so a flow whose pulls keep failing has no row at all and reads
	// as "never retrieved" — which made it due on every single restart and
	// turned a restart loop into a retry storm against an endpoint that was
	// already refusing us. Observed live: a 403'd CelesTrak endpoint re-hit on
	// boot. What a publisher is owed is a limit on ATTEMPTS, not on successes.
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS app_attempts (
			app_id          TEXT PRIMARY KEY,
			last_attempt_at INTEGER NOT NULL DEFAULT 0,
			attempt_count   INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return fmt.Errorf("sourcemetrics: create app_attempts: %w", err)
	}
	return nil
}

// RecordAttempt stamps that an app was allowed to start a retrieval, whether or
// not it goes on to succeed.
func (s *Store) RecordAttempt(appID string) {
	if s == nil || s.db == nil {
		return
	}
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`
		INSERT INTO app_attempts (app_id, last_attempt_at, attempt_count)
		VALUES (?, ?, 1)
		ON CONFLICT(app_id) DO UPDATE SET
			last_attempt_at = excluded.last_attempt_at,
			attempt_count   = app_attempts.attempt_count + 1`,
		appID, unixOrZero(s.now())); err != nil {
		log.Debugf("record attempt %s: %v", appID, err)
	}
}

// LastAttempt returns when an app last started a retrieval, or nil if never.
func (s *Store) LastAttempt(appID string) *time.Time {
	if s == nil || s.db == nil {
		return nil
	}
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var at int64
	if err := s.db.QueryRow(
		`SELECT last_attempt_at FROM app_attempts WHERE app_id = ?`, appID).Scan(&at); err != nil {
		return nil
	}
	return timeOrNil(at)
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

// appendSchema adds one schema name to a comma-separated list, preserving
// arrival order and never repeating a name.
func appendSchema(list, schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return list
	}
	if list == "" {
		return schema
	}
	for _, existing := range strings.Split(list, ",") {
		if existing == schema {
			return list
		}
	}
	return list + "," + schema
}

// splitSchemas parses the stored comma-separated schema list.
func splitSchemas(list string) []string {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil
	}
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func timeOrNil(unix int64) *time.Time {
	if unix <= 0 {
		return nil
	}
	t := time.Unix(unix, 0).UTC()
	return &t
}

// RecordFetch books one completed outbound retrieval. Called from the module
// HTTP egress connector; never fails the caller.
func (s *Store) RecordFetch(f Fetch) {
	if s == nil || s.db == nil {
		return
	}
	url := strings.TrimSpace(f.URL)
	if url == "" {
		return
	}
	at := f.At
	if at.IsZero() {
		at = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`
		INSERT INTO fetch_events (url, last_status, last_bytes, last_duration_ms, last_error, last_at, fetch_count)
		VALUES (?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(url) DO UPDATE SET
			last_status      = excluded.last_status,
			last_bytes       = excluded.last_bytes,
			last_duration_ms = excluded.last_duration_ms,
			last_error       = excluded.last_error,
			last_at          = excluded.last_at,
			fetch_count      = fetch_events.fetch_count + 1`,
		url, f.Status, f.Bytes, f.DurationMs, f.Err, unixOrZero(at)); err != nil {
		log.Debugf("record fetch %s: %v", url, err)
		return
	}
	// A source already attributed to this URL adopts the fetch immediately, so
	// last_retrieved_at is honest even when the pull carried no new records
	// (304, unchanged payload, or a parse that produced nothing).
	if _, err := s.db.Exec(`
		UPDATE source_metrics SET
			last_retrieved_at = ?,
			last_status       = ?,
			last_error        = ?,
			last_duration_ms  = ?,
			fetch_count       = fetch_count + 1,
			updated_at        = ?
		WHERE source_url = ?`,
		unixOrZero(at), f.Status, f.Err, f.DurationMs, unixOrZero(s.now()), url); err != nil {
		log.Debugf("attribute fetch %s: %v", url, err)
	}
}

// RecordIngest books one provenance-tagged batch that reached the store. This
// is where a URL becomes a SOURCE: the wasm parser supplies the provenance
// pair, the host records it against the fetch it already booked.
func (s *Store) RecordIngest(in Ingest) {
	if s == nil || s.db == nil {
		return
	}
	id := SourceID(in.ProviderID, in.SourceName)
	if id == "" {
		return
	}
	at := in.At
	if at.IsZero() {
		at = s.now()
	}
	url := strings.TrimSpace(in.SourceURL)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Pull size: prefer the raw payload the parser handed back; fall back to
	// the bytes the egress connector actually read for this URL.
	pullBytes := in.PullBytes
	var (
		fetchStatus   int
		fetchErr      string
		fetchDuration int64
		fetchAt       int64
		fetchSeq      int64
	)
	if url != "" {
		row := s.db.QueryRow(
			`SELECT last_status, last_bytes, last_duration_ms, last_error, last_at, fetch_count FROM fetch_events WHERE url = ?`, url)
		var bytesRead int64
		if err := row.Scan(&fetchStatus, &bytesRead, &fetchDuration, &fetchErr, &fetchAt, &fetchSeq); err == nil {
			if pullBytes <= 0 {
				pullBytes = bytesRead
			}
		}
	}
	retrievedAt := fetchAt
	if retrievedAt <= 0 {
		retrievedAt = unixOrZero(at)
	}

	// "Repeated" means the PUBLISHER served the same bytes on a NEW pull — not
	// that a single payload produced a second schema. A pull is new when the
	// URL's fetch counter has advanced since this source's last ingest.
	var (
		priorBatch    string
		priorFetchSeq int64
		priorRepeated int
		priorSchemas  string
		priorRecords  int
		priorInserted int
	)
	_ = s.db.QueryRow(
		`SELECT last_batch_id, last_fetch_seq, last_batch_repeated, last_schemas, last_records, last_inserted
		 FROM source_metrics WHERE source_id = ?`, id).
		Scan(&priorBatch, &priorFetchSeq, &priorRepeated, &priorSchemas, &priorRecords, &priorInserted)

	samePull := fetchSeq != 0 && fetchSeq == priorFetchSeq

	repeated := priorRepeated
	if !samePull {
		repeated = 0
		if priorBatch != "" && in.BatchID != "" && priorBatch == in.BatchID {
			repeated = 1
		}
	}

	// Totals describe the PULL, not the last ingest call: one GP payload lands
	// as both OMM and MPE, and reporting only the final call made a
	// 500-record pull read as a 2-record pull.
	schemas := strings.TrimSpace(in.Schema)
	records := in.Records
	inserted := in.Inserted
	if samePull {
		schemas = appendSchema(priorSchemas, in.Schema)
		records += priorRecords
		inserted += priorInserted
	}

	if _, err := s.db.Exec(`
		INSERT INTO source_metrics (
			source_id, app_id, provider_id, source_name, source_url,
			last_retrieved_at, debounce_hours, last_pull_size_bytes,
			last_status, last_error, last_duration_ms,
			last_batch_id, last_batch_repeated, last_fetch_seq,
			last_schemas, last_records, last_inserted,
			fetch_count, ingest_count, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			app_id               = CASE WHEN excluded.app_id = '' THEN source_metrics.app_id ELSE excluded.app_id END,
			provider_id          = excluded.provider_id,
			source_name          = excluded.source_name,
			source_url           = CASE WHEN excluded.source_url = '' THEN source_metrics.source_url ELSE excluded.source_url END,
			last_retrieved_at    = excluded.last_retrieved_at,
			debounce_hours       = excluded.debounce_hours,
			last_pull_size_bytes = excluded.last_pull_size_bytes,
			last_status          = excluded.last_status,
			last_error           = excluded.last_error,
			last_duration_ms     = excluded.last_duration_ms,
			last_batch_id        = excluded.last_batch_id,
			last_batch_repeated  = excluded.last_batch_repeated,
			last_fetch_seq       = excluded.last_fetch_seq,
			last_schemas         = excluded.last_schemas,
			last_records         = excluded.last_records,
			last_inserted        = excluded.last_inserted,
			ingest_count         = source_metrics.ingest_count + 1,
			updated_at           = excluded.updated_at`,
		id, strings.TrimSpace(in.AppID), strings.TrimSpace(in.ProviderID), strings.TrimSpace(in.SourceName), url,
		retrievedAt, DefaultDebounceHours, pullBytes,
		fetchStatus, fetchErr, fetchDuration,
		in.BatchID, repeated, fetchSeq,
		schemas, records, inserted,
		unixOrZero(s.now())); err != nil {
		log.Debugf("record ingest %s: %v", id, err)
	}
}

// RecordPNM books the latest publication notification for a source. The
// authoritative PNM lives in the record store; this is the durable projection
// the $APPS feed reads so "last PNM" survives a restart and stays available
// even when the store is busy.
func (s *Store) RecordPNM(providerID, sourceName string, p PNM) {
	if s == nil || s.db == nil {
		return
	}
	id := SourceID(providerID, sourceName)
	if id == "" || strings.TrimSpace(p.CID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := unixOrZero(s.now())
	if _, err := s.db.Exec(`
		INSERT INTO source_metrics (
			source_id, provider_id, source_name,
			debounce_hours,
			last_pnm_id, last_pnm_cid, last_pnm_schema, last_pnm_feed_head, last_pnm_records, last_pnm_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			last_pnm_id        = excluded.last_pnm_id,
			last_pnm_cid       = excluded.last_pnm_cid,
			last_pnm_schema    = excluded.last_pnm_schema,
			last_pnm_feed_head = excluded.last_pnm_feed_head,
			last_pnm_records   = excluded.last_pnm_records,
			last_pnm_at        = excluded.last_pnm_at,
			updated_at         = excluded.updated_at`,
		id, strings.TrimSpace(providerID), strings.TrimSpace(sourceName),
		DefaultDebounceHours,
		p.ID, p.CID, p.Schema, p.FeedHead, p.RecordCount, unixOrZero(p.PublishedAt),
		now); err != nil {
		log.Debugf("record pnm %s: %v", id, err)
	}
}

// Sources returns every ledger row, most recently retrieved first.
func (s *Store) Sources() ([]Source, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`
		SELECT source_id, app_id, provider_id, source_name, source_url,
		       last_retrieved_at, debounce_hours, last_pull_size_bytes,
		       last_status, last_error, last_duration_ms,
		       last_batch_id, last_batch_repeated,
		       last_schemas, last_records, last_inserted,
		       fetch_count, ingest_count,
		       last_pnm_id, last_pnm_cid, last_pnm_schema, last_pnm_feed_head, last_pnm_records, last_pnm_at
		FROM source_metrics
		ORDER BY last_retrieved_at DESC, source_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("sourcemetrics: list sources: %w", err)
	}
	defer rows.Close()

	out := make([]Source, 0, 8)
	for rows.Next() {
		var (
			src         Source
			retrieved   int64
			repeated    int
			schemas     string
			pnmID       string
			pnmCID      string
			pnmSchema   string
			pnmFeedHead string
			pnmRecords  int
			pnmAt       int64
		)
		if err := rows.Scan(&src.SourceID, &src.AppID, &src.ProviderID, &src.SourceName, &src.SourceURL,
			&retrieved, &src.DebounceHours, &src.LastPullSizeBytes,
			&src.LastStatus, &src.LastError, &src.LastDurationMs,
			&src.LastBatchID, &repeated,
			&schemas, &src.LastRecords, &src.LastInserted,
			&src.FetchCount, &src.IngestCount,
			&pnmID, &pnmCID, &pnmSchema, &pnmFeedHead, &pnmRecords, &pnmAt); err != nil {
			return nil, fmt.Errorf("sourcemetrics: scan source: %w", err)
		}
		src.Origin = "retrieved"
		src.LastSchemas = splitSchemas(schemas)
		src.LastRetrievedAt = timeOrNil(retrieved)
		src.LastBatchRepeated = repeated == 1
		if src.DebounceHours <= 0 {
			src.DebounceHours = DefaultDebounceHours
		}
		if pnmCID != "" {
			src.LastPNM = &PNM{
				ID:          pnmID,
				CID:         pnmCID,
				Schema:      pnmSchema,
				FeedHead:    pnmFeedHead,
				RecordCount: pnmRecords,
			}
			if t := timeOrNil(pnmAt); t != nil {
				src.LastPNM.PublishedAt = *t
			}
		}
		out = append(out, src)
	}
	return out, rows.Err()
}
