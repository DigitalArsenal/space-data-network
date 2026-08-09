package assetpin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestRetainerSweepAppliesLifecycleAndProtectsSharedCID(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 123, time.UTC)
	approved := retentionReference("approved", "cid-approved", storage.AssetReferenceApproved, now.Add(-200*24*time.Hour), time.Time{})
	staleStaged := retentionReference("stale-staged", "cid-staged", storage.AssetReferenceStaged, now.Add(-91*24*time.Hour), now.Add(-24*time.Hour))
	staleReview := retentionReference("stale-review", "cid-shared", storage.AssetReferenceReviewOpen, now.Add(-91*24*time.Hour), now.Add(-time.Hour))
	sharedApproved := retentionReference("shared-approved", "cid-shared", storage.AssetReferenceApproved, now.Add(-180*24*time.Hour), time.Time{})
	expiredRejected := retentionReference("expired-rejected", "cid-rejected", storage.AssetReferenceRejected, now.Add(-31*24*time.Hour), now.Add(-24*time.Hour))
	futureSuperseded := retentionReference("future-superseded", "cid-future", storage.AssetReferenceSuperseded, now.Add(-20*24*time.Hour), now.Add(10*24*time.Hour))

	store := newFakeRetentionStore(now, approved, staleStaged, staleReview, sharedApproved, expiredRejected, futureSuperseded)
	pins := &fakeRetentionPins{pinned: map[string]bool{
		"cid-approved": true, "cid-staged": true, "cid-shared": true,
		"cid-rejected": true, "cid-future": true,
	}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	for _, key := range []string{staleStaged.ReferenceKey, staleReview.ReferenceKey, expiredRejected.ReferenceKey} {
		if _, exists := store.reference(key); exists {
			t.Errorf("expired reference %q remains after successful sweep", key)
		}
	}
	for _, key := range []string{approved.ReferenceKey, sharedApproved.ReferenceKey, futureSuperseded.ReferenceKey} {
		if _, exists := store.reference(key); !exists {
			t.Errorf("protected reference %q was deleted", key)
		}
	}
	if got, want := pins.unpinCalls, []string{"cid-rejected", "cid-staged"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unpin calls = %v, want %v", got, want)
	}
	if containsString(pins.unpinCalls, "cid-shared") || containsString(pins.unpinCalls, "cid-approved") {
		t.Fatalf("protected shared/approved CID was unpinned: %v", pins.unpinCalls)
	}
	if got := store.transitionKinds(); !reflect.DeepEqual(got, []string{"asset_pin_retention_abandon", "asset_pin_retention_abandon"}) {
		t.Fatalf("transition audit kinds = %v", got)
	}
	if got := store.deleteKinds(); !reflect.DeepEqual(got, []string{
		"asset_pin_retention_delete", "asset_pin_retention_delete", "asset_pin_retention_delete",
	}) {
		t.Fatalf("delete audit kinds = %v", got)
	}
	for _, ref := range store.references() {
		if ref.State == storage.AssetReferenceApproved && !ref.ExpiresAt.IsZero() {
			t.Fatalf("approved reference gained expiry: %+v", ref)
		}
	}
}

func TestRetainerSweepPersistsJournaledTransitionAndDelete(t *testing.T) {
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref := retentionReference("journaled-stale", "cid-journaled", storage.AssetReferenceStaged, now.Add(-91*24*time.Hour), now.Add(-time.Hour))
	ref.MetadataJSON = `{}`
	upsert := storage.AssetPinAuditEvent{
		EventID:      "fixture-journaled-stale-upsert",
		Kind:         "fixture_upsert",
		Result:       "created",
		CandidateKey: ref.CandidateKey,
		ReferenceKey: ref.ReferenceKey,
		CID:          ref.CID,
		SHA256:       ref.SHA256,
		ByteCount:    ref.ByteCount,
		OccurredAt:   ref.CreatedAt,
	}
	if err := store.UpsertAssetPinReference(context.Background(), ref, upsert); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	pins := &fakeRetentionPins{pinned: map[string]bool{ref.CID: true}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if _, found, err := store.FindAssetPinReference(context.Background(), ref.ReferenceKey); err != nil || found {
		t.Fatalf("FindAssetPinReference() found=%v error=%v, want journaled deletion", found, err)
	}
	for _, kind := range []string{"asset_pin_retention_abandon", "asset_pin_retention_delete"} {
		events, err := store.ListAssetPinAuditEvents(context.Background(), storage.AssetPinAuditEventQuery{Kind: kind})
		if err != nil || len(events) != 1 {
			t.Fatalf("ListAssetPinAuditEvents(%q) = %d, %v; want one", kind, len(events), err)
		}
	}
}

// TestRetainerSweepDeadlineNeverPoisonsTheAssetPinLedger is the sweep-level
// guard for sdn-assetpin-retention-sweep-double-commits. An expiring per-call
// deadline is an ordinary, retryable outcome: the next sweep tries again. It
// must never be reported as a transaction-lifecycle failure, and above all it
// must never leave the asset-pin ledger demanding recovery — a state that
// refuses EVERY later asset mutation until the store is closed and reopened.
//
// The deadline here is pathologically small on purpose, so the sweep's real
// (fsync-bound) store calls are certain to be cut off mid-flight.
func TestRetainerSweepDeadlineNeverPoisonsTheAssetPinLedger(t *testing.T) {
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref := retentionReference("deadline-stale", "cid-deadline", storage.AssetReferenceStaged, now.Add(-91*24*time.Hour), now.Add(-time.Hour))
	ref.MetadataJSON = `{}`
	upsert := storage.AssetPinAuditEvent{
		EventID:      "fixture-deadline-stale-upsert",
		Kind:         "fixture_upsert",
		Result:       "created",
		CandidateKey: ref.CandidateKey,
		ReferenceKey: ref.ReferenceKey,
		CID:          ref.CID,
		SHA256:       ref.SHA256,
		ByteCount:    ref.ByteCount,
		OccurredAt:   ref.CreatedAt,
	}
	if err := store.UpsertAssetPinReference(context.Background(), ref, upsert); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	retainer, err := NewRetainer(RetainerOptions{
		Store:            store,
		Pins:             &fakeRetentionPins{pinned: map[string]bool{ref.CID: true}},
		Recovery:         newFakeRecoveryPager(),
		Gate:             NewMutationGate(),
		CallTimeout:      time.Nanosecond,
		RecoveryPageSize: 2,
	})
	if err != nil {
		t.Fatalf("NewRetainer() error = %v", err)
	}

	sweepErr := retainer.Sweep(context.Background(), now)
	if sweepErr != nil {
		if strings.Contains(sweepErr.Error(), "transaction has already been committed or rolled back") {
			t.Fatalf("Sweep() error = %v; an expired per-call deadline must not surface as a transaction-lifecycle failure", sweepErr)
		}
		if errors.Is(sweepErr, storage.ErrAssetPinLedgerRecoveryRequired) {
			t.Fatalf("Sweep() error = %v; an expired per-call deadline must not poison the asset pin ledger", sweepErr)
		}
	}

	// The store is still usable: the sweep is retryable, which is the whole
	// point of failing on a deadline rather than on a lifecycle error.
	retry := storage.AssetPinAuditEvent{
		EventID:      "fixture-deadline-stale-retry",
		Kind:         "fixture_upsert",
		Result:       "created",
		CandidateKey: ref.CandidateKey,
		ReferenceKey: ref.ReferenceKey,
		CID:          ref.CID,
		SHA256:       ref.SHA256,
		ByteCount:    ref.ByteCount,
		OccurredAt:   ref.CreatedAt,
	}
	if err := store.UpsertAssetPinReference(context.Background(), ref, retry); err != nil {
		t.Fatalf("UpsertAssetPinReference() after a deadline-cut sweep: error = %v; want a healthy ledger", err)
	}
}

func TestRetainerSweepKeepsFailedUnpinRetryable(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	expired := retentionReference("retry-unpin", "cid-retry", storage.AssetReferenceRejected, now.Add(-31*24*time.Hour), now.Add(-24*time.Hour))
	store := newFakeRetentionStore(now, expired)
	pins := &fakeRetentionPins{
		pinned:       map[string]bool{"cid-retry": true},
		failUnpinFor: map[string]int{"cid-retry": 1},
	}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())

	if err := retainer.Sweep(context.Background(), now); err == nil || !strings.Contains(err.Error(), "unpin") {
		t.Fatalf("first Sweep() error = %v, want unpin failure", err)
	}
	if _, exists := store.reference(expired.ReferenceKey); !exists {
		t.Fatal("failed unpin deleted the only retry record")
	}
	if len(store.deleteKinds()) != 0 {
		t.Fatalf("failed unpin wrote delete audit: %v", store.deleteKinds())
	}

	if err := retainer.Sweep(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatalf("retry Sweep() error = %v", err)
	}
	if _, exists := store.reference(expired.ReferenceKey); exists {
		t.Fatal("successful retry left expired reference")
	}
	if got, want := pins.unpinCalls, []string{"cid-retry", "cid-retry"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unpin attempts = %v, want %v", got, want)
	}
}

