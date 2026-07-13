package storage

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestAuxiliaryMetadataDeduplicatesIdenticalAssetReceiptFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), auxiliaryMetadataFileName)
	journal, err := openAuxiliaryMetadataStore(path, false)
	if err != nil {
		t.Fatalf("openAuxiliaryMetadataStore() error = %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	receipt := AssetOIDCReceipt{
		Digest:      strings.Repeat("1", 64),
		ExpiresAt:   time.Date(2026, 7, 13, 21, 0, 0, 123456789, time.UTC),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "123456789",
		RunAttempt:  "1",
		SHA:         strings.Repeat("2", 40),
		ConsumedAt:  time.Date(2026, 7, 13, 20, 55, 0, 987654321, time.UTC),
	}
	frame := auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &receipt}
	if err := journal.Append(frame); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal after first append: %v", err)
	}
	if err := journal.Append(frame); err != nil {
		t.Fatalf("identical Append() error = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal after duplicate append: %v", err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("journal size after identical frame = %d, want %d", after.Size(), before.Size())
	}
}

func TestAuxiliaryMetadataRebuildsAssetFrameIndexOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), auxiliaryMetadataFileName)
	receipt := AssetOIDCReceipt{
		Digest:      strings.Repeat("3", 64),
		ExpiresAt:   time.Date(2026, 7, 13, 22, 0, 0, 123456789, time.UTC),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "987654321",
		RunAttempt:  "2",
		SHA:         strings.Repeat("4", 40),
		ConsumedAt:  time.Date(2026, 7, 13, 21, 55, 0, 987654321, time.UTC),
	}
	frame := auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &receipt}
	journal, err := openAuxiliaryMetadataStore(path, false)
	if err != nil {
		t.Fatalf("first openAuxiliaryMetadataStore() error = %v", err)
	}
	if err := journal.Append(frame); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal before reopen: %v", err)
	}

	reopened, err := openAuxiliaryMetadataStore(path, false)
	if err != nil {
		t.Fatalf("reopen openAuxiliaryMetadataStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Append(frame); err != nil {
		t.Fatalf("Append() after reopen error = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal after reopen append: %v", err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("journal size after reopened identical frame = %d, want %d", after.Size(), before.Size())
	}
}

func TestAuxiliaryMetadataDeduplicatesIdenticalAssetEventFrames(t *testing.T) {
	now := time.Date(2026, 7, 13, 22, 30, 0, 123456789, time.UTC)
	ref := testAssetPinReference("reference-frame-dedupe", "candidate-frame-dedupe", "bafybeiframededupe", strings.Repeat("5", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	event := testAssetPinEvent("event-frame-dedupe", "reference_upsert", ref, now)
	transition := AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		UpdatedAt:    now.Add(time.Nanosecond),
	}
	tests := []struct {
		name  string
		frame auxiliaryMetadataEvent
	}{
		{name: "upsert", frame: auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceUpsert, AssetPinReferenceUpsert: &auxiliaryAssetPinReferenceUpsert{Reference: ref, Event: event}}},
		{name: "transition", frame: auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceTransition, AssetPinReferenceTransition: &auxiliaryAssetPinReferenceTransition{Transition: transition, Event: event}}},
		{name: "delete", frame: auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceDelete, AssetPinReferenceDelete: &auxiliaryAssetPinReferenceDelete{ReferenceKey: ref.ReferenceKey, Event: event}}},
		{name: "standalone audit", frame: auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinAuditAppend, AssetPinAuditEvent: &event}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), auxiliaryMetadataFileName)
			journal, err := openAuxiliaryMetadataStore(path, false)
			if err != nil {
				t.Fatalf("openAuxiliaryMetadataStore() error = %v", err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			if err := journal.Append(tc.frame); err != nil {
				t.Fatalf("first Append() error = %v", err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat journal after first append: %v", err)
			}
			if err := journal.Append(tc.frame); err != nil {
				t.Fatalf("identical Append() error = %v", err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat journal after duplicate append: %v", err)
			}
			if after.Size() != before.Size() {
				t.Fatalf("journal size after identical frame = %d, want %d", after.Size(), before.Size())
			}
		})
	}
}

