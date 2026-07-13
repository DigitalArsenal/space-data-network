package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// AssetReferenceState is the durable lifecycle state of one asset reference.
type AssetReferenceState string

const (
	AssetReferenceStaged     AssetReferenceState = "staged"
	AssetReferenceReviewOpen AssetReferenceState = "review_open"
	AssetReferenceApproved   AssetReferenceState = "approved"
	AssetReferenceRejected   AssetReferenceState = "rejected"
	AssetReferenceSuperseded AssetReferenceState = "superseded"
	AssetReferenceAbandoned  AssetReferenceState = "abandoned"
)

var (
	// ErrAssetOIDCTokenReplay reports an atomic uniqueness conflict for a
	// previously consumed OIDC token digest. Callers may map this error to the
	// authentication layer's replay sentinel without storage importing it.
	ErrAssetOIDCTokenReplay = errors.New("storage: asset OIDC token digest already consumed")
	// ErrAssetOIDCReceiptConflict reports corrupt or contradictory receipt data
	// under a digest already reconstructed from the auxiliary journal.
	ErrAssetOIDCReceiptConflict = errors.New("storage: conflicting asset OIDC receipt")
	// ErrAssetPinReferenceNotFound reports a missing reference key.
	ErrAssetPinReferenceNotFound = errors.New("storage: asset pin reference not found")
	// ErrAssetPinReferenceConflict reports a deterministic-key or state compare-and-swap conflict.
	ErrAssetPinReferenceConflict = errors.New("storage: asset pin reference conflict")
	// ErrAssetPinReferenceNotExpired reports an attempt to delete a reference
	// which is still protected or has no finite expiry.
	ErrAssetPinReferenceNotExpired = errors.New("storage: asset pin reference is not expired")
	// ErrAssetPinAuditConflict reports different audit data under one stable event ID.
	ErrAssetPinAuditConflict = errors.New("storage: conflicting asset pin audit event")
)

// AssetOIDCReceipt is the storage-owned audit record for one verified token.
// Digest is the SHA-256 digest of the token; raw JWTs have no field here and
// must never be persisted.
type AssetOIDCReceipt struct {
	Digest      string    `json:"digest"`
	ExpiresAt   time.Time `json:"expires_at"`
	Repository  string    `json:"repository"`
	Ref         string    `json:"ref"`
	WorkflowRef string    `json:"workflow_ref"`
	Actor       string    `json:"actor"`
	RunID       string    `json:"run_id"`
	RunAttempt  string    `json:"run_attempt"`
	SHA         string    `json:"sha"`
	ConsumedAt  time.Time `json:"consumed_at"`
}

// AssetPinReference retains provenance and lifecycle ownership for one
// deterministic candidate/reference key. Different references may point at
// the same content-addressed asset. WorkflowRunID identifies the originating
// pin workflow and is retained across later lifecycle transitions.
type AssetPinReference struct {
	ReferenceKey   string              `json:"reference_key"`
	CandidateKey   string              `json:"candidate_key"`
	CID            string              `json:"cid"`
	SHA256         string              `json:"sha256"`
	ByteCount      int64               `json:"byte_count"`
	State          AssetReferenceState `json:"state"`
	SourceURL      string              `json:"source_url"`
	LicenseName    string              `json:"license_name"`
	Attribution    string              `json:"attribution"`
	MetadataJSON   string              `json:"metadata_json"`
	GitHubIssue    int64               `json:"github_issue"`
	WorkflowRunID  string              `json:"workflow_run_id"`
	DecisionSHA256 string              `json:"decision_sha256"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	ExpiresAt      time.Time           `json:"expires_at"`
}

// AssetPinReferenceTransition atomically compare-and-swaps a reference state
// and records the decision digest, update time, and new expiry.
type AssetPinReferenceTransition struct {
	ReferenceKey   string              `json:"reference_key"`
	FromState      AssetReferenceState `json:"from_state"`
	ToState        AssetReferenceState `json:"to_state"`
	GitHubIssue    int64               `json:"github_issue"`
	DecisionSHA256 string              `json:"decision_sha256"`
	UpdatedAt      time.Time           `json:"updated_at"`
	ExpiresAt      time.Time           `json:"expires_at"`
}

// AssetPinAuditEvent is one immutable operational audit entry. EventID must be
// stable across retries and journal replay; it is the table's unique key.
type AssetPinAuditEvent struct {
	EventID        string    `json:"event_id"`
	MutationDigest string    `json:"mutation_digest"`
	Kind           string    `json:"kind"`
	Result         string    `json:"result"`
	TokenDigest    string    `json:"token_digest"`
	Repository     string    `json:"repository"`
	Ref            string    `json:"ref"`
	WorkflowRef    string    `json:"workflow_ref"`
	Actor          string    `json:"actor"`
	WorkflowRunID  string    `json:"workflow_run_id"`
	RunAttempt     string    `json:"run_attempt"`
	CommitSHA      string    `json:"commit_sha"`
	CandidateKey   string    `json:"candidate_key"`
	ReferenceKey   string    `json:"reference_key"`
	CID            string    `json:"cid"`
	SHA256         string    `json:"sha256"`
	ByteCount      int64     `json:"byte_count"`
	Detail         string    `json:"detail"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// AssetPinReferenceQuery selects operational references for retention and
// reconciliation. Zero fields are not used as filters.
type AssetPinReferenceQuery struct {
	ReferenceKey string
	CandidateKey string
	CID          string
	SHA256       string
	State        AssetReferenceState
	Limit        int
}

// AssetPinAuditEventQuery selects audit entries. Zero fields are not filters.
type AssetPinAuditEventQuery struct {
	EventID      string
	Kind         string
	CandidateKey string
	ReferenceKey string
	CID          string
	SHA256       string
	Limit        int
}

type auxiliaryAssetPinReferenceUpsert struct {
	Reference AssetPinReference  `json:"reference"`
	Event     AssetPinAuditEvent `json:"event"`
}

type auxiliaryAssetPinReferenceTransition struct {
	Transition AssetPinReferenceTransition `json:"transition"`
	Event      AssetPinAuditEvent          `json:"event"`
}

type auxiliaryAssetPinReferenceDelete struct {
	ReferenceKey string             `json:"reference_key"`
	Event        AssetPinAuditEvent `json:"event"`
}

const assetPinReferenceSelectColumns = `
	reference_key, candidate_key, cid, sha256, byte_count, state,
	source_url, license_name, attribution, metadata_json,
	github_issue, workflow_run_id, decision_sha256,
	created_at, updated_at, expires_at`

const assetPinAuditSelectColumns = `
	event_id, mutation_digest, event_kind, result, token_digest,
	repository, git_ref, workflow_ref, actor, workflow_run_id,
	run_attempt, commit_sha, candidate_key, reference_key,
	cid, sha256, byte_count, detail, occurred_at`

func (s *FlatSQLStore) initAssetPinLedgerTables() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_asset_pin_refs (
			reference_key TEXT NOT NULL PRIMARY KEY,
			candidate_key TEXT NOT NULL,
			cid TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			byte_count INTEGER NOT NULL,
			state TEXT NOT NULL CHECK (state IN (
				'staged', 'review_open', 'approved', 'rejected',
				'superseded', 'abandoned'
			)),
			source_url TEXT NOT NULL DEFAULT '',
			license_name TEXT NOT NULL DEFAULT '',
			attribution TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL,
			github_issue INTEGER NOT NULL DEFAULT 0,
			workflow_run_id TEXT NOT NULL DEFAULT '',
			decision_sha256 TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			expires_at TEXT
		)
	`); err != nil {
		return fmt.Errorf("create asset pin reference table: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_asset_pin_events (
			event_id TEXT NOT NULL PRIMARY KEY,
			mutation_digest TEXT NOT NULL DEFAULT '',
			event_kind TEXT NOT NULL,
			result TEXT NOT NULL,
			token_digest TEXT NOT NULL DEFAULT '',
			repository TEXT NOT NULL DEFAULT '',
			git_ref TEXT NOT NULL DEFAULT '',
			workflow_ref TEXT NOT NULL DEFAULT '',
			actor TEXT NOT NULL DEFAULT '',
			workflow_run_id TEXT NOT NULL DEFAULT '',
			run_attempt TEXT NOT NULL DEFAULT '',
			commit_sha TEXT NOT NULL DEFAULT '',
			candidate_key TEXT NOT NULL DEFAULT '',
			reference_key TEXT NOT NULL DEFAULT '',
			cid TEXT NOT NULL DEFAULT '',
			sha256 TEXT NOT NULL DEFAULT '',
			byte_count INTEGER NOT NULL DEFAULT 0,
			detail TEXT NOT NULL DEFAULT '',
			occurred_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create asset pin audit event table: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_asset_oidc_receipts (
			digest TEXT NOT NULL PRIMARY KEY,
			expires_at TEXT NOT NULL,
			repository TEXT NOT NULL,
			git_ref TEXT NOT NULL,
			workflow_ref TEXT NOT NULL,
			actor TEXT NOT NULL,
			run_id TEXT NOT NULL,
			run_attempt TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			consumed_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create asset OIDC receipt table: %w", err)
	}

	indexes := []struct {
		table string
		name  string
		sql   string
	}{
		{table: "sdn_asset_pin_refs", name: "idx_sdn_asset_pin_refs_candidate", sql: `
			CREATE UNIQUE INDEX IF NOT EXISTS idx_sdn_asset_pin_refs_candidate
			ON sdn_asset_pin_refs (candidate_key)
		`},
		{table: "sdn_asset_pin_refs", name: "idx_sdn_asset_pin_refs_sha", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_refs_sha
			ON sdn_asset_pin_refs (sha256, updated_at DESC, reference_key)
		`},
		{table: "sdn_asset_pin_refs", name: "idx_sdn_asset_pin_refs_cid", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_refs_cid
			ON sdn_asset_pin_refs (cid, state, expires_at)
		`},
		{table: "sdn_asset_pin_refs", name: "idx_sdn_asset_pin_refs_state_expiry", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_refs_state_expiry
			ON sdn_asset_pin_refs (state, expires_at, updated_at)
		`},
		{table: "sdn_asset_pin_events", name: "idx_sdn_asset_pin_events_reference", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_events_reference
			ON sdn_asset_pin_events (reference_key, occurred_at, event_id)
		`},
		{table: "sdn_asset_pin_events", name: "idx_sdn_asset_pin_events_cid_sha", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_events_cid_sha
			ON sdn_asset_pin_events (cid, sha256, occurred_at)
		`},
		{table: "sdn_asset_pin_events", name: "idx_sdn_asset_pin_events_occurred", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_events_occurred
			ON sdn_asset_pin_events (occurred_at, event_id)
		`},
		{table: "sdn_asset_pin_events", name: "idx_sdn_asset_pin_events_kind_occurred", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_events_kind_occurred
			ON sdn_asset_pin_events (event_kind, occurred_at, event_id)
		`},
		{table: "sdn_asset_pin_events", name: "idx_sdn_asset_pin_events_candidate_occurred", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_events_candidate_occurred
			ON sdn_asset_pin_events (candidate_key, occurred_at, event_id)
		`},
		{table: "sdn_asset_pin_events", name: "idx_sdn_asset_pin_events_sha_occurred", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_pin_events_sha_occurred
			ON sdn_asset_pin_events (sha256, occurred_at, event_id)
		`},
		{table: "sdn_asset_oidc_receipts", name: "idx_sdn_asset_oidc_receipts_expiry", sql: `
			CREATE INDEX IF NOT EXISTS idx_sdn_asset_oidc_receipts_expiry
			ON sdn_asset_oidc_receipts (expires_at, consumed_at)
		`},
	}
	for _, index := range indexes {
		if err := s.createRequiredStartupIndex(index.table, index.name, index.sql); err != nil {
			return fmt.Errorf("create asset ledger index %s: %w", index.name, err)
		}
	}
	return nil
}

