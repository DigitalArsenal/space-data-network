package assetpin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/go-cid"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	defaultRetentionCallTimeout      = 10 * time.Second
	defaultRetentionRecoveryPageSize = 100
	retentionRecoveryGracePeriod     = 10 * time.Minute
	retentionRecoveryReadBatchSize   = 128
	retentionKuboMaxResponseBytes    = 64 << 10
)

// RetentionStore is the journal-backed subset needed by asset retention and
// crash recovery. FlatSQLStore implements this interface directly.
type RetentionStore interface {
	FindAssetPinReference(context.Context, string) (storage.AssetPinReference, bool, error)
	UpsertAssetPinReference(context.Context, storage.AssetPinReference, storage.AssetPinAuditEvent) error
	ListAssetPinReferences(context.Context, storage.AssetPinReferenceQuery) ([]storage.AssetPinReference, error)
	TransitionAssetPinReference(context.Context, storage.AssetPinReferenceTransition, storage.AssetPinAuditEvent) error
	ListExpiredAssetPinReferences(context.Context, time.Time) ([]storage.AssetPinReference, error)
	CountProtectedAssetReferences(context.Context, string, time.Time) (int, error)
	DeleteExpiredAssetPinReference(context.Context, string, storage.AssetPinAuditEvent) error
}

// RetentionPins is the narrow Kubo surface used for read-only reconciliation
// and retention unpins.
type RetentionPins interface {
	IsAssetCIDPinned(context.Context, string) (bool, error)
	UnpinAssetCID(context.Context, string) error
}

// RetentionRecoveryStore provides stable, bounded pages of crash markers.
type RetentionRecoveryStore interface {
	ListPage(afterReferenceKey string, limit int) ([]AssetPinRecoveryMarker, string, error)
	Remove(referenceKey string) error
}

// RetainerOptions binds the durable ledger, Kubo, and recovery marker store.
type RetainerOptions struct {
	Store            RetentionStore
	Pins             RetentionPins
	Recovery         RetentionRecoveryStore
	CallTimeout      time.Duration
	RecoveryPageSize int
}

// Retainer reconciles interrupted uploads before applying lifecycle expiry.
// It never drops the last durable retry record before the corresponding Kubo
// or journal operation has completed.
type Retainer struct {
	store            RetentionStore
	pins             RetentionPins
	recovery         RetentionRecoveryStore
	callTimeout      time.Duration
	recoveryPageSize int
}

// NewRetainer validates and binds a retention worker.
func NewRetainer(options RetainerOptions) (*Retainer, error) {
	if options.Store == nil {
		return nil, errors.New("asset retention store is required")
	}
	if options.Pins == nil {
		return nil, errors.New("asset retention Kubo client is required")
	}
	if options.Recovery == nil {
		return nil, errors.New("asset retention recovery store is required")
	}
	if options.CallTimeout < 0 {
		return nil, errors.New("asset retention call timeout must not be negative")
	}
	if options.CallTimeout == 0 {
		options.CallTimeout = defaultRetentionCallTimeout
	}
	if options.RecoveryPageSize < 0 {
		return nil, errors.New("asset retention recovery page size must not be negative")
	}
	if options.RecoveryPageSize == 0 {
		options.RecoveryPageSize = defaultRetentionRecoveryPageSize
	}
	if options.RecoveryPageSize > AssetPinRecoveryListMax {
		options.RecoveryPageSize = AssetPinRecoveryListMax
	}
	return &Retainer{
		store:            options.Store,
		pins:             options.Pins,
		recovery:         options.Recovery,
		callTimeout:      options.CallTimeout,
		recoveryPageSize: options.RecoveryPageSize,
	}, nil
}