func TestRetainerSweepReconcilesEveryRemainingCID(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	refs := []storage.AssetPinReference{
		retentionReference("reconcile-a", "cid-a", storage.AssetReferenceApproved, now.Add(-time.Hour), time.Time{}),
		retentionReference("reconcile-b", "cid-b", storage.AssetReferenceReviewOpen, now.Add(-time.Hour), now.Add(89*24*time.Hour)),
		retentionReference("reconcile-a-duplicate", "cid-a", storage.AssetReferenceStaged, now.Add(-time.Hour), now.Add(89*24*time.Hour)),
	}
	store := newFakeRetentionStore(now, refs...)
	pins := &fakeRetentionPins{pinned: map[string]bool{"cid-a": true, "cid-b": false}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())

	err := retainer.Sweep(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "cid-b") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Sweep() error = %v, want named missing pin", err)
	}
	if got, want := pins.checkCalls, []string{"cid-a", "cid-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pin/ls calls = %v, want one for every unique ledger CID %v", got, want)
	}
}

func TestRetainerSweepContinuesHealthChecksButBlocksFailedStaleCID(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stale := retentionReference("failed-stale", "cid-failed-stale", storage.AssetReferenceStaged, now.Add(-91*24*time.Hour), now.Add(-time.Hour))
	healthy := retentionReference("healthy-approved", "cid-healthy", storage.AssetReferenceApproved, now.Add(-time.Hour), time.Time{})
	store := newFakeRetentionStore(now, stale, healthy)
	store.transitionErrFor = map[string]error{stale.ReferenceKey: errors.New("journal transition unavailable")}
	pins := &fakeRetentionPins{pinned: map[string]bool{stale.CID: true, healthy.CID: true}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())

	err := retainer.Sweep(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "journal transition unavailable") {
		t.Fatalf("Sweep() error = %v, want stale transition failure", err)
	}
	if _, exists := store.reference(stale.ReferenceKey); !exists {
		t.Fatal("failed stale transition led to deletion")
	}
	if containsString(pins.unpinCalls, stale.CID) {
		t.Fatalf("failed stale transition led to unpin: %v", pins.unpinCalls)
	}
	if got, want := pins.checkCalls, []string{stale.CID, healthy.CID}; !sameStringSet(got, want) {
		t.Fatalf("reconciliation calls = %v, want every remaining CID %v", got, want)
	}
}

func TestRetainerSweepContinuesLedgerReconciliationAfterRecoveryError(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	approved := retentionReference("recovery-health", "cid-recovery-health", storage.AssetReferenceApproved, now.Add(-time.Hour), time.Time{})
	store := newFakeRetentionStore(now, approved)
	pins := &fakeRetentionPins{pinned: map[string]bool{approved.CID: true}}
	recovery := newFakeRecoveryPager()
	recovery.listErr = errors.New("recovery directory unavailable")
	retainer := mustTestRetainer(t, store, pins, recovery)

	err := retainer.Sweep(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "recovery directory unavailable") {
		t.Fatalf("Sweep() error = %v, want recovery error", err)
	}
	if got := pins.checkCalls; !reflect.DeepEqual(got, []string{approved.CID}) {
		t.Fatalf("pin reconciliation calls = %v, want %s despite recovery error", got, approved.CID)
	}
}

func TestRetainerSweepBoundsEveryKuboCall(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	approved := retentionReference("bounded-call", "cid-blocked", storage.AssetReferenceApproved, now.Add(-time.Hour), time.Time{})
	store := newFakeRetentionStore(now, approved)
	pins := &fakeRetentionPins{blockChecks: true}
	retainer, err := NewRetainer(RetainerOptions{
		Store:            store,
		Pins:             pins,
		Recovery:         newFakeRecoveryPager(),
		Gate:             NewMutationGate(),
		CallTimeout:      20 * time.Millisecond,
		RecoveryPageSize: 2,
	})
	if err != nil {
		t.Fatalf("NewRetainer() error = %v", err)
	}

	started := time.Now()
	err = retainer.Sweep(context.Background(), now)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Sweep() error = %v, want per-call deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded Kubo call returned after %v", elapsed)
	}
}

func TestRetainerMutationGateIsContextAwareAndRequired(t *testing.T) {
	gate := NewMutationGate()
	freeCancelled, cancelFree := context.WithCancel(context.Background())
	cancelFree()
	if _, err := gate.Acquire(freeCancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire(cancelled free gate) error = %v, want context.Canceled", err)
	}
	release, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.Acquire(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire(cancelled) error = %v, want context.Canceled", err)
	}
	release()
	secondRelease, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(after release) error = %v", err)
	}
	secondRelease()

	valid := RetainerOptions{
		Store:    newFakeRetentionStore(time.Now().UTC()),
		Pins:     &fakeRetentionPins{},
		Recovery: newFakeRecoveryPager(),
		Gate:     NewMutationGate(),
	}
	retainer, err := NewRetainer(valid)
	if err != nil {
		t.Fatalf("NewRetainer(defaults) error = %v", err)
	}
	if retainer.sweepTimeout != defaultRetentionSweepTimeout {
		t.Fatalf("default sweep timeout = %v, want %v", retainer.sweepTimeout, defaultRetentionSweepTimeout)
	}
	var typedNilStore *fakeRetentionStore
	var typedNilPins *fakeRetentionPins
	var typedNilRecovery *fakeRecoveryPager
	var typedNilGate *MutationGate
	tests := []struct {
		name   string
		mutate func(*RetainerOptions)
	}{
		{name: "nil store", mutate: func(options *RetainerOptions) { options.Store = nil }},
		{name: "typed nil store", mutate: func(options *RetainerOptions) { options.Store = typedNilStore }},
		{name: "nil pins", mutate: func(options *RetainerOptions) { options.Pins = nil }},
		{name: "typed nil pins", mutate: func(options *RetainerOptions) { options.Pins = typedNilPins }},
		{name: "nil recovery", mutate: func(options *RetainerOptions) { options.Recovery = nil }},
		{name: "typed nil recovery", mutate: func(options *RetainerOptions) { options.Recovery = typedNilRecovery }},
		{name: "nil gate", mutate: func(options *RetainerOptions) { options.Gate = nil }},
		{name: "typed nil gate", mutate: func(options *RetainerOptions) { options.Gate = typedNilGate }},
		{name: "negative sweep timeout", mutate: func(options *RetainerOptions) { options.SweepTimeout = -time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if retainer, err := NewRetainer(options); err == nil || retainer != nil {
				t.Fatalf("NewRetainer() = %#v, %v; want fail-closed dependency rejection", retainer, err)
			}
		})
	}
}

