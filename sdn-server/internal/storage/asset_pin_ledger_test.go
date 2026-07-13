package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestAssetPinOIDCTokenDigestIsConsumedAtomicallyOnce(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	consumedAt := time.Date(2026, 7, 13, 12, 0, 0, 123456789, time.UTC)
	receipt := AssetOIDCReceipt{
		Digest:      strings.Repeat("a", 64),
		ExpiresAt:   consumedAt.Add(10 * time.Minute),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "123456789",
		RunAttempt:  "2",
		SHA:         strings.Repeat("b", 40),
		ConsumedAt:  consumedAt,
	}

	const attempts = 48
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.ConsumeAssetOIDCToken(ctx, receipt)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	replays := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAssetOIDCTokenReplay):
			replays++
		default:
			t.Fatalf("ConsumeAssetOIDCToken() error = %v, want success or ErrAssetOIDCTokenReplay", err)
		}
	}
	if successes != 1 || replays != attempts-1 {
		t.Fatalf("concurrent consume results = %d successes, %d replays; want 1 success and %d replays", successes, replays, attempts-1)
	}

	got, ok, err := store.FindAssetOIDCReceipt(ctx, receipt.Digest)
	if err != nil {
		t.Fatalf("FindAssetOIDCReceipt() error = %v", err)
	}
	if !ok {
		t.Fatal("FindAssetOIDCReceipt() did not find consumed digest")
	}
	if got.Digest != receipt.Digest || got.Repository != receipt.Repository || got.RunAttempt != receipt.RunAttempt || got.SHA != receipt.SHA {
		t.Fatalf("FindAssetOIDCReceipt() = %+v, want audit metadata %+v", got, receipt)
	}
	if !got.ExpiresAt.Equal(receipt.ExpiresAt) || !got.ConsumedAt.Equal(receipt.ConsumedAt) {
		t.Fatalf("receipt times = expires %v consumed %v, want %v and %v", got.ExpiresAt, got.ConsumedAt, receipt.ExpiresAt, receipt.ConsumedAt)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_asset_oidc_receipts WHERE digest = ?`, receipt.Digest).Scan(&count); err != nil {
		t.Fatalf("count OIDC receipts: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored OIDC receipt rows = %d, want 1", count)
	}
}

func TestAssetPinOIDCTokenReplayUsesSemanticReceiptIdentity(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newAssetPinTestStore(t, basePath)
	ctx := context.Background()
	consumedAt := time.Date(2026, 7, 13, 12, 30, 0, 123456789, time.UTC)
	receipt := AssetOIDCReceipt{
		Digest:      strings.Repeat("c", 64),
		ExpiresAt:   consumedAt.Add(10 * time.Minute),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "semantic-replay",
		RunAttempt:  "1",
		SHA:         strings.Repeat("d", 40),
		ConsumedAt:  consumedAt,
	}
	if err := store.ConsumeAssetOIDCToken(ctx, receipt); err != nil {
		t.Fatalf("first ConsumeAssetOIDCToken() error = %v", err)
	}
	journalSize := auxiliaryJournalSizeForTest(t, basePath)

	replayed := receipt
	replayed.ConsumedAt = consumedAt.Add(time.Nanosecond)
	if err := store.ConsumeAssetOIDCToken(ctx, replayed); !errors.Is(err, ErrAssetOIDCTokenReplay) {
		t.Fatalf("second ConsumeAssetOIDCToken() error = %v, want ErrAssetOIDCTokenReplay", err)
	}
	if got := auxiliaryJournalSizeForTest(t, basePath); got != journalSize {
		t.Fatalf("journal size after semantic replay = %d, want %d", got, journalSize)
	}
	assertSingleAssetOIDCReceiptForTest(t, ctx, store, receipt)

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened := newAssetPinTestStore(t, basePath)
	assertSingleAssetOIDCReceiptForTest(t, ctx, reopened, receipt)
	frames, err := reopened.auxiliaryMetadata.Replay(reopened)
	if err != nil {
		t.Fatalf("second Replay() error = %v", err)
	}
	if frames != 1 {
		t.Fatalf("replayed auxiliary frames = %d, want 1 receipt frame", frames)
	}
	assertSingleAssetOIDCReceiptForTest(t, ctx, reopened, receipt)
}

func TestAssetPinCommitFailureBlocksConflictingUpsertUntilReopen(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newAssetPinTestStore(t, basePath)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 3, 0, 0, 123456789, time.UTC)
	first := testAssetPinReference("reference-commit-first", "candidate-commit-shared", "bafybeicommitfirst", strings.Repeat("1", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	firstEvent := testAssetPinEvent("event-commit-first", "reference_upsert", first, now)
	commitFailure := errors.New("injected asset pin commit failure")
	store.assetPinTransactions = failingAssetPinTransactionBeginner{
		delegate:  store.assetPinTransactions,
		commitErr: commitFailure,
	}

	before := auxiliaryJournalSizeForTest(t, basePath)
	err := store.UpsertAssetPinReference(ctx, first, firstEvent)
	if !errors.Is(err, commitFailure) {
		t.Fatalf("first UpsertAssetPinReference() error = %v, want injected commit failure", err)
	}
	if !errors.Is(err, ErrAssetPinLedgerRecoveryRequired) {
		t.Errorf("first UpsertAssetPinReference() error = %v, want ErrAssetPinLedgerRecoveryRequired", err)
	}
	afterFirst := auxiliaryJournalSizeForTest(t, basePath)
	if afterFirst <= before {
		t.Fatalf("journal size after failed commit = %d, want greater than %d", afterFirst, before)
	}

	second := testAssetPinReference("reference-commit-second", first.CandidateKey, "bafybeicommitsecond", strings.Repeat("2", 64), AssetReferenceStaged, now.Add(time.Second), now.Add(2*time.Hour))
	secondEvent := testAssetPinEvent("event-commit-second", "reference_upsert", second, second.CreatedAt)
	if err := store.UpsertAssetPinReference(ctx, second, secondEvent); !errors.Is(err, ErrAssetPinLedgerRecoveryRequired) {
		t.Fatalf("second UpsertAssetPinReference() error = %v, want ErrAssetPinLedgerRecoveryRequired", err)
	}
	if afterSecond := auxiliaryJournalSizeForTest(t, basePath); afterSecond != afterFirst {
		t.Fatalf("journal size after blocked upsert = %d, want %d", afterSecond, afterFirst)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened := newAssetPinTestStore(t, basePath)
	got, ok, err := reopened.FindAssetPinReference(ctx, first.ReferenceKey)
	if err != nil || !ok || !equalAssetPinReference(got, first) {
		t.Fatalf("first reference after reopen = %+v, %v, %v; want %+v", got, ok, err, first)
	}
	if got, ok, err := reopened.FindAssetPinReference(ctx, second.ReferenceKey); err != nil || ok {
		t.Fatalf("second reference after reopen = %+v, %v, %v; want absent", got, ok, err)
	}
}

func TestAssetPinCommitFailureBlocksCompetingTransitionUntilReopen(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newAssetPinTestStore(t, basePath)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 3, 30, 0, 123456789, time.UTC)
	ref := testAssetPinReference("reference-transition-commit", "candidate-transition-commit", "bafybeitransitioncommit", strings.Repeat("3", 64), AssetReferenceStaged, now, now.Add(3*time.Hour))
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-transition-commit-upsert", "reference_upsert", ref, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	commitFailure := errors.New("injected asset pin transition commit failure")
	store.assetPinTransactions = failingAssetPinTransactionBeginner{
		delegate:  store.assetPinTransactions,
		commitErr: commitFailure,
	}
	first := AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  5150,
		UpdatedAt:    now.Add(time.Minute),
		ExpiresAt:    ref.ExpiresAt,
	}
	firstEvent := testAssetPinEvent("event-transition-commit-first", "reference_transition", ref, first.UpdatedAt)
	firstEvent.Result = string(first.ToState)

	before := auxiliaryJournalSizeForTest(t, basePath)
	err := store.TransitionAssetPinReference(ctx, first, firstEvent)
	if !errors.Is(err, commitFailure) {
		t.Fatalf("first TransitionAssetPinReference() error = %v, want injected commit failure", err)
	}
	if !errors.Is(err, ErrAssetPinLedgerRecoveryRequired) {
		t.Errorf("first TransitionAssetPinReference() error = %v, want ErrAssetPinLedgerRecoveryRequired", err)
	}
	afterFirst := auxiliaryJournalSizeForTest(t, basePath)
	if afterFirst <= before {
		t.Fatalf("journal size after failed transition commit = %d, want greater than %d", afterFirst, before)
	}

	second := AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceRejected,
		UpdatedAt:    now.Add(2 * time.Minute),
		ExpiresAt:    now.Add(5 * time.Hour),
	}
	secondEvent := testAssetPinEvent("event-transition-commit-second", "reference_transition", ref, second.UpdatedAt)
	secondEvent.Result = string(second.ToState)
	if err := store.TransitionAssetPinReference(ctx, second, secondEvent); !errors.Is(err, ErrAssetPinLedgerRecoveryRequired) {
		t.Fatalf("second TransitionAssetPinReference() error = %v, want ErrAssetPinLedgerRecoveryRequired", err)
	}
	if afterSecond := auxiliaryJournalSizeForTest(t, basePath); afterSecond != afterFirst {
		t.Fatalf("journal size after blocked transition = %d, want %d", afterSecond, afterFirst)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened := newAssetPinTestStore(t, basePath)
	got, ok, err := reopened.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("reference after reopen = %+v, %v, %v; want present", got, ok, err)
	}
	if got.State != first.ToState || got.GitHubIssue != first.GitHubIssue || !got.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("reference after reopen = %+v, want only first transition %+v", got, first)
	}
	secondEvents, err := reopened.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: secondEvent.EventID})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents(second) error = %v", err)
	}
	if len(secondEvents) != 0 {
		t.Fatalf("second transition audit events after reopen = %+v, want none", secondEvents)
	}
}

func TestAssetPinAppendFailureDoesNotRequireRecovery(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 4, 0, 0, 123456789, time.UTC)
	if err := store.auxiliaryMetadata.f.Close(); err != nil {
		t.Fatalf("close auxiliary metadata file: %v", err)
	}
	ref := testAssetPinReference("reference-append-failure", "candidate-append-failure", "bafybeiappendfailure", strings.Repeat("4", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-append-failure", "reference_upsert", ref, now))
	if err == nil {
		t.Fatal("UpsertAssetPinReference() succeeded with closed auxiliary journal")
	}
	if errors.Is(err, ErrAssetPinLedgerRecoveryRequired) {
		t.Fatalf("UpsertAssetPinReference() error = %v, append failure must not require recovery", err)
	}
	if err := store.requireWritable("probe append failure"); err != nil {
		t.Fatalf("requireWritable() after append failure = %v, want writable", err)
	}
}

func TestAssetPinAppendOutcomeControlsRecovery(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name             string
		mode             auxiliaryAppendFaultMode
		wantRecovery     bool
		wantBytesChanged bool
		wantFirst        bool
		wantSecond       bool
	}{
		{
			name:             "partial payload write",
			mode:             auxiliaryAppendFaultPartialPayload,
			wantRecovery:     true,
			wantBytesChanged: true,
		},
		{
			name:             "reported sync error",
			mode:             auxiliaryAppendFaultSync,
			wantRecovery:     true,
			wantBytesChanged: true,
			wantFirst:        true,
		},
		{
			name:         "zero byte header failure",
			mode:         auxiliaryAppendFaultZeroHeader,
			wantRecovery: false,
			wantSecond:   true,
		},
		{
			name:         "negative write count",
			mode:         auxiliaryAppendFaultNegativeCount,
			wantRecovery: true,
		},
		{
			name:         "oversized write count",
			mode:         auxiliaryAppendFaultOversizedCount,
			wantRecovery: true,
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			basePath := filepath.Join(t.TempDir(), "store")
			store := newAssetPinTestStore(t, basePath)
			now := time.Date(2026, 7, 14, 5, i, 0, 123456789, time.UTC)
			candidateKey := "candidate-append-outcome-" + strings.ReplaceAll(tc.name, " ", "-")
			first := testAssetPinReference("reference-append-outcome-first-"+strings.ReplaceAll(tc.name, " ", "-"), candidateKey, "bafybeiappendoutcomefirst", strings.Repeat("5", 64), AssetReferenceStaged, now, now.Add(time.Hour))
			second := testAssetPinReference("reference-append-outcome-second-"+strings.ReplaceAll(tc.name, " ", "-"), candidateKey, "bafybeiappendoutcomesecond", strings.Repeat("6", 64), AssetReferenceStaged, now.Add(time.Second), now.Add(2*time.Hour))
			faultErr := errors.New("injected auxiliary append failure")
			fault := &faultingAuxiliaryMetadataAppendFile{
				delegate: store.auxiliaryMetadata.appendFile,
				mode:     tc.mode,
				err:      faultErr,
			}
			store.auxiliaryMetadata.appendFile = fault

			before := auxiliaryJournalSizeForTest(t, basePath)
			err := store.UpsertAssetPinReference(ctx, first, testAssetPinEvent("event-append-outcome-first-"+strings.ReplaceAll(tc.name, " ", "-"), "reference_upsert", first, first.CreatedAt))
			if !errors.Is(err, faultErr) {
				t.Fatalf("first UpsertAssetPinReference() error = %v, want injected append failure", err)
			}
			if got := errors.Is(err, ErrAssetPinLedgerRecoveryRequired); got != tc.wantRecovery {
				t.Errorf("first UpsertAssetPinReference() recovery error = %v, want %v: %v", got, tc.wantRecovery, err)
			}
			afterFirst := auxiliaryJournalSizeForTest(t, basePath)
			if got := afterFirst > before; got != tc.wantBytesChanged {
				t.Fatalf("journal bytes changed after first append = %v (size %d -> %d), want %v", got, before, afterFirst, tc.wantBytesChanged)
			}

			if tc.wantRecovery {
				if err := store.UpsertAssetPinReference(ctx, second, testAssetPinEvent("event-append-outcome-second-"+strings.ReplaceAll(tc.name, " ", "-"), "reference_upsert", second, second.CreatedAt)); !errors.Is(err, ErrAssetPinLedgerRecoveryRequired) {
					t.Fatalf("second UpsertAssetPinReference() error = %v, want ErrAssetPinLedgerRecoveryRequired", err)
				}
				if afterSecond := auxiliaryJournalSizeForTest(t, basePath); afterSecond != afterFirst {
					t.Fatalf("journal size after blocked second append = %d, want %d", afterSecond, afterFirst)
				}
			} else {
				store.auxiliaryMetadata.appendFile = fault.delegate
				if err := store.UpsertAssetPinReference(ctx, second, testAssetPinEvent("event-append-outcome-second-"+strings.ReplaceAll(tc.name, " ", "-"), "reference_upsert", second, second.CreatedAt)); err != nil {
					t.Fatalf("second UpsertAssetPinReference() error = %v, want success after restoring append backend", err)
				}
			}

			if err := store.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			reopened := newAssetPinTestStore(t, basePath)
			gotFirst, firstOK, err := reopened.FindAssetPinReference(ctx, first.ReferenceKey)
			if err != nil || firstOK != tc.wantFirst {
				t.Fatalf("first reference after reopen = %+v, %v, %v; want present %v", gotFirst, firstOK, err, tc.wantFirst)
			}
			if firstOK && !equalAssetPinReference(gotFirst, first) {
				t.Fatalf("first reference after reopen = %+v, want %+v", gotFirst, first)
			}
			gotSecond, secondOK, err := reopened.FindAssetPinReference(ctx, second.ReferenceKey)
			if err != nil || secondOK != tc.wantSecond {
				t.Fatalf("second reference after reopen = %+v, %v, %v; want present %v", gotSecond, secondOK, err, tc.wantSecond)
			}
			if secondOK && !equalAssetPinReference(gotSecond, second) {
				t.Fatalf("second reference after reopen = %+v, want %+v", gotSecond, second)
			}
		})
	}
}

func TestAssetPinQueryCutoffsRejectUnixNanoOverflow(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	outOfRange := time.Date(2500, 1, 1, 0, 0, 0, 987654321, time.UTC)
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "expired references",
			run: func() error {
				_, err := store.ListExpiredAssetPinReferences(ctx, outOfRange)
				return err
			},
		},
		{
			name: "protected references",
			run: func() error {
				_, err := store.CountProtectedAssetReferences(ctx, "bafybeiquerycutoff", outOfRange)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "UnixNano range") {
				t.Fatalf("query with out-of-range cutoff error = %v, want UnixNano range rejection", err)
			}
		})
	}
}

func TestAssetPinPublicWritesRequireExplicitPersistedTimestamps(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("receipt consumed_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		receipt := AssetOIDCReceipt{
			Digest:      strings.Repeat("a", 64),
			ExpiresAt:   now.Add(time.Hour),
			Repository:  "SpaceDataNetwork/asset-models",
			Ref:         "refs/heads/main",
			WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
			Actor:       "asset-bot",
			RunID:       "timestamp-receipt",
			RunAttempt:  "1",
			SHA:         strings.Repeat("b", 40),
		}
		if err := store.ConsumeAssetOIDCToken(ctx, receipt); err == nil {
			t.Fatal("ConsumeAssetOIDCToken() accepted zero consumed_at")
		}
	})

	t.Run("reference created_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-zero-created", "candidate-zero-created", "bafybeizerocreated", strings.Repeat("c", 64), AssetReferenceStaged, now, now.Add(2*time.Hour))
		ref.CreatedAt = time.Time{}
		ref.UpdatedAt = now.Add(time.Hour)
		event := testAssetPinEvent("event-zero-created", "reference_upsert", ref, now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, event); err == nil {
			t.Fatal("UpsertAssetPinReference() accepted zero created_at")
		}
	})

	t.Run("reference updated_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-zero-updated", "candidate-zero-updated", "bafybeizeroupdated", strings.Repeat("d", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		ref.UpdatedAt = time.Time{}
		event := testAssetPinEvent("event-zero-updated", "reference_upsert", ref, now)
		if err := store.UpsertAssetPinReference(ctx, ref, event); err == nil {
			t.Fatal("UpsertAssetPinReference() accepted zero updated_at")
		}
	})

	t.Run("upsert audit occurred_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-zero-upsert-audit", "candidate-zero-upsert-audit", "bafybeizeroupsertaudit", strings.Repeat("e", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		event := testAssetPinEvent("event-zero-upsert-audit", "reference_upsert", ref, time.Time{})
		if err := store.UpsertAssetPinReference(ctx, ref, event); err == nil {
			t.Fatal("UpsertAssetPinReference() accepted zero audit occurred_at")
		}
	})

	t.Run("transition updated_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-zero-transition", "candidate-zero-transition", "bafybeizerotransition", strings.Repeat("f", 64), AssetReferenceStaged, now.Add(-time.Minute), now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-zero-transition-upsert", "reference_upsert", ref, ref.UpdatedAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
			ReferenceKey: ref.ReferenceKey,
			FromState:    AssetReferenceStaged,
			ToState:      AssetReferenceReviewOpen,
		}, testAssetPinEvent("event-zero-transition", "reference_transition", ref, now)); err == nil {
			t.Fatal("TransitionAssetPinReference() accepted zero updated_at")
		}
	})

	t.Run("transition audit occurred_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-zero-transition-audit", "candidate-zero-transition-audit", "bafybeizerotransitionaudit", strings.Repeat("1", 64), AssetReferenceStaged, now.Add(-time.Minute), now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-zero-transition-audit-upsert", "reference_upsert", ref, ref.UpdatedAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
			ReferenceKey: ref.ReferenceKey,
			FromState:    AssetReferenceStaged,
			ToState:      AssetReferenceReviewOpen,
			UpdatedAt:    now,
		}, testAssetPinEvent("event-zero-transition-audit", "reference_transition", ref, time.Time{})); err == nil {
			t.Fatal("TransitionAssetPinReference() accepted zero audit occurred_at")
		}
	})

	t.Run("standalone audit occurred_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		event := testAssetPinEvent("event-zero-standalone-audit", "reconcile", AssetPinReference{}, time.Time{})
		if err := store.AppendAssetPinAuditEvent(ctx, event); err == nil {
			t.Fatal("AppendAssetPinAuditEvent() accepted zero occurred_at")
		}
	})
}

func TestAssetPinPersistedTimesRejectUnixNanoOverflow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 20, 0, 0, 123456789, time.UTC)
	outOfRange := time.Date(2500, 1, 1, 0, 0, 0, 987654321, time.UTC)
	tests := []struct {
		name string
		run  func(*FlatSQLStore) error
	}{
		{
			name: "receipt expiry",
			run: func(store *FlatSQLStore) error {
				return store.ConsumeAssetOIDCToken(ctx, AssetOIDCReceipt{
					Digest:      strings.Repeat("2", 64),
					ExpiresAt:   outOfRange,
					Repository:  "SpaceDataNetwork/asset-models",
					Ref:         "refs/heads/main",
					WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
					Actor:       "asset-bot",
					RunID:       "range-receipt",
					RunAttempt:  "1",
					SHA:         strings.Repeat("3", 40),
					ConsumedAt:  now,
				})
			},
		},
		{
			name: "reference created_at",
			run: func(store *FlatSQLStore) error {
				ref := testAssetPinReference("reference-range", "candidate-range", "bafybeirange", strings.Repeat("4", 64), AssetReferenceStaged, outOfRange, time.Time{})
				return store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-range-reference", "reference_upsert", ref, outOfRange))
			},
		},
		{
			name: "transition updated_at",
			run: func(store *FlatSQLStore) error {
				ref := testAssetPinReference("reference-range-transition", "candidate-range-transition", "bafybeirangetransition", strings.Repeat("5", 64), AssetReferenceStaged, now, time.Time{})
				if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-range-transition-upsert", "reference_upsert", ref, now)); err != nil {
					return err
				}
				return store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
					ReferenceKey: ref.ReferenceKey,
					FromState:    AssetReferenceStaged,
					ToState:      AssetReferenceReviewOpen,
					UpdatedAt:    outOfRange,
				}, testAssetPinEvent("event-range-transition", "reference_transition", ref, now.Add(time.Second)))
			},
		},
		{
			name: "audit occurred_at",
			run: func(store *FlatSQLStore) error {
				return store.AppendAssetPinAuditEvent(ctx, testAssetPinEvent("event-range-audit", "reconcile", AssetPinReference{}, outOfRange))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
			err := tc.run(store)
			if err == nil || !strings.Contains(err.Error(), "UnixNano range") {
				t.Fatalf("persist out-of-range time error = %v, want UnixNano range rejection", err)
			}
		})
	}
}

func TestAssetPinReferencesCanShareCIDAndRemainIndependent(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 13, 0, 0, 987654321, time.UTC)
	cid := "bafybeigdyrsharedassetcid"
	sha256 := strings.Repeat("c", 64)

	first := testAssetPinReference("reference-issue-101", "candidate-issue-101", cid, sha256, AssetReferenceStaged, now, now.Add(time.Hour))
	second := testAssetPinReference("reference-issue-102", "candidate-issue-102", cid, sha256, AssetReferenceReviewOpen, now.Add(time.Second), time.Time{})
	if err := store.UpsertAssetPinReference(ctx, first, testAssetPinEvent("event-upsert-101", "reference_upsert", first, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference(first) error = %v", err)
	}
	if err := store.UpsertAssetPinReference(ctx, second, testAssetPinEvent("event-upsert-102", "reference_upsert", second, now.Add(time.Second))); err != nil {
		t.Fatalf("UpsertAssetPinReference(second) error = %v", err)
	}

	refs, err := store.ListAssetPinReferences(ctx, AssetPinReferenceQuery{CID: cid})
	if err != nil {
		t.Fatalf("ListAssetPinReferences() error = %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("references sharing CID = %d, want 2: %+v", len(refs), refs)
	}
	for _, want := range []AssetPinReference{first, second} {
		got, ok, err := store.FindAssetPinReferenceByCandidateKey(ctx, want.CandidateKey)
		if err != nil {
			t.Fatalf("FindAssetPinReferenceByCandidateKey(%q) error = %v", want.CandidateKey, err)
		}
		if !ok || got.ReferenceKey != want.ReferenceKey || got.CID != cid {
			t.Fatalf("candidate lookup %q = %+v, %v; want reference %q with shared CID", want.CandidateKey, got, ok, want.ReferenceKey)
		}
	}
	bySHA, ok, err := store.FindAssetBySHA256(ctx, sha256)
	if err != nil {
		t.Fatalf("FindAssetBySHA256() error = %v", err)
	}
	if !ok || bySHA.CID != cid {
		t.Fatalf("FindAssetBySHA256() = %+v, %v; want shared CID %q", bySHA, ok, cid)
	}
	protected, err := store.CountProtectedAssetReferences(ctx, cid, now)
	if err != nil {
		t.Fatalf("CountProtectedAssetReferences() error = %v", err)
	}
	if protected != 2 {
		t.Fatalf("protected reference count = %d, want 2 independent references", protected)
	}

	transition := AssetPinReferenceTransition{
		ReferenceKey: first.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceAbandoned,
		UpdatedAt:    now.Add(2 * time.Second),
		ExpiresAt:    now.Add(time.Hour),
	}
	transitionEvent := testAssetPinEvent("event-abandon-101", "reference_transition", first, transition.UpdatedAt)
	transitionEvent.Result = string(AssetReferenceAbandoned)
	if err := store.TransitionAssetPinReference(ctx, transition, transitionEvent); err != nil {
		t.Fatalf("TransitionAssetPinReference() error = %v", err)
	}
	protected, err = store.CountProtectedAssetReferences(ctx, cid, now)
	if err != nil {
		t.Fatalf("CountProtectedAssetReferences() after transition error = %v", err)
	}
	if protected != 1 {
		t.Fatalf("protected reference count after abandoning one = %d, want 1", protected)
	}
}

func TestAssetPinReferenceUpsertIsInsertOrFullEquivalence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 23, 30, 0, 123456789, time.UTC)
	tests := []struct {
		name   string
		mutate func(*AssetPinReference)
	}{
		{name: "candidate key", mutate: func(ref *AssetPinReference) { ref.CandidateKey = "candidate-immutable-other" }},
		{name: "CID", mutate: func(ref *AssetPinReference) { ref.CID = "bafybeiimmutableother" }},
		{name: "SHA-256", mutate: func(ref *AssetPinReference) { ref.SHA256 = strings.Repeat("7", 64) }},
		{name: "byte count", mutate: func(ref *AssetPinReference) { ref.ByteCount++ }},
		{name: "state and expiry", mutate: func(ref *AssetPinReference) {
			ref.State = AssetReferenceStaged
			ref.GitHubIssue = 0
			ref.DecisionSHA256 = ""
			ref.ExpiresAt = ref.UpdatedAt.Add(time.Hour)
		}},
		{name: "source URL", mutate: func(ref *AssetPinReference) { ref.SourceURL = "https://example.test/assets/other.glb" }},
		{name: "license", mutate: func(ref *AssetPinReference) { ref.LicenseName = "Apache-2.0" }},
		{name: "attribution", mutate: func(ref *AssetPinReference) { ref.Attribution = "Different Artist" }},
		{name: "metadata", mutate: func(ref *AssetPinReference) { ref.MetadataJSON = `{"format":"glb","polygon_count":2048}` }},
		{name: "GitHub issue", mutate: func(ref *AssetPinReference) { ref.GitHubIssue++ }},
		{name: "workflow owner", mutate: func(ref *AssetPinReference) { ref.WorkflowRunID = "different-run" }},
		{name: "decision digest", mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = strings.Repeat("8", 64) }},
		{name: "created_at", mutate: func(ref *AssetPinReference) { ref.CreatedAt = ref.CreatedAt.Add(-time.Nanosecond) }},
		{name: "updated_at", mutate: func(ref *AssetPinReference) { ref.UpdatedAt = ref.UpdatedAt.Add(time.Nanosecond) }},
		{name: "expiry", mutate: func(ref *AssetPinReference) {
			ref.State = AssetReferenceRejected
			ref.ExpiresAt = ref.UpdatedAt.Add(30 * 24 * time.Hour)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
			original := testAssetPinReference("reference-immutable", "candidate-immutable", "bafybeiimmutable", strings.Repeat("6", 64), AssetReferenceApproved, now, time.Time{})
			original.DecisionSHA256 = strings.Repeat("9", 64)
			if err := store.UpsertAssetPinReference(ctx, original, testAssetPinEvent("event-immutable-original", "reference_upsert", original, now)); err != nil {
				t.Fatalf("initial UpsertAssetPinReference() error = %v", err)
			}
			changed := original
			tc.mutate(&changed)
			event := testAssetPinEvent("event-immutable-conflict-"+strings.ReplaceAll(tc.name, " ", "-"), "reference_upsert", changed, now.Add(time.Second))
			if err := store.UpsertAssetPinReference(ctx, changed, event); !errors.Is(err, ErrAssetPinReferenceConflict) {
				t.Fatalf("changed UpsertAssetPinReference() error = %v, want ErrAssetPinReferenceConflict", err)
			}
			got, ok, err := store.FindAssetPinReference(ctx, original.ReferenceKey)
			if err != nil || !ok || got != original {
				t.Fatalf("reference after conflicting upsert = %+v, %v, %v; want %+v", got, ok, err, original)
			}
			events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: event.EventID})
			if err != nil {
				t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("conflicting upsert wrote audit events: %+v", events)
			}
		})
	}
}

func TestAssetPinUpsertRejectsAuditIdentityMismatchWithoutMutation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 0, 0, 0, 123456789, time.UTC)
	tests := []struct {
		name   string
		mutate func(*AssetPinAuditEvent)
	}{
		{name: "candidate key", mutate: func(event *AssetPinAuditEvent) { event.CandidateKey = "candidate-audit-other" }},
		{name: "reference key", mutate: func(event *AssetPinAuditEvent) { event.ReferenceKey = "reference-audit-other" }},
		{name: "CID", mutate: func(event *AssetPinAuditEvent) { event.CID = "bafybeiauditother" }},
		{name: "SHA-256", mutate: func(event *AssetPinAuditEvent) { event.SHA256 = strings.Repeat("b", 64) }},
		{name: "byte count", mutate: func(event *AssetPinAuditEvent) { event.ByteCount++ }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			basePath := filepath.Join(t.TempDir(), "store")
			store := newAssetPinTestStore(t, basePath)
			ref := testAssetPinReference("reference-audit-identity", "candidate-audit-identity", "bafybeiauditidentity", strings.Repeat("a", 64), AssetReferenceStaged, now, now.Add(time.Hour))
			event := testAssetPinEvent("event-audit-identity", "reference_upsert", ref, now)
			tc.mutate(&event)
			if err := store.UpsertAssetPinReference(ctx, ref, event); !errors.Is(err, ErrAssetPinAuditConflict) {
				t.Fatalf("UpsertAssetPinReference() error = %v, want ErrAssetPinAuditConflict", err)
			}
			if _, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey); err != nil || ok {
				t.Fatalf("reference after audit identity conflict = ok %v, err %v; want absent", ok, err)
			}
			events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: event.EventID})
			if err != nil {
				t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("audit identity conflict wrote events: %+v", events)
			}
			info, err := os.Stat(filepath.Join(basePath, auxiliaryMetadataFileName))
			if err != nil {
				t.Fatalf("stat auxiliary journal: %v", err)
			}
			if info.Size() != 0 {
				t.Fatalf("audit identity conflict journal size = %d, want 0", info.Size())
			}
		})
	}
}

func TestAssetPinTransitionPayloadIsBoundToStableEventID(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 0, 30, 0, 123456789, time.UTC)
	ref := testAssetPinReference("reference-transition-digest", "candidate-transition-digest", "bafybeitransitiondigest", strings.Repeat("c", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-transition-digest-upsert", "reference_upsert", ref, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	reviewedAt := now.Add(time.Second)
	event := testAssetPinEvent("event-transition-digest", "reference_transition", ref, reviewedAt)
	event.Result = string(AssetReferenceReviewOpen)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  4242,
		UpdatedAt:    reviewedAt,
		ExpiresAt:    ref.ExpiresAt,
	}, event); err != nil {
		t.Fatalf("first TransitionAssetPinReference() error = %v", err)
	}
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey:   ref.ReferenceKey,
		FromState:      AssetReferenceReviewOpen,
		ToState:        AssetReferenceApproved,
		GitHubIssue:    4242,
		DecisionSHA256: strings.Repeat("d", 64),
		UpdatedAt:      reviewedAt.Add(time.Second),
	}, event); !errors.Is(err, ErrAssetPinAuditConflict) {
		t.Fatalf("different-payload TransitionAssetPinReference() error = %v, want ErrAssetPinAuditConflict", err)
	}
	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok || got.State != AssetReferenceReviewOpen || got.GitHubIssue != 4242 || got.DecisionSHA256 != "" || !got.UpdatedAt.Equal(reviewedAt) {
		t.Fatalf("reference after transition payload conflict = %+v, %v, %v; want unchanged review state", got, ok, err)
	}
}

func TestAssetPinTransitionRejectsAuditIdentityMismatchWithoutMutation(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 0, 45, 0, 123456789, time.UTC)
	ref := testAssetPinReference("reference-transition-identity", "candidate-transition-identity", "bafybeitransitionidentity", strings.Repeat("e", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-transition-identity-upsert", "reference_upsert", ref, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	event := testAssetPinEvent("event-transition-identity", "reference_transition", ref, now.Add(time.Second))
	event.CandidateKey = "candidate-transition-identity-other"
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  4242,
		UpdatedAt:    event.OccurredAt,
		ExpiresAt:    ref.ExpiresAt,
	}, event); !errors.Is(err, ErrAssetPinAuditConflict) {
		t.Fatalf("TransitionAssetPinReference() error = %v, want ErrAssetPinAuditConflict", err)
	}
	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok || got.State != AssetReferenceStaged || !got.UpdatedAt.Equal(now) {
		t.Fatalf("reference after transition audit mismatch = %+v, %v, %v; want unchanged staged state", got, ok, err)
	}
	events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: event.EventID})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("transition audit mismatch wrote events: %+v", events)
	}
}

func TestAssetPinJournalConflictIsDetectedBeforeSQLMutation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 1, 0, 0, 123456789, time.UTC)

	t.Run("receipt", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		receipt := AssetOIDCReceipt{
			Digest:      strings.Repeat("f", 64),
			ExpiresAt:   now.Add(time.Hour),
			Repository:  "SpaceDataNetwork/asset-models",
			Ref:         "refs/heads/main",
			WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
			Actor:       "asset-bot",
			RunID:       "preflight-receipt",
			RunAttempt:  "1",
			SHA:         strings.Repeat("1", 40),
			ConsumedAt:  now,
		}
		prepared, err := prepareAssetOIDCReceipt(receipt)
		if err != nil {
			t.Fatalf("prepareAssetOIDCReceipt() error = %v", err)
		}
		if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &prepared}); err != nil {
			t.Fatalf("append durable receipt frame: %v", err)
		}
		if _, err := store.db.Exec(`DROP TABLE sdn_asset_oidc_receipts`); err != nil {
			t.Fatalf("drop receipt table: %v", err)
		}
		conflict := receipt
		conflict.Actor = "different-bot"
		if err := store.ConsumeAssetOIDCToken(ctx, conflict); !errors.Is(err, ErrAssetOIDCReceiptConflict) {
			t.Fatalf("ConsumeAssetOIDCToken() error = %v, want journal ErrAssetOIDCReceiptConflict before SQL", err)
		}
	})

	t.Run("reference upsert", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-preflight", "candidate-preflight", "bafybeipreflight", strings.Repeat("2", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		event := testAssetPinEvent("event-preflight", "reference_upsert", ref, now)
		frame := prepareAssetPinUpsertFrameForTest(t, ref, event)
		if err := store.auxiliaryMetadata.Append(frame); err != nil {
			t.Fatalf("append durable upsert frame: %v", err)
		}
		if _, err := store.db.Exec(`DROP TABLE sdn_asset_pin_refs`); err != nil {
			t.Fatalf("drop reference table: %v", err)
		}
		conflict := event
		conflict.Detail = "different durable payload"
		if err := store.UpsertAssetPinReference(ctx, ref, conflict); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("UpsertAssetPinReference() error = %v, want journal ErrAssetPinAuditConflict before SQL", err)
		}
	})

	t.Run("reference transition", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-preflight-transition", "candidate-preflight-transition", "bafybeipreflighttransition", strings.Repeat("3", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-preflight-transition-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		event := testAssetPinEvent("event-preflight-transition", "reference_transition", ref, now.Add(time.Second))
		first := AssetPinReferenceTransition{ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceStaged, ToState: AssetReferenceReviewOpen, GitHubIssue: 4242, UpdatedAt: event.OccurredAt, ExpiresAt: ref.ExpiresAt}
		if err := store.auxiliaryMetadata.Append(prepareAssetPinTransitionFrameForTest(t, ref, first, event)); err != nil {
			t.Fatalf("append durable transition frame: %v", err)
		}
		conflict := AssetPinReferenceTransition{ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceStaged, ToState: AssetReferenceReviewOpen, GitHubIssue: 4243, UpdatedAt: event.OccurredAt.Add(time.Second), ExpiresAt: ref.ExpiresAt}
		if err := store.TransitionAssetPinReference(ctx, conflict, event); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("TransitionAssetPinReference() error = %v, want journal ErrAssetPinAuditConflict before CAS", err)
		}
	})

	t.Run("reference delete", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		createdAt := time.Date(2019, 1, 1, 0, 0, 0, 123456789, time.UTC)
		first := testAssetPinReference("reference-preflight-delete-first", "candidate-preflight-delete-first", "bafybeipreflightdeletefirst", strings.Repeat("4", 64), AssetReferenceStaged, createdAt, createdAt.Add(time.Hour))
		second := testAssetPinReference("reference-preflight-delete-second", "candidate-preflight-delete-second", "bafybeipreflightdeletesecond", strings.Repeat("5", 64), AssetReferenceApproved, now, time.Time{})
		if err := store.UpsertAssetPinReference(ctx, first, testAssetPinEvent("event-preflight-delete-first-upsert", "reference_upsert", first, createdAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference(first) error = %v", err)
		}
		if err := store.UpsertAssetPinReference(ctx, second, testAssetPinEvent("event-preflight-delete-second-upsert", "reference_upsert", second, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference(second) error = %v", err)
		}
		firstEvent := testAssetPinEvent("event-preflight-delete", "reference_delete", first, now.Add(time.Second))
		if err := store.auxiliaryMetadata.Append(prepareAssetPinDeleteFrameForTest(t, first, firstEvent)); err != nil {
			t.Fatalf("append durable delete frame: %v", err)
		}
		secondEvent := testAssetPinEvent(firstEvent.EventID, "reference_delete", second, firstEvent.OccurredAt)
		if err := store.DeleteExpiredAssetPinReference(ctx, second.ReferenceKey, secondEvent); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("DeleteExpiredAssetPinReference() error = %v, want journal ErrAssetPinAuditConflict before expiry check", err)
		}
	})
}

func TestAssetPinPublicWritesCommitAfterDurableFrameRetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 1, 30, 0, 123456789, time.UTC)

	t.Run("receipt", func(t *testing.T) {
		basePath := filepath.Join(t.TempDir(), "store")
		store := newAssetPinTestStore(t, basePath)
		receipt := AssetOIDCReceipt{
			Digest:      strings.Repeat("3", 64),
			ExpiresAt:   now.Add(time.Hour),
			Repository:  "SpaceDataNetwork/asset-models",
			Ref:         "refs/heads/main",
			WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
			Actor:       "asset-bot",
			RunID:       "durable-receipt",
			RunAttempt:  "1",
			SHA:         strings.Repeat("4", 40),
			ConsumedAt:  now,
		}
		prepared, err := prepareAssetOIDCReceipt(receipt)
		if err != nil {
			t.Fatalf("prepareAssetOIDCReceipt() error = %v", err)
		}
		if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &prepared}); err != nil {
			t.Fatalf("append durable receipt frame: %v", err)
		}
		before := auxiliaryJournalSizeForTest(t, basePath)
		retry := receipt
		retry.ConsumedAt = now.Add(time.Nanosecond)
		if err := store.ConsumeAssetOIDCToken(ctx, retry); err != nil {
			t.Fatalf("ConsumeAssetOIDCToken() retry error = %v", err)
		}
		got, ok, err := store.FindAssetOIDCReceipt(ctx, receipt.Digest)
		if err != nil || !ok {
			t.Fatalf("receipt after retry = %+v, ok %v, err %v; want present", got, ok, err)
		}
		if !got.ConsumedAt.Equal(receipt.ConsumedAt) {
			t.Fatalf("receipt consumed_at after retry = %v, want first durable timestamp %v", got.ConsumedAt, receipt.ConsumedAt)
		}
		if after := auxiliaryJournalSizeForTest(t, basePath); after != before {
			t.Fatalf("receipt journal size after retry = %d, want %d", after, before)
		}
	})

	t.Run("reference upsert", func(t *testing.T) {
		basePath := filepath.Join(t.TempDir(), "store")
		store := newAssetPinTestStore(t, basePath)
		ref := testAssetPinReference("reference-durable-upsert", "candidate-durable-upsert", "bafybeidurableupsert", strings.Repeat("5", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		event := testAssetPinEvent("event-durable-upsert", "reference_upsert", ref, now)
		if err := store.auxiliaryMetadata.Append(prepareAssetPinUpsertFrameForTest(t, ref, event)); err != nil {
			t.Fatalf("append durable upsert frame: %v", err)
		}
		before := auxiliaryJournalSizeForTest(t, basePath)
		if err := store.UpsertAssetPinReference(ctx, ref, event); err != nil {
			t.Fatalf("UpsertAssetPinReference() retry error = %v", err)
		}
		if _, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey); err != nil || !ok {
			t.Fatalf("reference after retry = ok %v, err %v; want present", ok, err)
		}
		if after := auxiliaryJournalSizeForTest(t, basePath); after != before {
			t.Fatalf("upsert journal size after retry = %d, want %d", after, before)
		}
	})

	t.Run("reference transition", func(t *testing.T) {
		basePath := filepath.Join(t.TempDir(), "store")
		store := newAssetPinTestStore(t, basePath)
		ref := testAssetPinReference("reference-durable-transition", "candidate-durable-transition", "bafybeidurabletransition", strings.Repeat("6", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-durable-transition-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		transition := AssetPinReferenceTransition{ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceStaged, ToState: AssetReferenceReviewOpen, GitHubIssue: 8181, UpdatedAt: now.Add(time.Second), ExpiresAt: ref.ExpiresAt}
		event := testAssetPinEvent("event-durable-transition", "reference_transition", ref, transition.UpdatedAt)
		event.Result = string(AssetReferenceReviewOpen)
		frame := prepareAssetPinTransitionFrameForTest(t, ref, transition, event)
		if err := store.auxiliaryMetadata.Append(frame); err != nil {
			t.Fatalf("append durable transition frame: %v", err)
		}
		before := auxiliaryJournalSizeForTest(t, basePath)
		if err := store.TransitionAssetPinReference(ctx, transition, event); err != nil {
			t.Fatalf("TransitionAssetPinReference() retry error = %v", err)
		}
		got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
		if err != nil || !ok || got.State != AssetReferenceReviewOpen || got.GitHubIssue != 8181 {
			t.Fatalf("reference after transition retry = %+v, %v, %v; want review_open", got, ok, err)
		}
		if after := auxiliaryJournalSizeForTest(t, basePath); after != before {
			t.Fatalf("transition journal size after retry = %d, want %d", after, before)
		}
	})

	t.Run("reference delete", func(t *testing.T) {
		basePath := filepath.Join(t.TempDir(), "store")
		store := newAssetPinTestStore(t, basePath)
		createdAt := time.Date(2019, 1, 1, 0, 0, 0, 123456789, time.UTC)
		ref := testAssetPinReference("reference-durable-delete", "candidate-durable-delete", "bafybeidurabledelete", strings.Repeat("7", 64), AssetReferenceStaged, createdAt, createdAt.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-durable-delete-upsert", "reference_upsert", ref, createdAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		event := testAssetPinEvent("event-durable-delete", "reference_delete", ref, now)
		frame := prepareAssetPinDeleteFrameForTest(t, ref, event)
		if err := store.auxiliaryMetadata.Append(frame); err != nil {
			t.Fatalf("append durable delete frame: %v", err)
		}
		before := auxiliaryJournalSizeForTest(t, basePath)
		if err := store.DeleteExpiredAssetPinReference(ctx, ref.ReferenceKey, event); err != nil {
			t.Fatalf("DeleteExpiredAssetPinReference() retry error = %v", err)
		}
		if _, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey); err != nil || ok {
			t.Fatalf("reference after delete retry = ok %v, err %v; want absent", ok, err)
		}
		if after := auxiliaryJournalSizeForTest(t, basePath); after != before {
			t.Fatalf("delete journal size after retry = %d, want %d", after, before)
		}
	})

	t.Run("standalone audit", func(t *testing.T) {
		basePath := filepath.Join(t.TempDir(), "store")
		store := newAssetPinTestStore(t, basePath)
		event := testAssetPinEvent("event-durable-audit", "reconcile", AssetPinReference{}, now)
		prepared, err := prepareAssetPinAuditEvent(event)
		if err != nil {
			t.Fatalf("prepareAssetPinAuditEvent() error = %v", err)
		}
		if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinAuditAppend, AssetPinAuditEvent: &prepared}); err != nil {
			t.Fatalf("append durable audit frame: %v", err)
		}
		before := auxiliaryJournalSizeForTest(t, basePath)
		if err := store.AppendAssetPinAuditEvent(ctx, event); err != nil {
			t.Fatalf("AppendAssetPinAuditEvent() retry error = %v", err)
		}
		events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: event.EventID})
		if err != nil || len(events) != 1 {
			t.Fatalf("audit events after retry = %+v, %v; want one", events, err)
		}
		if after := auxiliaryJournalSizeForTest(t, basePath); after != before {
			t.Fatalf("audit journal size after retry = %d, want %d", after, before)
		}
	})
}

func TestAssetPinReferenceStatesExpiryAndValidation(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 14, 0, 0, 555000111, time.UTC)
	cid := "bafybeistateprotectioncid"
	sha256 := strings.Repeat("d", 64)

	cases := []struct {
		key       string
		state     AssetReferenceState
		createdAt time.Time
		expiresAt time.Time
		protected bool
		expired   bool
	}{
		{key: "approved", state: AssetReferenceApproved, createdAt: now.Add(-time.Hour), protected: true},
		{key: "review-past", state: AssetReferenceReviewOpen, createdAt: now.Add(-2 * time.Hour), expiresAt: now.Add(-time.Hour), protected: true},
		{key: "staged-future", state: AssetReferenceStaged, createdAt: now.Add(-time.Hour), expiresAt: now.Add(time.Hour), protected: true},
		{key: "staged-past", state: AssetReferenceStaged, createdAt: now.Add(-2 * time.Hour), expiresAt: now.Add(-time.Hour), expired: true},
		{key: "rejected-future", state: AssetReferenceRejected, createdAt: now.Add(-29 * 24 * time.Hour), protected: true},
		{key: "superseded-past", state: AssetReferenceSuperseded, createdAt: now.Add(-31 * 24 * time.Hour), expired: true},
		{key: "abandoned-past", state: AssetReferenceAbandoned, createdAt: now.Add(-2 * time.Hour), expiresAt: now.Add(-time.Second), expired: true},
		{key: "abandoned-future", state: AssetReferenceAbandoned, createdAt: now, expiresAt: now.Add(time.Hour)},
	}
	for _, tc := range cases {
		ref := testAssetPinReference("reference-"+tc.key, "candidate-"+tc.key, cid, sha256, tc.state, tc.createdAt, tc.expiresAt)
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-"+tc.key, "reference_upsert", ref, ref.UpdatedAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference(%s) error = %v", tc.key, err)
		}
	}

	wantProtected := 0
	wantExpired := map[string]bool{}
	for _, tc := range cases {
		if tc.protected {
			wantProtected++
		}
		if tc.expired {
			wantExpired["reference-"+tc.key] = true
		}
	}
	protected, err := store.CountProtectedAssetReferences(ctx, cid, now)
	if err != nil {
		t.Fatalf("CountProtectedAssetReferences() error = %v", err)
	}
	if protected != wantProtected {
		t.Fatalf("protected count = %d, want %d", protected, wantProtected)
	}
	expired, err := store.ListExpiredAssetPinReferences(ctx, now)
	if err != nil {
		t.Fatalf("ListExpiredAssetPinReferences() error = %v", err)
	}
	if len(expired) != len(wantExpired) {
		t.Fatalf("expired references = %d, want %d: %+v", len(expired), len(wantExpired), expired)
	}
	for _, ref := range expired {
		if !wantExpired[ref.ReferenceKey] {
			t.Errorf("unexpected expired reference %q in state %q", ref.ReferenceKey, ref.State)
		}
	}

	var stagedPast AssetPinReference
	for _, ref := range expired {
		if ref.ReferenceKey == "reference-staged-past" {
			stagedPast = ref
			break
		}
	}
	deleteEvent := testAssetPinEvent("event-delete-staged-past", "reference_delete", stagedPast, time.Now().UTC())
	if err := store.DeleteExpiredAssetPinReference(ctx, "reference-staged-past", deleteEvent); err != nil {
		t.Fatalf("DeleteExpiredAssetPinReference(expired) error = %v", err)
	}
	if _, ok, err := store.FindAssetPinReference(ctx, "reference-staged-past"); err != nil || ok {
		t.Fatalf("deleted reference lookup = ok %v, err %v; want absent", ok, err)
	}
	protectedEvent := testAssetPinEvent("event-delete-approved", "reference_delete", AssetPinReference{ReferenceKey: "reference-approved"}, time.Now().UTC())
	if err := store.DeleteExpiredAssetPinReference(ctx, "reference-approved", protectedEvent); !errors.Is(err, ErrAssetPinReferenceNotExpired) {
		t.Fatalf("DeleteExpiredAssetPinReference(approved) error = %v, want ErrAssetPinReferenceNotExpired", err)
	}

	invalid := testAssetPinReference("reference-invalid", "candidate-invalid", cid, sha256, AssetReferenceState("pending"), now, now.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, invalid, testAssetPinEvent("event-invalid", "reference_upsert", invalid, now)); err == nil {
		t.Fatal("UpsertAssetPinReference() accepted unknown state")
	}
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: "reference-staged-future",
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceState("pending"),
		UpdatedAt:    now.Add(time.Minute),
	}, testAssetPinEvent("event-invalid-transition", "reference_transition", AssetPinReference{ReferenceKey: "reference-staged-future"}, now)); err == nil {
		t.Fatal("TransitionAssetPinReference() accepted unknown target state")
	}
}

func TestAssetPinReferenceTimesAreMonotonic(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 2, 0, 0, 123456789, time.UTC)

	t.Run("insert expiry precedes updated_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-time-insert", "candidate-time-insert", "bafybeitimeinsert", strings.Repeat("6", 64), AssetReferenceStaged, now, now.Add(time.Second))
		ref.UpdatedAt = now.Add(2 * time.Second)
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-time-insert", "reference_upsert", ref, ref.UpdatedAt)); err == nil {
			t.Fatal("UpsertAssetPinReference() accepted expiry before updated_at")
		}
		if _, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey); err != nil || ok {
			t.Fatalf("invalid-time reference lookup = ok %v, err %v; want absent", ok, err)
		}
	})

	t.Run("transition updated_at moves backward", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-time-backward", "candidate-time-backward", "bafybeitimebackward", strings.Repeat("7", 64), AssetReferenceStaged, now, time.Time{})
		ref.UpdatedAt = now.Add(2 * time.Second)
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-time-backward-upsert", "reference_upsert", ref, ref.UpdatedAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		transitionedAt := now.Add(time.Second)
		if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
			ReferenceKey: ref.ReferenceKey,
			FromState:    AssetReferenceStaged,
			ToState:      AssetReferenceReviewOpen,
			GitHubIssue:  4242,
			UpdatedAt:    transitionedAt,
			ExpiresAt:    ref.ExpiresAt,
		}, testAssetPinEvent("event-time-backward", "reference_transition", ref, transitionedAt)); err == nil {
			t.Fatal("TransitionAssetPinReference() accepted updated_at before current updated_at")
		}
		got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
		if err != nil || !ok || got.State != AssetReferenceStaged || !got.UpdatedAt.Equal(ref.UpdatedAt) {
			t.Fatalf("reference after backward transition = %+v, %v, %v; want unchanged", got, ok, err)
		}
	})

	t.Run("transition expiry precedes updated_at", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-time-expiry", "candidate-time-expiry", "bafybeitimeexpiry", strings.Repeat("8", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-time-expiry-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		updatedAt := now.Add(2 * time.Second)
		if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
			ReferenceKey: ref.ReferenceKey,
			FromState:    AssetReferenceStaged,
			ToState:      AssetReferenceRejected,
			UpdatedAt:    updatedAt,
			ExpiresAt:    now.Add(time.Second),
		}, testAssetPinEvent("event-time-expiry", "reference_transition", ref, updatedAt)); err == nil {
			t.Fatal("TransitionAssetPinReference() accepted expiry before updated_at")
		}
		got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
		if err != nil || !ok || got.State != AssetReferenceStaged || !got.UpdatedAt.Equal(now) {
			t.Fatalf("reference after invalid expiry transition = %+v, %v, %v; want unchanged", got, ok, err)
		}
	})
}

func TestAssetPinAuditIndexesCoverOperationalFilters(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	for _, name := range []string{
		"idx_sdn_asset_pin_events_kind_occurred",
		"idx_sdn_asset_pin_events_candidate_occurred",
		"idx_sdn_asset_pin_events_sha_occurred",
	} {
		exists, err := store.indexExists(name)
		if err != nil {
			t.Fatalf("indexExists(%q) error = %v", name, err)
		}
		if !exists {
			t.Errorf("required asset audit index %q is missing", name)
		}
	}
}

func TestAssetPinReferencePreservesCanonicalMetadataAndPreciseTimes(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	createdAt := time.Date(2026, 7, 13, 15, 4, 5, 123456789, time.FixedZone("test", -4*60*60))
	updatedAt := createdAt.Add(987654321 * time.Nanosecond)
	ref := testAssetPinReference("reference-precise", "candidate-precise", "bafybeiprecisecid", strings.Repeat("e", 64), AssetReferenceStaged, createdAt, time.Time{})
	ref.UpdatedAt = updatedAt
	ref.MetadataJSON = `{ "z": [3, 2, 1], "a": {"y": 2, "x": 1} }`
	ref.GitHubIssue = 0
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-precise", "reference_upsert", ref, updatedAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}

	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil {
		t.Fatalf("FindAssetPinReference() error = %v", err)
	}
	if !ok {
		t.Fatal("FindAssetPinReference() did not find precise reference")
	}
	if !got.CreatedAt.Equal(createdAt) || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("stored timestamps = created %v updated %v, want %v and %v", got.CreatedAt, got.UpdatedAt, createdAt, updatedAt)
	}
	if !got.ExpiresAt.Equal(normalizeAssetPinTime(ref.ExpiresAt)) {
		t.Fatalf("expiry = %v, want %v", got.ExpiresAt, normalizeAssetPinTime(ref.ExpiresAt))
	}
	if got.MetadataJSON != `{"a":{"x":1,"y":2},"z":[3,2,1]}` {
		t.Fatalf("MetadataJSON = %q, want canonical sorted JSON", got.MetadataJSON)
	}
	if got.GitHubIssue != 0 {
		t.Fatalf("GitHubIssue = %d, want unset zero", got.GitHubIssue)
	}
	var expiryIsNull bool
	if err := store.db.QueryRow(`SELECT expires_at IS NULL FROM sdn_asset_pin_refs WHERE reference_key = ?`, ref.ReferenceKey).Scan(&expiryIsNull); err != nil {
		t.Fatalf("read zero expiry representation: %v", err)
	}
	if expiryIsNull {
		t.Fatal("finite staged expiry must not be stored as SQL NULL")
	}
}

func TestAssetPinAuditEventIDsAreAppendOnlyAndIdempotent(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 16, 0, 0, 999888777, time.UTC)
	event := AssetPinAuditEvent{
		EventID:       "event-audit-stable-id",
		Kind:          "pin_reused",
		Result:        "success",
		TokenDigest:   strings.Repeat("f", 64),
		Repository:    "SpaceDataNetwork/asset-models",
		Ref:           "refs/heads/main",
		WorkflowRef:   "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:         "asset-bot",
		WorkflowRunID: "987654321",
		RunAttempt:    "1",
		CommitSHA:     strings.Repeat("1", 40),
		CandidateKey:  "candidate-audit",
		ReferenceKey:  "reference-audit",
		CID:           "bafybeiauditcid",
		SHA256:        strings.Repeat("2", 64),
		ByteCount:     12345,
		Detail:        "reused existing content address",
		OccurredAt:    now,
	}
	if err := store.AppendAssetPinAuditEvent(ctx, event); err != nil {
		t.Fatalf("AppendAssetPinAuditEvent() error = %v", err)
	}
	if err := store.AppendAssetPinAuditEvent(ctx, event); err != nil {
		t.Fatalf("idempotent AppendAssetPinAuditEvent() error = %v", err)
	}
	conflict := event
	conflict.Detail = "changed detail under same stable ID"
	if err := store.AppendAssetPinAuditEvent(ctx, conflict); !errors.Is(err, ErrAssetPinAuditConflict) {
		t.Fatalf("conflicting AppendAssetPinAuditEvent() error = %v, want ErrAssetPinAuditConflict", err)
	}

	events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{ReferenceKey: event.ReferenceKey})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventID != event.EventID || !events[0].OccurredAt.Equal(event.OccurredAt) {
		t.Fatalf("audit events = %+v, want one precise append-only event", events)
	}
}

func TestAssetPinAuditEventEqualityCoversEveryPersistedField(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 16, 15, 0, 123456789, time.UTC)
	event := AssetPinAuditEvent{
		EventID:       "event-all-persisted-fields",
		Kind:          "reference_upsert",
		Result:        "success",
		TokenDigest:   strings.Repeat("1", 64),
		Repository:    "SpaceDataNetwork/asset-models",
		Ref:           "refs/heads/main",
		WorkflowRef:   "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:         "asset-bot",
		WorkflowRunID: "111111111",
		RunAttempt:    "1",
		CommitSHA:     strings.Repeat("2", 40),
		CandidateKey:  "candidate-all-fields",
		ReferenceKey:  "reference-all-fields",
		CID:           "bafybeiallfields",
		SHA256:        strings.Repeat("3", 64),
		ByteCount:     4096,
		Detail:        "original detail",
		OccurredAt:    now,
	}
	if err := store.AppendAssetPinAuditEvent(ctx, event); err != nil {
		t.Fatalf("AppendAssetPinAuditEvent() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AssetPinAuditEvent)
	}{
		{name: "mutation digest", mutate: func(got *AssetPinAuditEvent) { got.MutationDigest = strings.Repeat("0", 64) }},
		{name: "kind", mutate: func(got *AssetPinAuditEvent) { got.Kind = "reference_transition" }},
		{name: "result", mutate: func(got *AssetPinAuditEvent) { got.Result = "failure" }},
		{name: "token digest", mutate: func(got *AssetPinAuditEvent) { got.TokenDigest = strings.Repeat("4", 64) }},
		{name: "repository", mutate: func(got *AssetPinAuditEvent) { got.Repository = "SpaceDataNetwork/other" }},
		{name: "ref", mutate: func(got *AssetPinAuditEvent) { got.Ref = "refs/heads/review" }},
		{name: "workflow ref", mutate: func(got *AssetPinAuditEvent) {
			got.WorkflowRef = "SpaceDataNetwork/asset-models/.github/workflows/review.yml@refs/heads/main"
		}},
		{name: "actor", mutate: func(got *AssetPinAuditEvent) { got.Actor = "other-bot" }},
		{name: "workflow run ID", mutate: func(got *AssetPinAuditEvent) { got.WorkflowRunID = "222222222" }},
		{name: "run attempt", mutate: func(got *AssetPinAuditEvent) { got.RunAttempt = "2" }},
		{name: "commit SHA", mutate: func(got *AssetPinAuditEvent) { got.CommitSHA = strings.Repeat("5", 40) }},
		{name: "candidate key", mutate: func(got *AssetPinAuditEvent) { got.CandidateKey = "candidate-other" }},
		{name: "reference key", mutate: func(got *AssetPinAuditEvent) { got.ReferenceKey = "reference-other" }},
		{name: "CID", mutate: func(got *AssetPinAuditEvent) { got.CID = "bafybeiotherfields" }},
		{name: "SHA-256", mutate: func(got *AssetPinAuditEvent) { got.SHA256 = strings.Repeat("6", 64) }},
		{name: "byte count", mutate: func(got *AssetPinAuditEvent) { got.ByteCount++ }},
		{name: "byte count cleared", mutate: func(got *AssetPinAuditEvent) { got.ByteCount = 0 }},
		{name: "detail", mutate: func(got *AssetPinAuditEvent) { got.Detail = "different detail" }},
		{name: "occurred at", mutate: func(got *AssetPinAuditEvent) { got.OccurredAt = got.OccurredAt.Add(time.Nanosecond) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conflict := event
			tc.mutate(&conflict)
			if err := store.AppendAssetPinAuditEvent(ctx, conflict); !errors.Is(err, ErrAssetPinAuditConflict) {
				t.Fatalf("AppendAssetPinAuditEvent(conflict) error = %v, want ErrAssetPinAuditConflict", err)
			}
		})
	}

	events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: event.EventID})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
	}
	if len(events) != 1 || !equalAssetPinAuditEvent(events[0], event) {
		t.Fatalf("persisted audit event changed after conflicts: %+v", events)
	}
}

func TestAssetPinAuditPrecheckDoesNotFillStandaloneIdentityBlanks(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	event := testAssetPinEvent("event-standalone-exact", "reconcile", AssetPinReference{
		CandidateKey:  "candidate-standalone-exact",
		ReferenceKey:  "reference-standalone-exact",
		CID:           "bafybeistandaloneexact",
		SHA256:        strings.Repeat("a", 64),
		ByteCount:     4096,
		WorkflowRunID: "standalone-run",
	}, time.Date(2026, 7, 14, 2, 30, 0, 123456789, time.UTC))
	if err := store.AppendAssetPinAuditEvent(ctx, event); err != nil {
		t.Fatalf("AppendAssetPinAuditEvent() error = %v", err)
	}
	cleared := event
	cleared.ByteCount = 0
	if same, err := assetPinAuditEventAlreadyApplied(ctx, store.db, cleared); !errors.Is(err, ErrAssetPinAuditConflict) || same {
		t.Fatalf("standalone audit precheck = same %v, error %v; want ErrAssetPinAuditConflict", same, err)
	}
}

func TestAssetPinEventBackedMutationsAreIdempotentOnLiveRetry(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 16, 20, 0, 987654321, time.UTC)
	ref := testAssetPinReference("reference-live-retry", "candidate-live-retry", "bafybeiliveretry", strings.Repeat("7", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	upsertEvent := testAssetPinEvent("event-live-retry-upsert", "reference_upsert", ref, now)
	if err := store.UpsertAssetPinReference(ctx, ref, upsertEvent); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	reviewedAt := now.Add(time.Nanosecond)
	transition := AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  7070,
		UpdatedAt:    reviewedAt,
		ExpiresAt:    ref.ExpiresAt,
	}
	transitionEvent := testAssetPinEvent("event-live-retry-transition", "reference_transition", ref, reviewedAt)
	transitionEvent.Result = string(AssetReferenceReviewOpen)
	if err := store.TransitionAssetPinReference(ctx, transition, transitionEvent); err != nil {
		t.Fatalf("TransitionAssetPinReference() error = %v", err)
	}
	if err := store.UpsertAssetPinReference(ctx, ref, upsertEvent); err != nil {
		t.Fatalf("retried UpsertAssetPinReference() error = %v", err)
	}
	if err := store.TransitionAssetPinReference(ctx, transition, transitionEvent); err != nil {
		t.Fatalf("retried TransitionAssetPinReference() error = %v", err)
	}
	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok || got.State != AssetReferenceReviewOpen || got.GitHubIssue != 7070 || !got.UpdatedAt.Equal(reviewedAt) {
		t.Fatalf("reference after live retries = %+v, %v, %v; want review_open", got, ok, err)
	}

	deletedAt := time.Date(2019, 1, 1, 0, 0, 0, 123456789, time.UTC)
	deleted := testAssetPinReference("reference-live-delete-retry", "candidate-live-delete-retry", "bafybeilivedeleteold", strings.Repeat("8", 64), AssetReferenceStaged, deletedAt, time.Date(2020, 1, 1, 0, 0, 0, 123456789, time.UTC))
	if err := store.UpsertAssetPinReference(ctx, deleted, testAssetPinEvent("event-live-delete-upsert", "reference_upsert", deleted, deleted.CreatedAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference(deleted) error = %v", err)
	}
	deleteEvent := testAssetPinEvent("event-live-delete-retry", "reference_delete", deleted, now.Add(3*time.Nanosecond))
	if err := store.DeleteExpiredAssetPinReference(ctx, deleted.ReferenceKey, deleteEvent); err != nil {
		t.Fatalf("DeleteExpiredAssetPinReference() error = %v", err)
	}
	recreated := deleted
	recreated.CID = "bafybeilivedeletenew"
	recreated.SHA256 = strings.Repeat("9", 64)
	recreated.State = AssetReferenceApproved
	recreated.GitHubIssue = 4242
	recreated.DecisionSHA256 = strings.Repeat("a", 64)
	recreated.CreatedAt = now.Add(4 * time.Nanosecond)
	recreated.UpdatedAt = recreated.CreatedAt
	recreated.ExpiresAt = time.Time{}
	if err := store.UpsertAssetPinReference(ctx, recreated, testAssetPinEvent("event-live-delete-recreated", "reference_upsert", recreated, recreated.CreatedAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference(recreated) error = %v", err)
	}
	if err := store.DeleteExpiredAssetPinReference(ctx, deleted.ReferenceKey, deleteEvent); err != nil {
		t.Fatalf("retried DeleteExpiredAssetPinReference() error = %v", err)
	}
	got, ok, err = store.FindAssetPinReference(ctx, recreated.ReferenceKey)
	if err != nil || !ok || got.CID != recreated.CID || got.SHA256 != recreated.SHA256 || got.State != recreated.State {
		t.Fatalf("recreated reference after live delete retry = %+v, %v, %v; want recreated", got, ok, err)
	}
}

func TestAssetPinMutationConflictsDoNotPartiallyWrite(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 16, 30, 0, 123456789, time.UTC)
	first := testAssetPinReference("reference-owner", "candidate-unique", "bafybeiconflictowner", strings.Repeat("9", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, first, testAssetPinEvent("event-owner", "reference_upsert", first, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference(owner) error = %v", err)
	}

	conflicting := testAssetPinReference("reference-conflict", first.CandidateKey, "bafybeiconflictingcid", strings.Repeat("a", 64), AssetReferenceStaged, now.Add(time.Second), now.Add(time.Hour))
	conflictingEvent := testAssetPinEvent("event-candidate-conflict", "reference_upsert", conflicting, conflicting.CreatedAt)
	if err := store.UpsertAssetPinReference(ctx, conflicting, conflictingEvent); !errors.Is(err, ErrAssetPinReferenceConflict) {
		t.Fatalf("candidate-conflict UpsertAssetPinReference() error = %v, want ErrAssetPinReferenceConflict", err)
	}
	if _, ok, err := store.FindAssetPinReference(ctx, conflicting.ReferenceKey); err != nil || ok {
		t.Fatalf("conflicting reference lookup = ok %v, err %v; want absent", ok, err)
	}
	events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: conflictingEvent.EventID})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents(candidate conflict) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("candidate conflict wrote audit events: %+v", events)
	}

	transitionEvent := testAssetPinEvent("event-state-conflict", "reference_transition", first, now.Add(2*time.Second))
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: first.ReferenceKey,
		FromState:    AssetReferenceApproved,
		ToState:      AssetReferenceRejected,
		UpdatedAt:    transitionEvent.OccurredAt,
		ExpiresAt:    now.Add(time.Hour),
	}, transitionEvent); !errors.Is(err, ErrAssetPinReferenceConflict) {
		t.Fatalf("state-conflict TransitionAssetPinReference() error = %v, want ErrAssetPinReferenceConflict", err)
	}
	got, ok, err := store.FindAssetPinReference(ctx, first.ReferenceKey)
	if err != nil || !ok || got.State != AssetReferenceStaged {
		t.Fatalf("reference after failed transition = %+v, %v, %v; want staged", got, ok, err)
	}
	events, err = store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: transitionEvent.EventID})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents(state conflict) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("state conflict wrote audit events: %+v", events)
	}

	missingEvent := testAssetPinEvent("event-missing-delete", "reference_delete", AssetPinReference{ReferenceKey: "reference-missing"}, now.Add(3*time.Second))
	if err := store.DeleteExpiredAssetPinReference(ctx, "reference-missing", missingEvent); !errors.Is(err, ErrAssetPinReferenceNotFound) {
		t.Fatalf("missing DeleteExpiredAssetPinReference() error = %v, want ErrAssetPinReferenceNotFound", err)
	}
	events, err = store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: missingEvent.EventID})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents(missing delete) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("missing delete wrote audit events: %+v", events)
	}
}

func TestAssetPinTransitionReplacesApprovalDecisionDigest(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 16, 45, 0, 246813579, time.UTC)
	ref := testAssetPinReference("reference-decision", "candidate-decision", "bafybeidecisioncid", strings.Repeat("b", 64), AssetReferenceReviewOpen, now, time.Time{})
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-decision-upsert", "reference_upsert", ref, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	decisionSHA := strings.Repeat("c", 64)
	approvedAt := now.Add(time.Second)
	approvedEvent := testAssetPinEvent("event-decision-approved", "reference_transition", ref, approvedAt)
	approvedEvent.Result = string(AssetReferenceApproved)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey:   ref.ReferenceKey,
		FromState:      AssetReferenceReviewOpen,
		ToState:        AssetReferenceApproved,
		GitHubIssue:    ref.GitHubIssue,
		DecisionSHA256: decisionSHA,
		UpdatedAt:      approvedAt,
	}, approvedEvent); err != nil {
		t.Fatalf("approve TransitionAssetPinReference() error = %v", err)
	}

	supersededAt := approvedAt.Add(time.Second)
	supersedeSHA := strings.Repeat("d", 64)
	supersededEvent := testAssetPinEvent("event-decision-superseded", "reference_transition", ref, supersededAt)
	supersededEvent.Result = string(AssetReferenceSuperseded)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey:   ref.ReferenceKey,
		FromState:      AssetReferenceApproved,
		ToState:        AssetReferenceSuperseded,
		GitHubIssue:    ref.GitHubIssue,
		DecisionSHA256: supersedeSHA,
		UpdatedAt:      supersededAt,
		ExpiresAt:      supersededAt.Add(30 * 24 * time.Hour),
	}, supersededEvent); err != nil {
		t.Fatalf("supersede TransitionAssetPinReference() error = %v", err)
	}

	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("FindAssetPinReference() = %+v, %v, %v; want present", got, ok, err)
	}
	if got.State != AssetReferenceSuperseded || got.DecisionSHA256 != supersedeSHA {
		t.Fatalf("superseded reference = state %q decision %q, want %q and replacement %q", got.State, got.DecisionSHA256, AssetReferenceSuperseded, supersedeSHA)
	}
}

func TestAssetPinTransitionSetsAndPreservesGitHubIssue(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 16, 50, 0, 112358132, time.UTC)
	ref := testAssetPinReference("reference-issue", "candidate-issue", "bafybeiissuecid", strings.Repeat("d", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	ref.GitHubIssue = 0
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-issue-upsert", "reference_upsert", ref, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}

	reviewAt := now.Add(time.Second)
	reviewEvent := testAssetPinEvent("event-issue-review", "reference_transition", ref, reviewAt)
	reviewEvent.Result = string(AssetReferenceReviewOpen)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  4242,
		UpdatedAt:    reviewAt,
		ExpiresAt:    ref.ExpiresAt,
	}, reviewEvent); err != nil {
		t.Fatalf("review-open TransitionAssetPinReference() error = %v", err)
	}
	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok || got.GitHubIssue != 4242 {
		t.Fatalf("reference after review transition = %+v, %v, %v; want GitHub issue 4242", got, ok, err)
	}

	approvedAt := reviewAt.Add(time.Second)
	approvedEvent := testAssetPinEvent("event-issue-approved", "reference_transition", ref, approvedAt)
	approvedEvent.Result = string(AssetReferenceApproved)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey:   ref.ReferenceKey,
		FromState:      AssetReferenceReviewOpen,
		ToState:        AssetReferenceApproved,
		GitHubIssue:    4242,
		DecisionSHA256: strings.Repeat("e", 64),
		UpdatedAt:      approvedAt,
	}, approvedEvent); err != nil {
		t.Fatalf("approve TransitionAssetPinReference() error = %v", err)
	}
	got, ok, err = store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("FindAssetPinReference() = %+v, %v, %v; want present", got, ok, err)
	}
	if got.State != AssetReferenceApproved || got.GitHubIssue != 4242 {
		t.Fatalf("approved reference = state %q issue %d, want approved with preserved issue 4242", got.State, got.GitHubIssue)
	}
}

func TestAssetPinTransitionLifecycleMatrix(t *testing.T) {
	states := []AssetReferenceState{
		AssetReferenceStaged,
		AssetReferenceReviewOpen,
		AssetReferenceApproved,
		AssetReferenceRejected,
		AssetReferenceSuperseded,
		AssetReferenceAbandoned,
	}
	allowed := map[[2]AssetReferenceState]bool{
		{AssetReferenceStaged, AssetReferenceReviewOpen}:    true,
		{AssetReferenceStaged, AssetReferenceAbandoned}:     true,
		{AssetReferenceReviewOpen, AssetReferenceApproved}:  true,
		{AssetReferenceReviewOpen, AssetReferenceRejected}:  true,
		{AssetReferenceReviewOpen, AssetReferenceAbandoned}: true,
		{AssetReferenceApproved, AssetReferenceSuperseded}:  true,
	}
	ctx := context.Background()
	baseTime := time.Date(2026, 7, 14, 8, 0, 0, 123456789, time.UTC)
	for _, from := range states {
		for _, to := range states {
			name := string(from) + "_to_" + string(to)
			t.Run(name, func(t *testing.T) {
				store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
				ref := validAssetPinReferenceForState("reference-matrix-"+name, "candidate-matrix-"+name, from, baseTime)
				if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-matrix-upsert-"+name, "reference_upsert", ref, baseTime)); err != nil {
					t.Fatalf("UpsertAssetPinReference() error = %v", err)
				}
				updatedAt := baseTime.Add(time.Second)
				transition := AssetPinReferenceTransition{
					ReferenceKey: ref.ReferenceKey,
					FromState:    from,
					ToState:      to,
					GitHubIssue:  ref.GitHubIssue,
					UpdatedAt:    updatedAt,
				}
				switch to {
				case AssetReferenceStaged:
					transition.GitHubIssue = 0
					transition.ExpiresAt = updatedAt.Add(time.Hour)
				case AssetReferenceReviewOpen:
					transition.GitHubIssue = 4242
					transition.ExpiresAt = ref.ExpiresAt
				case AssetReferenceApproved:
					if transition.GitHubIssue == 0 {
						transition.GitHubIssue = 4242
					}
					transition.DecisionSHA256 = strings.Repeat("b", 64)
				case AssetReferenceRejected, AssetReferenceSuperseded:
					if transition.GitHubIssue == 0 {
						transition.GitHubIssue = 4242
					}
					transition.DecisionSHA256 = strings.Repeat("b", 64)
					transition.ExpiresAt = updatedAt.Add(30 * 24 * time.Hour)
				case AssetReferenceAbandoned:
					transition.ExpiresAt = updatedAt
				}
				event := testAssetPinEvent("event-matrix-transition-"+name, "asset_reference_state", ref, updatedAt)
				event.Result = string(to)
				err := store.TransitionAssetPinReference(ctx, transition, event)
				if !allowed[[2]AssetReferenceState{from, to}] {
					if !errors.Is(err, ErrAssetPinReferenceConflict) {
						t.Fatalf("forbidden transition error = %v, want ErrAssetPinReferenceConflict", err)
					}
					got, ok, findErr := store.FindAssetPinReference(ctx, ref.ReferenceKey)
					if findErr != nil || !ok || got.State != from {
						t.Fatalf("reference after forbidden transition = %+v, %v, %v; want unchanged %s", got, ok, findErr, from)
					}
					return
				}
				if err != nil {
					t.Fatalf("allowed transition error = %v", err)
				}
				got, ok, findErr := store.FindAssetPinReference(ctx, ref.ReferenceKey)
				if findErr != nil || !ok || got.State != to || got.GitHubIssue != transition.GitHubIssue || got.DecisionSHA256 != transition.DecisionSHA256 || !got.ExpiresAt.Equal(transition.ExpiresAt) {
					t.Fatalf("reference after allowed transition = %+v, %v, %v; want %+v", got, ok, findErr, transition)
				}
			})
		}
	}
}

func TestAssetPinTransitionEnforcesIssueDigestAndExpiryInvariants(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 123456789, time.UTC)
	tests := []struct {
		name   string
		from   AssetReferenceState
		to     AssetReferenceState
		mutate func(*AssetPinReferenceTransition, AssetPinReference)
	}{
		{name: "review requires positive issue", from: AssetReferenceStaged, to: AssetReferenceReviewOpen, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.GitHubIssue = 0 }},
		{name: "review forbids digest", from: AssetReferenceStaged, to: AssetReferenceReviewOpen, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) {
			tr.DecisionSHA256 = strings.Repeat("c", 64)
		}},
		{name: "review retains staged expiry", from: AssetReferenceStaged, to: AssetReferenceReviewOpen, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) {
			tr.ExpiresAt = tr.ExpiresAt.Add(time.Second)
		}},
		{name: "approval preserves issue", from: AssetReferenceReviewOpen, to: AssetReferenceApproved, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.GitHubIssue++ }},
		{name: "approval requires digest", from: AssetReferenceReviewOpen, to: AssetReferenceApproved, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.DecisionSHA256 = "" }},
		{name: "approval requires lowercase digest", from: AssetReferenceReviewOpen, to: AssetReferenceApproved, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) {
			tr.DecisionSHA256 = strings.Repeat("C", 64)
		}},
		{name: "approval has no expiry", from: AssetReferenceReviewOpen, to: AssetReferenceApproved, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.ExpiresAt = tr.UpdatedAt.Add(time.Hour) }},
		{name: "rejection preserves issue", from: AssetReferenceReviewOpen, to: AssetReferenceRejected, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.GitHubIssue++ }},
		{name: "rejection expires exactly thirty days", from: AssetReferenceReviewOpen, to: AssetReferenceRejected, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) {
			tr.ExpiresAt = tr.ExpiresAt.Add(time.Nanosecond)
		}},
		{name: "supersede preserves issue", from: AssetReferenceApproved, to: AssetReferenceSuperseded, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.GitHubIssue++ }},
		{name: "supersede replaces digest", from: AssetReferenceApproved, to: AssetReferenceSuperseded, mutate: func(tr *AssetPinReferenceTransition, ref AssetPinReference) { tr.DecisionSHA256 = ref.DecisionSHA256 }},
		{name: "abandon preserves issue", from: AssetReferenceReviewOpen, to: AssetReferenceAbandoned, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.GitHubIssue++ }},
		{name: "abandon forbids digest", from: AssetReferenceReviewOpen, to: AssetReferenceAbandoned, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) {
			tr.DecisionSHA256 = strings.Repeat("c", 64)
		}},
		{name: "abandon requires finite expiry", from: AssetReferenceStaged, to: AssetReferenceAbandoned, mutate: func(tr *AssetPinReferenceTransition, _ AssetPinReference) { tr.ExpiresAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
			ref := validAssetPinReferenceForState("reference-invariant-"+strings.ReplaceAll(test.name, " ", "-"), "candidate-invariant-"+strings.ReplaceAll(test.name, " ", "-"), test.from, now)
			if err := store.UpsertAssetPinReference(context.Background(), ref, testAssetPinEvent("event-invariant-upsert-"+strings.ReplaceAll(test.name, " ", "-"), "reference_upsert", ref, now)); err != nil {
				t.Fatalf("UpsertAssetPinReference() error = %v", err)
			}
			transition := validAssetPinTransitionForTest(ref, test.to, now.Add(time.Second))
			test.mutate(&transition, ref)
			event := testAssetPinEvent("event-invariant-transition-"+strings.ReplaceAll(test.name, " ", "-"), "asset_reference_state", ref, transition.UpdatedAt)
			err := store.TransitionAssetPinReference(context.Background(), transition, event)
			if !errors.Is(err, ErrAssetPinReferenceConflict) {
				t.Fatalf("TransitionAssetPinReference() error = %v, want ErrAssetPinReferenceConflict", err)
			}
			got, ok, findErr := store.FindAssetPinReference(context.Background(), ref.ReferenceKey)
			if findErr != nil || !ok || got != ref {
				t.Fatalf("reference after invalid transition = %+v, %v, %v; want unchanged %+v", got, ok, findErr, ref)
			}
			events, listErr := store.ListAssetPinAuditEvents(context.Background(), AssetPinAuditEventQuery{EventID: event.EventID})
			if listErr != nil || len(events) != 0 {
				t.Fatalf("invalid transition audit events = %+v, %v; want none", events, listErr)
			}
		})
	}
}

func TestAssetPinReferenceUpsertEnforcesLifecycleInvariants(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 123456789, time.UTC)
	tests := []struct {
		name   string
		state  AssetReferenceState
		mutate func(*AssetPinReference)
	}{
		{name: "staged issue", state: AssetReferenceStaged, mutate: func(ref *AssetPinReference) { ref.GitHubIssue = 1 }},
		{name: "staged digest", state: AssetReferenceStaged, mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = strings.Repeat("c", 64) }},
		{name: "staged zero expiry", state: AssetReferenceStaged, mutate: func(ref *AssetPinReference) { ref.ExpiresAt = time.Time{} }},
		{name: "review zero issue", state: AssetReferenceReviewOpen, mutate: func(ref *AssetPinReference) { ref.GitHubIssue = 0 }},
		{name: "review digest", state: AssetReferenceReviewOpen, mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = strings.Repeat("c", 64) }},
		{name: "review zero expiry", state: AssetReferenceReviewOpen, mutate: func(ref *AssetPinReference) { ref.ExpiresAt = time.Time{} }},
		{name: "approved zero issue", state: AssetReferenceApproved, mutate: func(ref *AssetPinReference) { ref.GitHubIssue = 0 }},
		{name: "approved missing digest", state: AssetReferenceApproved, mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = "" }},
		{name: "approved uppercase digest", state: AssetReferenceApproved, mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = strings.Repeat("C", 64) }},
		{name: "approved finite expiry", state: AssetReferenceApproved, mutate: func(ref *AssetPinReference) { ref.ExpiresAt = now.Add(time.Hour) }},
		{name: "rejected wrong expiry", state: AssetReferenceRejected, mutate: func(ref *AssetPinReference) { ref.ExpiresAt = ref.ExpiresAt.Add(time.Nanosecond) }},
		{name: "superseded missing digest", state: AssetReferenceSuperseded, mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = "" }},
		{name: "abandoned digest", state: AssetReferenceAbandoned, mutate: func(ref *AssetPinReference) { ref.DecisionSHA256 = strings.Repeat("c", 64) }},
		{name: "abandoned zero expiry", state: AssetReferenceAbandoned, mutate: func(ref *AssetPinReference) { ref.ExpiresAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
			key := strings.ReplaceAll(test.name, " ", "-")
			ref := validAssetPinReferenceForState("reference-upsert-"+key, "candidate-upsert-"+key, test.state, now)
			test.mutate(&ref)
			event := testAssetPinEvent("event-upsert-"+key, "reference_upsert", ref, now)
			if err := store.UpsertAssetPinReference(context.Background(), ref, event); err == nil {
				t.Fatal("UpsertAssetPinReference() accepted invalid lifecycle fields")
			}
			if _, ok, err := store.FindAssetPinReference(context.Background(), ref.ReferenceKey); err != nil || ok {
				t.Fatalf("invalid reference lookup = ok %v err %v, want absent", ok, err)
			}
		})
	}
}

func TestAssetPinReplayRejectsMalformedLegacyDecisionAndExpiryTransitions(t *testing.T) {
	tests := []struct {
		name       string
		transition func(AssetPinReference, time.Time) AssetPinReferenceTransition
	}{
		{
			name: "approval missing persisted issue",
			transition: func(ref AssetPinReference, updatedAt time.Time) AssetPinReferenceTransition {
				return AssetPinReferenceTransition{
					ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceReviewOpen, ToState: AssetReferenceApproved,
					DecisionSHA256: strings.Repeat("c", 64), UpdatedAt: updatedAt,
				}
			},
		},
		{
			name: "rejection has noncanonical retention expiry",
			transition: func(ref AssetPinReference, updatedAt time.Time) AssetPinReferenceTransition {
				return AssetPinReferenceTransition{
					ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceReviewOpen, ToState: AssetReferenceRejected,
					GitHubIssue: ref.GitHubIssue, DecisionSHA256: strings.Repeat("d", 64), UpdatedAt: updatedAt,
					ExpiresAt: updatedAt.Add(30*24*time.Hour + time.Nanosecond),
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := filepath.Join(t.TempDir(), "store")
			store := newAssetPinTestStore(t, basePath)
			ctx := context.Background()
			now := time.Date(2026, 7, 14, 10, 30, 0, 123456789, time.UTC)
			ref := validAssetPinReferenceForState("reference-malformed-replay", "candidate-malformed-replay", AssetReferenceStaged, now)
			if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-malformed-replay-upsert", "reference_upsert", ref, now)); err != nil {
				t.Fatalf("UpsertAssetPinReference() error = %v", err)
			}
			review := validAssetPinTransitionForTest(ref, AssetReferenceReviewOpen, now.Add(time.Second))
			if err := store.TransitionAssetPinReference(ctx, review, testAssetPinEvent("event-malformed-replay-review", "asset_reference_state", ref, review.UpdatedAt)); err != nil {
				t.Fatalf("TransitionAssetPinReference(review) error = %v", err)
			}
			current, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
			if err != nil || !ok {
				t.Fatalf("FindAssetPinReference() = %+v, %v, %v; want review reference", current, ok, err)
			}
			malformed := test.transition(current, now.Add(2*time.Second))
			event := testAssetPinEvent("event-malformed-replay-decision", "asset_reference_state", current, malformed.UpdatedAt)
			event.Result = string(malformed.ToState)
			payload := auxiliaryAssetPinReferenceTransition{Transition: malformed, Event: event}
			if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{
				Kind: auxiliaryEventAssetPinReferenceTransition, AssetPinReferenceTransition: &payload,
			}); err != nil {
				t.Fatalf("append malformed legacy frame: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if _, err := newFlatSQLStoreWithoutTestCleanup(basePath); !errors.Is(err, ErrAssetPinReferenceConflict) {
				t.Fatalf("reopen malformed legacy journal error = %v, want ErrAssetPinReferenceConflict", err)
			}
		})
	}
}

func TestAssetPinTransitionConcurrentRetriesAndConflicts(t *testing.T) {
	now := time.Date(2026, 7, 14, 11, 0, 0, 123456789, time.UTC)

	t.Run("identical event is idempotent", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := validAssetPinReferenceForState("reference-concurrent-identical", "candidate-concurrent-identical", AssetReferenceStaged, now)
		if err := store.UpsertAssetPinReference(context.Background(), ref, testAssetPinEvent("event-concurrent-identical-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		transition := validAssetPinTransitionForTest(ref, AssetReferenceReviewOpen, now.Add(time.Second))
		event := testAssetPinEvent("event-concurrent-identical", "asset_reference_state", ref, transition.UpdatedAt)
		event.Result = string(transition.ToState)
		errorsOut := make(chan error, 2)
		var start sync.WaitGroup
		start.Add(1)
		for i := 0; i < 2; i++ {
			go func() {
				start.Wait()
				errorsOut <- store.TransitionAssetPinReference(context.Background(), transition, event)
			}()
		}
		start.Done()
		for i := 0; i < 2; i++ {
			if err := <-errorsOut; err != nil {
				t.Fatalf("identical concurrent transition error = %v", err)
			}
		}
		events, err := store.ListAssetPinAuditEvents(context.Background(), AssetPinAuditEventQuery{EventID: event.EventID})
		if err != nil || len(events) != 1 {
			t.Fatalf("identical transition events = %+v, %v; want one", events, err)
		}
	})

	t.Run("different decisions have one winner", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := validAssetPinReferenceForState("reference-concurrent-conflict", "candidate-concurrent-conflict", AssetReferenceReviewOpen, now)
		if err := store.UpsertAssetPinReference(context.Background(), ref, testAssetPinEvent("event-concurrent-conflict-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		errorsOut := make(chan error, 2)
		var start sync.WaitGroup
		start.Add(1)
		for i, digestChar := range []string{"b", "c"} {
			transition := validAssetPinTransitionForTest(ref, AssetReferenceApproved, now.Add(time.Second))
			transition.DecisionSHA256 = strings.Repeat(digestChar, 64)
			event := testAssetPinEvent(fmt.Sprintf("event-concurrent-conflict-%d", i), "asset_reference_state", ref, transition.UpdatedAt)
			event.Result = string(transition.ToState)
			go func(transition AssetPinReferenceTransition, event AssetPinAuditEvent) {
				start.Wait()
				errorsOut <- store.TransitionAssetPinReference(context.Background(), transition, event)
			}(transition, event)
		}
		start.Done()
		successes, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			err := <-errorsOut
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrAssetPinReferenceConflict):
				conflicts++
			default:
				t.Fatalf("unexpected concurrent decision error = %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("concurrent decisions = %d successes/%d conflicts, want 1/1", successes, conflicts)
		}
		got, ok, err := store.FindAssetPinReference(context.Background(), ref.ReferenceKey)
		if err != nil || !ok || got.State != AssetReferenceApproved || (got.DecisionSHA256 != strings.Repeat("b", 64) && got.DecisionSHA256 != strings.Repeat("c", 64)) {
			t.Fatalf("winning decision = %+v, %v, %v", got, ok, err)
		}
	})
}

func TestAssetPinAllowedTransitionsReplayToIdenticalLifecycleState(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newAssetPinTestStore(t, basePath)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 123456789, time.UTC)
	refs := []AssetPinReference{
		validAssetPinReferenceForState("reference-replay-superseded", "candidate-replay-superseded", AssetReferenceStaged, now),
		validAssetPinReferenceForState("reference-replay-rejected", "candidate-replay-rejected", AssetReferenceStaged, now),
		validAssetPinReferenceForState("reference-replay-abandoned-staged", "candidate-replay-abandoned-staged", AssetReferenceStaged, now),
		validAssetPinReferenceForState("reference-replay-abandoned-review", "candidate-replay-abandoned-review", AssetReferenceStaged, now),
	}
	for index := range refs {
		ref := refs[index]
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent(fmt.Sprintf("event-replay-upsert-%d", index), "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference(%d) error = %v", index, err)
		}
	}
	apply := func(ref *AssetPinReference, transition AssetPinReferenceTransition, eventID string) {
		t.Helper()
		event := testAssetPinEvent(eventID, "asset_reference_state", *ref, transition.UpdatedAt)
		event.Result = string(transition.ToState)
		if err := store.TransitionAssetPinReference(ctx, transition, event); err != nil {
			t.Fatalf("TransitionAssetPinReference(%s) error = %v", eventID, err)
		}
		ref.State = transition.ToState
		ref.GitHubIssue = transition.GitHubIssue
		ref.DecisionSHA256 = transition.DecisionSHA256
		ref.UpdatedAt = transition.UpdatedAt
		ref.ExpiresAt = transition.ExpiresAt
	}

	review := validAssetPinTransitionForTest(refs[0], AssetReferenceReviewOpen, now.Add(time.Second))
	apply(&refs[0], review, "event-replay-superseded-review")
	approve := validAssetPinTransitionForTest(refs[0], AssetReferenceApproved, now.Add(2*time.Second))
	apply(&refs[0], approve, "event-replay-superseded-approve")
	supersede := validAssetPinTransitionForTest(refs[0], AssetReferenceSuperseded, now.Add(3*time.Second))
	supersede.DecisionSHA256 = strings.Repeat("c", 64)
	apply(&refs[0], supersede, "event-replay-superseded-final")

	review = validAssetPinTransitionForTest(refs[1], AssetReferenceReviewOpen, now.Add(4*time.Second))
	apply(&refs[1], review, "event-replay-rejected-review")
	reject := validAssetPinTransitionForTest(refs[1], AssetReferenceRejected, now.Add(5*time.Second))
	apply(&refs[1], reject, "event-replay-rejected-final")

	abandon := validAssetPinTransitionForTest(refs[2], AssetReferenceAbandoned, now.Add(6*time.Second))
	apply(&refs[2], abandon, "event-replay-abandoned-staged-final")

	review = validAssetPinTransitionForTest(refs[3], AssetReferenceReviewOpen, now.Add(7*time.Second))
	apply(&refs[3], review, "event-replay-abandoned-review-open")
	abandon = validAssetPinTransitionForTest(refs[3], AssetReferenceAbandoned, now.Add(8*time.Second))
	apply(&refs[3], abandon, "event-replay-abandoned-review-final")

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := newFlatSQLStoreWithoutTestCleanup(basePath)
	if err != nil {
		t.Fatalf("reopen NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for _, want := range refs {
		got, ok, err := reopened.FindAssetPinReference(ctx, want.ReferenceKey)
		if err != nil || !ok || got != want {
			t.Fatalf("replayed reference %q = %+v, %v, %v; want %+v", want.ReferenceKey, got, ok, err, want)
		}
	}
}

func newFlatSQLStoreWithoutTestCleanup(basePath string) (*FlatSQLStore, error) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, err
	}
	return NewFlatSQLStore(basePath, validator)
}

func validAssetPinTransitionForTest(ref AssetPinReference, to AssetReferenceState, updatedAt time.Time) AssetPinReferenceTransition {
	transition := AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    ref.State,
		ToState:      to,
		GitHubIssue:  ref.GitHubIssue,
		UpdatedAt:    updatedAt,
	}
	switch to {
	case AssetReferenceReviewOpen:
		transition.GitHubIssue = 4242
		transition.ExpiresAt = ref.ExpiresAt
	case AssetReferenceApproved:
		transition.DecisionSHA256 = strings.Repeat("b", 64)
	case AssetReferenceRejected, AssetReferenceSuperseded:
		transition.DecisionSHA256 = strings.Repeat("b", 64)
		transition.ExpiresAt = updatedAt.Add(30 * 24 * time.Hour)
	case AssetReferenceAbandoned:
		transition.ExpiresAt = updatedAt
	}
	return transition
}

func validAssetPinReferenceForState(referenceKey, candidateKey string, state AssetReferenceState, updatedAt time.Time) AssetPinReference {
	ref := testAssetPinReference(referenceKey, candidateKey, "bafybeimatrixcid"+strings.ReplaceAll(string(state), "_", ""), strings.Repeat("a", 64), state, updatedAt, updatedAt.Add(90*24*time.Hour))
	switch state {
	case AssetReferenceStaged:
		ref.GitHubIssue = 0
	case AssetReferenceReviewOpen:
		ref.GitHubIssue = 4242
	case AssetReferenceApproved:
		ref.GitHubIssue = 4242
		ref.DecisionSHA256 = strings.Repeat("a", 64)
		ref.ExpiresAt = time.Time{}
	case AssetReferenceRejected, AssetReferenceSuperseded:
		ref.GitHubIssue = 4242
		ref.DecisionSHA256 = strings.Repeat("a", 64)
		ref.ExpiresAt = updatedAt.Add(30 * 24 * time.Hour)
	case AssetReferenceAbandoned:
		ref.GitHubIssue = 0
		ref.ExpiresAt = updatedAt
	}
	return ref
}

func newAssetPinTestStore(t *testing.T, basePath string) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func assertSingleAssetOIDCReceiptForTest(t *testing.T, ctx context.Context, store *FlatSQLStore, want AssetOIDCReceipt) {
	t.Helper()
	got, ok, err := store.FindAssetOIDCReceipt(ctx, want.Digest)
	if err != nil || !ok {
		t.Fatalf("FindAssetOIDCReceipt() = %+v, %v, %v; want present", got, ok, err)
	}
	if !equalAssetOIDCReceipt(got, want) {
		t.Fatalf("FindAssetOIDCReceipt() = %+v, want first receipt %+v", got, want)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_asset_oidc_receipts WHERE digest = ?`, want.Digest).Scan(&count); err != nil {
		t.Fatalf("count OIDC receipts: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored OIDC receipt rows = %d, want 1", count)
	}
}