// Sweep first resolves crash markers, then abandons stale review candidates,
// removes expired ownership records without unpinning shared content, and
// finally verifies every CID still present in the ledger.
func (r *Retainer) Sweep(ctx context.Context, now time.Time) error {
	if ctx == nil {
		return errors.New("asset retention context is required")
	}
	now, err := normalizeRetentionTime(now)
	if err != nil {
		return err
	}
	var sweepErrors []error
	recoveryBlockedCIDs, recoveryBlocksAll, recoveryErr := r.recoverPendingPins(ctx, now)
	if recoveryErr != nil {
		sweepErrors = append(sweepErrors, fmt.Errorf("recover pending asset pin: %w", recoveryErr))
	}
	blockedCIDs, blockAllRetention, abandonErr := r.abandonStaleReferences(ctx, now)
	if blockedCIDs == nil {
		blockedCIDs = make(map[string]struct{})
	}
	for cidValue := range recoveryBlockedCIDs {
		blockedCIDs[cidValue] = struct{}{}
	}
	blockAllRetention = blockAllRetention || recoveryBlocksAll
	if abandonErr != nil {
		sweepErrors = append(sweepErrors, fmt.Errorf("abandon stale asset reference: %w", abandonErr))
	}
	// Reconcile the complete post-transition ledger snapshot before retention
	// deletes any rows, so every ledger CID is checked at least once per sweep.
	if reconcileErr := r.reconcileLedgerPins(ctx); reconcileErr != nil {
		sweepErrors = append(sweepErrors, reconcileErr)
	}

	expired, err := retentionCall(ctx, r.callTimeout, func(callCtx context.Context) ([]storage.AssetPinReference, error) {
		return r.store.ListExpiredAssetPinReferences(callCtx, now)
	})
	if err != nil {
		sweepErrors = append(sweepErrors, fmt.Errorf("list expired asset references: %w", err))
		expired = nil
	}
	groups := make(map[string][]storage.AssetPinReference)
	for _, ref := range expired {
		if strings.TrimSpace(ref.CID) == "" || strings.TrimSpace(ref.ReferenceKey) == "" {
			sweepErrors = append(sweepErrors, errors.New("expired asset reference has an empty identity"))
			continue
		}
		if blockAllRetention {
			continue
		}
		if _, blocked := blockedCIDs[ref.CID]; blocked {
			continue
		}
		groups[ref.CID] = append(groups[ref.CID], ref)
	}
	cids := sortedRetentionKeys(groups)
	for _, cidValue := range cids {
		refs := groups[cidValue]
		sort.Slice(refs, func(i, j int) bool { return refs[i].ReferenceKey < refs[j].ReferenceKey })
		protected, countErr := retentionCall(ctx, r.callTimeout, func(callCtx context.Context) (int, error) {
			return r.store.CountProtectedAssetReferences(callCtx, cidValue, now)
		})
		if countErr != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("count protected references for CID %s: %w", cidValue, countErr))
			continue
		}
		if protected < 0 {
			sweepErrors = append(sweepErrors, fmt.Errorf("count protected references for CID %s returned a negative count", cidValue))
			continue
		}
		if protected == 0 {
			if unpinErr := r.unpin(ctx, cidValue); unpinErr != nil {
				sweepErrors = append(sweepErrors, fmt.Errorf("unpin expired asset CID %s: %w", cidValue, unpinErr))
				// Keep every reference in the group as the durable retry record.
				continue
			}
		}
		for _, ref := range refs {
			event := retentionDeleteEvent(ref)
			deleteErr := retentionCallErr(ctx, r.callTimeout, func(callCtx context.Context) error {
				return r.store.DeleteExpiredAssetPinReference(callCtx, ref.ReferenceKey, event)
			})
			if deleteErr != nil && !errors.Is(deleteErr, storage.ErrAssetPinReferenceNotFound) {
				sweepErrors = append(sweepErrors, fmt.Errorf("delete expired asset reference %s: %w", ref.ReferenceKey, deleteErr))
			}
		}
	}
	return errors.Join(sweepErrors...)
}