func TestRetainerGateCoversMutationAndReleasesBeforeReconciliation(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	expired := retentionReference("gate-expired", "cid-gate", storage.AssetReferenceRejected, now.Add(-31*24*time.Hour), now.Add(-time.Hour))
	approved := retentionReference("gate-approved", "cid-approved", storage.AssetReferenceApproved, now.Add(-31*24*time.Hour), time.Time{})
	store := newFakeRetentionStore(now, expired, approved)
	deleteStarted := make(chan struct{})
	allowDelete := make(chan struct{})
	store.deleteStarted = deleteStarted
	store.allowDelete = allowDelete
	checkStarted := make(chan struct{})
	allowCheck := make(chan struct{})
	pins := &fakeRetentionPins{
		pinned:       map[string]bool{expired.CID: true, approved.CID: true},
		checkStarted: checkStarted,
		allowCheck:   allowCheck,
	}
	gate := NewMutationGate()
	retainer, err := NewRetainer(RetainerOptions{
		Store: store, Pins: pins, Recovery: newFakeRecoveryPager(), Gate: gate,
		CallTimeout: time.Second, SweepTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- retainer.Sweep(context.Background(), now) }()

	select {
	case <-deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("retention delete did not start")
	}
	blockedCtx, cancelBlocked := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelBlocked()
	if release, err := gate.Acquire(blockedCtx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			release()
		}
		t.Fatalf("gate acquired during destructive retention: %v", err)
	}
	close(allowDelete)

	select {
	case <-checkStarted:
	case <-time.After(time.Second):
		t.Fatal("read-only reconciliation did not start")
	}
	release, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("gate remained held during reconciliation: %v", err)
	}
	release()
	close(allowCheck)
	if err := <-result; err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
}

