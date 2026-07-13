package storage

import (
	"context"
	"errors"
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
		ref := testAssetPinReference("reference-"+tc.key, "candidate-"+tc.key, cid, sha256, tc.state, now.Add(time.Duration(i)*time.Nanosecond), tc.expiresAt)
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