func testAssetPinReference(referenceKey, candidateKey, cid, sha256 string, state AssetReferenceState, createdAt, expiresAt time.Time) AssetPinReference {
	ref := AssetPinReference{
		ReferenceKey:  referenceKey,
		CandidateKey:  candidateKey,
		CID:           cid,
		SHA256:        sha256,
		ByteCount:     4096,
		State:         state,
		SourceURL:     "https://example.test/assets/model.glb",
		LicenseName:   "CC-BY-4.0",
		Attribution:   "Example Artist",
		MetadataJSON:  `{"format":"glb","polygon_count":1024}`,
		WorkflowRunID: "123456789",
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		ExpiresAt:     expiresAt,
	}
	switch state {
	case AssetReferenceStaged:
		if ref.ExpiresAt.IsZero() && !createdAt.IsZero() {
			ref.ExpiresAt = createdAt.Add(90 * 24 * time.Hour)
		}
	case AssetReferenceReviewOpen:
		ref.GitHubIssue = 101
		if ref.ExpiresAt.IsZero() && !createdAt.IsZero() {
			ref.ExpiresAt = createdAt.Add(90 * 24 * time.Hour)
		}
	case AssetReferenceApproved:
		ref.GitHubIssue = 101
		ref.DecisionSHA256 = strings.Repeat("f", 64)
		ref.ExpiresAt = time.Time{}
	case AssetReferenceRejected, AssetReferenceSuperseded:
		ref.GitHubIssue = 101
		ref.DecisionSHA256 = strings.Repeat("f", 64)
		ref.ExpiresAt = createdAt.Add(30 * 24 * time.Hour)
	case AssetReferenceAbandoned:
		if ref.ExpiresAt.IsZero() {
			ref.ExpiresAt = createdAt
		}
	}
	return ref
}