func (r *Retainer) abandonStaleReferences(ctx context.Context, now time.Time) (map[string]struct{}, bool, error) {
	refs, err := retentionCall(ctx, r.callTimeout, func(callCtx context.Context) ([]storage.AssetPinReference, error) {
		return r.store.ListAssetPinReferences(callCtx, storage.AssetPinReferenceQuery{})
	})
	if err != nil {
		return nil, true, err
	}
	blockedCIDs := make(map[string]struct{})
	var transitionErrors []error
	sort.Slice(refs, func(i, j int) bool { return refs[i].ReferenceKey < refs[j].ReferenceKey })
	for _, ref := range refs {
		if ref.State != storage.AssetReferenceStaged && ref.State != storage.AssetReferenceReviewOpen {
			continue
		}
		if ref.ExpiresAt.IsZero() || ref.ExpiresAt.After(now) {
			continue
		}
		abandonedAt, timeErr := normalizeRetentionTime(ref.ExpiresAt)
		if timeErr != nil || abandonedAt.Before(ref.UpdatedAt) {
			blockedCIDs[ref.CID] = struct{}{}
			transitionErrors = append(transitionErrors, fmt.Errorf("reference %s has an invalid stale expiry", ref.ReferenceKey))
			continue
		}
		transition := storage.AssetPinReferenceTransition{
			ReferenceKey: ref.ReferenceKey,
			FromState:    ref.State,
			ToState:      storage.AssetReferenceAbandoned,
			GitHubIssue:  ref.GitHubIssue,
			UpdatedAt:    abandonedAt,
			ExpiresAt:    abandonedAt,
		}
		event := retentionAbandonEvent(ref, abandonedAt)
		err := retentionCallErr(ctx, r.callTimeout, func(callCtx context.Context) error {
			return r.store.TransitionAssetPinReference(callCtx, transition, event)
		})
		if errors.Is(err, storage.ErrAssetPinReferenceNotFound) {
			continue
		}
		if errors.Is(err, storage.ErrAssetPinReferenceConflict) {
			// A decision raced the stale scan. The fresh protected-count pass below
			// is authoritative, but block this CID from mutation in the current
			// sweep in case the conflict did not expose the new row yet.
			blockedCIDs[ref.CID] = struct{}{}
			continue
		}
		if err != nil {
			blockedCIDs[ref.CID] = struct{}{}
			transitionErrors = append(transitionErrors, fmt.Errorf("transition reference %s: %w", ref.ReferenceKey, err))
		}
	}
	return blockedCIDs, false, errors.Join(transitionErrors...)
}

func (r *Retainer) recoverPendingPins(ctx context.Context, now time.Time) (map[string]struct{}, bool, error) {
	blockedCIDs := make(map[string]struct{})
	var recoveryErrors []error
	blocksAllRetention := false
	cursor := ""
	for {
		markers, next, err := r.recovery.ListPage(cursor, r.recoveryPageSize)
		if err != nil {
			return blockedCIDs, true, errors.Join(append(recoveryErrors, err)...)
		}
		if len(markers) > r.recoveryPageSize {
			return blockedCIDs, true, errors.Join(append(recoveryErrors, errors.New("asset pin recovery store exceeded its page bound"))...)
		}
		for index := 1; index < len(markers); index++ {
			if markers[index-1].ReferenceKey >= markers[index].ReferenceKey {
				return blockedCIDs, true, errors.Join(append(recoveryErrors, errors.New("asset pin recovery page is not strictly ordered"))...)
			}
		}
		for _, marker := range markers {
			if cursor != "" && marker.ReferenceKey <= cursor {
				return blockedCIDs, true, errors.Join(append(recoveryErrors, errors.New("asset pin recovery cursor did not advance"))...)
			}
			fresh := marker.UpdatedAt.Add(retentionRecoveryGracePeriod).After(now)
			if err := r.recoverMarker(ctx, now, marker); err != nil {
				if errors.Is(err, ErrInvalidAssetPinRecoveryMarker) || errors.Is(err, ErrUnsafeAssetPinDirectory) {
					blocksAllRetention = true
				}
				if marker.ExpectedCID != "" {
					blockedCIDs[marker.ExpectedCID] = struct{}{}
				}
				if marker.CID != "" {
					blockedCIDs[marker.CID] = struct{}{}
				}
				recoveryErrors = append(recoveryErrors, fmt.Errorf("marker %s: %w", marker.ReferenceKey, err))
				continue
			}
			if fresh {
				blockedCIDs[marker.ExpectedCID] = struct{}{}
				if marker.CID != "" {
					blockedCIDs[marker.CID] = struct{}{}
				}
			}
		}
		if len(markers) < r.recoveryPageSize {
			return blockedCIDs, blocksAllRetention, errors.Join(recoveryErrors...)
		}
		if next == "" || next <= cursor || next != markers[len(markers)-1].ReferenceKey {
			return blockedCIDs, true, errors.Join(append(recoveryErrors, errors.New("asset pin recovery store returned an invalid cursor"))...)
		}
		cursor = next
	}
}