func TestRetainerSweepHasGlobalDeadlineAndCancelableRecoveryPaging(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	recovery := &blockingRetentionRecoveryPager{started: make(chan struct{})}
	retainer, err := NewRetainer(RetainerOptions{
		Store: newFakeRetentionStore(now), Pins: &fakeRetentionPins{}, Recovery: recovery,
		Gate: NewMutationGate(), CallTimeout: time.Second, SweepTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = retainer.Sweep(context.Background(), now)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Sweep() error = %v, want global deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("global deadline returned after %v", elapsed)
	}

	fileStore, err := NewFileAssetPinRecoveryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := fileStore.ListPage(cancelled, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPage(cancelled) error = %v, want context.Canceled", err)
	}
	fileStore.mu.Lock()
	blocked, cancelBlocked := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, _, blockedErr := fileStore.ListPage(blocked, "", 1)
	cancelBlocked()
	fileStore.mu.Unlock()
	if !errors.Is(blockedErr, context.DeadlineExceeded) {
		t.Fatalf("ListPage(contended) error = %v, want context deadline", blockedErr)
	}
}

func TestRetainerMarkerLedgerConflictBlocksWholeDestructiveSweep(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	marker := testAssetPinRecoveryMarker("candidate-retainer-ledger-conflict", "3")
	marker.Phase = AssetPinRecoveryPinnedUncommitted
	marker.CID = marker.ExpectedCID
	committed := retentionReferenceFromMarker(marker)
	committed.CID = testRecoveryAlternateCID
	expired := retentionReference("conflict-expired", testRecoveryCID, storage.AssetReferenceRejected, now.Add(-31*24*time.Hour), now.Add(-time.Hour))
	store := newFakeRetentionStore(now, committed, expired)
	pins := &fakeRetentionPins{pinned: map[string]bool{
		committed.CID: true,
		expired.CID:   true,
	}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager(marker))

	err := retainer.Sweep(context.Background(), now)
	if !errors.Is(err, ErrAssetPinRecoveryMarkerConflict) {
		t.Fatalf("Sweep() error = %v, want ErrAssetPinRecoveryMarkerConflict", err)
	}
	var conflict *AssetPinRecoveryLedgerConflictError
	if !errors.As(err, &conflict) || conflict.ActualCID != committed.CID {
		t.Fatalf("Sweep() conflict = %#v, want actual ledger CID %q", conflict, committed.CID)
	}
	if len(pins.unpinCalls) != 0 || len(store.deleteKinds()) != 0 {
		t.Fatalf("integrity conflict allowed destructive retention: unpins=%v deletes=%v", pins.unpinCalls, store.deleteKinds())
	}
	if _, exists := store.reference(expired.ReferenceKey); !exists {
		t.Fatal("integrity conflict deleted an unrelated expired retry row")
	}
}

func TestRetainerPrescanFindsLaterLedgerConflictBeforeMismatchCompensation(t *testing.T) {
	const unrelatedLedgerCID = "bafkreicfbpehrnn2ynqbs7tf7cu4rj57tsw222kkna33ytyfaz6v37oniu"
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	first := testAssetPinRecoveryMarker("candidate-prescan-conflict-order-a", "4")
	second := testAssetPinRecoveryMarker("candidate-prescan-conflict-order-b", "5")
	lower, higher := first, second
	if lower.ReferenceKey > higher.ReferenceKey {
		lower, higher = higher, lower
	}
	mismatch := lower
	mismatch.Phase = AssetPinRecoveryPinnedUncommitted
	mismatch.CID = testRecoveryAlternateCID
	conflict := higher
	conflict.Phase = AssetPinRecoveryPinnedUncommitted
	conflict.CID = conflict.ExpectedCID
	committed := retentionReferenceFromMarker(conflict)
	committed.CID = unrelatedLedgerCID
	store := newFakeRetentionStore(now, committed)
	pins := &fakeRetentionPins{pinned: map[string]bool{
		testRecoveryAlternateCID: true,
		unrelatedLedgerCID:       true,
	}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager(mismatch, conflict))

	err := retainer.Sweep(context.Background(), now)
	if !errors.Is(err, ErrAssetPinRecoveryMarkerConflict) {
		t.Fatalf("Sweep() error = %v, want later marker conflict", err)
	}
	if len(pins.unpinCalls) != 0 {
		t.Fatalf("mismatch compensation ran before complete conflict prescan: %v", pins.unpinCalls)
	}
}

func TestRetainerSweepRecoversPinnedMarkerBeforeRetention(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	marker := testAssetPinRecoveryMarker("candidate-retainer-recovery", "8")
	marker.Phase = AssetPinRecoveryPinnedUncommitted
	marker.CID = marker.ExpectedCID
	store := newFakeRetentionStore(now)
	pins := &fakeRetentionPins{pinned: map[string]bool{marker.CID: true}}
	recovery := newFakeRecoveryPager(marker)
	retainer := mustTestRetainer(t, store, pins, recovery)

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	got, exists := store.reference(marker.ReferenceKey)
	if !exists {
		t.Fatal("pinned-uncommitted marker was not reconstructed")
	}
	if got.CID != marker.CID || got.CandidateKey != marker.CandidateKey || got.MetadataJSON != marker.MetadataJSON || got.State != storage.AssetReferenceStaged {
		t.Fatalf("reconstructed reference = %+v, marker = %+v", got, marker)
	}
	if recovery.has(marker.ReferenceKey) {
		t.Fatal("committed recovery marker was not removed")
	}
	if got := store.upsertKinds(); !reflect.DeepEqual(got, []string{"asset_pin_upload"}) {
		t.Fatalf("recovery audit kinds = %v", got)
	}
}

func TestRetainerSweepSafelyHandlesIntentAndCIDMismatchMarkers(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	intent := testAssetPinRecoveryMarker("candidate-retainer-intent", "9")
	mismatch := testAssetPinRecoveryMarker("candidate-retainer-mismatch", "a")
	mismatch.Phase = AssetPinRecoveryPinnedUncommitted
	mismatch.CID = testRecoveryAlternateCID
	store := newFakeRetentionStore(now)
	pins := &fakeRetentionPins{pinned: map[string]bool{
		intent.ExpectedCID: false,
		mismatch.CID:       true,
	}}
	recovery := newFakeRecoveryPager(intent, mismatch)
	retainer := mustTestRetainer(t, store, pins, recovery)

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if recovery.has(intent.ReferenceKey) || recovery.has(mismatch.ReferenceKey) {
		t.Fatalf("resolved recovery markers remain: %+v", recovery.markers)
	}
	if len(store.references()) != 0 {
		t.Fatalf("uncommitted or mismatched marker created ledger rows: %+v", store.references())
	}
	if got, want := pins.unpinCalls, []string{mismatch.CID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatch compensation = %v, want %v", got, want)
	}
}

func TestRetainerMismatchCompensationPreservesOtherRecoveryOwnershipInEitherOrder(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	first := testAssetPinRecoveryMarker("candidate-recovery-owner-order-a", "1")
	second := testAssetPinRecoveryMarker("candidate-recovery-owner-order-b", "2")
	lower, higher := first, second
	if lower.ReferenceKey > higher.ReferenceKey {
		lower, higher = higher, lower
	}
	orders := []struct {
		name       string
		mismatch   AssetPinRecoveryMarker
		legitimate AssetPinRecoveryMarker
	}{
		{name: "mismatch marker first", mismatch: lower, legitimate: higher},
		{name: "legitimate marker first", mismatch: higher, legitimate: lower},
	}
	freshnesses := []struct {
		name  string
		fresh bool
	}{
		{name: "fresh legitimate owner", fresh: true},
		{name: "stale legitimate owner", fresh: false},
	}

	for _, freshness := range freshnesses {
		for _, order := range orders {
			t.Run(freshness.name+"/"+order.name, func(t *testing.T) {
				mismatch := order.mismatch
				mismatch.Phase = AssetPinRecoveryPinnedUncommitted
				mismatch.CID = testRecoveryAlternateCID

				legitimate := order.legitimate
				legitimate.ExpectedCID = testRecoveryAlternateCID
				legitimate.Phase = AssetPinRecoveryPinnedUncommitted
				legitimate.CID = legitimate.ExpectedCID
				if freshness.fresh {
					legitimate.CreatedAt = now.Add(-time.Minute)
					legitimate.UpdatedAt = legitimate.CreatedAt
					legitimate.ExpiresAt = legitimate.CreatedAt.Add(90 * 24 * time.Hour)
				}

				store := newFakeRetentionStore(now)
				pins := &fakeRetentionPins{pinned: map[string]bool{testRecoveryAlternateCID: true}}
				recovery := newFakeRecoveryPager(mismatch, legitimate)
				retainer := mustTestRetainer(t, store, pins, recovery)

				if err := retainer.Sweep(context.Background(), now); err != nil {
					t.Fatalf("first Sweep() error = %v", err)
				}
				if len(pins.unpinCalls) != 0 {
					t.Fatalf("shared recovery CID was unpinned on first sweep: %v", pins.unpinCalls)
				}

				if freshness.fresh {
					if !recovery.has(mismatch.ReferenceKey) || !recovery.has(legitimate.ReferenceKey) {
						t.Fatalf("deferred recovery markers were consumed: %+v", recovery.markers)
					}
					if _, found := store.reference(legitimate.ReferenceKey); found {
						t.Fatal("fresh legitimate marker committed before its grace period")
					}
				}
				if recovery.has(mismatch.ReferenceKey) {
					secondSweepAt := now.Add(time.Minute)
					if freshness.fresh {
						secondSweepAt = now.Add(retentionRecoveryGracePeriod + 2*time.Minute)
					}
					if err := retainer.Sweep(context.Background(), secondSweepAt); err != nil {
						t.Fatalf("second Sweep() error = %v", err)
					}
					if recovery.has(mismatch.ReferenceKey) {
						if err := retainer.Sweep(context.Background(), secondSweepAt.Add(time.Minute)); err != nil {
							t.Fatalf("third Sweep() error = %v", err)
						}
					}
				}

				legitimateRef, found := store.reference(legitimate.ReferenceKey)
				if !found || legitimateRef.CID != testRecoveryAlternateCID {
					t.Fatalf("legitimate recovery reference = %+v, %v", legitimateRef, found)
				}
				if recovery.has(mismatch.ReferenceKey) || recovery.has(legitimate.ReferenceKey) {
					t.Fatalf("recoverable markers remain after ownership committed: %+v", recovery.markers)
				}
				if len(pins.unpinCalls) != 0 {
					t.Fatalf("shared recovery CID was unpinned: %v", pins.unpinCalls)
				}
			})
		}
	}
}

func TestRetainerSweepLeavesFreshRecoveryMarkersUntouched(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	intent := testAssetPinRecoveryMarker("candidate-retainer-fresh-intent", "d")
	intent.CreatedAt = now.Add(-time.Minute)
	intent.UpdatedAt = intent.CreatedAt
	intent.ExpiresAt = intent.CreatedAt.Add(90 * 24 * time.Hour)
	pinned := testAssetPinRecoveryMarker("candidate-retainer-fresh-pinned", "e")
	pinned.CreatedAt = now.Add(-time.Minute)
	pinned.UpdatedAt = pinned.CreatedAt
	pinned.ExpiresAt = pinned.CreatedAt.Add(90 * 24 * time.Hour)
	pinned.Phase = AssetPinRecoveryPinnedUncommitted
	pinned.CID = pinned.ExpectedCID
	store := newFakeRetentionStore(now)
	pins := &fakeRetentionPins{pinned: map[string]bool{intent.ExpectedCID: true}}
	recovery := newFakeRecoveryPager(intent, pinned)
	retainer := mustTestRetainer(t, store, pins, recovery)

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if !recovery.has(intent.ReferenceKey) || !recovery.has(pinned.ReferenceKey) {
		t.Fatalf("fresh recovery marker was consumed: %+v", recovery.markers)
	}
	if got := store.references(); len(got) != 0 {
		t.Fatalf("fresh marker created references: %+v", got)
	}
	if len(pins.checkCalls) != 0 || len(pins.unpinCalls) != 0 {
		t.Fatalf("fresh markers touched Kubo: check=%v unpin=%v", pins.checkCalls, pins.unpinCalls)
	}
}

func TestRetainerSweepFreshRecoveryMarkerProtectsMatchingExpiredCID(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	marker := testAssetPinRecoveryMarker("candidate-retainer-fresh-protected", "f")
	marker.CreatedAt = now.Add(-time.Minute)
	marker.UpdatedAt = marker.CreatedAt
	marker.ExpiresAt = marker.CreatedAt.Add(90 * 24 * time.Hour)
	expired := retentionReference("expired-while-uploading", marker.ExpectedCID, storage.AssetReferenceRejected, now.Add(-31*24*time.Hour), now.Add(-time.Hour))
	store := newFakeRetentionStore(now, expired)
	pins := &fakeRetentionPins{pinned: map[string]bool{marker.ExpectedCID: true}}
	recovery := newFakeRecoveryPager(marker)
	retainer := mustTestRetainer(t, store, pins, recovery)

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if _, exists := store.reference(expired.ReferenceKey); !exists {
		t.Fatal("active recovery CID lost its expired ledger retry record")
	}
	if len(pins.unpinCalls) != 0 {
		t.Fatalf("active recovery CID was unpinned: %v", pins.unpinCalls)
	}
	if !recovery.has(marker.ReferenceKey) {
		t.Fatal("fresh recovery marker was consumed")
	}
}

func TestRetainerSweepPreservesPinnedMarkerWhenLedgerCommitFails(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	marker := testAssetPinRecoveryMarker("candidate-retainer-commit-failure", "b")
	marker.Phase = AssetPinRecoveryPinnedUncommitted
	marker.CID = marker.ExpectedCID
	store := newFakeRetentionStore(now)
	store.upsertErr = errors.New("journal unavailable")
	pins := &fakeRetentionPins{pinned: map[string]bool{marker.CID: true}}
	recovery := newFakeRecoveryPager(marker)
	retainer := mustTestRetainer(t, store, pins, recovery)

	if err := retainer.Sweep(context.Background(), now); err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("Sweep() error = %v, want ledger failure", err)
	}
	if !recovery.has(marker.ReferenceKey) {
		t.Fatal("failed recovery commit removed retry marker")
	}
	if len(pins.unpinCalls) != 0 {
		t.Fatalf("failed recovery commit unpinned recoverable content: %v", pins.unpinCalls)
	}
}