// ConsumeAssetOIDCToken atomically inserts a digest receipt exactly once.
func (s *FlatSQLStore) ConsumeAssetOIDCToken(ctx context.Context, receipt AssetOIDCReceipt) error {
	if err := s.requireWritable("consume asset OIDC token"); err != nil {
		return err
	}
	if err := requireAssetPinContext(ctx); err != nil {
		return err
	}
	receipt, err := prepareAssetOIDCReceipt(receipt)
	if err != nil {
		return err
	}
	metadataEvent := auxiliaryMetadataEvent{
		Kind:             auxiliaryEventAssetOIDCReceiptConsume,
		AssetOIDCReceipt: &receipt,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAuxiliaryAssetFrame(metadataEvent); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin asset OIDC receipt transaction: %w", err)
	}
	defer tx.Rollback()

	inserted, err := insertAssetOIDCReceipt(ctx, tx, receipt)
	if err != nil {
		return err
	}
	if !inserted {
		return fmt.Errorf("digest %s: %w", receipt.Digest, ErrAssetOIDCTokenReplay)
	}
	if err := s.appendAuxiliaryMetadata(metadataEvent); err != nil {
		return fmt.Errorf("append asset OIDC receipt metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit asset OIDC receipt: %w", err)
	}
	return nil
}

// FindAssetOIDCReceipt looks up a consumed token digest without exposing token material.
func (s *FlatSQLStore) FindAssetOIDCReceipt(ctx context.Context, digest string) (AssetOIDCReceipt, bool, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return AssetOIDCReceipt{}, false, err
	}
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !isAssetSHA256(digest) {
		return AssetOIDCReceipt{}, false, errors.New("asset OIDC token digest must be lowercase SHA-256 hex")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	receipt, err := findAssetOIDCReceiptTo(ctx, s.db, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetOIDCReceipt{}, false, nil
	}
	if err != nil {
		return AssetOIDCReceipt{}, false, fmt.Errorf("find asset OIDC receipt: %w", err)
	}
	return receipt, true, nil
}