func (r *Retainer) recoverMarker(ctx context.Context, now time.Time, marker AssetPinRecoveryMarker) error {
	if err := validateAssetPinRecoveryMarker(marker); err != nil {
		return err
	}
	// Upload and Kubo calls can still be using a newly-created marker through a
	// different recovery-store instance. Leave a generous grace window before
	// treating it as crash residue; file-store mutexes alone cannot prevent that
	// cross-instance race.
	if marker.UpdatedAt.Add(retentionRecoveryGracePeriod).After(now) {
		return nil
	}
	found, err := retentionCall(ctx, r.callTimeout, func(callCtx context.Context) (retentionReferenceResult, error) {
		value, ok, findErr := r.store.FindAssetPinReference(callCtx, marker.ReferenceKey)
		return retentionReferenceResult{reference: value, found: ok}, findErr
	})
	if err != nil {
		return err
	}
	if found.found {
		if !recoveryMarkerMatchesReference(marker, found.reference) {
			return errors.New("recovery marker conflicts with committed ledger reference")
		}
		pinned, pinErr := r.isPinned(ctx, found.reference.CID)
		if pinErr != nil {
			return pinErr
		}
		if !pinned {
			return fmt.Errorf("committed recovery CID %s is missing from Kubo", found.reference.CID)
		}
		return r.recovery.Remove(marker.ReferenceKey)
	}

	if marker.Phase == AssetPinRecoveryPinnedUncommitted && marker.CID != marker.ExpectedCID {
		return r.compensateMismatchedMarker(ctx, now, marker)
	}
	pinned, err := r.isPinned(ctx, marker.ExpectedCID)
	if err != nil {
		return err
	}
	if !pinned {
		if marker.Phase == AssetPinRecoveryIntent {
			return r.recovery.Remove(marker.ReferenceKey)
		}
		return fmt.Errorf("pinned-uncommitted CID %s is missing from Kubo", marker.ExpectedCID)
	}

	reference := retentionReferenceFromMarker(marker)
	event := retentionUploadEventFromMarker(marker)
	if err := retentionCallErr(ctx, r.callTimeout, func(callCtx context.Context) error {
		return r.store.UpsertAssetPinReference(callCtx, reference, event)
	}); err != nil {
		return err
	}
	return r.recovery.Remove(marker.ReferenceKey)
}

type retentionReferenceResult struct {
	reference storage.AssetPinReference
	found     bool
}

func (r *Retainer) compensateMismatchedMarker(ctx context.Context, now time.Time, marker AssetPinRecoveryMarker) error {
	pinned, err := r.isPinned(ctx, marker.CID)
	if err != nil {
		return err
	}
	if pinned {
		protected, countErr := retentionCall(ctx, r.callTimeout, func(callCtx context.Context) (int, error) {
			return r.store.CountProtectedAssetReferences(callCtx, marker.CID, now)
		})
		if countErr != nil {
			return countErr
		}
		if protected < 0 {
			return errors.New("protected asset reference count is negative")
		}
		if protected == 0 {
			if err := r.unpin(ctx, marker.CID); err != nil {
				return err
			}
		}
	}
	return r.recovery.Remove(marker.ReferenceKey)
}