func TestRetainerSweepClearsMarkerAfterCommittedReferenceAdvanced(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	marker := testAssetPinRecoveryMarker("candidate-retainer-advanced", "c")
	marker.Phase = AssetPinRecoveryPinnedUncommitted
	marker.CID = marker.ExpectedCID
	advanced := retentionReferenceFromMarker(marker)
	advanced.State = storage.AssetReferenceApproved
	advanced.GitHubIssue = 77
	advanced.DecisionSHA256 = strings.Repeat("d", 64)
	advanced.UpdatedAt = marker.UpdatedAt.Add(time.Hour)
	advanced.ExpiresAt = time.Time{}
	store := newFakeRetentionStore(now, advanced)
	pins := &fakeRetentionPins{pinned: map[string]bool{marker.CID: true}}
	recovery := newFakeRecoveryPager(marker)
	retainer := mustTestRetainer(t, store, pins, recovery)

	if err := retainer.Sweep(context.Background(), now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if recovery.has(marker.ReferenceKey) {
		t.Fatal("marker survived after its immutable ledger reference advanced")
	}
	got, exists := store.reference(marker.ReferenceKey)
	if !exists || got.State != storage.AssetReferenceApproved {
		t.Fatalf("advanced reference = %+v, %v; want approved and retained", got, exists)
	}
}

func TestRetainerEventIDsBindReferenceGeneration(t *testing.T) {
	first := retentionReference("recreated-key", "cid-generation", storage.AssetReferenceRejected,
		time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 1, time.UTC))
	second := first
	second.CreatedAt = first.CreatedAt.Add(24 * time.Hour)
	second.UpdatedAt = first.UpdatedAt.Add(24 * time.Hour)
	second.ExpiresAt = first.ExpiresAt.Add(24 * time.Hour)
	if firstID, secondID := retentionDeleteEvent(first).EventID, retentionDeleteEvent(second).EventID; firstID == secondID {
		t.Fatalf("delete event IDs collide across generations: %s", firstID)
	}
	if firstID, secondID := retentionAbandonEvent(first, first.ExpiresAt).EventID, retentionAbandonEvent(second, second.ExpiresAt).EventID; firstID == secondID {
		t.Fatalf("abandon event IDs collide across generations: %s", firstID)
	}
}

func TestRetainerFileRecoveryPagesAreBoundedAndDeterministic(t *testing.T) {
	store, err := NewFileAssetPinRecoveryStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileAssetPinRecoveryStore() error = %v", err)
	}
	want := make([]string, 0, 7)
	for index, discriminator := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		marker := testAssetPinRecoveryMarker(fmt.Sprintf("candidate-retention-page-%d", index), discriminator)
		if err := store.CreateIntent(marker); err != nil {
			t.Fatalf("CreateIntent(%d) error = %v", index, err)
		}
		want = append(want, marker.ReferenceKey)
	}
	sort.Strings(want)
	recoveryDir := filepath.Join(store.dataDir, "asset-pins", "recovery")
	if err := os.WriteFile(filepath.Join(recoveryDir, "00-ignored.tmp"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write ignored fixture: %v", err)
	}

	var got []string
	cursor := ""
	for {
		page, next, err := store.ListPage(context.Background(), cursor, 2)
		if err != nil {
			t.Fatalf("ListPage(%q) error = %v", cursor, err)
		}
		if len(page) > 2 {
			t.Fatalf("page length = %d, exceeds bound", len(page))
		}
		for _, marker := range page {
			got = append(got, marker.ReferenceKey)
		}
		if len(page) < 2 {
			break
		}
		if next <= cursor {
			t.Fatalf("cursor did not advance: %q -> %q", cursor, next)
		}
		cursor = next
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paged keys = %v, want deterministic order %v", got, want)
	}
}