func TestAuxiliaryMetadataRejectsConflictingAssetFrameBeforeAppend(t *testing.T) {
	now := time.Date(2026, 7, 13, 23, 0, 0, 123456789, time.UTC)
	receipt := AssetOIDCReceipt{
		Digest:      strings.Repeat("6", 64),
		ExpiresAt:   now.Add(time.Hour),
		Repository:  "SpaceDataNetwork/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "SpaceDataNetwork/asset-models/.github/workflows/pin.yml@refs/heads/main",
		Actor:       "asset-bot",
		RunID:       "123456789",
		RunAttempt:  "1",
		SHA:         strings.Repeat("7", 40),
		ConsumedAt:  now,
	}
	conflictingReceipt := receipt
	conflictingReceipt.Actor = "different-bot"
	ref := testAssetPinReference("reference-frame-conflict", "candidate-frame-conflict", "bafybeiframeconflict", strings.Repeat("8", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	event := testAssetPinEvent("event-frame-conflict", "reference_upsert", ref, now)
	conflictingEvent := event
	conflictingEvent.Detail = "different payload"
	tests := []struct {
		name     string
		first    auxiliaryMetadataEvent
		conflict auxiliaryMetadataEvent
		wantErr  error
	}{
		{
			name:     "receipt digest",
			first:    auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &receipt},
			conflict: auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &conflictingReceipt},
			wantErr:  ErrAssetOIDCReceiptConflict,
		},
		{
			name:     "audit event ID",
			first:    auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceUpsert, AssetPinReferenceUpsert: &auxiliaryAssetPinReferenceUpsert{Reference: ref, Event: event}},
			conflict: auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceUpsert, AssetPinReferenceUpsert: &auxiliaryAssetPinReferenceUpsert{Reference: ref, Event: conflictingEvent}},
			wantErr:  ErrAssetPinAuditConflict,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), auxiliaryMetadataFileName)
			journal, err := openAuxiliaryMetadataStore(path, false)
			if err != nil {
				t.Fatalf("openAuxiliaryMetadataStore() error = %v", err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			if err := journal.Append(tc.first); err != nil {
				t.Fatalf("first Append() error = %v", err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat journal before conflict: %v", err)
			}
			if err := journal.Append(tc.conflict); !errors.Is(err, tc.wantErr) {
				t.Fatalf("conflicting Append() error = %v, want %v", err, tc.wantErr)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat journal after conflict: %v", err)
			}
			if after.Size() != before.Size() {
				t.Fatalf("journal size after conflict = %d, want %d", after.Size(), before.Size())
			}
		})
	}
}

func TestAuxiliaryMetadataOpenRejectsConflictingAssetFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), auxiliaryMetadataFileName)
	now := time.Date(2026, 7, 13, 23, 15, 0, 123456789, time.UTC)
	ref := testAssetPinReference("reference-open-conflict", "candidate-open-conflict", "bafybeiopenconflict", strings.Repeat("9", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	event := testAssetPinEvent("event-open-conflict", "reference_upsert", ref, now)
	first := auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceUpsert, AssetPinReferenceUpsert: &auxiliaryAssetPinReferenceUpsert{Reference: ref, Event: event}}
	journal, err := openAuxiliaryMetadataStore(path, false)
	if err != nil {
		t.Fatalf("openAuxiliaryMetadataStore() error = %v", err)
	}
	if err := journal.Append(first); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	conflictingEvent := event
	conflictingEvent.Detail = "conflicting frame already on disk"
	conflict := auxiliaryMetadataEvent{Kind: auxiliaryEventAssetPinReferenceUpsert, AssetPinReferenceUpsert: &auxiliaryAssetPinReferenceUpsert{Reference: ref, Event: conflictingEvent}}
	appendRawAuxiliaryFrameForTest(t, path, conflict)
	if _, err := openAuxiliaryMetadataStore(path, false); !errors.Is(err, ErrAssetPinAuditConflict) {
		t.Fatalf("openAuxiliaryMetadataStore(conflicting journal) error = %v, want ErrAssetPinAuditConflict", err)
	}
}

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

	deletedAt := time.Date(2019, 1, 1, 0, 0, 0, 112233445, time.UTC)
	deleted := testAssetPinReference("reference-deleted", "candidate-deleted", "bafybeireplaydeleted", strings.Repeat("8", 64), AssetReferenceStaged, deletedAt, time.Date(2020, 1, 1, 0, 0, 0, 112233445, time.UTC))
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