func (r *Retainer) reconcileLedgerPins(ctx context.Context) error {
	refs, err := retentionCall(ctx, r.callTimeout, func(callCtx context.Context) ([]storage.AssetPinReference, error) {
		return r.store.ListAssetPinReferences(callCtx, storage.AssetPinReferenceQuery{})
	})
	if err != nil {
		return fmt.Errorf("list asset references for Kubo reconciliation: %w", err)
	}
	unique := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.CID) == "" {
			return fmt.Errorf("ledger reference %s has an empty CID", ref.ReferenceKey)
		}
		unique[ref.CID] = struct{}{}
	}
	cids := make([]string, 0, len(unique))
	for cidValue := range unique {
		cids = append(cids, cidValue)
	}
	sort.Strings(cids)
	var reconcileErrors []error
	for _, cidValue := range cids {
		pinned, pinErr := r.isPinned(ctx, cidValue)
		if pinErr != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile asset CID %s: %w", cidValue, pinErr))
			continue
		}
		if !pinned {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("asset CID %s is missing from Kubo", cidValue))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (r *Retainer) isPinned(ctx context.Context, cidValue string) (bool, error) {
	return retentionCall(ctx, r.callTimeout, func(callCtx context.Context) (bool, error) {
		return r.pins.IsAssetCIDPinned(callCtx, cidValue)
	})
}

func (r *Retainer) unpin(ctx context.Context, cidValue string) error {
	return retentionCallErr(ctx, r.callTimeout, func(callCtx context.Context) error {
		return r.pins.UnpinAssetCID(callCtx, cidValue)
	})
}