func TestRetainerKuboPinLookupIsStrictAndBounded(t *testing.T) {
	t.Run("exact recursive pin query", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v0/pin/ls" {
				t.Errorf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.URL.Query()["arg"]; !reflect.DeepEqual(got, []string{testRecoveryCID}) {
				t.Errorf("arg = %v", got)
			}
			if got := r.URL.Query()["type"]; !reflect.DeepEqual(got, []string{"recursive"}) {
				t.Errorf("type = %v", got)
			}
			_, _ = io.WriteString(w, `{"Keys":{"`+testRecoveryCID+`":{"Type":"recursive"}}}`)
		}))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatalf("NewKuboRetentionClient() error = %v", err)
		}
		pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID)
		if err != nil || !pinned {
			t.Fatalf("IsAssetCIDPinned() = %v, %v; want true", pinned, err)
		}
	})

	for _, receipt := range []string{
		`{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive","Name":""}}}`,
		`{"Keys":{"` + testRecoveryCID + `":{"Name":"reviewed asset","Type":"recursive"}}}`,
	} {
		t.Run("Kubo v0.39 named recursive pin", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, receipt)
			}))
			defer server.Close()
			client, err := NewKuboRetentionClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID); err != nil || !pinned {
				t.Fatalf("IsAssetCIDPinned() = %v, %v; want named recursive pin", pinned, err)
			}
		})
	}

	t.Run("missing is explicit false", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"Message":"path is not pinned","Code":0,"Type":"error"}`)
		}))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID)
		if err != nil || pinned {
			t.Fatalf("IsAssetCIDPinned() = %v, %v; want explicit missing", pinned, err)
		}
	})

	t.Run("Boxo not-pinned error is explicit false", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"Message":"not pinned or pinned indirectly","Code":0,"Type":"error"}`)
		}))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID)
		if err != nil || pinned {
			t.Fatalf("IsAssetCIDPinned() = %v, %v; want exact Boxo missing pin", pinned, err)
		}
	})

	t.Run("oversized success is rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), retentionKuboMaxResponseBytes+1))
		}))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized response error = %v", err)
		}
	})

	t.Run("uncertain responses never become missing", func(t *testing.T) {
		tests := []struct {
			name   string
			status int
			body   []byte
		}{
			{name: "malformed success", status: http.StatusOK, body: []byte(`{"Keys":`)},
			{name: "empty successful key set", status: http.StatusOK, body: []byte(`{"Keys":{}}`)},
			{name: "unrecognized failure", status: http.StatusBadGateway, body: []byte(`backend uncertain`)},
			{name: "substring failure", status: http.StatusInternalServerError, body: []byte(`backend says maybe not pinned while retrying`)},
			{name: "wrong-status structured failure", status: http.StatusBadGateway, body: []byte(`{"Message":"path is not pinned","Code":0,"Type":"error"}`)},
			{name: "extra-field structured failure", status: http.StatusInternalServerError, body: []byte(`{"Message":"path is not pinned","Code":0,"Type":"error","Extra":true}`)},
			{name: "duplicate-field structured failure", status: http.StatusInternalServerError, body: []byte(`{"Message":"path is not pinned","Message":"path is not pinned","Code":0,"Type":"error"}`)},
			{name: "extra-data structured failure", status: http.StatusInternalServerError, body: []byte(`{"Message":"path is not pinned","Code":0,"Type":"error"} trailing`)},
			{name: "truncated not-pinned failure", status: http.StatusInternalServerError, body: append([]byte(`not pinned `), bytes.Repeat([]byte("x"), retentionKuboMaxResponseBytes)...)},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(test.status)
					_, _ = w.Write(test.body)
				}))
				defer server.Close()
				client, err := NewKuboRetentionClient(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID); err == nil {
					t.Fatalf("IsAssetCIDPinned() = %v, nil; uncertain response must error", pinned)
				}
			})
		}
	})

	t.Run("pin rm requires exact structured missing response", func(t *testing.T) {
		responses := []struct {
			body    string
			wantErr bool
		}{
			{body: `{"Message":"path is not pinned","Code":0,"Type":"error"}`},
			{body: `Error: CID is not pinned`, wantErr: true},
			{body: `{"Message":"path is not pinned","Code":1,"Type":"error"}`, wantErr: true},
		}
		for _, response := range responses {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, response.body)
			}))
			client, err := NewKuboRetentionClient(server.URL)
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			err = client.UnpinAssetCID(context.Background(), testRecoveryCID)
			server.Close()
			if (err != nil) != response.wantErr {
				t.Errorf("UnpinAssetCID() error = %v for %q, wantErr=%v", err, response.body, response.wantErr)
			}
		}
	})

	t.Run("decoder errors do not echo backend fields", func(t *testing.T) {
		const backendControlled = "backend-secret-field"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"Keys":{},"`+backendControlled+`":"do-not-log"}`)
		}))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.IsAssetCIDPinned(context.Background(), testRecoveryCID)
		if err == nil {
			t.Fatal("backend-controlled unknown field was accepted")
		}
		if strings.Contains(err.Error(), backendControlled) || strings.Contains(err.Error(), "do-not-log") {
			t.Fatalf("decoder error leaked backend response data: %v", err)
		}
	})

	t.Run("redirect is rejected", func(t *testing.T) {
		targetRequests := 0
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetRequests++
			_, _ = io.WriteString(w, `{"Keys":{"`+testRecoveryCID+`":{"Type":"recursive"}}}`)
		}))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer redirect.Close()
		client, err := NewKuboRetentionClient(redirect.URL)
		if err != nil {
			t.Fatal(err)
		}
		if pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID); err == nil {
			t.Fatalf("redirect pin lookup = %v, nil; want error", pinned)
		}
		if targetRequests != 0 {
			t.Fatalf("redirect target requests = %d, want 0", targetRequests)
		}
	})

	t.Run("invalid inputs make no request", func(t *testing.T) {
		for _, rawURL := range []string{"", " https://kubo.example", "https://user:secret@kubo.example", "https://kubo.example?token=secret", "https://kubo.example/#fragment", "https://kubo.example:", "https://kubo.example:0", "https://kubo.example:65536", "https://kubo.example:notaport"} {
			if client, err := NewKuboRetentionClient(rawURL); err == nil || client != nil {
				t.Errorf("NewKuboRetentionClient(%q) = %#v, %v; want rejection", rawURL, client, err)
			}
		}
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{"", " not-a-cid", "not-a-cid", strings.ToUpper(testRecoveryCID)} {
			if pinned, err := client.IsAssetCIDPinned(context.Background(), value); err == nil {
				t.Errorf("IsAssetCIDPinned(%q) = %v, nil; want canonical CID rejection", value, pinned)
			}
		}
		if requests != 0 {
			t.Fatalf("invalid CID requests = %d, want 0", requests)
		}
	})
}

func TestRetainerKuboRequiresExactProtocolReceipts(t *testing.T) {
	t.Run("pin ls", func(t *testing.T) {
		valid := `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive"}}}`
		tests := []struct {
			name   string
			status int
			body   string
		}{
			{name: "other 2xx", status: http.StatusCreated, body: valid},
			{name: "no content", status: http.StatusNoContent},
			{name: "empty", status: http.StatusOK},
			{name: "html", status: http.StatusOK, body: `<html>backend-secret</html>`},
			{name: "empty object", status: http.StatusOK, body: `{}`},
			{name: "empty keys", status: http.StatusOK, body: `{"Keys":{}}`},
			{name: "multiple keys", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive"},"` + testRecoveryAlternateCID + `":{"Type":"recursive"}}}`},
			{name: "wrong key", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryAlternateCID + `":{"Type":"recursive"}}}`},
			{name: "wrong type", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"direct"}}}`},
			{name: "case variant keys", status: http.StatusOK, body: `{"keys":{"` + testRecoveryCID + `":{"Type":"recursive"}}}`},
			{name: "case variant type", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"type":"recursive"}}}`},
			{name: "unknown root", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive"}},"backend-secret-field":true}`},
			{name: "unknown entry", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive","backend-secret-field":true}}}`},
			{name: "duplicate keys", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive"}},"Keys":{"` + testRecoveryCID + `":{"Type":"recursive"}}}`},
			{name: "duplicate type", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive","Type":"recursive"}}}`},
			{name: "name wrong type", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive","Name":7}}}`},
			{name: "duplicate name", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Type":"recursive","Name":"","Name":""}}}`},
			{name: "name without type", status: http.StatusOK, body: `{"Keys":{"` + testRecoveryCID + `":{"Name":"asset"}}}`},
			{name: "trailing data", status: http.StatusOK, body: valid + ` backend-secret-trailer`},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, test.body)
				}))
				defer server.Close()
				client, err := NewKuboRetentionClient(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if pinned, err := client.IsAssetCIDPinned(context.Background(), testRecoveryCID); err == nil {
					t.Fatalf("IsAssetCIDPinned() = %v, nil; want uncertain protocol error", pinned)
				} else if strings.Contains(err.Error(), "backend-secret") {
					t.Fatalf("pin/ls error leaked body-derived text: %v", err)
				}
			})
		}
	})

	t.Run("pin rm", func(t *testing.T) {
		valid := `{"Pins":["` + testRecoveryCID + `"]}`
		tests := []struct {
			name   string
			status int
			body   string
		}{
			{name: "other 2xx", status: http.StatusCreated, body: valid},
			{name: "no content", status: http.StatusNoContent},
			{name: "empty", status: http.StatusOK},
			{name: "html", status: http.StatusOK, body: `<html>backend-secret</html>`},
			{name: "empty object", status: http.StatusOK, body: `{}`},
			{name: "empty pins", status: http.StatusOK, body: `{"Pins":[]}`},
			{name: "multiple pins", status: http.StatusOK, body: `{"Pins":["` + testRecoveryCID + `","` + testRecoveryAlternateCID + `"]}`},
			{name: "wrong pin", status: http.StatusOK, body: `{"Pins":["` + testRecoveryAlternateCID + `"]}`},
			{name: "case variant", status: http.StatusOK, body: `{"pins":["` + testRecoveryCID + `"]}`},
			{name: "unknown field", status: http.StatusOK, body: `{"Pins":["` + testRecoveryCID + `"],"backend-secret-field":true}`},
			{name: "duplicate pins", status: http.StatusOK, body: `{"Pins":["` + testRecoveryCID + `"],"Pins":["` + testRecoveryCID + `"]}`},
			{name: "trailing data", status: http.StatusOK, body: valid + ` backend-secret-trailer`},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, test.body)
				}))
				defer server.Close()
				client, err := NewKuboRetentionClient(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if err := client.UnpinAssetCID(context.Background(), testRecoveryCID); err == nil {
					t.Fatal("UnpinAssetCID() accepted uncertain protocol receipt")
				} else if strings.Contains(err.Error(), "backend-secret") {
					t.Fatalf("pin/rm error leaked body-derived text: %v", err)
				}
			})
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, valid)
		}))
		defer server.Close()
		client, err := NewKuboRetentionClient(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.UnpinAssetCID(context.Background(), testRecoveryCID); err != nil {
			t.Fatalf("exact pin/rm receipt error = %v", err)
		}
	})
}