func TestAuxiliaryMetadataAssetReplayIsIdempotentWhenCandidateKeyIsReused(t *testing.T) {
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
	now := time.Date(2026, 7, 13, 18, 0, 0, 123456789, time.UTC)
	const candidateKey = "candidate-reused-after-delete"
	firstCreatedAt := time.Date(2019, 1, 1, 0, 0, 0, 987654321, time.UTC)
	first := testAssetPinReference("reference-reused-first", candidateKey, "bafybeireusedfirst", strings.Repeat("a", 64), AssetReferenceStaged, firstCreatedAt, time.Date(2020, 1, 1, 0, 0, 0, 987654321, time.UTC))
	firstEvent := testAssetPinEvent("event-reused-first-upsert", "reference_upsert", first, now)
	if err := store.UpsertAssetPinReference(ctx, first, firstEvent); err != nil {
		t.Fatalf("UpsertAssetPinReference(first) error = %v", err)
	}
	deleteEvent := testAssetPinEvent("event-reused-first-delete", "reference_delete", first, now.Add(time.Nanosecond))
	if err := store.DeleteExpiredAssetPinReference(ctx, first.ReferenceKey, deleteEvent); err != nil {
		t.Fatalf("DeleteExpiredAssetPinReference(first) error = %v", err)
	}
	second := testAssetPinReference("reference-reused-second", candidateKey, "bafybeireusedsecond", strings.Repeat("b", 64), AssetReferenceApproved, now.Add(2*time.Nanosecond), time.Time{})
	secondEvent := testAssetPinEvent("event-reused-second-upsert", "reference_upsert", second, second.CreatedAt)
	if err := store.UpsertAssetPinReference(ctx, second, secondEvent); err != nil {
		t.Fatalf("UpsertAssetPinReference(second) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("reopen NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertReusedCandidateFinalState(t, ctx, reopened, first.ReferenceKey, second)

	replayedFrames, err := reopened.auxiliaryMetadata.Replay(reopened)
	if err != nil {
		t.Fatalf("second auxiliary Replay() error = %v", err)
	}
	if replayedFrames != 3 {
		t.Fatalf("second auxiliary Replay() frames = %d, want 3", replayedFrames)
	}
	assertReusedCandidateFinalState(t, ctx, reopened, first.ReferenceKey, second)
	events, err := reopened.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("audit events after second replay = %d, want 3", len(events))
	}
}

func TestAuxiliaryMetadataAssetReplayIsIdempotentAfterTransitions(t *testing.T) {
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
	now := time.Date(2026, 7, 13, 18, 30, 0, 246813579, time.UTC)
	ref := testAssetPinReference("reference-transition-replay", "candidate-transition-replay", "bafybeitransitionreplay", strings.Repeat("c", 64), AssetReferenceStaged, now, now.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-transition-replay-upsert", "reference_upsert", ref, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	reviewedAt := now.Add(time.Nanosecond)
	reviewEvent := testAssetPinEvent("event-transition-replay-review", "reference_transition", ref, reviewedAt)
	reviewEvent.Result = string(AssetReferenceReviewOpen)
	if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
		ReferenceKey: ref.ReferenceKey,
		FromState:    AssetReferenceStaged,
		ToState:      AssetReferenceReviewOpen,
		GitHubIssue:  5150,
		UpdatedAt:    reviewedAt,
	}, reviewEvent); err != nil {
		t.Fatalf("review TransitionAssetPinReference() error = %v", err)
	}
	approvedAt := now.Add(2 * time.Nanosecond)
	decisionSHA := strings.Repeat("d", 64)
	approvedEvent := testAssetPinEvent("event-transition-replay-approved", "reference_transition", ref, approvedAt)
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
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("reopen NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertTransitionReplayFinalState(t, ctx, reopened, ref.ReferenceKey, approvedAt, decisionSHA)

	replayedFrames, err := reopened.auxiliaryMetadata.Replay(reopened)
	if err != nil {
		t.Fatalf("second auxiliary Replay() error = %v", err)
	}
	if replayedFrames != 3 {
		t.Fatalf("second auxiliary Replay() frames = %d, want 3", replayedFrames)
	}
	assertTransitionReplayFinalState(t, ctx, reopened, ref.ReferenceKey, approvedAt, decisionSHA)
	events, err := reopened.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("audit events after second replay = %d, want 3", len(events))
	}
}

