package storage

import (
	"context"
	"errors"
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
		{name: "expiry", mutate: func(ref *AssetPinReference) { ref.ExpiresAt = ref.UpdatedAt.Add(time.Hour) }},
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
	}, event); err != nil {
		t.Fatalf("first TransitionAssetPinReference() error = %v", err)
	}
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey:   ref.ReferenceKey,
		FromState:      AssetReferenceReviewOpen,
		ToState:        AssetReferenceApproved,
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
		UpdatedAt:    event.OccurredAt,
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
		first := AssetPinReferenceTransition{ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceStaged, ToState: AssetReferenceReviewOpen, UpdatedAt: event.OccurredAt}
		if err := store.auxiliaryMetadata.Append(prepareAssetPinTransitionFrameForTest(t, ref, first, event)); err != nil {
			t.Fatalf("append durable transition frame: %v", err)
		}
		conflict := AssetPinReferenceTransition{ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceApproved, ToState: AssetReferenceRejected, UpdatedAt: event.OccurredAt.Add(time.Second)}
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
		transition := AssetPinReferenceTransition{ReferenceKey: ref.ReferenceKey, FromState: AssetReferenceStaged, ToState: AssetReferenceReviewOpen, GitHubIssue: 8181, UpdatedAt: now.Add(time.Second)}
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
		expiresAt time.Time
		protected bool
		expired   bool
	}{
		{key: "approved-past", state: AssetReferenceApproved, expiresAt: now.Add(-time.Hour), protected: true},
		{key: "review-past", state: AssetReferenceReviewOpen, expiresAt: now.Add(-time.Hour), protected: true},
		{key: "staged-zero", state: AssetReferenceStaged, protected: true},
		{key: "staged-past", state: AssetReferenceStaged, expiresAt: now.Add(-time.Hour), expired: true},
		{key: "rejected-future", state: AssetReferenceRejected, expiresAt: now.Add(time.Hour), protected: true},
		{key: "superseded-past", state: AssetReferenceSuperseded, expiresAt: now.Add(-time.Second), expired: true},
		{key: "abandoned-past", state: AssetReferenceAbandoned, expiresAt: now.Add(-time.Second), expired: true},
		{key: "abandoned-zero", state: AssetReferenceAbandoned},
	}
	for i, tc := range cases {
		createdAt := now.Add(time.Duration(i) * time.Nanosecond)
		if !tc.expiresAt.IsZero() && tc.expiresAt.Before(createdAt) {
			createdAt = tc.expiresAt.Add(-time.Hour)
		}
		ref := testAssetPinReference("reference-"+tc.key, "candidate-"+tc.key, cid, sha256, tc.state, createdAt, tc.expiresAt)
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

	deleteEvent := testAssetPinEvent("event-delete-staged-past", "reference_delete", expired[0], time.Now().UTC())
	deleteEvent.ReferenceKey = "reference-staged-past"
	if err := store.DeleteExpiredAssetPinReference(ctx, "reference-staged-past", deleteEvent); err != nil {
		t.Fatalf("DeleteExpiredAssetPinReference(expired) error = %v", err)
	}
	if _, ok, err := store.FindAssetPinReference(ctx, "reference-staged-past"); err != nil || ok {
		t.Fatalf("deleted reference lookup = ok %v, err %v; want absent", ok, err)
	}
	protectedEvent := testAssetPinEvent("event-delete-approved", "reference_delete", AssetPinReference{ReferenceKey: "reference-approved-past"}, time.Now().UTC())
	if err := store.DeleteExpiredAssetPinReference(ctx, "reference-approved-past", protectedEvent); !errors.Is(err, ErrAssetPinReferenceNotExpired) {
		t.Fatalf("DeleteExpiredAssetPinReference(approved) error = %v, want ErrAssetPinReferenceNotExpired", err)
	}

	invalid := testAssetPinReference("reference-invalid", "candidate-invalid", cid, sha256, AssetReferenceState("pending"), now, now.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, invalid, testAssetPinEvent("event-invalid", "reference_upsert", invalid, now)); err == nil {
		t.Fatal("UpsertAssetPinReference() accepted unknown state")
	}
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: "reference-staged-zero",
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceState("pending"),
		UpdatedAt:    now.Add(time.Minute),
	}, testAssetPinEvent("event-invalid-transition", "reference_transition", AssetPinReference{ReferenceKey: "reference-staged-zero"}, now)); err == nil {
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
			UpdatedAt:    transitionedAt,
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
	if !got.ExpiresAt.IsZero() {
		t.Fatalf("zero expiry was not preserved: %v", got.ExpiresAt)
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
	if !expiryIsNull {
		t.Fatal("zero expiry must be stored as SQL NULL")
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

func TestAssetPinTransitionPreservesExistingDecisionDigest(t *testing.T) {
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
		DecisionSHA256: decisionSHA,
		UpdatedAt:      approvedAt,
	}, approvedEvent); err != nil {
		t.Fatalf("approve TransitionAssetPinReference() error = %v", err)
	}

	supersededAt := approvedAt.Add(time.Second)
	supersededEvent := testAssetPinEvent("event-decision-superseded", "reference_transition", ref, supersededAt)
	supersededEvent.Result = string(AssetReferenceSuperseded)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceApproved,
		ToState:      AssetReferenceSuperseded,
		UpdatedAt:    supersededAt,
		ExpiresAt:    supersededAt.Add(time.Hour),
	}, supersededEvent); err != nil {
		t.Fatalf("supersede TransitionAssetPinReference() error = %v", err)
	}

	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("FindAssetPinReference() = %+v, %v, %v; want present", got, ok, err)
	}
	if got.State != AssetReferenceSuperseded || got.DecisionSHA256 != decisionSHA {
		t.Fatalf("superseded reference = state %q decision %q, want %q and retained %q", got.State, got.DecisionSHA256, AssetReferenceSuperseded, decisionSHA)
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
		ExpiresAt:    time.Time{},
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
		GitHubIssue:    0,
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
	return AssetPinReference{
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
		GitHubIssue:   101,
		WorkflowRunID: "123456789",
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		ExpiresAt:     expiresAt,
	}
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