// Run performs one startup sweep and then exactly one sweep per configured
// ticker event until its context is cancelled.
func (r *Retainer) Run(ctx context.Context, interval time.Duration, clock func() time.Time, report func(error)) {
	if report == nil {
		report = func(error) {}
	}
	if ctx == nil {
		report(errors.New("asset retention context is required"))
		return
	}
	if interval <= 0 {
		report(errors.New("asset retention interval must be positive"))
		return
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	sweep := func() {
		if err := r.Sweep(ctx, clock()); err != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
	}
	if ctx.Err() != nil {
		return
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func retentionReferenceFromMarker(marker AssetPinRecoveryMarker) storage.AssetPinReference {
	return storage.AssetPinReference{
		ReferenceKey:  marker.ReferenceKey,
		CandidateKey:  marker.CandidateKey,
		CID:           marker.ExpectedCID,
		SHA256:        marker.SHA256,
		ByteCount:     marker.ByteCount,
		State:         storage.AssetReferenceStaged,
		SourceURL:     marker.SourceURL,
		LicenseName:   marker.LicenseName,
		Attribution:   marker.Attribution,
		MetadataJSON:  marker.MetadataJSON,
		WorkflowRunID: marker.WorkflowRunID,
		CreatedAt:     marker.CreatedAt,
		UpdatedAt:     marker.UpdatedAt,
		ExpiresAt:     marker.ExpiresAt,
	}
}

func retentionUploadEventFromMarker(marker AssetPinRecoveryMarker) storage.AssetPinAuditEvent {
	return storage.AssetPinAuditEvent{
		EventID:       marker.EventID,
		Kind:          "asset_pin_upload",
		Result:        "pinned",
		TokenDigest:   marker.TokenDigest,
		Repository:    marker.Repository,
		Ref:           marker.Ref,
		WorkflowRef:   marker.WorkflowRef,
		Actor:         marker.Actor,
		WorkflowRunID: marker.WorkflowRunID,
		RunAttempt:    marker.RunAttempt,
		CommitSHA:     marker.CommitSHA,
		CandidateKey:  marker.CandidateKey,
		ReferenceKey:  marker.ReferenceKey,
		CID:           marker.ExpectedCID,
		SHA256:        marker.SHA256,
		ByteCount:     marker.ByteCount,
		OccurredAt:    marker.CreatedAt,
	}
}

func recoveryMarkerMatchesReference(marker AssetPinRecoveryMarker, ref storage.AssetPinReference) bool {
	return ref.ReferenceKey == marker.ReferenceKey &&
		ref.CandidateKey == marker.CandidateKey &&
		ref.CID == marker.ExpectedCID &&
		ref.SHA256 == marker.SHA256 &&
		ref.ByteCount == marker.ByteCount &&
		ref.SourceURL == marker.SourceURL &&
		ref.LicenseName == marker.LicenseName &&
		ref.Attribution == marker.Attribution &&
		ref.MetadataJSON == marker.MetadataJSON &&
		ref.WorkflowRunID == marker.WorkflowRunID &&
		ref.CreatedAt.Equal(marker.CreatedAt)
}

func retentionAbandonEvent(ref storage.AssetPinReference, abandonedAt time.Time) storage.AssetPinAuditEvent {
	return storage.AssetPinAuditEvent{
		EventID:      retentionStableID("asset-pin-retention-abandon:v1\n" + retentionReferenceGeneration(ref)),
		Kind:         "asset_pin_retention_abandon",
		Result:       "abandoned",
		CandidateKey: ref.CandidateKey,
		ReferenceKey: ref.ReferenceKey,
		CID:          ref.CID,
		SHA256:       ref.SHA256,
		ByteCount:    ref.ByteCount,
		OccurredAt:   abandonedAt,
	}
}

func retentionDeleteEvent(ref storage.AssetPinReference) storage.AssetPinAuditEvent {
	occurredAt := ref.ExpiresAt
	if occurredAt.IsZero() {
		occurredAt = ref.UpdatedAt
	}
	return storage.AssetPinAuditEvent{
		EventID:      retentionStableID("asset-pin-retention-delete:v1\n" + retentionReferenceGeneration(ref)),
		Kind:         "asset_pin_retention_delete",
		Result:       "deleted",
		CandidateKey: ref.CandidateKey,
		ReferenceKey: ref.ReferenceKey,
		CID:          ref.CID,
		SHA256:       ref.SHA256,
		ByteCount:    ref.ByteCount,
		OccurredAt:   occurredAt,
	}
}

func retentionReferenceGeneration(ref storage.AssetPinReference) string {
	return ref.ReferenceKey + "\n" +
		strconv.FormatInt(ref.CreatedAt.UnixNano(), 10) + "\n" +
		strconv.FormatInt(ref.ExpiresAt.UnixNano(), 10)
}

func retentionStableID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeRetentionTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, errors.New("asset retention time must be within UnixNano range")
	}
	normalized := time.Unix(0, value.UnixNano()).UTC()
	if !normalized.Equal(value) {
		return time.Time{}, errors.New("asset retention time must be within UnixNano range")
	}
	return normalized, nil
}

func sortedRetentionKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func retentionCall[T any](ctx context.Context, timeout time.Duration, call func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return call(callCtx)
}

func retentionCallErr(ctx context.Context, timeout time.Duration, call func(context.Context) error) error {
	_, err := retentionCall(ctx, timeout, func(callCtx context.Context) (struct{}, error) {
		return struct{}{}, call(callCtx)
	})
	return err
}

// ListPage deterministically returns the lexicographically next bounded page
// of recovery markers. It scans directory entries in bounded batches and
// retains only the smallest requested keys, so memory never scales with the
// directory size and a restart can resume from an opaque last-key cursor.
func (s *FileAssetPinRecoveryStore) ListPage(afterReferenceKey string, limit int) ([]AssetPinRecoveryMarker, string, error) {
	if s == nil {
		return nil, "", errors.New("asset pin recovery store is required")
	}
	if afterReferenceKey != "" && !isLowerSHA256(afterReferenceKey) {
		return nil, "", errors.New("asset pin recovery cursor must be a lowercase SHA-256 key")
	}
	if limit <= 0 {
		return nil, "", errors.New("asset pin recovery marker limit must be positive")
	}
	if limit > AssetPinRecoveryListMax {
		limit = AssetPinRecoveryListMax
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := ensurePrivateAssetPinDirectory(s.dataDir, "recovery")
	if err != nil {
		return nil, "", err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return nil, "", fmt.Errorf("open asset pin recovery marker directory: %w", err)
	}
	defer handle.Close()

	keys := make([]string, 0, limit+retentionRecoveryReadBatchSize)
	for {
		entries, readErr := handle.ReadDir(retentionRecoveryReadBatchSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, "", fmt.Errorf("list bounded asset pin recovery marker page: %w", readErr)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return nil, "", fmt.Errorf("%w: recovery marker is not a regular file", ErrUnsafeAssetPinDirectory)
			}
			key := strings.TrimSuffix(entry.Name(), ".json")
			if !isLowerSHA256(key) {
				return nil, "", fmt.Errorf("%w: recovery marker filename is invalid", ErrInvalidAssetPinRecoveryMarker)
			}
			if key > afterReferenceKey {
				keys = append(keys, key)
			}
		}
		if len(keys) > limit {
			sort.Strings(keys)
			keys = keys[:limit]
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	sort.Strings(keys)
	markers := make([]AssetPinRecoveryMarker, 0, len(keys))
	for _, key := range keys {
		marker, ok, loadErr := loadAssetPinRecoveryMarker(directory, key)
		if loadErr != nil {
			return nil, "", loadErr
		}
		if !ok {
			return nil, "", errors.New("asset pin recovery marker disappeared during locked page read")
		}
		markers = append(markers, marker)
	}
	next := afterReferenceKey
	if len(markers) > 0 {
		next = markers[len(markers)-1].ReferenceKey
	}
	return markers, next, nil
}

// KuboRetentionClient performs strict, bounded pin/ls and pin/rm requests.
type KuboRetentionClient struct {
	apiURL string
	client *http.Client
}

// NewKuboRetentionClient constructs a no-redirect Kubo retention client.
func NewKuboRetentionClient(apiURL string) (*KuboRetentionClient, error) {
	if apiURL == "" || strings.TrimSpace(apiURL) != apiURL {
		return nil, errors.New("asset retention Kubo API URL must be canonical")
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawPath != "" || parsed.String() != apiURL {
		return nil, errors.New("asset retention Kubo API URL must be a canonical absolute HTTP base URL")
	}
	if err := validateRetentionExplicitPort(parsed); err != nil {
		return nil, err
	}
	normalizedPath := strings.TrimSuffix(parsed.Path, "/")
	if normalizedPath != "" && path.Clean(normalizedPath) != normalizedPath {
		return nil, errors.New("asset retention Kubo API URL path must be canonical")
	}
	parsed.Path = normalizedPath
	return &KuboRetentionClient{
		apiURL: parsed.String(),
		client: &http.Client{
			Timeout: defaultRetentionCallTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// IsAssetCIDPinned verifies a direct recursive Kubo pin without mutating it.
func (c *KuboRetentionClient) IsAssetCIDPinned(ctx context.Context, cidValue string) (bool, error) {
	body, status, err := c.command(ctx, "/api/v0/pin/ls", cidValue, url.Values{"type": {"recursive"}})
	if err != nil {
		return false, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if recognizedKuboMissingPin(status, body, cidValue) {
			return false, nil
		}
		return false, fmt.Errorf("Kubo pin/ls failed with status %d", status)
	}
	var response struct {
		Keys map[string]struct {
			Type string `json:"Type"`
		} `json:"Keys"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return false, errors.New("Kubo pin/ls returned invalid bounded JSON")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return false, errors.New("Kubo pin/ls returned invalid bounded JSON")
	}
	entry, ok := response.Keys[cidValue]
	if !ok {
		return false, errors.New("Kubo pin/ls omitted the requested recursive pin")
	}
	if len(response.Keys) != 1 || entry.Type != "recursive" {
		return false, errors.New("Kubo pin/ls returned an inconsistent recursive pin")
	}
	return true, nil
}

// UnpinAssetCID removes one direct asset pin. Kubo's complete "not pinned"
// response is idempotent success, preserving crash retry safety.
func (c *KuboRetentionClient) UnpinAssetCID(ctx context.Context, cidValue string) error {
	body, status, err := c.command(ctx, "/api/v0/pin/rm", cidValue, nil)
	if err != nil {
		return err
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	if recognizedKuboMissingPin(status, body, cidValue) {
		return nil
	}
	return fmt.Errorf("Kubo pin/rm failed with status %d", status)
}

func validateRetentionExplicitPort(parsed *url.URL) error {
	if parsed == nil {
		return errors.New("asset retention Kubo API URL is required")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("asset retention Kubo API URL has an invalid explicit port")
	}
	portValue := parsed.Port()
	if portValue == "" {
		return nil
	}
	portNumber, err := strconv.Atoi(portValue)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("asset retention Kubo API URL has an invalid explicit port")
	}
	return nil
}

func recognizedKuboMissingPin(status int, body []byte, cidValue string) bool {
	if status != http.StatusInternalServerError {
		return false
	}
	message, code, errorType, ok := decodeStrictKuboError(body)
	if !ok || code != 0 || errorType != "error" {
		return false
	}
	for _, recognized := range []string{
		"path is not pinned",
		"path " + cidValue + " is not pinned",
		"path '" + cidValue + "' is not pinned",
		`path "` + cidValue + `" is not pinned`,
		cidValue + " is not pinned",
		cidValue + " is not pinned or pinned indirectly",
	} {
		if message == recognized {
			return true
		}
	}
	return false
}

func decodeStrictKuboError(body []byte) (message string, code int, errorType string, ok bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return "", 0, "", false
	}
	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, keyOK := keyToken.(string)
		if err != nil || !keyOK {
			return "", 0, "", false
		}
		if _, duplicate := seen[key]; duplicate {
			return "", 0, "", false
		}
		seen[key] = struct{}{}
		switch key {
		case "Message":
			if err := decoder.Decode(&message); err != nil {
				return "", 0, "", false
			}
		case "Code":
			if err := decoder.Decode(&code); err != nil {
				return "", 0, "", false
			}
		case "Type":
			if err := decoder.Decode(&errorType); err != nil {
				return "", 0, "", false
			}
		default:
			return "", 0, "", false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || requireJSONEOF(decoder) != nil {
		return "", 0, "", false
	}
	if len(seen) != 3 {
		return "", 0, "", false
	}
	return message, code, errorType, true
}

func (c *KuboRetentionClient) command(ctx context.Context, commandPath, cidValue string, values url.Values) ([]byte, int, error) {
	if c == nil || c.client == nil {
		return nil, 0, errors.New("asset retention Kubo client is required")
	}
	if ctx == nil {
		return nil, 0, errors.New("asset retention Kubo context is required")
	}
	if strings.TrimSpace(cidValue) != cidValue || cidValue == "" {
		return nil, 0, errors.New("asset retention CID must be canonical")
	}
	parsedCID, err := cid.Decode(cidValue)
	if err != nil || parsedCID.Version() != 1 || parsedCID.String() != cidValue {
		return nil, 0, errors.New("asset retention CID must be canonical CIDv1")
	}
	endpoint, err := url.JoinPath(c.apiURL, commandPath)
	if err != nil {
		return nil, 0, fmt.Errorf("build Kubo retention URL: %w", err)
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("parse Kubo retention URL: %w", err)
	}
	query := requestURL.Query()
	query.Set("arg", cidValue)
	for key, entries := range values {
		query.Del(key)
		for _, entry := range entries {
			query.Add(key, entry)
		}
	}
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create Kubo retention request: %w", err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("perform Kubo retention request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, retentionKuboMaxResponseBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read Kubo retention response: %w", err)
	}
	if len(body) > retentionKuboMaxResponseBytes {
		return nil, 0, fmt.Errorf("Kubo retention response exceeds %d bytes", retentionKuboMaxResponseBytes)
	}
	return body, response.StatusCode, nil
}