func TestAuxiliaryMetadataAssetReplayDoesNotRepeatAppliedDelete(t *testing.T) {
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
	now := time.Date(2026, 7, 13, 19, 0, 0, 314159265, time.UTC)
	firstCreatedAt := time.Date(2019, 1, 1, 0, 0, 0, 271828182, time.UTC)
	first := testAssetPinReference("reference-delete-recreated", "candidate-delete-recreated", "bafybeideleteold", strings.Repeat("e", 64), AssetReferenceStaged, firstCreatedAt, time.Date(2020, 1, 1, 0, 0, 0, 271828182, time.UTC))
	if err := store.UpsertAssetPinReference(ctx, first, testAssetPinEvent("event-delete-recreated-old-upsert", "reference_upsert", first, now)); err != nil {
		t.Fatalf("UpsertAssetPinReference(old) error = %v", err)
	}
	deleteEvent := testAssetPinEvent("event-delete-recreated-delete", "reference_delete", first, now.Add(time.Nanosecond))
	if err := store.DeleteExpiredAssetPinReference(ctx, first.ReferenceKey, deleteEvent); err != nil {
		t.Fatalf("DeleteExpiredAssetPinReference() error = %v", err)
	}
	recreated := testAssetPinReference(first.ReferenceKey, first.CandidateKey, "bafybeideleterecreated", strings.Repeat("f", 64), AssetReferenceApproved, now.Add(2*time.Nanosecond), time.Time{})
	recreated.GitHubIssue = 9090
	recreated.DecisionSHA256 = strings.Repeat("1", 64)
	if err := store.UpsertAssetPinReference(ctx, recreated, testAssetPinEvent("event-delete-recreated-new-upsert", "reference_upsert", recreated, recreated.CreatedAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference(recreated) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("reopen NewFlatSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertRecreatedReferenceFinalState(t, ctx, reopened, recreated)

	replayedFrames, err := reopened.auxiliaryMetadata.Replay(reopened)
	if err != nil {
		t.Fatalf("second auxiliary Replay() error = %v", err)
	}
	if replayedFrames != 3 {
		t.Fatalf("second auxiliary Replay() frames = %d, want 3", replayedFrames)
	}
	assertRecreatedReferenceFinalState(t, ctx, reopened, recreated)
	events, err := reopened.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{})
	if err != nil {
		t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("audit events after second replay = %d, want 3", len(events))
	}
}

func TestAuxiliaryMetadataAssetApplyConflictsDoNotMutateFinalState(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 19, 30, 0, 161803398, time.UTC)

	t.Run("upsert", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-apply-upsert-conflict", "candidate-apply-upsert-conflict", "bafybeiapplyupsert", strings.Repeat("2", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		event := testAssetPinEvent("event-apply-upsert-conflict", "reference_upsert", ref, now)
		if err := store.UpsertAssetPinReference(ctx, ref, event); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		mutated := ref
		mutated.CID = "bafybeiapplyupsertmutated"
		mutated.SHA256 = strings.Repeat("3", 64)
		conflict := event
		conflict.Detail = "conflicting upsert event"
		if err := store.applyAssetPinReferenceUpsert(auxiliaryAssetPinReferenceUpsert{Reference: mutated, Event: conflict}); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("applyAssetPinReferenceUpsert(conflict) error = %v, want ErrAssetPinAuditConflict", err)
		}
		got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
		if err != nil || !ok || got.CID != ref.CID || got.SHA256 != ref.SHA256 || got.State != ref.State {
			t.Fatalf("reference after conflicting upsert = %+v, %v, %v; want original", got, ok, err)
		}
	})

	t.Run("transition", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		ref := testAssetPinReference("reference-apply-transition-conflict", "candidate-apply-transition-conflict", "bafybeiapplytransition", strings.Repeat("4", 64), AssetReferenceStaged, now, now.Add(time.Hour))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-apply-transition-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference() error = %v", err)
		}
		transitionedAt := now.Add(time.Nanosecond)
		event := testAssetPinEvent("event-apply-transition-conflict", "reference_transition", ref, transitionedAt)
		event.Result = string(AssetReferenceReviewOpen)
		if err := store.TransitionAssetPinReference(ctx, AssetPinReferenceTransition{
			ReferenceKey: ref.ReferenceKey,
			FromState:    AssetReferenceStaged,
			ToState:      AssetReferenceReviewOpen,
			GitHubIssue:  6060,
			UpdatedAt:    transitionedAt,
		}, event); err != nil {
			t.Fatalf("TransitionAssetPinReference() error = %v", err)
		}
		conflict := event
		conflict.Detail = "conflicting transition event"
		if err := store.applyAssetPinReferenceTransition(auxiliaryAssetPinReferenceTransition{
			Transition: AssetPinReferenceTransition{
				ReferenceKey:   ref.ReferenceKey,
				FromState:      AssetReferenceReviewOpen,
				ToState:        AssetReferenceApproved,
				DecisionSHA256: strings.Repeat("5", 64),
				UpdatedAt:      transitionedAt.Add(time.Nanosecond),
			},
			Event: conflict,
		}); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("applyAssetPinReferenceTransition(conflict) error = %v, want ErrAssetPinAuditConflict", err)
		}
		got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
		if err != nil || !ok || got.State != AssetReferenceReviewOpen || got.GitHubIssue != 6060 || got.DecisionSHA256 != "" {
			t.Fatalf("reference after conflicting transition = %+v, %v, %v; want review_open", got, ok, err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		createdAt := time.Date(2019, 1, 1, 0, 0, 0, 141421356, time.UTC)
		ref := testAssetPinReference("reference-apply-delete-conflict", "candidate-apply-delete-conflict", "bafybeiapplydeleteold", strings.Repeat("6", 64), AssetReferenceStaged, createdAt, time.Date(2020, 1, 1, 0, 0, 0, 141421356, time.UTC))
		if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-apply-delete-upsert", "reference_upsert", ref, now)); err != nil {
			t.Fatalf("UpsertAssetPinReference(old) error = %v", err)
		}
		deleteEvent := testAssetPinEvent("event-apply-delete-conflict", "reference_delete", ref, now.Add(time.Nanosecond))
		if err := store.DeleteExpiredAssetPinReference(ctx, ref.ReferenceKey, deleteEvent); err != nil {
			t.Fatalf("DeleteExpiredAssetPinReference() error = %v", err)
		}
		recreated := ref
		recreated.CID = "bafybeiapplydeletenew"
		recreated.SHA256 = strings.Repeat("7", 64)
		recreated.State = AssetReferenceApproved
		recreated.CreatedAt = now.Add(2 * time.Nanosecond)
		recreated.UpdatedAt = recreated.CreatedAt
		recreated.ExpiresAt = time.Time{}
		if err := store.UpsertAssetPinReference(ctx, recreated, testAssetPinEvent("event-apply-delete-recreated", "reference_upsert", recreated, recreated.CreatedAt)); err != nil {
			t.Fatalf("UpsertAssetPinReference(recreated) error = %v", err)
		}
		conflict := deleteEvent
		conflict.Detail = "conflicting delete event"
		if err := store.applyAssetPinReferenceDelete(auxiliaryAssetPinReferenceDelete{ReferenceKey: ref.ReferenceKey, Event: conflict}); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("applyAssetPinReferenceDelete(conflict) error = %v, want ErrAssetPinAuditConflict", err)
		}
		got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
		if err != nil || !ok || got.CID != recreated.CID || got.SHA256 != recreated.SHA256 || got.State != recreated.State {
			t.Fatalf("reference after conflicting delete = %+v, %v, %v; want recreated", got, ok, err)
		}
	})

	t.Run("standalone audit", func(t *testing.T) {
		store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
		event := testAssetPinEvent("event-apply-standalone-conflict", "reconcile", AssetPinReference{}, now)
		event.Detail = "original standalone event"
		if err := store.AppendAssetPinAuditEvent(ctx, event); err != nil {
			t.Fatalf("AppendAssetPinAuditEvent() error = %v", err)
		}
		conflict := event
		conflict.Detail = "conflicting standalone event"
		if err := store.applyAssetPinAuditAppend(conflict); !errors.Is(err, ErrAssetPinAuditConflict) {
			t.Fatalf("applyAssetPinAuditAppend(conflict) error = %v, want ErrAssetPinAuditConflict", err)
		}
		events, err := store.ListAssetPinAuditEvents(ctx, AssetPinAuditEventQuery{EventID: event.EventID})
		if err != nil {
			t.Fatalf("ListAssetPinAuditEvents() error = %v", err)
		}
		if len(events) != 1 || !equalAssetPinAuditEvent(events[0], event) {
			t.Fatalf("standalone audit event changed after conflict: %+v", events)
		}
	})
}