func TestRetainerSweepRetainsRowsWhenKuboUnpinReceiptIsUncertain(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	expired := retentionReference("uncertain-unpin-receipt", testRecoveryCID, storage.AssetReferenceRejected, now.Add(-31*24*time.Hour), now.Add(-time.Hour))
	store := newFakeRetentionStore(now, expired)
	unpinRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/pin/ls":
			_, _ = io.WriteString(w, `{"Keys":{"`+testRecoveryCID+`":{"Type":"recursive"}}}`)
		case "/api/v0/pin/rm":
			unpinRequests++
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	pins, err := NewKuboRetentionClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())

	err = retainer.Sweep(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "pin/rm") {
		t.Fatalf("Sweep() error = %v, want sanitized uncertain pin/rm failure", err)
	}
	if _, exists := store.reference(expired.ReferenceKey); !exists {
		t.Fatal("uncertain Kubo unpin receipt deleted the retry row")
	}
	if unpinRequests != 1 {
		t.Fatalf("pin/rm requests = %d, want one", unpinRequests)
	}
	if len(store.deleteKinds()) != 0 {
		t.Fatalf("uncertain receipt wrote delete audit: %v", store.deleteKinds())
	}
}

func TestRetainerRunSweepsAtStartupAndStopsWithContext(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := newFakeRetentionStore(now)
	pins := &fakeRetentionPins{pinned: map[string]bool{}}
	retainer := mustTestRetainer(t, store, pins, newFakeRecoveryPager())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		retainer.Run(ctx, 10*time.Millisecond, func() time.Time { return now }, func(error) {})
	}()

	deadline := time.Now().Add(time.Second)
	// Each empty sweep performs the stale scan and the final all-CID
	// reconciliation scan, so four calls prove startup plus one ticker sweep.
	for store.listCount() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := store.listCount(); got < 4 {
		t.Fatalf("sweep count = %d, want startup plus ticker", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func retentionReference(key, cid string, state storage.AssetReferenceState, updatedAt, expiresAt time.Time) storage.AssetPinReference {
	ref := storage.AssetPinReference{
		ReferenceKey: key,
		CandidateKey: "candidate-" + key,
		CID:          cid,
		SHA256:       strings.Repeat("a", 64),
		ByteCount:    128,
		State:        state,
		CreatedAt:    updatedAt.Add(-time.Hour),
		UpdatedAt:    updatedAt,
		ExpiresAt:    expiresAt,
	}
	switch state {
	case storage.AssetReferenceReviewOpen:
		ref.GitHubIssue = 42
	case storage.AssetReferenceApproved, storage.AssetReferenceRejected, storage.AssetReferenceSuperseded:
		ref.GitHubIssue = 42
		ref.DecisionSHA256 = strings.Repeat("d", 64)
	}
	return ref
}

// mustTestRetainer builds the fixture retainer for the FUNCTIONAL retention
// tests — the ones asserting what a sweep does, not how fast it does it.
//
// It deliberately runs the shipped per-call deadline. That deadline is a
// LIVENESS bound ("never block forever on a wedged store"), not a latency
// assertion, and the value that asserts it exists belongs in the one test that
// asserts it: TestRetainerSweepBoundsEveryKuboCall drives its own 20 ms
// deadline against a store that blocks forever. This fixture previously used
// 100 ms, which quietly turned every functional test into a bet on how fast the
// box's disk was — one live asset-pin mutation is two fsyncs, ~26 ms on an idle
// box and far more under a parallel test tier, so the suite failed on machine
// load rather than on code (sdn-assetpin-retainer-calltimeout-flake). Using the
// production default keeps every functional test bounded, keeps them on the
// configuration the daemon actually runs, and stops them racing the disk.
func mustTestRetainer(t *testing.T, store RetentionStore, pins RetentionPins, recovery RetentionRecoveryStore) *Retainer {
	t.Helper()
	retainer, err := NewRetainer(RetainerOptions{
		Store:            store,
		Pins:             pins,
		Recovery:         recovery,
		Gate:             NewMutationGate(),
		CallTimeout:      defaultRetentionCallTimeout,
		RecoveryPageSize: 2,
	})
	if err != nil {
		t.Fatalf("NewRetainer() error = %v", err)
	}
	return retainer
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameStringSet(got, want []string) bool {
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	return reflect.DeepEqual(gotCopy, wantCopy)
}

type fakeRetentionStore struct {
	mu               sync.Mutex
	now              time.Time
	refs             map[string]storage.AssetPinReference
	upserts          []storage.AssetPinAuditEvent
	transitions      []storage.AssetPinAuditEvent
	deletes          []storage.AssetPinAuditEvent
	listCalls        int
	upsertErr        error
	transitionErrFor map[string]error
	deleteStarted    chan struct{}
	allowDelete      chan struct{}
}

func newFakeRetentionStore(now time.Time, refs ...storage.AssetPinReference) *fakeRetentionStore {
	store := &fakeRetentionStore{now: now, refs: make(map[string]storage.AssetPinReference)}
	for _, ref := range refs {
		store.refs[ref.ReferenceKey] = ref
	}
	return store
}

func (s *fakeRetentionStore) FindAssetPinReference(_ context.Context, key string) (storage.AssetPinReference, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.refs[key]
	return ref, ok, nil
}

func (s *fakeRetentionStore) UpsertAssetPinReference(_ context.Context, ref storage.AssetPinReference, event storage.AssetPinAuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.refs[ref.ReferenceKey] = ref
	s.upserts = append(s.upserts, event)
	return nil
}

func (s *fakeRetentionStore) ListAssetPinReferences(_ context.Context, _ storage.AssetPinReferenceQuery) ([]storage.AssetPinReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	refs := make([]storage.AssetPinReference, 0, len(s.refs))
	for _, ref := range s.refs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ReferenceKey < refs[j].ReferenceKey })
	return refs, nil
}

func (s *fakeRetentionStore) TransitionAssetPinReference(_ context.Context, transition storage.AssetPinReferenceTransition, event storage.AssetPinAuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.transitionErrFor[transition.ReferenceKey]; err != nil {
		return err
	}
	ref, ok := s.refs[transition.ReferenceKey]
	if !ok {
		return storage.ErrAssetPinReferenceNotFound
	}
	if ref.State != transition.FromState {
		return storage.ErrAssetPinReferenceConflict
	}
	ref.State = transition.ToState
	ref.GitHubIssue = transition.GitHubIssue
	ref.DecisionSHA256 = transition.DecisionSHA256
	ref.UpdatedAt = transition.UpdatedAt
	ref.ExpiresAt = transition.ExpiresAt
	s.refs[ref.ReferenceKey] = ref
	s.transitions = append(s.transitions, event)
	return nil
}

func (s *fakeRetentionStore) ListExpiredAssetPinReferences(_ context.Context, now time.Time) ([]storage.AssetPinReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var refs []storage.AssetPinReference
	for _, ref := range s.refs {
		eligible := ref.State == storage.AssetReferenceStaged || ref.State == storage.AssetReferenceRejected ||
			ref.State == storage.AssetReferenceSuperseded || ref.State == storage.AssetReferenceAbandoned
		if eligible && !ref.ExpiresAt.IsZero() && !ref.ExpiresAt.After(now) {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].CID != refs[j].CID {
			return refs[i].CID < refs[j].CID
		}
		return refs[i].ReferenceKey < refs[j].ReferenceKey
	})
	return refs, nil
}