// UpsertAssetPinReference writes one reference and its immutable audit event in
// one SQL transaction, then journals the same pair as one replay unit.
func (s *FlatSQLStore) UpsertAssetPinReference(ctx context.Context, ref AssetPinReference, event AssetPinAuditEvent) error {
	if err := s.requireWritable("upsert asset pin reference"); err != nil {
		return err
	}
	if err := requireAssetPinContext(ctx); err != nil {
		return err
	}
	ref, err := prepareAssetPinReference(ref)
	if err != nil {
		return err
	}
	event, err = completeAssetPinAuditEvent(event, ref)
	if err != nil {
		return err
	}
	event, err = bindAssetPinMutationDigest(event, auxiliaryEventAssetPinReferenceUpsert, ref)
	if err != nil {
		return err
	}
	event, err = prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}
	payload := auxiliaryAssetPinReferenceUpsert{Reference: ref, Event: event}
	metadataEvent := auxiliaryMetadataEvent{
		Kind:                    auxiliaryEventAssetPinReferenceUpsert,
		AssetPinReferenceUpsert: &payload,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAuxiliaryAssetFrame(metadataEvent); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin asset pin reference upsert: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	if err := upsertAssetPinReferenceTo(ctx, tx, ref); err != nil {
		return err
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(metadataEvent); err != nil {
		return fmt.Errorf("append asset pin reference upsert metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit asset pin reference upsert: %w", err)
	}
	return nil
}

// TransitionAssetPinReference compare-and-swaps one lifecycle state and writes
// the associated append-only audit event atomically.
func (s *FlatSQLStore) TransitionAssetPinReference(ctx context.Context, transition AssetPinReferenceTransition, event AssetPinAuditEvent) error {
	if err := s.requireWritable("transition asset pin reference"); err != nil {
		return err
	}
	if err := requireAssetPinContext(ctx); err != nil {
		return err
	}
	transition, err := prepareAssetPinReferenceTransition(transition)
	if err != nil {
		return err
	}
	event, err = bindAssetPinMutationDigest(event, auxiliaryEventAssetPinReferenceTransition, transition)
	if err != nil {
		return err
	}
	event.ReferenceKey = firstNonEmpty(event.ReferenceKey, transition.ReferenceKey)
	event, err = prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin asset pin reference transition: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	ref, err := findAssetPinReferenceWhere(ctx, tx, "reference_key = ?", transition.ReferenceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reference %q: %w", transition.ReferenceKey, ErrAssetPinReferenceNotFound)
	}
	if err != nil {
		return fmt.Errorf("find asset pin reference for audit binding: %w", err)
	}
	event, err = completeAssetPinAuditEvent(event, ref)
	if err != nil {
		return err
	}
	payload := auxiliaryAssetPinReferenceTransition{Transition: transition, Event: event}
	metadataEvent := auxiliaryMetadataEvent{
		Kind:                        auxiliaryEventAssetPinReferenceTransition,
		AssetPinReferenceTransition: &payload,
	}
	if err := s.checkAuxiliaryAssetFrame(metadataEvent); err != nil {
		return err
	}
	if err := transitionAssetPinReferenceTo(ctx, tx, transition); err != nil {
		return err
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(metadataEvent); err != nil {
		return fmt.Errorf("append asset pin reference transition metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit asset pin reference transition: %w", err)
	}
	return nil
}

// DeleteExpiredAssetPinReference deletes a finite, expired, non-permanently
// protected reference and retains its audit event. Zero expiry fails safe.
func (s *FlatSQLStore) DeleteExpiredAssetPinReference(ctx context.Context, referenceKey string, event AssetPinAuditEvent) error {
	if err := s.requireWritable("delete expired asset pin reference"); err != nil {
		return err
	}
	if err := requireAssetPinContext(ctx); err != nil {
		return err
	}
	referenceKey = strings.TrimSpace(referenceKey)
	if referenceKey == "" {
		return errors.New("asset pin reference key is required")
	}
	if auditReferenceKey := strings.TrimSpace(event.ReferenceKey); auditReferenceKey != "" && auditReferenceKey != referenceKey {
		return fmt.Errorf("asset pin audit reference key %q does not match mutation %q: %w", auditReferenceKey, referenceKey, ErrAssetPinAuditConflict)
	}
	event.ReferenceKey = referenceKey
	event, err := bindAssetPinMutationDigest(event, auxiliaryEventAssetPinReferenceDelete, auxiliaryAssetPinReferenceDelete{ReferenceKey: referenceKey})
	if err != nil {
		return err
	}
	event, err = prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin expired asset pin reference delete: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	ref, findErr := findAssetPinReferenceWhere(ctx, tx, "reference_key = ?", referenceKey)
	if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
		return fmt.Errorf("find expired asset pin reference: %w", findErr)
	}
	if findErr == nil {
		event, err = completeAssetPinAuditEvent(event, ref)
		if err != nil {
			return err
		}
	}
	payload := auxiliaryAssetPinReferenceDelete{ReferenceKey: referenceKey, Event: event}
	metadataEvent := auxiliaryMetadataEvent{
		Kind:                    auxiliaryEventAssetPinReferenceDelete,
		AssetPinReferenceDelete: &payload,
	}
	if err := s.checkAuxiliaryAssetFrame(metadataEvent); err != nil {
		return err
	}
	if errors.Is(findErr, sql.ErrNoRows) {
		return fmt.Errorf("reference %q: %w", referenceKey, ErrAssetPinReferenceNotFound)
	}
	if !assetPinReferenceExpired(ref, assetPinNow()) {
		return fmt.Errorf("reference %q in state %q: %w", referenceKey, ref.State, ErrAssetPinReferenceNotExpired)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sdn_asset_pin_refs WHERE reference_key = ?`, referenceKey); err != nil {
		return fmt.Errorf("delete expired asset pin reference: %w", err)
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(metadataEvent); err != nil {
		return fmt.Errorf("append asset pin reference delete metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit expired asset pin reference delete: %w", err)
	}
	return nil
}

// AppendAssetPinAuditEvent records a standalone immutable audit event.
func (s *FlatSQLStore) AppendAssetPinAuditEvent(ctx context.Context, event AssetPinAuditEvent) error {
	if err := s.requireWritable("append asset pin audit event"); err != nil {
		return err
	}
	if err := requireAssetPinContext(ctx); err != nil {
		return err
	}
	event, err := prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}
	metadataEvent := auxiliaryMetadataEvent{
		Kind:               auxiliaryEventAssetPinAuditAppend,
		AssetPinAuditEvent: &event,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAuxiliaryAssetFrame(metadataEvent); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin asset pin audit append: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(metadataEvent); err != nil {
		return fmt.Errorf("append asset pin audit metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit asset pin audit append: %w", err)
	}
	return nil
}

// FindAssetBySHA256 returns the most recently updated reference for reusable content.
func (s *FlatSQLStore) FindAssetBySHA256(ctx context.Context, sha256 string) (AssetPinReference, bool, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return AssetPinReference{}, false, err
	}
	sha256 = strings.ToLower(strings.TrimSpace(sha256))
	if !isAssetSHA256(sha256) {
		return AssetPinReference{}, false, errors.New("asset SHA-256 must be lowercase hex")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, err := findAssetPinReferenceWhere(ctx, s.db, "sha256 = ? ORDER BY updated_at DESC, reference_key ASC LIMIT 1", sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetPinReference{}, false, nil
	}
	if err != nil {
		return AssetPinReference{}, false, fmt.Errorf("find asset by SHA-256: %w", err)
	}
	return ref, true, nil
}

// FindAssetPinReference looks up one deterministic reference key.
func (s *FlatSQLStore) FindAssetPinReference(ctx context.Context, referenceKey string) (AssetPinReference, bool, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return AssetPinReference{}, false, err
	}
	referenceKey = strings.TrimSpace(referenceKey)
	if referenceKey == "" {
		return AssetPinReference{}, false, errors.New("asset pin reference key is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, err := findAssetPinReferenceWhere(ctx, s.db, "reference_key = ?", referenceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetPinReference{}, false, nil
	}
	if err != nil {
		return AssetPinReference{}, false, fmt.Errorf("find asset pin reference: %w", err)
	}
	return ref, true, nil
}

// FindAssetPinReferenceByReferenceKey is an explicit alias for composition code.
func (s *FlatSQLStore) FindAssetPinReferenceByReferenceKey(ctx context.Context, referenceKey string) (AssetPinReference, bool, error) {
	return s.FindAssetPinReference(ctx, referenceKey)
}

// FindAssetPinReferenceByCandidateKey looks up the unique reference for a candidate.
func (s *FlatSQLStore) FindAssetPinReferenceByCandidateKey(ctx context.Context, candidateKey string) (AssetPinReference, bool, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return AssetPinReference{}, false, err
	}
	candidateKey = strings.TrimSpace(candidateKey)
	if candidateKey == "" {
		return AssetPinReference{}, false, errors.New("asset pin candidate key is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, err := findAssetPinReferenceWhere(ctx, s.db, "candidate_key = ?", candidateKey)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetPinReference{}, false, nil
	}
	if err != nil {
		return AssetPinReference{}, false, fmt.Errorf("find asset pin reference by candidate key: %w", err)
	}
	return ref, true, nil
}

// ListAssetPinReferences lists references for retention and reconciliation.
func (s *FlatSQLStore) ListAssetPinReferences(ctx context.Context, query AssetPinReferenceQuery) ([]AssetPinReference, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return nil, err
	}
	query = normalizeAssetPinReferenceQuery(query)
	if query.State != "" && !validAssetReferenceState(query.State) {
		return nil, fmt.Errorf("unknown asset reference state %q", query.State)
	}
	if query.Limit < 0 {
		return nil, errors.New("asset pin reference limit must be non-negative")
	}
	where := []string{"1 = 1"}
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			where = append(where, column+" = ?")
			args = append(args, value)
		}
	}
	add("reference_key", query.ReferenceKey)
	add("candidate_key", query.CandidateKey)
	add("cid", query.CID)
	add("sha256", query.SHA256)
	add("state", string(query.State))
	statement := `SELECT ` + assetPinReferenceSelectColumns + `
		FROM sdn_asset_pin_refs WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY updated_at DESC, reference_key ASC`
	if query.Limit > 0 {
		statement += " LIMIT ?"
		args = append(args, query.Limit)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return queryAssetPinReferences(ctx, s.db, statement, args...)
}

// ListExpiredAssetPinReferences returns finite expired references eligible for deletion.
func (s *FlatSQLStore) ListExpiredAssetPinReferences(ctx context.Context, now time.Time) ([]AssetPinReference, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return nil, err
	}
	if now.IsZero() {
		return nil, errors.New("asset pin expiry cutoff is required")
	}
	now = normalizeAssetPinTime(now)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return queryAssetPinReferences(ctx, s.db, `SELECT `+assetPinReferenceSelectColumns+`
		FROM sdn_asset_pin_refs
		WHERE state IN ('staged', 'rejected', 'superseded', 'abandoned')
		  AND expires_at IS NOT NULL
		  AND expires_at <= ?
		ORDER BY expires_at ASC, reference_key ASC`, assetPinTimestamp(now))
}

// CountProtectedAssetReferences reports independent live owners of a CID.
// approved/review_open are permanent; staged/rejected/superseded fail safe on
// zero expiry and remain protected strictly through a future expiry.
func (s *FlatSQLStore) CountProtectedAssetReferences(ctx context.Context, cid string, now time.Time) (int, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return 0, err
	}
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return 0, errors.New("asset CID is required")
	}
	if now.IsZero() {
		return 0, errors.New("asset protection cutoff is required")
	}
	now = normalizeAssetPinTime(now)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sdn_asset_pin_refs
		WHERE cid = ?
		  AND (
			state IN ('approved', 'review_open')
			OR (
				state IN ('staged', 'rejected', 'superseded')
				AND (expires_at IS NULL OR expires_at > ?)
			)
		  )
	`, cid, assetPinTimestamp(now)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count protected asset pin references: %w", err)
	}
	return count, nil
}

// ListAssetPinAuditEvents lists immutable audit events in occurrence order.
func (s *FlatSQLStore) ListAssetPinAuditEvents(ctx context.Context, query AssetPinAuditEventQuery) ([]AssetPinAuditEvent, error) {
	if err := requireAssetPinContext(ctx); err != nil {
		return nil, err
	}
	query = normalizeAssetPinAuditEventQuery(query)
	if query.Limit < 0 {
		return nil, errors.New("asset pin audit limit must be non-negative")
	}
	where := []string{"1 = 1"}
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			where = append(where, column+" = ?")
			args = append(args, value)
		}
	}
	add("event_id", query.EventID)
	add("event_kind", query.Kind)
	add("candidate_key", query.CandidateKey)
	add("reference_key", query.ReferenceKey)
	add("cid", query.CID)
	add("sha256", query.SHA256)
	statement := `SELECT ` + assetPinAuditSelectColumns + `
		FROM sdn_asset_pin_events WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY occurred_at ASC, event_id ASC`
	if query.Limit > 0 {
		statement += " LIMIT ?"
		args = append(args, query.Limit)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list asset pin audit events: %w", err)
	}
	defer rows.Close()
	var events []AssetPinAuditEvent
	for rows.Next() {
		event, err := scanAssetPinAuditEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset pin audit event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list asset pin audit event rows: %w", err)
	}
	return events, nil
}