func TestAuxiliaryMetadataAssetDeleteBindsAuditIdentityBeforeMutation(t *testing.T) {
	store := newAssetPinTestStore(t, filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	createdAt := time.Date(2019, 1, 1, 0, 0, 0, 123456789, time.UTC)
	ref := testAssetPinReference("reference-apply-delete-identity", "candidate-apply-delete-identity", "bafybeiapplydeleteidentity", strings.Repeat("8", 64), AssetReferenceStaged, createdAt, createdAt.Add(time.Hour))
	if err := store.UpsertAssetPinReference(ctx, ref, testAssetPinEvent("event-apply-delete-identity-upsert", "reference_upsert", ref, createdAt)); err != nil {
		t.Fatalf("UpsertAssetPinReference() error = %v", err)
	}
	event := testAssetPinEvent("event-apply-delete-identity", "reference_delete", ref, time.Date(2026, 7, 14, 3, 0, 0, 987654321, time.UTC))
	event.CID = "bafybeiapplydeleteidentityother"
	if err := store.applyAssetPinReferenceDelete(auxiliaryAssetPinReferenceDelete{ReferenceKey: ref.ReferenceKey, Event: event}); !errors.Is(err, ErrAssetPinAuditConflict) {
		t.Fatalf("applyAssetPinReferenceDelete() error = %v, want ErrAssetPinAuditConflict", err)
	}
	got, ok, err := store.FindAssetPinReference(ctx, ref.ReferenceKey)
	if err != nil || !ok || got.CID != ref.CID {
		t.Fatalf("reference after replayed delete identity conflict = %+v, %v, %v; want unchanged", got, ok, err)
	}
}

func assertRecreatedReferenceFinalState(t *testing.T, ctx context.Context, store *FlatSQLStore, want AssetPinReference) {
	t.Helper()
	got, ok, err := store.FindAssetPinReference(ctx, want.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("recreated reference lookup = %+v, %v, %v; want present", got, ok, err)
	}
	if got.CID != want.CID || got.SHA256 != want.SHA256 || got.State != want.State || got.GitHubIssue != want.GitHubIssue || got.DecisionSHA256 != want.DecisionSHA256 {
		t.Fatalf("recreated reference = %+v, want %+v", got, want)
	}
}

func assertTransitionReplayFinalState(t *testing.T, ctx context.Context, store *FlatSQLStore, referenceKey string, updatedAt time.Time, decisionSHA string) {
	t.Helper()
	got, ok, err := store.FindAssetPinReference(ctx, referenceKey)
	if err != nil || !ok {
		t.Fatalf("transitioned reference lookup = %+v, %v, %v; want present", got, ok, err)
	}
	if got.State != AssetReferenceApproved || got.GitHubIssue != 5150 || got.DecisionSHA256 != decisionSHA || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("transitioned reference = %+v, want final approved state", got)
	}
}