func testAssetPinEvent(eventID, kind string, ref AssetPinReference, occurredAt time.Time) AssetPinAuditEvent {
	return AssetPinAuditEvent{
		EventID:       eventID,
		Kind:          kind,
		Result:        "success",
		Repository:    "SpaceDataNetwork/asset-models",
		Ref:           "refs/heads/main",
		WorkflowRef:   "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:         "asset-bot",
		WorkflowRunID: ref.WorkflowRunID,
		RunAttempt:    "1",
		CommitSHA:     strings.Repeat("3", 40),
		CandidateKey:  ref.CandidateKey,
		ReferenceKey:  ref.ReferenceKey,
		CID:           ref.CID,
		SHA256:        ref.SHA256,
		ByteCount:     ref.ByteCount,
		OccurredAt:    occurredAt,
	}
}

func prepareAssetPinUpsertFrameForTest(t *testing.T, ref AssetPinReference, event AssetPinAuditEvent) auxiliaryMetadataEvent {
	t.Helper()
	preparedRef, err := prepareAssetPinReference(ref)
	if err != nil {
		t.Fatalf("prepareAssetPinReference() error = %v", err)
	}
	preparedEvent, err := completeAssetPinAuditEvent(event, preparedRef)
	if err != nil {
		t.Fatalf("completeAssetPinAuditEvent() error = %v", err)
	}
	preparedEvent, err = bindAssetPinMutationDigest(preparedEvent, auxiliaryEventAssetPinReferenceUpsert, preparedRef)
	if err != nil {
		t.Fatalf("bindAssetPinMutationDigest() error = %v", err)
	}
	preparedEvent, err = prepareAssetPinAuditEvent(preparedEvent)
	if err != nil {
		t.Fatalf("prepareAssetPinAuditEvent() error = %v", err)
	}
	payload := auxiliaryAssetPinReferenceUpsert{Reference: preparedRef, Event: preparedEvent}
	return auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceUpsert, AssetPinReferenceUpsert: &payload}
}