func (s *FlatSQLStore) applyAssetOIDCReceiptConsume(receipt AssetOIDCReceipt) error {
	receipt, err := prepareAssetOIDCReceipt(receipt)
	if err != nil {
		return err
	}
	ctx := context.Background()
	inserted, err := insertAssetOIDCReceipt(ctx, s.db, receipt)
	if err != nil || inserted {
		return err
	}
	existing, err := findAssetOIDCReceiptTo(ctx, s.db, receipt.Digest)
	if err != nil {
		return fmt.Errorf("read replayed asset OIDC receipt: %w", err)
	}
	if !equalAssetOIDCReceipt(existing, receipt) {
		return fmt.Errorf("digest %s: %w", receipt.Digest, ErrAssetOIDCReceiptConflict)
	}
	return nil
}

func (s *FlatSQLStore) applyAssetPinReferenceUpsert(payload auxiliaryAssetPinReferenceUpsert) error {
	ref, err := prepareAssetPinReference(payload.Reference)
	if err != nil {
		return err
	}
	event, err := completeAssetPinAuditEvent(payload.Event, ref)
	if err != nil {
		return err
	}
	event, err = bindAssetPinMutationDigest(event, auxiliaryEventAssetPinReferenceUpsert, ref)
	if err != nil {
		return err
	}
	event, err = prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replayed asset pin upsert: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	if err := upsertAssetPinReferenceTo(ctx, tx, ref); err != nil {
		return err
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FlatSQLStore) applyAssetPinReferenceTransition(payload auxiliaryAssetPinReferenceTransition) error {
	transition, err := prepareAssetPinReferenceTransition(payload.Transition)
	if err != nil {
		return err
	}
	event, err := bindAssetPinMutationDigest(payload.Event, auxiliaryEventAssetPinReferenceTransition, transition)
	if err != nil {
		return err
	}
	event, err = prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replayed asset pin transition: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	ref, err := findAssetPinReferenceWhere(ctx, tx, "reference_key = ?", transition.ReferenceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reference %q: %w", transition.ReferenceKey, ErrAssetPinReferenceNotFound)
	}
	if err != nil {
		return fmt.Errorf("find replayed asset pin reference for audit binding: %w", err)
	}
	event, err = completeAssetPinAuditEvent(event, ref)
	if err != nil {
		return err
	}
	if err := transitionAssetPinReferenceTo(ctx, tx, transition); err != nil {
		return err
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FlatSQLStore) applyAssetPinReferenceDelete(payload auxiliaryAssetPinReferenceDelete) error {
	payload.ReferenceKey = strings.TrimSpace(payload.ReferenceKey)
	if payload.ReferenceKey == "" {
		return errors.New("replayed asset pin reference delete is missing reference key")
	}
	if auditReferenceKey := strings.TrimSpace(payload.Event.ReferenceKey); auditReferenceKey != "" && auditReferenceKey != payload.ReferenceKey {
		return fmt.Errorf("asset pin audit reference key %q does not match mutation %q: %w", auditReferenceKey, payload.ReferenceKey, ErrAssetPinAuditConflict)
	}
	payload.Event.ReferenceKey = payload.ReferenceKey
	event, err := bindAssetPinMutationDigest(payload.Event, auxiliaryEventAssetPinReferenceDelete, auxiliaryAssetPinReferenceDelete{ReferenceKey: payload.ReferenceKey})
	if err != nil {
		return err
	}
	event, err = prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replayed asset pin delete: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	ref, findErr := findAssetPinReferenceWhere(ctx, tx, "reference_key = ?", payload.ReferenceKey)
	if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
		return fmt.Errorf("find replayed asset pin reference for audit binding: %w", findErr)
	}
	if findErr == nil {
		event, err = completeAssetPinAuditEvent(event, ref)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sdn_asset_pin_refs WHERE reference_key = ?`, payload.ReferenceKey); err != nil {
		return fmt.Errorf("replay asset pin reference delete: %w", err)
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FlatSQLStore) applyAssetPinAuditAppend(event AssetPinAuditEvent) error {
	event, err := prepareAssetPinAuditEvent(event)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replayed asset pin audit append: %w", err)
	}
	defer tx.Rollback()
	if same, err := assetPinAuditEventAlreadyApplied(ctx, tx, event); err != nil {
		return err
	} else if same {
		return nil
	}
	if _, err := insertAssetPinAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

type assetPinExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type assetPinQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type assetPinQueryExecer interface {
	assetPinExecer
	assetPinQueryer
}

func insertAssetOIDCReceipt(ctx context.Context, exec assetPinExecer, receipt AssetOIDCReceipt) (bool, error) {
	result, err := exec.ExecContext(ctx, `
		INSERT INTO sdn_asset_oidc_receipts (
			digest, expires_at, repository, git_ref, workflow_ref,
			actor, run_id, run_attempt, commit_sha, consumed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(digest) DO NOTHING
	`, receipt.Digest, assetPinTimestamp(receipt.ExpiresAt), receipt.Repository, receipt.Ref,
		receipt.WorkflowRef, receipt.Actor, receipt.RunID, receipt.RunAttempt,
		receipt.SHA, assetPinTimestamp(receipt.ConsumedAt))
	if err != nil {
		return false, fmt.Errorf("insert asset OIDC receipt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read asset OIDC receipt insert result: %w", err)
	}
	return rows == 1, nil
}

func findAssetOIDCReceiptTo(ctx context.Context, queryer assetPinQueryer, digest string) (AssetOIDCReceipt, error) {
	var receipt AssetOIDCReceipt
	var expiresAt, consumedAt string
	err := queryer.QueryRowContext(ctx, `
		SELECT digest, expires_at, repository, git_ref, workflow_ref,
		       actor, run_id, run_attempt, commit_sha, consumed_at
		FROM sdn_asset_oidc_receipts WHERE digest = ?
	`, digest).Scan(&receipt.Digest, &expiresAt, &receipt.Repository, &receipt.Ref,
		&receipt.WorkflowRef, &receipt.Actor, &receipt.RunID, &receipt.RunAttempt,
		&receipt.SHA, &consumedAt)
	if err != nil {
		return AssetOIDCReceipt{}, err
	}
	receipt.ExpiresAt, err = assetPinTimeFromTimestamp(expiresAt)
	if err != nil {
		return AssetOIDCReceipt{}, fmt.Errorf("decode asset OIDC receipt expiry: %w", err)
	}
	receipt.ConsumedAt, err = assetPinTimeFromTimestamp(consumedAt)
	if err != nil {
		return AssetOIDCReceipt{}, fmt.Errorf("decode asset OIDC receipt consumed_at: %w", err)
	}
	return receipt, nil
}

func upsertAssetPinReferenceTo(ctx context.Context, exec assetPinQueryExecer, ref AssetPinReference) error {
	existing, err := findAssetPinReferenceWhere(ctx, exec, "reference_key = ?", ref.ReferenceKey)
	if err == nil {
		if equalAssetPinReference(existing, ref) {
			return nil
		}
		return fmt.Errorf("reference key %q already has different persisted data: %w", ref.ReferenceKey, ErrAssetPinReferenceConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing asset pin reference: %w", err)
	}
	var owner string
	err = exec.QueryRowContext(ctx, `SELECT reference_key FROM sdn_asset_pin_refs WHERE candidate_key = ?`, ref.CandidateKey).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check asset candidate key ownership: %w", err)
	}
	if err == nil && owner != ref.ReferenceKey {
		return fmt.Errorf("candidate key %q belongs to reference %q: %w", ref.CandidateKey, owner, ErrAssetPinReferenceConflict)
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO sdn_asset_pin_refs (
			reference_key, candidate_key, cid, sha256, byte_count, state,
			source_url, license_name, attribution, metadata_json,
			github_issue, workflow_run_id, decision_sha256,
			created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ref.ReferenceKey, ref.CandidateKey, ref.CID, ref.SHA256, ref.ByteCount,
		string(ref.State), ref.SourceURL, ref.LicenseName, ref.Attribution,
		ref.MetadataJSON, ref.GitHubIssue, ref.WorkflowRunID, ref.DecisionSHA256,
		assetPinTimestamp(ref.CreatedAt), assetPinTimestamp(ref.UpdatedAt), nullableAssetPinTime(ref.ExpiresAt))
	if err != nil {
		return fmt.Errorf("upsert asset pin reference: %w", err)
	}
	return nil
}

func equalAssetPinReference(left, right AssetPinReference) bool {
	return left.ReferenceKey == right.ReferenceKey &&
		left.CandidateKey == right.CandidateKey &&
		left.CID == right.CID &&
		left.SHA256 == right.SHA256 &&
		left.ByteCount == right.ByteCount &&
		left.State == right.State &&
		left.SourceURL == right.SourceURL &&
		left.LicenseName == right.LicenseName &&
		left.Attribution == right.Attribution &&
		left.MetadataJSON == right.MetadataJSON &&
		left.GitHubIssue == right.GitHubIssue &&
		left.WorkflowRunID == right.WorkflowRunID &&
		left.DecisionSHA256 == right.DecisionSHA256 &&
		left.CreatedAt.Equal(right.CreatedAt) &&
		left.UpdatedAt.Equal(right.UpdatedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

func transitionAssetPinReferenceTo(ctx context.Context, exec assetPinQueryExecer, transition AssetPinReferenceTransition) error {
	ref, err := findAssetPinReferenceWhere(ctx, exec, "reference_key = ?", transition.ReferenceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reference %q: %w", transition.ReferenceKey, ErrAssetPinReferenceNotFound)
	}
	if err != nil {
		return fmt.Errorf("find asset pin reference for transition: %w", err)
	}
	if ref.State != transition.FromState {
		return fmt.Errorf("reference %q state is %q, expected %q: %w", transition.ReferenceKey, ref.State, transition.FromState, ErrAssetPinReferenceConflict)
	}
	if transition.UpdatedAt.Before(ref.CreatedAt) {
		return errors.New("asset pin transition updated_at precedes reference created_at")
	}
	if transition.UpdatedAt.Before(ref.UpdatedAt) {
		return fmt.Errorf("reference %q transition updated_at precedes current updated_at: %w", transition.ReferenceKey, ErrAssetPinReferenceConflict)
	}
	decisionSHA256 := transition.DecisionSHA256
	if decisionSHA256 == "" {
		decisionSHA256 = ref.DecisionSHA256
	}
	githubIssue := transition.GitHubIssue
	if githubIssue == 0 {
		githubIssue = ref.GitHubIssue
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE sdn_asset_pin_refs
		SET state = ?, github_issue = ?, decision_sha256 = ?, updated_at = ?, expires_at = ?
		WHERE reference_key = ? AND state = ?
	`, string(transition.ToState), githubIssue, decisionSHA256,
		assetPinTimestamp(transition.UpdatedAt), nullableAssetPinTime(transition.ExpiresAt),
		transition.ReferenceKey, string(transition.FromState))
	if err != nil {
		return fmt.Errorf("transition asset pin reference: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read asset pin transition result: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("reference %q changed concurrently: %w", transition.ReferenceKey, ErrAssetPinReferenceConflict)
	}
	return nil
}

func insertAssetPinAuditEvent(ctx context.Context, exec assetPinQueryExecer, event AssetPinAuditEvent) (bool, error) {
	result, err := exec.ExecContext(ctx, `
		INSERT INTO sdn_asset_pin_events (
			event_id, mutation_digest, event_kind, result, token_digest,
			repository, git_ref, workflow_ref, actor, workflow_run_id,
			run_attempt, commit_sha, candidate_key, reference_key,
			cid, sha256, byte_count, detail, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id) DO NOTHING
	`, event.EventID, event.MutationDigest, event.Kind, event.Result, event.TokenDigest,
		event.Repository, event.Ref, event.WorkflowRef, event.Actor, event.WorkflowRunID,
		event.RunAttempt, event.CommitSHA, event.CandidateKey, event.ReferenceKey,
		event.CID, event.SHA256, event.ByteCount, event.Detail, assetPinTimestamp(event.OccurredAt))
	if err != nil {
		return false, fmt.Errorf("insert asset pin audit event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read asset pin audit insert result: %w", err)
	}
	if rows == 1 {
		return true, nil
	}
	existing, err := findAssetPinAuditEventTo(ctx, exec, event.EventID)
	if err != nil {
		return false, fmt.Errorf("find existing asset pin audit event: %w", err)
	}
	if !equalAssetPinAuditEvent(existing, event) {
		return false, fmt.Errorf("event ID %q: %w", event.EventID, ErrAssetPinAuditConflict)
	}
	return false, nil
}

func assetPinAuditEventAlreadyApplied(ctx context.Context, queryer assetPinQueryer, event AssetPinAuditEvent) (bool, error) {
	existing, err := findAssetPinAuditEventTo(ctx, queryer, event.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find existing asset pin audit event: %w", err)
	}
	candidate := event
	if candidate.MutationDigest != "" {
		if candidate.CandidateKey == "" {
			candidate.CandidateKey = existing.CandidateKey
		}
		if candidate.ReferenceKey == "" {
			candidate.ReferenceKey = existing.ReferenceKey
		}
		if candidate.CID == "" {
			candidate.CID = existing.CID
		}
		if candidate.SHA256 == "" {
			candidate.SHA256 = existing.SHA256
		}
		if candidate.ByteCount == 0 {
			candidate.ByteCount = existing.ByteCount
		}
		if candidate.WorkflowRunID == "" {
			candidate.WorkflowRunID = existing.WorkflowRunID
		}
	}
	if !equalAssetPinAuditEvent(existing, candidate) {
		return false, fmt.Errorf("event ID %q: %w", event.EventID, ErrAssetPinAuditConflict)
	}
	return true, nil
}

func findAssetPinReferenceWhere(ctx context.Context, queryer assetPinQueryer, where string, args ...any) (AssetPinReference, error) {
	row := queryer.QueryRowContext(ctx, `SELECT `+assetPinReferenceSelectColumns+`
		FROM sdn_asset_pin_refs WHERE `+where, args...)
	return scanAssetPinReference(row)
}

type assetPinRowsQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryAssetPinReferences(ctx context.Context, queryer assetPinRowsQueryer, statement string, args ...any) ([]AssetPinReference, error) {
	rows, err := queryer.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list asset pin references: %w", err)
	}
	defer rows.Close()
	var refs []AssetPinReference
	for rows.Next() {
		ref, err := scanAssetPinReference(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset pin reference: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list asset pin reference rows: %w", err)
	}
	return refs, nil
}

type assetPinScanner interface {
	Scan(...any) error
}

func scanAssetPinReference(scanner assetPinScanner) (AssetPinReference, error) {
	var ref AssetPinReference
	var state string
	var createdAt, updatedAt string
	var expiresAt sql.NullString
	if err := scanner.Scan(
		&ref.ReferenceKey, &ref.CandidateKey, &ref.CID, &ref.SHA256,
		&ref.ByteCount, &state, &ref.SourceURL, &ref.LicenseName,
		&ref.Attribution, &ref.MetadataJSON, &ref.GitHubIssue,
		&ref.WorkflowRunID, &ref.DecisionSHA256, &createdAt, &updatedAt,
		&expiresAt,
	); err != nil {
		return AssetPinReference{}, err
	}
	ref.State = AssetReferenceState(state)
	if !validAssetReferenceState(ref.State) {
		return AssetPinReference{}, fmt.Errorf("stored asset pin reference has unknown state %q", state)
	}
	var err error
	ref.CreatedAt, err = assetPinTimeFromTimestamp(createdAt)
	if err != nil {
		return AssetPinReference{}, fmt.Errorf("decode asset pin reference created_at: %w", err)
	}
	ref.UpdatedAt, err = assetPinTimeFromTimestamp(updatedAt)
	if err != nil {
		return AssetPinReference{}, fmt.Errorf("decode asset pin reference updated_at: %w", err)
	}
	if expiresAt.Valid {
		ref.ExpiresAt, err = assetPinTimeFromTimestamp(expiresAt.String)
		if err != nil {
			return AssetPinReference{}, fmt.Errorf("decode asset pin reference expires_at: %w", err)
		}
	}
	return ref, nil
}

func findAssetPinAuditEventTo(ctx context.Context, queryer assetPinQueryer, eventID string) (AssetPinAuditEvent, error) {
	row := queryer.QueryRowContext(ctx, `SELECT `+assetPinAuditSelectColumns+`
		FROM sdn_asset_pin_events WHERE event_id = ?`, eventID)
	return scanAssetPinAuditEvent(row)
}

func scanAssetPinAuditEvent(scanner assetPinScanner) (AssetPinAuditEvent, error) {
	var event AssetPinAuditEvent
	var occurredAt string
	if err := scanner.Scan(
		&event.EventID, &event.MutationDigest, &event.Kind, &event.Result, &event.TokenDigest,
		&event.Repository, &event.Ref, &event.WorkflowRef, &event.Actor,
		&event.WorkflowRunID, &event.RunAttempt, &event.CommitSHA,
		&event.CandidateKey, &event.ReferenceKey, &event.CID,
		&event.SHA256, &event.ByteCount, &event.Detail, &occurredAt,
	); err != nil {
		return AssetPinAuditEvent{}, err
	}
	var err error
	event.OccurredAt, err = assetPinTimeFromTimestamp(occurredAt)
	if err != nil {
		return AssetPinAuditEvent{}, fmt.Errorf("decode asset pin audit occurred_at: %w", err)
	}
	return event, nil
}

func prepareAssetPinReference(ref AssetPinReference) (AssetPinReference, error) {
	if err := validateAssetPinPersistedTime("asset pin reference created_at", ref.CreatedAt, false); err != nil {
		return AssetPinReference{}, err
	}
	if err := validateAssetPinPersistedTime("asset pin reference updated_at", ref.UpdatedAt, false); err != nil {
		return AssetPinReference{}, err
	}
	if err := validateAssetPinPersistedTime("asset pin reference expires_at", ref.ExpiresAt, true); err != nil {
		return AssetPinReference{}, err
	}
	ref.ReferenceKey = strings.TrimSpace(ref.ReferenceKey)
	ref.CandidateKey = strings.TrimSpace(ref.CandidateKey)
	ref.CID = strings.TrimSpace(ref.CID)
	ref.SHA256 = strings.ToLower(strings.TrimSpace(ref.SHA256))
	ref.State = AssetReferenceState(strings.TrimSpace(string(ref.State)))
	ref.SourceURL = strings.TrimSpace(ref.SourceURL)
	ref.LicenseName = strings.TrimSpace(ref.LicenseName)
	ref.Attribution = strings.TrimSpace(ref.Attribution)
	ref.WorkflowRunID = strings.TrimSpace(ref.WorkflowRunID)
	ref.DecisionSHA256 = strings.ToLower(strings.TrimSpace(ref.DecisionSHA256))
	if ref.ReferenceKey == "" {
		return AssetPinReference{}, errors.New("asset pin reference key is required")
	}
	if ref.CandidateKey == "" {
		return AssetPinReference{}, errors.New("asset pin candidate key is required")
	}
	if ref.CID == "" {
		return AssetPinReference{}, errors.New("asset CID is required")
	}
	if !isAssetSHA256(ref.SHA256) {
		return AssetPinReference{}, errors.New("asset SHA-256 must be lowercase hex")
	}
	if ref.ByteCount < 0 {
		return AssetPinReference{}, errors.New("asset byte count must be non-negative")
	}
	if !validAssetReferenceState(ref.State) {
		return AssetPinReference{}, fmt.Errorf("unknown asset reference state %q", ref.State)
	}
	if ref.GitHubIssue < 0 {
		return AssetPinReference{}, errors.New("asset GitHub issue must be non-negative")
	}
	if ref.DecisionSHA256 != "" && !isAssetSHA256(ref.DecisionSHA256) {
		return AssetPinReference{}, errors.New("asset decision SHA-256 must be lowercase hex")
	}
	canonical, err := canonicalAssetMetadataJSON(ref.MetadataJSON)
	if err != nil {
		return AssetPinReference{}, err
	}
	ref.MetadataJSON = canonical
	ref.CreatedAt = normalizeAssetPinTime(ref.CreatedAt)
	ref.UpdatedAt = normalizeAssetPinTime(ref.UpdatedAt)
	ref.ExpiresAt = normalizeAssetPinTime(ref.ExpiresAt)
	if ref.CreatedAt.IsZero() || ref.UpdatedAt.IsZero() {
		return AssetPinReference{}, errors.New("asset pin reference created_at and updated_at are required")
	}
	if ref.UpdatedAt.Before(ref.CreatedAt) {
		return AssetPinReference{}, errors.New("asset pin reference updated_at precedes created_at")
	}
	if !ref.ExpiresAt.IsZero() && ref.ExpiresAt.Before(ref.UpdatedAt) {
		return AssetPinReference{}, errors.New("asset pin reference expires_at precedes updated_at")
	}
	return ref, nil
}

func prepareAssetPinReferenceTransition(transition AssetPinReferenceTransition) (AssetPinReferenceTransition, error) {
	if err := validateAssetPinPersistedTime("asset pin transition updated_at", transition.UpdatedAt, false); err != nil {
		return AssetPinReferenceTransition{}, err
	}
	if err := validateAssetPinPersistedTime("asset pin transition expires_at", transition.ExpiresAt, true); err != nil {
		return AssetPinReferenceTransition{}, err
	}
	transition.ReferenceKey = strings.TrimSpace(transition.ReferenceKey)
	transition.FromState = AssetReferenceState(strings.TrimSpace(string(transition.FromState)))
	transition.ToState = AssetReferenceState(strings.TrimSpace(string(transition.ToState)))
	transition.DecisionSHA256 = strings.ToLower(strings.TrimSpace(transition.DecisionSHA256))
	if transition.ReferenceKey == "" {
		return AssetPinReferenceTransition{}, errors.New("asset pin transition reference key is required")
	}
	if !validAssetReferenceState(transition.FromState) {
		return AssetPinReferenceTransition{}, fmt.Errorf("unknown asset reference state %q", transition.FromState)
	}
	if !validAssetReferenceState(transition.ToState) {
		return AssetPinReferenceTransition{}, fmt.Errorf("unknown asset reference state %q", transition.ToState)
	}
	if transition.FromState == transition.ToState {
		return AssetPinReferenceTransition{}, errors.New("asset pin transition must change state")
	}
	if transition.GitHubIssue < 0 {
		return AssetPinReferenceTransition{}, errors.New("asset pin transition GitHub issue must be non-negative")
	}
	if transition.DecisionSHA256 != "" && !isAssetSHA256(transition.DecisionSHA256) {
		return AssetPinReferenceTransition{}, errors.New("asset decision SHA-256 must be lowercase hex")
	}
	transition.UpdatedAt = normalizeAssetPinTime(transition.UpdatedAt)
	transition.ExpiresAt = normalizeAssetPinTime(transition.ExpiresAt)
	if transition.UpdatedAt.IsZero() {
		return AssetPinReferenceTransition{}, errors.New("asset pin transition updated_at is required")
	}
	if !transition.ExpiresAt.IsZero() && transition.ExpiresAt.Before(transition.UpdatedAt) {
		return AssetPinReferenceTransition{}, errors.New("asset pin transition expires_at precedes updated_at")
	}
	return transition, nil
}

func prepareAssetPinAuditEvent(event AssetPinAuditEvent) (AssetPinAuditEvent, error) {
	if err := validateAssetPinPersistedTime("asset pin audit occurred_at", event.OccurredAt, false); err != nil {
		return AssetPinAuditEvent{}, err
	}
	event.EventID = strings.TrimSpace(event.EventID)
	event.MutationDigest = strings.ToLower(strings.TrimSpace(event.MutationDigest))
	event.Kind = strings.TrimSpace(event.Kind)
	event.Result = strings.TrimSpace(event.Result)
	event.TokenDigest = strings.ToLower(strings.TrimSpace(event.TokenDigest))
	event.Repository = strings.TrimSpace(event.Repository)
	event.Ref = strings.TrimSpace(event.Ref)
	event.WorkflowRef = strings.TrimSpace(event.WorkflowRef)
	event.Actor = strings.TrimSpace(event.Actor)
	event.WorkflowRunID = strings.TrimSpace(event.WorkflowRunID)
	event.RunAttempt = strings.TrimSpace(event.RunAttempt)
	event.CommitSHA = strings.TrimSpace(event.CommitSHA)
	event.CandidateKey = strings.TrimSpace(event.CandidateKey)
	event.ReferenceKey = strings.TrimSpace(event.ReferenceKey)
	event.CID = strings.TrimSpace(event.CID)
	event.SHA256 = strings.ToLower(strings.TrimSpace(event.SHA256))
	event.Detail = strings.TrimSpace(event.Detail)
	if event.EventID == "" {
		return AssetPinAuditEvent{}, errors.New("asset pin audit event ID is required")
	}
	if event.Kind == "" {
		return AssetPinAuditEvent{}, errors.New("asset pin audit event kind is required")
	}
	if event.MutationDigest != "" && !isAssetSHA256(event.MutationDigest) {
		return AssetPinAuditEvent{}, errors.New("asset pin audit mutation digest must be lowercase SHA-256 hex")
	}
	if event.Result == "" {
		return AssetPinAuditEvent{}, errors.New("asset pin audit event result is required")
	}
	if event.TokenDigest != "" && !isAssetSHA256(event.TokenDigest) {
		return AssetPinAuditEvent{}, errors.New("asset pin audit token digest must be lowercase SHA-256 hex")
	}
	if event.SHA256 != "" && !isAssetSHA256(event.SHA256) {
		return AssetPinAuditEvent{}, errors.New("asset pin audit SHA-256 must be lowercase hex")
	}
	if event.ByteCount < 0 {
		return AssetPinAuditEvent{}, errors.New("asset pin audit byte count must be non-negative")
	}
	event.OccurredAt = normalizeAssetPinTime(event.OccurredAt)
	if event.OccurredAt.IsZero() {
		return AssetPinAuditEvent{}, errors.New("asset pin audit occurred_at is required")
	}
	return event, nil
}

func completeAssetPinAuditEvent(event AssetPinAuditEvent, ref AssetPinReference) (AssetPinAuditEvent, error) {
	identities := []struct {
		name string
		got  *string
		want string
	}{
		{name: "candidate key", got: &event.CandidateKey, want: ref.CandidateKey},
		{name: "reference key", got: &event.ReferenceKey, want: ref.ReferenceKey},
		{name: "CID", got: &event.CID, want: ref.CID},
		{name: "SHA-256", got: &event.SHA256, want: ref.SHA256},
	}
	for _, identity := range identities {
		value := strings.TrimSpace(*identity.got)
		want := strings.TrimSpace(identity.want)
		if identity.name == "SHA-256" {
			value = strings.ToLower(value)
			want = strings.ToLower(want)
		}
		if value != "" && value != want {
			return AssetPinAuditEvent{}, fmt.Errorf("asset pin audit %s %q does not match mutation %q: %w", identity.name, value, want, ErrAssetPinAuditConflict)
		}
		*identity.got = want
	}
	if event.ByteCount != 0 && event.ByteCount != ref.ByteCount {
		return AssetPinAuditEvent{}, fmt.Errorf("asset pin audit byte count %d does not match mutation %d: %w", event.ByteCount, ref.ByteCount, ErrAssetPinAuditConflict)
	}
	if event.ByteCount == 0 {
		event.ByteCount = ref.ByteCount
	}
	if event.WorkflowRunID == "" {
		event.WorkflowRunID = ref.WorkflowRunID
	}
	return event, nil
}

func bindAssetPinMutationDigest(event AssetPinAuditEvent, kind string, payload any) (AssetPinAuditEvent, error) {
	encoded, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Payload any    `json:"payload"`
	}{Kind: kind, Payload: payload})
	if err != nil {
		return AssetPinAuditEvent{}, fmt.Errorf("encode asset pin mutation digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	want := hex.EncodeToString(sum[:])
	got := strings.ToLower(strings.TrimSpace(event.MutationDigest))
	if got != "" && got != want {
		return AssetPinAuditEvent{}, fmt.Errorf("asset pin audit mutation digest %q does not match operation %q: %w", got, want, ErrAssetPinAuditConflict)
	}
	event.MutationDigest = want
	return event, nil
}

func normalizeAssetOIDCReceipt(receipt AssetOIDCReceipt) AssetOIDCReceipt {
	receipt.Digest = strings.ToLower(strings.TrimSpace(receipt.Digest))
	receipt.Repository = strings.TrimSpace(receipt.Repository)
	receipt.Ref = strings.TrimSpace(receipt.Ref)
	receipt.WorkflowRef = strings.TrimSpace(receipt.WorkflowRef)
	receipt.Actor = strings.TrimSpace(receipt.Actor)
	receipt.RunID = strings.TrimSpace(receipt.RunID)
	receipt.RunAttempt = strings.TrimSpace(receipt.RunAttempt)
	receipt.SHA = strings.TrimSpace(receipt.SHA)
	receipt.ExpiresAt = normalizeAssetPinTime(receipt.ExpiresAt)
	receipt.ConsumedAt = normalizeAssetPinTime(receipt.ConsumedAt)
	return receipt
}

func prepareAssetOIDCReceipt(receipt AssetOIDCReceipt) (AssetOIDCReceipt, error) {
	if err := validateAssetPinPersistedTime("asset OIDC receipt expires_at", receipt.ExpiresAt, false); err != nil {
		return AssetOIDCReceipt{}, err
	}
	if err := validateAssetPinPersistedTime("asset OIDC receipt consumed_at", receipt.ConsumedAt, false); err != nil {
		return AssetOIDCReceipt{}, err
	}
	receipt = normalizeAssetOIDCReceipt(receipt)
	if err := validateAssetOIDCReceipt(receipt); err != nil {
		return AssetOIDCReceipt{}, err
	}
	return receipt, nil
}

func validateAssetOIDCReceipt(receipt AssetOIDCReceipt) error {
	if !isAssetSHA256(receipt.Digest) {
		return errors.New("asset OIDC token digest must be lowercase SHA-256 hex")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "repository", value: receipt.Repository},
		{name: "ref", value: receipt.Ref},
		{name: "workflow", value: receipt.WorkflowRef},
		{name: "actor", value: receipt.Actor},
		{name: "run ID", value: receipt.RunID},
		{name: "run attempt", value: receipt.RunAttempt},
		{name: "commit SHA", value: receipt.SHA},
	} {
		if field.value == "" {
			return fmt.Errorf("asset OIDC receipt %s is required", field.name)
		}
	}
	if receipt.ExpiresAt.IsZero() || receipt.ConsumedAt.IsZero() {
		return errors.New("asset OIDC receipt expiry and consumed_at are required")
	}
	if !receipt.ExpiresAt.After(receipt.ConsumedAt) {
		return errors.New("asset OIDC receipt expiry must follow consumed_at")
	}
	return nil
}

func normalizeAssetPinReferenceQuery(query AssetPinReferenceQuery) AssetPinReferenceQuery {
	query.ReferenceKey = strings.TrimSpace(query.ReferenceKey)
	query.CandidateKey = strings.TrimSpace(query.CandidateKey)
	query.CID = strings.TrimSpace(query.CID)
	query.SHA256 = strings.ToLower(strings.TrimSpace(query.SHA256))
	query.State = AssetReferenceState(strings.TrimSpace(string(query.State)))
	return query
}

func normalizeAssetPinAuditEventQuery(query AssetPinAuditEventQuery) AssetPinAuditEventQuery {
	query.EventID = strings.TrimSpace(query.EventID)
	query.Kind = strings.TrimSpace(query.Kind)
	query.CandidateKey = strings.TrimSpace(query.CandidateKey)
	query.ReferenceKey = strings.TrimSpace(query.ReferenceKey)
	query.CID = strings.TrimSpace(query.CID)
	query.SHA256 = strings.ToLower(strings.TrimSpace(query.SHA256))
	return query
}

func validAssetReferenceState(state AssetReferenceState) bool {
	switch state {
	case AssetReferenceStaged,
		AssetReferenceReviewOpen,
		AssetReferenceApproved,
		AssetReferenceRejected,
		AssetReferenceSuperseded,
		AssetReferenceAbandoned:
		return true
	default:
		return false
	}
}

func assetPinReferenceExpired(ref AssetPinReference, now time.Time) bool {
	if ref.ExpiresAt.IsZero() || ref.ExpiresAt.After(now) {
		return false
	}
	switch ref.State {
	case AssetReferenceStaged, AssetReferenceRejected, AssetReferenceSuperseded, AssetReferenceAbandoned:
		return true
	case AssetReferenceReviewOpen, AssetReferenceApproved:
		return false
	default:
		return false
	}
}

func canonicalAssetMetadataJSON(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("asset metadata_json is invalid: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("asset metadata_json contains multiple values")
		}
		return "", fmt.Errorf("asset metadata_json trailing data: %w", err)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("canonicalize asset metadata_json: %w", err)
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

func isAssetSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func equalAssetOIDCReceipt(left, right AssetOIDCReceipt) bool {
	left = normalizeAssetOIDCReceipt(left)
	right = normalizeAssetOIDCReceipt(right)
	return left.Digest == right.Digest &&
		left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.Repository == right.Repository &&
		left.Ref == right.Ref &&
		left.WorkflowRef == right.WorkflowRef &&
		left.Actor == right.Actor &&
		left.RunID == right.RunID &&
		left.RunAttempt == right.RunAttempt &&
		left.SHA == right.SHA &&
		left.ConsumedAt.Equal(right.ConsumedAt)
}

func equalAssetPinAuditEvent(left, right AssetPinAuditEvent) bool {
	left = normalizeAssetPinAuditForComparison(left)
	right = normalizeAssetPinAuditForComparison(right)
	return left.EventID == right.EventID &&
		left.MutationDigest == right.MutationDigest &&
		left.Kind == right.Kind &&
		left.Result == right.Result &&
		left.TokenDigest == right.TokenDigest &&
		left.Repository == right.Repository &&
		left.Ref == right.Ref &&
		left.WorkflowRef == right.WorkflowRef &&
		left.Actor == right.Actor &&
		left.WorkflowRunID == right.WorkflowRunID &&
		left.RunAttempt == right.RunAttempt &&
		left.CommitSHA == right.CommitSHA &&
		left.CandidateKey == right.CandidateKey &&
		left.ReferenceKey == right.ReferenceKey &&
		left.CID == right.CID &&
		left.SHA256 == right.SHA256 &&
		left.ByteCount == right.ByteCount &&
		left.Detail == right.Detail &&
		left.OccurredAt.Equal(right.OccurredAt)
}

func normalizeAssetPinAuditForComparison(event AssetPinAuditEvent) AssetPinAuditEvent {
	event.EventID = strings.TrimSpace(event.EventID)
	event.MutationDigest = strings.ToLower(strings.TrimSpace(event.MutationDigest))
	event.Kind = strings.TrimSpace(event.Kind)
	event.Result = strings.TrimSpace(event.Result)
	event.TokenDigest = strings.ToLower(strings.TrimSpace(event.TokenDigest))
	event.Repository = strings.TrimSpace(event.Repository)
	event.Ref = strings.TrimSpace(event.Ref)
	event.WorkflowRef = strings.TrimSpace(event.WorkflowRef)
	event.Actor = strings.TrimSpace(event.Actor)
	event.WorkflowRunID = strings.TrimSpace(event.WorkflowRunID)
	event.RunAttempt = strings.TrimSpace(event.RunAttempt)
	event.CommitSHA = strings.TrimSpace(event.CommitSHA)
	event.CandidateKey = strings.TrimSpace(event.CandidateKey)
	event.ReferenceKey = strings.TrimSpace(event.ReferenceKey)
	event.CID = strings.TrimSpace(event.CID)
	event.SHA256 = strings.ToLower(strings.TrimSpace(event.SHA256))
	event.Detail = strings.TrimSpace(event.Detail)
	event.OccurredAt = normalizeAssetPinTime(event.OccurredAt)
	return event
}

func requireAssetPinContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("asset pin storage context is required")
	}
	return nil
}

func nullableAssetPinTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return assetPinTimestamp(value)
}

func normalizeAssetPinTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return time.Unix(0, value.UnixNano()).UTC()
}

func validateAssetPinPersistedTime(name string, value time.Time, allowZero bool) error {
	if value.IsZero() {
		if allowZero {
			return nil
		}
		return fmt.Errorf("%s is required", name)
	}
	nanos := value.UnixNano()
	if !time.Unix(0, nanos).Equal(value) {
		return fmt.Errorf("%s is outside the UnixNano range", name)
	}
	return nil
}

func assetPinTimestamp(value time.Time) string {
	// FlatSQL's numeric bridge cannot represent absolute Unix nanoseconds
	// exactly. Flip the sign bit and store a fixed-width decimal string: this
	// preserves every nanosecond and retains chronological TEXT index ordering.
	ordered := uint64(normalizeAssetPinTime(value).UnixNano()) ^ (uint64(1) << 63)
	return fmt.Sprintf("%020d", ordered)
}

func assetPinTimeFromTimestamp(encoded string) (time.Time, error) {
	if len(encoded) != 20 {
		return time.Time{}, fmt.Errorf("invalid timestamp length %d", len(encoded))
	}
	ordered, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	nanos := int64(ordered ^ (uint64(1) << 63))
	return time.Unix(0, nanos).UTC(), nil
}

func assetPinNow() time.Time {
	return normalizeAssetPinTime(time.Now())
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