func assertReusedCandidateFinalState(t *testing.T, ctx context.Context, store *FlatSQLStore, deletedReferenceKey string, want AssetPinReference) {
	t.Helper()
	if _, ok, err := store.FindAssetPinReference(ctx, deletedReferenceKey); err != nil || ok {
		t.Fatalf("deleted first reference lookup = ok %v, err %v; want absent", ok, err)
	}
	got, ok, err := store.FindAssetPinReferenceByCandidateKey(ctx, want.CandidateKey)
	if err != nil || !ok {
		t.Fatalf("reused candidate lookup = %+v, %v, %v; want second reference", got, ok, err)
	}
	if got.ReferenceKey != want.ReferenceKey || got.CID != want.CID || got.State != want.State {
		t.Fatalf("reused candidate owner = %+v, want %+v", got, want)
	}
}

func appendRawAuxiliaryFrameForTest(t *testing.T, path string, event auxiliaryMetadataEvent) {
	t.Helper()
	payload, err := encodeAuxiliaryMetadataEvent(event)
	if err != nil {
		t.Fatalf("encodeAuxiliaryMetadataEvent() error = %v", err)
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:], crc32.ChecksumIEEE(payload))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open journal for raw append: %v", err)
	}
	if _, err := f.Write(header[:]); err != nil {
		_ = f.Close()
		t.Fatalf("write raw journal header: %v", err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		t.Fatalf("write raw journal payload: %v", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		t.Fatalf("sync raw journal frame: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close raw journal append: %v", err)
	}
}