func (s *fakeRetentionStore) CountProtectedAssetReferences(_ context.Context, cid string, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, ref := range s.refs {
		if ref.CID != cid {
			continue
		}
		switch ref.State {
		case storage.AssetReferenceApproved, storage.AssetReferenceReviewOpen:
			count++
		case storage.AssetReferenceStaged, storage.AssetReferenceRejected, storage.AssetReferenceSuperseded:
			if ref.ExpiresAt.IsZero() || ref.ExpiresAt.After(now) {
				count++
			}
		}
	}
	return count, nil
}

func (s *fakeRetentionStore) DeleteExpiredAssetPinReference(ctx context.Context, key string, event storage.AssetPinAuditEvent) error {
	if s.deleteStarted != nil {
		select {
		case <-s.deleteStarted:
		default:
			close(s.deleteStarted)
		}
	}
	if s.allowDelete != nil {
		select {
		case <-s.allowDelete:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.refs[key]
	if !ok {
		return storage.ErrAssetPinReferenceNotFound
	}
	if ref.ExpiresAt.IsZero() || ref.ExpiresAt.After(s.now.Add(24*time.Hour)) {
		return storage.ErrAssetPinReferenceNotExpired
	}
	delete(s.refs, key)
	s.deletes = append(s.deletes, event)
	return nil
}

func (s *fakeRetentionStore) reference(key string) (storage.AssetPinReference, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.refs[key]
	return ref, ok
}

func (s *fakeRetentionStore) references() []storage.AssetPinReference {
	refs, _ := s.ListAssetPinReferences(context.Background(), storage.AssetPinReferenceQuery{})
	return refs
}

func (s *fakeRetentionStore) upsertKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return retentionEventKinds(s.upserts)
}

func (s *fakeRetentionStore) transitionKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return retentionEventKinds(s.transitions)
}

func (s *fakeRetentionStore) deleteKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return retentionEventKinds(s.deletes)
}

func (s *fakeRetentionStore) listCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls
}

func retentionEventKinds(events []storage.AssetPinAuditEvent) []string {
	result := make([]string, len(events))
	for index, event := range events {
		result[index] = event.Kind
	}
	return result
}

type fakeRetentionPins struct {
	mu           sync.Mutex
	pinned       map[string]bool
	checkCalls   []string
	unpinCalls   []string
	failUnpinFor map[string]int
	blockChecks  bool
	checkStarted chan struct{}
	allowCheck   chan struct{}
}

func (p *fakeRetentionPins) IsAssetCIDPinned(ctx context.Context, cid string) (bool, error) {
	if p.blockChecks {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if p.checkStarted != nil {
		select {
		case <-p.checkStarted:
		default:
			close(p.checkStarted)
		}
	}
	if p.allowCheck != nil {
		select {
		case <-p.allowCheck:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checkCalls = append(p.checkCalls, cid)
	return p.pinned[cid], nil
}

func (p *fakeRetentionPins) UnpinAssetCID(_ context.Context, cid string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unpinCalls = append(p.unpinCalls, cid)
	if p.failUnpinFor[cid] > 0 {
		p.failUnpinFor[cid]--
		return errors.New("injected unpin failure")
	}
	p.pinned[cid] = false
	return nil
}

type fakeRecoveryPager struct {
	mu      sync.Mutex
	markers map[string]AssetPinRecoveryMarker
	listErr error
}

func newFakeRecoveryPager(markers ...AssetPinRecoveryMarker) *fakeRecoveryPager {
	store := &fakeRecoveryPager{markers: make(map[string]AssetPinRecoveryMarker)}
	for _, marker := range markers {
		store.markers[marker.ReferenceKey] = marker
	}
	return store
}

func (s *fakeRecoveryPager) ListPage(ctx context.Context, after string, limit int) ([]AssetPinRecoveryMarker, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	keys := make([]string, 0, len(s.markers))
	for key := range s.markers {
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	page := make([]AssetPinRecoveryMarker, 0, len(keys))
	for _, key := range keys {
		page = append(page, s.markers[key])
	}
	next := after
	if len(keys) > 0 {
		next = keys[len(keys)-1]
	}
	return page, next, nil
}

type blockingRetentionRecoveryPager struct {
	started chan struct{}
}

func (s *blockingRetentionRecoveryPager) ListPage(ctx context.Context, _ string, _ int) ([]AssetPinRecoveryMarker, string, error) {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-ctx.Done()
	return nil, "", ctx.Err()
}

func (*blockingRetentionRecoveryPager) Remove(string) error { return nil }

func (s *fakeRecoveryPager) Remove(referenceKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.markers, referenceKey)
	return nil
}

func (s *fakeRecoveryPager) has(referenceKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.markers[referenceKey]
	return ok
}