func prepareAssetPinTransitionFrameForTest(t *testing.T, ref AssetPinReference, transition AssetPinReferenceTransition, event AssetPinAuditEvent) auxiliaryMetadataEvent {
	t.Helper()
	preparedTransition, err := prepareAssetPinReferenceTransition(transition)
	if err != nil {
		t.Fatalf("prepareAssetPinReferenceTransition() error = %v", err)
	}
	preparedEvent, err := bindAssetPinMutationDigest(event, auxiliaryEventAssetPinReferenceTransition, preparedTransition)
	if err != nil {
		t.Fatalf("bindAssetPinMutationDigest() error = %v", err)
	}
	preparedEvent.ReferenceKey = firstNonEmpty(preparedEvent.ReferenceKey, preparedTransition.ReferenceKey)
	preparedEvent, err = prepareAssetPinAuditEvent(preparedEvent)
	if err != nil {
		t.Fatalf("prepareAssetPinAuditEvent() error = %v", err)
	}
	preparedEvent, err = completeAssetPinAuditEvent(preparedEvent, ref)
	if err != nil {
		t.Fatalf("completeAssetPinAuditEvent() error = %v", err)
	}
	payload := auxiliaryAssetPinReferenceTransition{Transition: preparedTransition, Event: preparedEvent}
	return auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceTransition, AssetPinReferenceTransition: &payload}
}

