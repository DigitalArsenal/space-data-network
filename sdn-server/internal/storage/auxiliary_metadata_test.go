package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestAuxiliaryMetadataReplaysAssetPinReceiptsReferencesTransitionsDeletionsAndEvents(t *testing.T) {
	ctx := context.Background()
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore() error = %v", err)
	}
	now := time.Date(2026, 7, 13, 17, 0, 0, 135792468, time.UTC)
	receipt := AssetOIDCReceipt{
		Digest:      strings.Repeat("4", 64),
		ExpiresAt:   time.Date(2030, 1, 1, 0, 0, 0, 246813579, time.UTC),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "55555555",
		RunAttempt:  "3",
		SHA:         strings.Repeat("5", 40),
		ConsumedAt:  now,
	}
	if err := store.ConsumeAssetOIDCToken(ctx, receipt); err != nil {
		t.Fatalf("ConsumeAssetOIDCToken() error = %v", err)
	}

	kept := testAssetPinReference("reference-kept", "candidate-kept", "bafybeireplaykept", strings.Repeat("6", 64), AssetReferenceStaged, now.Add(time.Nanosecond), time.Time{})
	kept.GitHubIssue = 0
	if err := store.UpsertAssetPinReference(ctx, kept, testAssetPinEvent("event-kept-upsert", "reference_upsert", kept, kept.UpdatedAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference(kept) error = %v", err)
	}
	reviewedAt := now.Add(2 * time.Nanosecond)
	reviewEvent := testAssetPinEvent("event-kept-review", "reference_transition", kept, reviewedAt)
	reviewEvent.Result = string(AssetReferenceReviewOpen)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: kept.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  4242,
		UpdatedAt:    reviewedAt,
		ExpiresAt:    time.Time{},
	}, reviewEvent); err != nil {
		t.Fatalf("TransitionAssetPinReference(review open) error = %v", err)
	}
	transitionedAt := now.Add(3 * time.Nanosecond)
	decisionSHA := strings.Repeat("7", 64)
	transitionEvent := testAssetPinEvent("event-kept-approved", "reference_transition", kept, transitionedAt)
	transitionEvent.Result = string(AssetReferenceApproved)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey:   kept.ReferenceKey,
		FromState:      AssetReferenceReviewOpen,
		ToState:        AssetReferenceApproved,
		GitHubIssue:    0,
		DecisionSHA256: decisionSHA,
		UpdatedAt:      transitionedAt,
		ExpiresAt:      time.Time{},
	}, transitionEvent); err != nil {
		t.Fatalf("TransitionAssetPinReference(kept) error = %v", err)
	}

	deleted := testAssetPinReference("reference-deleted", "candidate-deleted", "bafybeireplaydeleted", strings.Repeat("8", 64), AssetReferenceStaged, now.Add(4*time.Nanosecond), time.Date(2020, 1, 1, 0, 0, 0, 112233445, time.UTC))
	if err := store.UpsertAssetPinReference(ctx, deleted, testAssetPinEvent("event-deleted-upsert", "reference_upsert", deleted, deleted.UpdatedAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference(deleted) error = %v", err)
	}
	deleteEvent := testAssetPinEvent("event-deleted-delete", "reference_delete", deleted, now.Add(5*time.Nanosecond))
	if err := store.DeleteExpiredAssetPinReference(ctx, deleted.ReferenceKey, deleteEvent); err != nil {
		t.Fatalf("DeleteExpiredAssetPinReference() error = %v", err)
	}
	standalone := testAssetPinEvent("event-standalone-audit", "reconcile", kept, now.Add(6*time.Nanosecond))
	standalone.Detail = "standalone audit frame"
	if err := store.AppendAssetPinAuditEvent(ctx, standalone); err != nil {
		t.Fatalf("AppendAssetPinAuditEvent() error = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("reopen NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	gotReceipt, ok, err := reopened.FindAssetOIDCReceipt(ctx, receipt.Digest)
	if err != nil || !ok {
		t.Fatalf("replayed receipt = %+v, %v, %v; want present", gotReceipt, ok, err)
	}
	if gotReceipt.RunID != receipt.RunID || !gotReceipt.ExpiresAt.Equal(receipt.ExpiresAt) || !gotReceipt.ConsumedAt.Equal(receipt.ConsumedAt) {
		t.Fatalf("replayed receipt = %+v, want %+v", gotReceipt, receipt)
	}
	if err := reopened.ConsumeAssetOIDCToken(ctx, receipt); !errors.Is(err, ErrAssetOIDCTokenReplay) {
		t.Fatalf("replayed token ConsumeAssetOIDCToken() error = %v, want ErrAssetOIDCTokenReplay", err)
	}

	gotKept, ok, err := reopened.FindAssetPinReference(ctx, kept.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("replayed kept reference = %+v, %v, %v; want present", gotKept, ok, err)
	}
	if gotKept.State != AssetReferenceApproved || gotKept.DecisionSHA256 != decisionSHA || gotKept.GitHubIssue != 4242 {
		t.Fatalf("replayed transitioned reference = %+v, want approved with decision SHA and preserved issue 4242", gotKept)
	}
	if !gotKept.CreatedAt.Equal(kept.CreatedAt) || !gotKept.UpdatedAt.Equal(transitionedAt) || !gotKept.ExpiresAt.IsZero() {
		t.Fatalf("replayed transitioned times = created %v updated %v expiry %v", gotKept.CreatedAt, gotKept.UpdatedAt, gotKept.ExpiresAt)
	}
	if _, ok, err := reopened.FindAssetPinReference(ctx, deleted.ReferenceKey); err != nil || ok {
		t.Fatalf("replayed deleted reference lookup = ok %v, err %v; want absent", ok, err)
	}
	events, err := reopened.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() after reopen error = %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("replayed audit events = %d, want 6: %+v", len(events), events)
	}

	replayedFrames, err := reopened.auxiliaryMetadata.Replay(reopened)
	if err != nil {
		t.Fatalf("second auxiliary Replay() error = %v", err)
	}
	if replayedFrames != 7 {
		t.Fatalf("second auxiliary Replay() frames = %d, want 7", replayedFrames)
	}
	events, err = reopened.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() after second replay error = %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("audit events after idempotent replay = %d, want 6", len(events))
	}
	gotKept, ok, err = reopened.FindAssetPinReference(ctx, kept.ReferenceKey)
	if err != nil || !ok || gotKept.State != AssetReferenceApproved || gotKept.GitHubIssue != 4242 || !gotKept.UpdatedAt.Equal(transitionedAt) {
		t.Fatalf("kept reference after second replay = %+v, %v, %v", gotKept, ok, err)
	}
	if _, ok, err := reopened.FindAssetPinReference(ctx, deleted.ReferenceKey); err != nil || ok {
		t.Fatalf("deleted reference after second replay = ok %v, err %v; want absent", ok, err)
	}
}
