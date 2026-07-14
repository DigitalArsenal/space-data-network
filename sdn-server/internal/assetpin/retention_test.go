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

func TestRetainerSweepKeepsFailedUnpinRetryable(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
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
		page, next, err := store.ListPage(cursor, 2)
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

func mustTestRetainer(t *testing.T, store RetentionStore, pins RetentionPins, recovery RetentionRecoveryStore) *Retainer {
	t.Helper()
	retainer, err := NewRetainer(RetainerOptions{
		Store:            store,
		Pins:             pins,
		Recovery:         recovery,
		CallTimeout:      100 * time.Millisecond,
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

func (s *fakeRetentionStore) DeleteExpiredAssetPinReference(_ context.Context, key string, event storage.AssetPinAuditEvent) error {
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
}

func (p *fakeRetentionPins) IsAssetCIDPinned(ctx context.Context, cid string) (bool, error) {
	if p.blockChecks {
		<-ctx.Done()
		return false, ctx.Err()
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

func (s *fakeRecoveryPager) ListPage(after string, limit int) ([]AssetPinRecoveryMarker, string, error) {
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