func prepareAssetPinDeleteFrameForTest(t *testing.T, ref AssetPinReference, event AssetPinAuditEvent) auxiliaryMetadataEvent {
	t.Helper()
	preparedEvent, err := completeAssetPinAuditEvent(event, ref)
	if err != nil {
		t.Fatalf("completeAssetPinAuditEvent() error = %v", err)
	}
	payload := auxiliaryAssetPinReferenceDelete{ReferenceKey: ref.ReferenceKey}
	preparedEvent, err = bindAssetPinMutationDigest(preparedEvent, auxiliaryEventAssetPinReferenceDelete, payload)
	if err != nil {
		t.Fatalf("bindAssetPinMutationDigest() error = %v", err)
	}
	preparedEvent, err = prepareAssetPinAuditEvent(preparedEvent)
	if err != nil {
		t.Fatalf("prepareAssetPinAuditEvent() error = %v", err)
	}
	payload.Event = preparedEvent
	return auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceDelete, AssetPinReferenceDelete: &payload}
}

func auxiliaryJournalSizeForTest(t *testing.T, basePath string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(basePath, auxiliaryMetadataFileName))
	if err != nil {
		t.Fatalf("stat auxiliary journal: %v", err)
	}
	return info.Size()
}

type failingAssetPinTransactionBeginner struct {
	delegate  assetPinTransactionBeginner
	commitErr error
}

func (beginner failingAssetPinTransactionBeginner) BeginTx(ctx context.Context, opts *sql.TxOptions) (assetPinTransaction, error) {
	tx, err := beginner.delegate.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return failingAssetPinTransaction{assetPinTransaction: tx, commitErr: beginner.commitErr}, nil
}

type failingAssetPinTransaction struct {
	assetPinTransaction
	commitErr error
}

func (tx failingAssetPinTransaction) Commit() error {
	return tx.commitErr
}

type auxiliaryAppendFaultMode int

const (
	auxiliaryAppendFaultPartialPayload auxiliaryAppendFaultMode = iota
	auxiliaryAppendFaultSync
	auxiliaryAppendFaultZeroHeader
	auxiliaryAppendFaultNegativeCount
	auxiliaryAppendFaultOversizedCount
)

type faultingAuxiliaryMetadataAppendFile struct {
	delegate auxiliaryMetadataAppendFile
	mode     auxiliaryAppendFaultMode
	err      error
	writes   int
}

func (file *faultingAuxiliaryMetadataAppendFile) Write(payload []byte) (int, error) {
	file.writes++
	switch file.mode {
	case auxiliaryAppendFaultPartialPayload:
		if file.writes == 2 {
			limit := len(payload) / 2
			if limit == 0 {
				limit = 1
			}
			n, err := file.delegate.Write(payload[:limit])
			if err != nil {
				return n, err
			}
			return n, file.err
		}
	case auxiliaryAppendFaultZeroHeader:
		if file.writes == 1 {
			return 0, file.err
		}
	case auxiliaryAppendFaultNegativeCount:
		if file.writes == 1 {
			return -1, file.err
		}
	case auxiliaryAppendFaultOversizedCount:
		if file.writes == 1 {
			return len(payload) + 1, file.err
		}
	}
	return file.delegate.Write(payload)
}

func (file *faultingAuxiliaryMetadataAppendFile) Sync() error {
	if err := file.delegate.Sync(); err != nil {
		return err
	}
	if file.mode == auxiliaryAppendFaultSync {
		return file.err
	}
	return nil
}
