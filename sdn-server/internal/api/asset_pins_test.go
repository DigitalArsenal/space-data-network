package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	testAssetCID          = "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e"
	testAssetAlternateCID = "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
)

type fakeAssetPinVerifier struct {
	mu     sync.Mutex
	claims assetpin.Claims
	err    error
	calls  int
	token  string
	kind   assetpin.WorkflowKind
	verify func(string, assetpin.WorkflowKind) (assetpin.Claims, error)
}

func (f *fakeAssetPinVerifier) VerifyAndConsume(_ context.Context, token string, kind assetpin.WorkflowKind) (assetpin.Claims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.token = token
	f.kind = kind
	if f.verify != nil {
		return f.verify(token, kind)
	}
	return f.claims, f.err
}

type fakeAssetPinStore struct {
	mu                 sync.Mutex
	foundRef           storage.AssetPinReference
	found              bool
	findErr            error
	upsertErr          error
	candidateFindErr   error
	findCalls          int
	candidateFindCalls int
	upserts            []storage.AssetPinReference
	transitions        []storage.AssetPinReferenceTransition
	events             []storage.AssetPinAuditEvent
	transitionErr      error
	candidateFind      func(int, string) (storage.AssetPinReference, bool, error)
	byCandidate        map[string]storage.AssetPinReference
	byReference        map[string]storage.AssetPinReference
	byEvent            map[string]storage.AssetPinAuditEvent
}

func (f *fakeAssetPinStore) FindAssetBySHA256(_ context.Context, _ string) (storage.AssetPinReference, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls++
	return f.foundRef, f.found, f.findErr
}

func (f *fakeAssetPinStore) FindAssetPinReferenceByCandidateKey(_ context.Context, candidateKey string) (storage.AssetPinReference, bool, error) {
	f.mu.Lock()
	f.candidateFindCalls++
	call := f.candidateFindCalls
	find := f.candidateFind
	if find != nil {
		f.mu.Unlock()
		return find(call, candidateKey)
	}
	defer f.mu.Unlock()
	if f.candidateFindErr != nil {
		return storage.AssetPinReference{}, false, f.candidateFindErr
	}
	ref, ok := f.byCandidate[candidateKey]
	return ref, ok, nil
}

func (f *fakeAssetPinStore) UpsertAssetPinReference(_ context.Context, ref storage.AssetPinReference, event storage.AssetPinAuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, ref)
	f.events = append(f.events, event)
	if f.upsertErr != nil {
		return f.upsertErr
	}
	if existing, ok := f.byCandidate[ref.CandidateKey]; ok && !reflect.DeepEqual(existing, ref) {
		return storage.ErrAssetPinReferenceConflict
	}
	if existing, ok := f.byReference[ref.ReferenceKey]; ok && !reflect.DeepEqual(existing, ref) {
		return storage.ErrAssetPinReferenceConflict
	}
	if existing, ok := f.byEvent[event.EventID]; ok && !reflect.DeepEqual(existing, event) {
		return storage.ErrAssetPinAuditConflict
	}
	f.byCandidate[ref.CandidateKey] = ref
	f.byReference[ref.ReferenceKey] = ref
	f.byEvent[event.EventID] = event
	return nil
}

type barrierAssetPinStore struct {
	*fakeAssetPinStore

	barrierMu          sync.Mutex
	initialReads       int
	initialReadsReady  chan struct{}
	durableTransitions int
}

func newBarrierAssetPinStore(store *fakeAssetPinStore) *barrierAssetPinStore {
	return &barrierAssetPinStore{
		fakeAssetPinStore: store,
		initialReadsReady: make(chan struct{}),
	}
}

func (s *barrierAssetPinStore) FindAssetPinReferenceByCandidateKey(ctx context.Context, candidateKey string) (storage.AssetPinReference, bool, error) {
	ref, found, err := s.fakeAssetPinStore.FindAssetPinReferenceByCandidateKey(ctx, candidateKey)

	s.barrierMu.Lock()
	wait := s.initialReads < 2
	if wait {
		s.initialReads++
		if s.initialReads == 2 {
			close(s.initialReadsReady)
		}
	}
	s.barrierMu.Unlock()
	if wait {
		<-s.initialReadsReady
	}
	return ref, found, err
}

func (s *barrierAssetPinStore) TransitionAssetPinReference(ctx context.Context, transition storage.AssetPinReferenceTransition, event storage.AssetPinAuditEvent) error {
	err := s.fakeAssetPinStore.TransitionAssetPinReference(ctx, transition, event)
	if err == nil {
		s.barrierMu.Lock()
		s.durableTransitions++
		s.barrierMu.Unlock()
	}
	return err
}

func (s *barrierAssetPinStore) durableTransitionCount() int {
	s.barrierMu.Lock()
	defer s.barrierMu.Unlock()
	return s.durableTransitions
}

type laterFirstAssetPinStore struct {
	*barrierAssetPinStore

	laterDecisionAt time.Time
	laterCommitted  chan struct{}
}

func newLaterFirstAssetPinStore(store *fakeAssetPinStore, laterDecisionAt time.Time) *laterFirstAssetPinStore {
	return &laterFirstAssetPinStore{
		barrierAssetPinStore: newBarrierAssetPinStore(store),
		laterDecisionAt:      laterDecisionAt,
		laterCommitted:       make(chan struct{}),
	}
}

func (s *laterFirstAssetPinStore) TransitionAssetPinReference(ctx context.Context, transition storage.AssetPinReferenceTransition, event storage.AssetPinAuditEvent) error {
	if transition.UpdatedAt.Before(s.laterDecisionAt) {
		<-s.laterCommitted
	}
	err := s.barrierAssetPinStore.TransitionAssetPinReference(ctx, transition, event)
	if transition.UpdatedAt.Equal(s.laterDecisionAt) {
		close(s.laterCommitted)
	}
	return err
}

func (f *fakeAssetPinStore) TransitionAssetPinReference(_ context.Context, transition storage.AssetPinReferenceTransition, event storage.AssetPinAuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transition)
	f.events = append(f.events, event)
	if f.transitionErr != nil {
		return f.transitionErr
	}
	ref, ok := f.byReference[transition.ReferenceKey]
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
	f.byReference[ref.ReferenceKey] = ref
	f.byCandidate[ref.CandidateKey] = ref
	f.byEvent[event.EventID] = event
	return nil
}

type fakeAssetPinCapacity struct {
	mu        sync.Mutex
	available uint64
	err       error
	calls     int
	path      string
}

func (f *fakeAssetPinCapacity) AvailableBytes(path string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.path = path
	return f.available, f.err
}

type fakeAssetPinner struct {
	mu             sync.Mutex
	pinned         bool
	checkErr       error
	calculatedCID  string
	calculateErr   error
	cid            string
	pinErr         error
	unpinErr       error
	calculateCalls int
	pinCalls       int
	unpinCalls     int
	checkCalls     int
	calculatePath  string
	path           string
	unpinned       string
	calculateFunc  func(context.Context, string) (string, error)
	pinFunc        func(context.Context, string) (string, error)
	checkFunc      func(context.Context, string) (bool, error)
}

func (f *fakeAssetPinner) IsAssetCIDPinned(ctx context.Context, cid string) (bool, error) {
	f.mu.Lock()
	f.checkCalls++
	callback := f.checkFunc
	pinned := f.pinned
	err := f.checkErr
	f.mu.Unlock()
	if callback != nil {
		return callback(ctx, cid)
	}
	return pinned, err
}

func (f *fakeAssetPinner) CalculateAssetGLBCID(ctx context.Context, path string) (string, error) {
	f.mu.Lock()
	f.calculateCalls++
	f.calculatePath = path
	callback := f.calculateFunc
	cidValue := f.calculatedCID
	err := f.calculateErr
	f.mu.Unlock()
	if callback != nil {
		return callback(ctx, path)
	}
	return cidValue, err
}

func (f *fakeAssetPinner) PinAssetGLB(ctx context.Context, path string) (string, error) {
	f.mu.Lock()
	f.pinCalls++
	f.path = path
	callback := f.pinFunc
	cidValue := f.cid
	err := f.pinErr
	f.mu.Unlock()
	if callback != nil {
		return callback(ctx, path)
	}
	return cidValue, err
}

func (f *fakeAssetPinner) UnpinAssetCID(_ context.Context, cid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpinCalls++
	f.unpinned = cid
	return f.unpinErr
}

type fakeAssetPinRecoveryStore struct {
	mu        sync.Mutex
	markers   map[string]assetpin.AssetPinRecoveryMarker
	calls     []string
	createErr error
	markErr   error
	loadErr   error
	removeErr error
}

func (f *fakeAssetPinRecoveryStore) CreateIntent(marker assetpin.AssetPinRecoveryMarker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create")
	if f.createErr != nil {
		return f.createErr
	}
	if _, exists := f.markers[marker.ReferenceKey]; exists {
		return assetpin.ErrAssetPinRecoveryMarkerExists
	}
	f.markers[marker.ReferenceKey] = marker
	return nil
}

func (f *fakeAssetPinRecoveryStore) MarkPinned(referenceKey, cidValue string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "mark_pinned")
	if f.markErr != nil {
		return f.markErr
	}
	marker, ok := f.markers[referenceKey]
	if !ok {
		return assetpin.ErrAssetPinRecoveryMarkerConflict
	}
	marker.Phase = assetpin.AssetPinRecoveryPinnedUncommitted
	marker.CID = cidValue
	f.markers[referenceKey] = marker
	return nil
}

func (f *fakeAssetPinRecoveryStore) Load(referenceKey string) (assetpin.AssetPinRecoveryMarker, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "load")
	if f.loadErr != nil {
		return assetpin.AssetPinRecoveryMarker{}, false, f.loadErr
	}
	marker, ok := f.markers[referenceKey]
	return marker, ok, nil
}

func (f *fakeAssetPinRecoveryStore) Remove(referenceKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "remove")
	if f.removeErr != nil {
		return f.removeErr
	}
	delete(f.markers, referenceKey)
	return nil
}

func (f *fakeAssetPinRecoveryStore) marker(referenceKey string) (assetpin.AssetPinRecoveryMarker, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	marker, ok := f.markers[referenceKey]
	return marker, ok
}

type assetPinTestRig struct {
	handler  *AssetPinHandler
	verifier *fakeAssetPinVerifier
	store    *fakeAssetPinStore
	capacity *fakeAssetPinCapacity
	pinner   *fakeAssetPinner
	recovery *fakeAssetPinRecoveryStore
	cfg      config.AssetPinConfig
	dataDir  string
	now      time.Time
}

func newAssetPinTestRig(t *testing.T, configure func(*AssetPinHandlerOptions)) *assetPinTestRig {
	t.Helper()
	cfg := config.Default().AssetPins
	cfg.MaxUploadBytes = 1024
	cfg.MinFreeBytes = 256
	now := time.Date(2026, 7, 13, 12, 30, 45, 123456789, time.UTC)
	verifier := &fakeAssetPinVerifier{claims: assetpin.Claims{
		Repository:  "DigitalArsenal/asset-models",
		Ref:         "refs/heads/main",
		WorkflowRef: "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main",
		Actor:       "review-bot",
		RunID:       "123456",
		RunAttempt:  "2",
		SHA:         strings.Repeat("c", 40),
		ExpiresAt:   now.Add(time.Hour).Unix(),
	}}
	store := &fakeAssetPinStore{
		byCandidate: make(map[string]storage.AssetPinReference),
		byReference: make(map[string]storage.AssetPinReference),
		byEvent:     make(map[string]storage.AssetPinAuditEvent),
	}
	capacity := &fakeAssetPinCapacity{available: 1 << 30}
	pinner := &fakeAssetPinner{pinned: true, calculatedCID: testAssetCID, cid: testAssetCID}
	recovery := &fakeAssetPinRecoveryStore{markers: make(map[string]assetpin.AssetPinRecoveryMarker)}
	dataDir := t.TempDir()
	options := AssetPinHandlerOptions{
		Verifier: verifier,
		Store:    store,
		Capacity: capacity,
		Pinner:   pinner,
		Recovery: recovery,
		Gate:     assetpin.NewMutationGate(),
		Config:   cfg,
		DataDir:  dataDir,
		Clock:    func() time.Time { return now },
	}
	if configure != nil {
		configure(&options)
	}
	handler, err := NewAssetPinHandler(options)
	if err != nil {
		t.Fatalf("NewAssetPinHandler() error = %v", err)
	}
	return &assetPinTestRig{
		handler: handler, verifier: verifier, store: store,
		capacity: capacity, pinner: pinner, cfg: options.Config,
		recovery: recovery, dataDir: handler.dataDir, now: now,
	}
}

func TestAssetPinMethodGatePrecedesAuthenticationAndBodyRead(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	reader := &trackingReader{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/pin", reader)
	req.Header.Set("Authorization", "Bearer good-token")
	response := serveAssetPin(rig.handler, req)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", response.Header().Get("Allow"))
	}
	if rig.verifier.calls != 0 {
		t.Fatalf("verifier calls = %d, want 0", rig.verifier.calls)
	}
	if reader.read {
		t.Fatal("wrong-method request body was read")
	}
}

func TestAssetReferenceStateReviewOpen(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	glb := testGLB([]byte("review-open"))
	candidateKey := "candidate-review-open"
	metadata := testCanonicalMetadata(glb, candidateKey)
	stored := testStoredAssetPinReference(glb, candidateKey, metadata, rig.now)
	rig.store.byCandidate[candidateKey] = stored
	rig.store.byReference[stored.ReferenceKey] = stored

	body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":"","issueNumber":42,"state":"review_open"}`, candidateKey, rig.now.Format(time.RFC3339Nano))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good-token")
	response := serveAssetPin(rig.handler, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if rig.verifier.calls != 1 || rig.verifier.kind != assetpin.WorkflowPin {
		t.Fatalf("verifier = calls %d kind %q, want one pin verification", rig.verifier.calls, rig.verifier.kind)
	}
	if len(rig.store.transitions) != 1 {
		t.Fatalf("transitions = %d, want 1", len(rig.store.transitions))
	}
	transition := rig.store.transitions[0]
	if transition.FromState != storage.AssetReferenceStaged ||
		transition.ToState != storage.AssetReferenceReviewOpen ||
		transition.GitHubIssue != 42 || transition.DecisionSHA256 != "" ||
		!transition.UpdatedAt.Equal(rig.now) || !transition.ExpiresAt.Equal(stored.ExpiresAt) {
		t.Fatalf("transition = %+v, want staged to review_open retaining expiry", transition)
	}
}

func TestAssetReferenceStateApprovedUsesDecisionWorkflowAndNullExpiry(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	glb := testGLB([]byte("approve"))
	candidateKey := "candidate-approve"
	stored := testStoredAssetPinReference(glb, candidateKey, testCanonicalMetadata(glb, candidateKey), rig.now)
	stored.State = storage.AssetReferenceReviewOpen
	stored.GitHubIssue = 77
	rig.store.byCandidate[candidateKey] = stored
	rig.store.byReference[stored.ReferenceKey] = stored
	digest := strings.Repeat("a", 64)
	body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":%q,"issueNumber":77,"state":"approved"}`, candidateKey, rig.now.Format(time.RFC3339Nano), digest)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer decision-token")
	response := serveAssetPin(rig.handler, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
	}
	if rig.verifier.calls != 1 || rig.verifier.kind != assetpin.WorkflowDecision || rig.verifier.token != "decision-token" {
		t.Fatalf("verifier = calls %d kind %q token %q, want decision verification", rig.verifier.calls, rig.verifier.kind, rig.verifier.token)
	}
	if len(rig.store.transitions) != 1 {
		t.Fatalf("transitions = %d, want 1", len(rig.store.transitions))
	}
	transition := rig.store.transitions[0]
	if transition.FromState != storage.AssetReferenceReviewOpen || transition.ToState != storage.AssetReferenceApproved ||
		transition.GitHubIssue != 77 || transition.DecisionSHA256 != digest || !transition.ExpiresAt.IsZero() {
		t.Fatalf("transition = %+v, want review_open to approved with null expiry", transition)
	}
	var responseBody struct {
		CandidateKey string                      `json:"candidateKey"`
		CID          string                      `json:"cid"`
		State        storage.AssetReferenceState `json:"state"`
		ExpiresAt    *string                     `json:"expiresAt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if responseBody.CandidateKey != candidateKey || responseBody.CID != stored.CID || responseBody.State != storage.AssetReferenceApproved || responseBody.ExpiresAt != nil {
		t.Fatalf("response = %+v, want approved with null expiry", responseBody)
	}
}

func TestAssetReferenceStateRejectedAndSupersededExpireThirtyDaysAfterDecision(t *testing.T) {
	tests := []struct {
		name        string
		from        storage.AssetReferenceState
		to          storage.AssetReferenceState
		priorDigest string
	}{
		{name: "reject review", from: storage.AssetReferenceReviewOpen, to: storage.AssetReferenceRejected},
		{name: "supersede approval", from: storage.AssetReferenceApproved, to: storage.AssetReferenceSuperseded, priorDigest: strings.Repeat("b", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			glb := testGLB([]byte(test.name))
			candidateKey := "candidate-" + strings.ReplaceAll(test.name, " ", "-")
			stored := testStoredAssetPinReference(glb, candidateKey, testCanonicalMetadata(glb, candidateKey), rig.now)
			stored.State = test.from
			stored.GitHubIssue = 88
			stored.DecisionSHA256 = test.priorDigest
			if test.from == storage.AssetReferenceApproved {
				stored.ExpiresAt = time.Time{}
			}
			rig.store.byCandidate[candidateKey] = stored
			rig.store.byReference[stored.ReferenceKey] = stored
			digest := strings.Repeat("c", 64)
			body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":%q,"issueNumber":88,"state":%q}`, candidateKey, rig.now.Format(time.RFC3339Nano), digest, test.to)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer decision-token")
			response := serveAssetPin(rig.handler, req)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body.String())
			}
			if len(rig.store.transitions) != 1 {
				t.Fatalf("transitions = %d, want 1", len(rig.store.transitions))
			}
			transition := rig.store.transitions[0]
			wantExpiry := rig.now.Add(30 * 24 * time.Hour)
			if transition.FromState != test.from || transition.ToState != test.to || transition.DecisionSHA256 != digest || !transition.ExpiresAt.Equal(wantExpiry) {
				t.Fatalf("transition = %+v, want %s to %s expiring %s", transition, test.from, test.to, wantExpiry)
			}
			var responseBody struct {
				ExpiresAt string `json:"expiresAt"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if responseBody.ExpiresAt != wantExpiry.Format(time.RFC3339Nano) {
				t.Fatalf("expiresAt = %q, want %q", responseBody.ExpiresAt, wantExpiry.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestAssetReferenceStateIdenticalSemanticRetryConsumesTokenWithoutAnotherTransition(t *testing.T) {
	tests := []struct {
		name     string
		state    storage.AssetReferenceState
		issue    int64
		digest   string
		expires  func(time.Time) time.Time
		workflow assetpin.WorkflowKind
	}{
		{name: "review opened", state: storage.AssetReferenceReviewOpen, issue: 91, expires: func(now time.Time) time.Time { return now.Add(89 * 24 * time.Hour) }, workflow: assetpin.WorkflowPin},
		{name: "rejected", state: storage.AssetReferenceRejected, issue: 92, digest: strings.Repeat("d", 64), expires: func(now time.Time) time.Time { return now.Add(-time.Hour).Add(30 * 24 * time.Hour) }, workflow: assetpin.WorkflowDecision},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			glb := testGLB([]byte(test.name))
			candidateKey := "candidate-retry-" + strings.ReplaceAll(test.name, " ", "-")
			stored := testStoredAssetPinReference(glb, candidateKey, testCanonicalMetadata(glb, candidateKey), rig.now)
			stored.State = test.state
			stored.GitHubIssue = test.issue
			stored.DecisionSHA256 = test.digest
			stored.CreatedAt = rig.now.Add(-2 * time.Hour)
			stored.UpdatedAt = rig.now.Add(-time.Hour)
			stored.ExpiresAt = test.expires(rig.now)
			rig.store.byCandidate[candidateKey] = stored
			rig.store.byReference[stored.ReferenceKey] = stored
			body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":%q,"issueNumber":%d,"state":%q}`, candidateKey, rig.now.Format(time.RFC3339Nano), test.digest, test.issue, test.state)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer fresh-retry-token")
			response := serveAssetPin(rig.handler, req)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s, want semantic retry 200", response.Code, response.Body.String())
			}
			if rig.verifier.calls != 1 || rig.verifier.kind != test.workflow {
				t.Fatalf("verifier = calls %d kind %q, want one %q verification", rig.verifier.calls, rig.verifier.kind, test.workflow)
			}
			if len(rig.store.transitions) != 0 || len(rig.store.events) != 0 {
				t.Fatalf("semantic retry wrote transitions/events = %d/%d, want 0/0", len(rig.store.transitions), len(rig.store.events))
			}
		})
	}
}

func TestAssetReferenceStateRejectsNonMonotonicOrExpiredReviewTime(t *testing.T) {
	tests := []struct {
		name      string
		decidedAt func(storage.AssetPinReference) time.Time
	}{
		{name: "before current update", decidedAt: func(ref storage.AssetPinReference) time.Time { return ref.UpdatedAt.Add(-time.Nanosecond) }},
		{name: "after staged expiry", decidedAt: func(ref storage.AssetPinReference) time.Time { return ref.ExpiresAt.Add(time.Nanosecond) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			glb := testGLB([]byte(test.name))
			candidateKey := "candidate-time-" + strings.ReplaceAll(test.name, " ", "-")
			stored := testStoredAssetPinReference(glb, candidateKey, testCanonicalMetadata(glb, candidateKey), rig.now)
			rig.store.byCandidate[candidateKey] = stored
			rig.store.byReference[stored.ReferenceKey] = stored
			body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":"","issueNumber":99,"state":"review_open"}`, candidateKey, test.decidedAt(stored).Format(time.RFC3339Nano))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer good-token")
			response := serveAssetPin(rig.handler, req)

			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d body = %s, want 409", response.Code, response.Body.String())
			}
			if len(rig.store.transitions) != 0 {
				t.Fatalf("transitions = %d, want 0", len(rig.store.transitions))
			}
		})
	}

	t.Run("decision retention overflows UnixNano", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		candidateKey := "candidate-time-overflow"
		stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceReviewOpen)
		maximum, err := time.Parse(time.RFC3339Nano, "2262-04-11T23:47:16.854775807Z")
		if err != nil {
			t.Fatalf("parse maximum UnixNano time: %v", err)
		}
		stored.CreatedAt = maximum.Add(-2 * time.Hour)
		stored.UpdatedAt = maximum.Add(-time.Hour)
		stored.ExpiresAt = maximum
		rig.store.byCandidate[candidateKey] = stored
		rig.store.byReference[stored.ReferenceKey] = stored
		response := serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceRejected, stored.GitHubIssue, strings.Repeat("b", 64), maximum)
		if response.Code != http.StatusConflict || len(rig.store.transitions) != 0 {
			t.Fatalf("overflow decision = status %d transitions %d, want 409/0", response.Code, len(rig.store.transitions))
		}
	})
}

func TestAssetReferenceStateCanonicalParserRejectsNoncanonicalAndInvalidBodies(t *testing.T) {
	canonicalTime := "2026-07-13T12:30:45.123456789Z"
	valid := `{"candidateKey":"candidate-canonical","decidedAt":"` + canonicalTime + `","decisionSha256":"","issueNumber":42,"state":"review_open"}`
	request, decidedAt, err := parseCanonicalAssetReferenceStateRequest(strings.NewReader(valid))
	if err != nil {
		t.Fatalf("parse valid canonical request: %v", err)
	}
	if request.CandidateKey != "candidate-canonical" || request.IssueNumber != 42 || request.State != "review_open" || decidedAt.Format(time.RFC3339Nano) != canonicalTime {
		t.Fatalf("parsed request = %+v at %s", request, decidedAt)
	}

	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "unknown field", raw: `{"candidateKey":"candidate-canonical","decidedAt":"` + canonicalTime + `","decisionSha256":"","extra":true,"issueNumber":42,"state":"review_open"}`},
		{name: "duplicate field", raw: `{"candidateKey":"candidate-canonical","candidateKey":"candidate-canonical","decidedAt":"` + canonicalTime + `","decisionSha256":"","issueNumber":42,"state":"review_open"}`},
		{name: "missing field", raw: `{"candidateKey":"candidate-canonical","decidedAt":"` + canonicalTime + `","issueNumber":42,"state":"review_open"}`},
		{name: "wrong field order", raw: `{"state":"review_open","issueNumber":42,"decisionSha256":"","decidedAt":"` + canonicalTime + `","candidateKey":"candidate-canonical"}`},
		{name: "leading whitespace", raw: " " + valid},
		{name: "trailing newline", raw: valid + "\n"},
		{name: "trailing value", raw: valid + `null`},
		{name: "fractional issue", raw: strings.Replace(valid, `"issueNumber":42`, `"issueNumber":42.0`, 1)},
		{name: "exponent issue", raw: strings.Replace(valid, `"issueNumber":42`, `"issueNumber":4.2e1`, 1)},
		{name: "quoted issue", raw: strings.Replace(valid, `"issueNumber":42`, `"issueNumber":"42"`, 1)},
		{name: "zero issue", raw: strings.Replace(valid, `"issueNumber":42`, `"issueNumber":0`, 1)},
		{name: "negative issue", raw: strings.Replace(valid, `"issueNumber":42`, `"issueNumber":-1`, 1)},
		{name: "candidate leading space", raw: strings.Replace(valid, `"candidateKey":"candidate-canonical"`, `"candidateKey":" candidate-canonical"`, 1)},
		{name: "candidate trailing space", raw: strings.Replace(valid, `"candidateKey":"candidate-canonical"`, `"candidateKey":"candidate-canonical "`, 1)},
		{name: "non UTC timestamp", raw: strings.Replace(valid, canonicalTime, "2026-07-13T12:30:45.123456789+00:00", 1)},
		{name: "noncanonical fractional timestamp", raw: strings.Replace(valid, canonicalTime, "2026-07-13T12:30:45.1200Z", 1)},
		{name: "timestamp outside UnixNano", raw: strings.Replace(valid, canonicalTime, "2500-01-01T00:00:00Z", 1)},
		{name: "unsupported abandoned", raw: strings.Replace(valid, `"state":"review_open"`, `"state":"abandoned"`, 1)},
		{name: "review digest", raw: strings.Replace(valid, `"decisionSha256":""`, `"decisionSha256":"`+strings.Repeat("a", 64)+`"`, 1)},
		{name: "decision missing digest", raw: strings.Replace(valid, `"state":"review_open"`, `"state":"approved"`, 1)},
		{name: "decision uppercase digest", raw: strings.Replace(strings.Replace(valid, `"state":"review_open"`, `"state":"approved"`, 1), `"decisionSha256":""`, `"decisionSha256":"`+strings.Repeat("A", 64)+`"`, 1)},
		{name: "oversized", raw: valid + strings.Repeat(" ", assetReferenceStateBodyMaxBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := parseCanonicalAssetReferenceStateRequest(strings.NewReader(test.raw)); err == nil {
				t.Fatal("parseCanonicalAssetReferenceStateRequest() accepted invalid body")
			}
		})
	}
}

func TestAssetReferenceStateMethodAuthAndBodyOrdering(t *testing.T) {
	canonicalBody := `{"candidateKey":"candidate-order","decidedAt":"2026-07-13T12:30:45.123456789Z","decisionSha256":"","issueNumber":42,"state":"review_open"}`

	t.Run("wrong method is rejected before auth and body", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		reader := &trackingReader{}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/reference-state", reader)
		response := serveAssetPin(rig.handler, req)
		if response.Code != http.StatusMethodNotAllowed || rig.verifier.calls != 0 || reader.read {
			t.Fatalf("wrong method = status %d verifier %d body-read %v", response.Code, rig.verifier.calls, reader.read)
		}
	})

	t.Run("malformed bearer is rejected before body", func(t *testing.T) {
		for _, header := range []string{"Basic no", " Bearer token", "Bearer  token", "Bearer\ttoken", "Bearer token ", "Bearer token extra"} {
			rig := newAssetPinTestRig(t, nil)
			reader := &trackingReader{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", reader)
			req.Header.Set("Authorization", header)
			response := serveAssetPin(rig.handler, req)
			if response.Code != http.StatusUnauthorized || rig.verifier.calls != 0 || reader.read {
				t.Fatalf("malformed auth %q = status %d verifier %d body-read %v", header, response.Code, rig.verifier.calls, reader.read)
			}
		}
	})

	t.Run("body is validated before token consumption", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer token")
		response := serveAssetPin(rig.handler, req)
		if response.Code != http.StatusBadRequest || rig.verifier.calls != 0 || rig.store.candidateFindCalls != 0 {
			t.Fatalf("invalid body = status %d verifier %d store %d", response.Code, rig.verifier.calls, rig.store.candidateFindCalls)
		}
	})

	for _, auth := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "invalid", err: assetpin.ErrInvalidToken, status: http.StatusUnauthorized},
		{name: "replay", err: assetpin.ErrTokenReplay, status: http.StatusUnauthorized},
		{name: "backend", err: assetpin.ErrTokenReceipt, status: http.StatusServiceUnavailable},
	} {
		t.Run(auth.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			rig.verifier.err = auth.err
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(canonicalBody))
			req.Header.Set("Authorization", "Bearer token")
			response := serveAssetPin(rig.handler, req)
			if response.Code != auth.status || rig.verifier.calls != 1 || rig.store.candidateFindCalls != 0 {
				t.Fatalf("auth failure = status %d verifier %d store %d; want %d/1/0", response.Code, rig.verifier.calls, rig.store.candidateFindCalls, auth.status)
			}
		})
	}

	t.Run("route is exact", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		for _, path := range []string{"/api/v1/assets/reference-state/", "/api/v1/assets/reference-state/suffix"} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(canonicalBody))
			req.Header.Set("Authorization", "Bearer token")
			response := serveAssetPin(rig.handler, req)
			if response.Code != http.StatusNotFound {
				t.Fatalf("path %q status = %d, want 404", path, response.Code)
			}
		}
	})
}

func TestAssetReferenceStateWorkflowCapabilitiesRemainSeparated(t *testing.T) {
	t.Run("decision token cannot pin", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		claims := rig.verifier.claims
		rig.verifier.verify = func(token string, kind assetpin.WorkflowKind) (assetpin.Claims, error) {
			if token == "decision-token" && kind == assetpin.WorkflowPin {
				return assetpin.Claims{}, assetpin.ErrInvalidToken
			}
			return claims, nil
		}
		glb := testGLB([]byte("decision-cannot-pin"))
		req := testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-decision-cannot-pin")}, {name: "file", filename: "asset.glb", data: glb}})
		req.Header.Set("Authorization", "Bearer decision-token")
		response := serveAssetPin(rig.handler, req)
		if response.Code != http.StatusUnauthorized || rig.verifier.kind != assetpin.WorkflowPin || rig.store.candidateFindCalls != 0 || rig.pinner.pinCalls != 0 {
			t.Fatalf("decision pin = status %d kind %q store %d pins %d", response.Code, rig.verifier.kind, rig.store.candidateFindCalls, rig.pinner.pinCalls)
		}
	})

	t.Run("upload token cannot decide", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		claims := rig.verifier.claims
		rig.verifier.verify = func(token string, kind assetpin.WorkflowKind) (assetpin.Claims, error) {
			if token == "upload-token" && kind == assetpin.WorkflowDecision {
				return assetpin.Claims{}, assetpin.ErrInvalidToken
			}
			return claims, nil
		}
		body := `{"candidateKey":"candidate-upload-cannot-decide","decidedAt":"2026-07-13T12:30:45.123456789Z","decisionSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","issueNumber":42,"state":"approved"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer upload-token")
		response := serveAssetPin(rig.handler, req)
		if response.Code != http.StatusUnauthorized || rig.verifier.kind != assetpin.WorkflowDecision || rig.store.candidateFindCalls != 0 {
			t.Fatalf("upload decision = status %d kind %q store %d", response.Code, rig.verifier.kind, rig.store.candidateFindCalls)
		}
	})
}

func TestAssetReferenceStateAPILifecycleMatrix(t *testing.T) {
	states := []storage.AssetReferenceState{
		storage.AssetReferenceStaged,
		storage.AssetReferenceReviewOpen,
		storage.AssetReferenceApproved,
		storage.AssetReferenceRejected,
		storage.AssetReferenceSuperseded,
		storage.AssetReferenceAbandoned,
	}
	targets := []storage.AssetReferenceState{
		storage.AssetReferenceReviewOpen,
		storage.AssetReferenceApproved,
		storage.AssetReferenceRejected,
		storage.AssetReferenceSuperseded,
	}
	allowed := map[[2]storage.AssetReferenceState]bool{
		{storage.AssetReferenceStaged, storage.AssetReferenceReviewOpen}:   true,
		{storage.AssetReferenceReviewOpen, storage.AssetReferenceApproved}: true,
		{storage.AssetReferenceReviewOpen, storage.AssetReferenceRejected}: true,
		{storage.AssetReferenceApproved, storage.AssetReferenceSuperseded}: true,
	}
	for _, from := range states {
		for _, target := range targets {
			name := string(from) + "_to_" + string(target)
			t.Run(name, func(t *testing.T) {
				rig := newAssetPinTestRig(t, nil)
				candidateKey := "candidate-api-matrix-" + name
				stored := testAPIAssetReferenceForState(rig, candidateKey, from)
				rig.store.byCandidate[candidateKey] = stored
				rig.store.byReference[stored.ReferenceKey] = stored
				issue := stored.GitHubIssue
				if issue == 0 {
					issue = 4242
				}
				digest := strings.Repeat("b", 64)
				if target == storage.AssetReferenceReviewOpen {
					digest = ""
				}
				if from == target {
					issue = stored.GitHubIssue
					digest = stored.DecisionSHA256
				}
				body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":%q,"issueNumber":%d,"state":%q}`, candidateKey, rig.now.Format(time.RFC3339Nano), digest, issue, target)
				req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer matrix-token")
				response := serveAssetPin(rig.handler, req)

				wantOK := from == target || allowed[[2]storage.AssetReferenceState{from, target}]
				wantStatus := http.StatusConflict
				if wantOK {
					wantStatus = http.StatusOK
				}
				if response.Code != wantStatus {
					t.Fatalf("status = %d body = %s, want %d", response.Code, response.Body.String(), wantStatus)
				}
				wantTransitions := 0
				if allowed[[2]storage.AssetReferenceState{from, target}] {
					wantTransitions = 1
				}
				if len(rig.store.transitions) != wantTransitions {
					t.Fatalf("transitions = %d, want %d", len(rig.store.transitions), wantTransitions)
				}
			})
		}
	}
}

func testAPIAssetReferenceForState(rig *assetPinTestRig, candidateKey string, state storage.AssetReferenceState) storage.AssetPinReference {
	glb := testGLB([]byte(candidateKey))
	ref := testStoredAssetPinReference(glb, candidateKey, testCanonicalMetadata(glb, candidateKey), rig.now)
	ref.State = state
	switch state {
	case storage.AssetReferenceStaged:
		ref.GitHubIssue = 0
	case storage.AssetReferenceReviewOpen:
		ref.GitHubIssue = 4242
	case storage.AssetReferenceApproved:
		ref.GitHubIssue = 4242
		ref.DecisionSHA256 = strings.Repeat("a", 64)
		ref.ExpiresAt = time.Time{}
	case storage.AssetReferenceRejected, storage.AssetReferenceSuperseded:
		ref.GitHubIssue = 4242
		ref.DecisionSHA256 = strings.Repeat("a", 64)
		ref.CreatedAt = rig.now.Add(-2 * time.Hour)
		ref.UpdatedAt = rig.now.Add(-time.Hour)
		ref.ExpiresAt = ref.UpdatedAt.Add(30 * 24 * time.Hour)
	case storage.AssetReferenceAbandoned:
		ref.GitHubIssue = 0
		ref.CreatedAt = rig.now.Add(-2 * time.Hour)
		ref.UpdatedAt = rig.now.Add(-time.Hour)
		ref.ExpiresAt = rig.now.Add(time.Hour)
	}
	return ref
}

func TestAssetReferenceStateAuditAndResponseAreBoundAndMinimal(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	candidateKey := "candidate-audit-state"
	stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
	rig.store.byCandidate[candidateKey] = stored
	rig.store.byReference[stored.ReferenceKey] = stored
	body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":"","issueNumber":313,"state":"review_open"}`, candidateKey, rig.now.Format(time.RFC3339Nano))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer fresh-audit-token")
	response := serveAssetPin(rig.handler, req)

	if response.Code != http.StatusOK || len(rig.store.events) != 1 {
		t.Fatalf("response status/events = %d/%d body %s, want 200/1", response.Code, len(rig.store.events), response.Body.String())
	}
	event := rig.store.events[0]
	wantEventIDInput := "asset-reference-state:v1\n" + fmt.Sprintf("%d:%s\n%s\n%s\n%d\n", len(candidateKey), candidateKey, storage.AssetReferenceStaged, storage.AssetReferenceReviewOpen, 313)
	if event.EventID != stableTestID(wantEventIDInput) || event.Kind != "asset_reference_state" || event.Result != "review_open" ||
		event.TokenDigest != stableTestID("fresh-audit-token") || event.Repository != rig.verifier.claims.Repository ||
		event.Ref != rig.verifier.claims.Ref || event.WorkflowRef != rig.verifier.claims.WorkflowRef || event.Actor != rig.verifier.claims.Actor ||
		event.WorkflowRunID != rig.verifier.claims.RunID || event.RunAttempt != rig.verifier.claims.RunAttempt || event.CommitSHA != rig.verifier.claims.SHA ||
		event.CandidateKey != stored.CandidateKey || event.ReferenceKey != stored.ReferenceKey || event.CID != stored.CID || event.SHA256 != stored.SHA256 ||
		event.ByteCount != stored.ByteCount || event.Detail != `{"decisionSha256":"","issueNumber":313,"state":"review_open"}` || !event.OccurredAt.Equal(rig.now) {
		t.Fatalf("audit event = %+v, want fully bound review transition", event)
	}
	if strings.Contains(fmt.Sprintf("%+v", event), "fresh-audit-token") {
		t.Fatal("audit event persisted the raw bearer token")
	}
	var responseObject map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &responseObject); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(responseObject) != 4 || responseObject["candidateKey"] != candidateKey || responseObject["cid"] != stored.CID || responseObject["state"] != "review_open" || responseObject["expiresAt"] != stored.ExpiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("response object = %#v, want exactly candidateKey/cid/state/expiresAt", responseObject)
	}
}

func TestAssetReferenceStateConflictsAndStoreFailuresMapToBoundedStatuses(t *testing.T) {
	t.Run("missing candidate", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		response := serveAssetReferenceStateTestRequest(rig, "missing-candidate", storage.AssetReferenceReviewOpen, 42, "", rig.now)
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d body = %s, want 404", response.Code, response.Body.String())
		}
	})

	t.Run("lookup backend", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.store.candidateFindErr = errors.New("database offline")
		response := serveAssetReferenceStateTestRequest(rig, "candidate-store-error", storage.AssetReferenceReviewOpen, 42, "", rig.now)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d body = %s, want 503", response.Code, response.Body.String())
		}
	})

	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "state conflict", err: storage.ErrAssetPinReferenceConflict, status: http.StatusConflict},
		{name: "audit conflict", err: storage.ErrAssetPinAuditConflict, status: http.StatusConflict},
		{name: "removed concurrently", err: storage.ErrAssetPinReferenceNotFound, status: http.StatusNotFound},
		{name: "recovery required", err: storage.ErrAssetPinLedgerRecoveryRequired, status: http.StatusServiceUnavailable},
		{name: "backend", err: errors.New("database offline"), status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			candidateKey := "candidate-store-" + strings.ReplaceAll(test.name, " ", "-")
			stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
			rig.store.byCandidate[candidateKey] = stored
			rig.store.byReference[stored.ReferenceKey] = stored
			rig.store.transitionErr = test.err
			response := serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceReviewOpen, 42, "", rig.now)
			if response.Code != test.status {
				t.Fatalf("status = %d body = %s, want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}

	t.Run("issue mismatch", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		candidateKey := "candidate-issue-mismatch"
		stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceReviewOpen)
		rig.store.byCandidate[candidateKey] = stored
		rig.store.byReference[stored.ReferenceKey] = stored
		response := serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceApproved, stored.GitHubIssue+1, strings.Repeat("b", 64), rig.now)
		if response.Code != http.StatusConflict || len(rig.store.transitions) != 0 {
			t.Fatalf("issue mismatch = status %d transitions %d, want 409/0", response.Code, len(rig.store.transitions))
		}
	})

	t.Run("same target conflicting digest", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		candidateKey := "candidate-digest-conflict"
		stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceApproved)
		rig.store.byCandidate[candidateKey] = stored
		rig.store.byReference[stored.ReferenceKey] = stored
		response := serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceApproved, stored.GitHubIssue, strings.Repeat("b", 64), rig.now)
		if response.Code != http.StatusConflict || len(rig.store.transitions) != 0 {
			t.Fatalf("digest conflict = status %d transitions %d, want 409/0", response.Code, len(rig.store.transitions))
		}
	})

	t.Run("corrupt stored lifecycle", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		candidateKey := "candidate-corrupt-state"
		stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceApproved)
		stored.DecisionSHA256 = ""
		rig.store.byCandidate[candidateKey] = stored
		rig.store.byReference[stored.ReferenceKey] = stored
		response := serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceSuperseded, stored.GitHubIssue, strings.Repeat("b", 64), rig.now)
		if response.Code != http.StatusServiceUnavailable || len(rig.store.transitions) != 0 {
			t.Fatalf("corrupt state = status %d transitions %d, want 503/0", response.Code, len(rig.store.transitions))
		}
	})
}

func TestAssetReferenceStateConflictRereadOnlyAcceptsExactPersistedTarget(t *testing.T) {
	tests := []struct {
		name          string
		transitionErr error
		reread        func(*assetPinTestRig, string) (storage.AssetPinReference, bool, error)
		status        int
	}{
		{
			name:          "reference conflict exact target",
			transitionErr: storage.ErrAssetPinReferenceConflict,
			reread: func(rig *assetPinTestRig, candidateKey string) (storage.AssetPinReference, bool, error) {
				ref := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceReviewOpen)
				ref.GitHubIssue = 42
				return ref, true, nil
			},
			status: http.StatusOK,
		},
		{
			name:          "audit conflict exact target",
			transitionErr: storage.ErrAssetPinAuditConflict,
			reread: func(rig *assetPinTestRig, candidateKey string) (storage.AssetPinReference, bool, error) {
				ref := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceReviewOpen)
				ref.GitHubIssue = 42
				return ref, true, nil
			},
			status: http.StatusOK,
		},
		{
			name:          "reread backend",
			transitionErr: storage.ErrAssetPinReferenceConflict,
			reread: func(_ *assetPinTestRig, _ string) (storage.AssetPinReference, bool, error) {
				return storage.AssetPinReference{}, false, errors.New("database offline")
			},
			status: http.StatusServiceUnavailable,
		},
		{
			name:          "reread missing",
			transitionErr: storage.ErrAssetPinAuditConflict,
			reread: func(_ *assetPinTestRig, _ string) (storage.AssetPinReference, bool, error) {
				return storage.AssetPinReference{}, false, nil
			},
			status: http.StatusConflict,
		},
		{
			name:          "reread corrupt exact target",
			transitionErr: storage.ErrAssetPinReferenceConflict,
			reread: func(rig *assetPinTestRig, candidateKey string) (storage.AssetPinReference, bool, error) {
				ref := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceReviewOpen)
				ref.GitHubIssue = 42
				ref.CID = ""
				return ref, true, nil
			},
			status: http.StatusServiceUnavailable,
		},
		{
			name:          "reread conflicting state",
			transitionErr: storage.ErrAssetPinReferenceConflict,
			reread: func(rig *assetPinTestRig, candidateKey string) (storage.AssetPinReference, bool, error) {
				return testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceApproved), true, nil
			},
			status: http.StatusConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			candidateKey := "candidate-conflict-reread-" + strings.ReplaceAll(test.name, " ", "-")
			stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
			rig.store.byCandidate[candidateKey] = stored
			rig.store.byReference[stored.ReferenceKey] = stored
			rig.store.transitionErr = test.transitionErr
			rig.store.candidateFind = func(call int, key string) (storage.AssetPinReference, bool, error) {
				if call == 1 {
					return stored, true, nil
				}
				return test.reread(rig, key)
			}

			response := serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceReviewOpen, 42, "", rig.now)
			if response.Code != test.status {
				t.Fatalf("status = %d body = %s, want %d", response.Code, response.Body.String(), test.status)
			}
			if rig.store.candidateFindCalls != 2 {
				t.Fatalf("candidate reads = %d, want initial read plus conflict reread", rig.store.candidateFindCalls)
			}
		})
	}
}

func serveAssetReferenceStateTestRequest(rig *assetPinTestRig, candidateKey string, state storage.AssetReferenceState, issue int64, digest string, decidedAt time.Time) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":%q,"issueNumber":%d,"state":%q}`, candidateKey, decidedAt.Format(time.RFC3339Nano), digest, issue, state)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good-token")
	return serveAssetPin(rig.handler, req)
}

func TestAssetReferenceStateConcurrentSemanticRetryWritesOneTransition(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	candidateKey := "candidate-concurrent-state"
	stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
	rig.store.byCandidate[candidateKey] = stored
	rig.store.byReference[stored.ReferenceKey] = stored
	body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":"","issueNumber":5150,"state":"review_open"}`, candidateKey, rig.now.Format(time.RFC3339Nano))

	responses := make(chan *httptest.ResponseRecorder, 2)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < 2; i++ {
		go func(index int) {
			start.Wait()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
			req.Header.Set("Authorization", fmt.Sprintf("Bearer fresh-token-%d", index))
			responses <- serveAssetPin(rig.handler, req)
		}(i)
	}
	start.Done()
	for i := 0; i < 2; i++ {
		response := <-responses
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent response status = %d body = %s, want 200", response.Code, response.Body.String())
		}
	}
	if rig.verifier.calls != 2 || len(rig.store.transitions) != 1 || len(rig.store.events) != 1 {
		t.Fatalf("concurrent retry = verifier %d transitions %d events %d, want 2/1/1", rig.verifier.calls, len(rig.store.transitions), len(rig.store.events))
	}
}

func TestAssetReferenceStateCrossHandlerIdenticalTimesWriteOneDurableTransition(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	sharedStore := newBarrierAssetPinStore(rig.store)
	rig.handler.store = sharedStore
	secondHandler, err := NewAssetPinHandler(AssetPinHandlerOptions{
		Verifier: rig.verifier,
		Store:    sharedStore,
		Capacity: rig.capacity,
		Pinner:   rig.pinner,
		Recovery: rig.recovery,
		// Separate gates model independent daemon processes and continue to
		// exercise the store's durable cross-process conflict handling.
		Gate:    assetpin.NewMutationGate(),
		Config:  rig.cfg,
		DataDir: rig.dataDir,
		Clock:   func() time.Time { return rig.now },
	})
	if err != nil {
		t.Fatalf("NewAssetPinHandler(second handler) error = %v", err)
	}

	candidateKey := "candidate-cross-handler-state"
	stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
	rig.store.byCandidate[candidateKey] = stored
	rig.store.byReference[stored.ReferenceKey] = stored
	body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":"","issueNumber":6160,"state":"review_open"}`, candidateKey, rig.now.Format(time.RFC3339Nano))

	responses := make(chan *httptest.ResponseRecorder, 2)
	for index, handler := range []*AssetPinHandler{rig.handler, secondHandler} {
		go func(index int, handler *AssetPinHandler) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
			req.Header.Set("Authorization", fmt.Sprintf("Bearer cross-handler-token-%d", index))
			responses <- serveAssetPin(handler, req)
		}(index, handler)
	}
	for i := 0; i < 2; i++ {
		response := <-responses
		if response.Code != http.StatusOK {
			t.Fatalf("cross-handler response status = %d body = %s, want 200", response.Code, response.Body.String())
		}
	}

	rig.store.mu.Lock()
	durableAudits := len(rig.store.byEvent)
	persisted := rig.store.byCandidate[candidateKey]
	rig.store.mu.Unlock()
	if sharedStore.durableTransitionCount() != 1 || durableAudits != 1 {
		t.Fatalf("durable transitions/audits = %d/%d, want 1/1", sharedStore.durableTransitionCount(), durableAudits)
	}
	if persisted.State != storage.AssetReferenceReviewOpen || persisted.GitHubIssue != 6160 || persisted.DecisionSHA256 != "" {
		t.Fatalf("persisted reference = %+v, want exact review_open target", persisted)
	}
}

func TestAssetReferenceStateCrossHandlerEarlierDecisionConflictsAfterLaterDecisionWins(t *testing.T) {
	rig := newAssetPinTestRig(t, nil)
	earlierDecisionAt := rig.now.Add(time.Minute)
	laterDecisionAt := rig.now.Add(2 * time.Minute)
	sharedStore := newLaterFirstAssetPinStore(rig.store, laterDecisionAt)
	rig.handler.store = sharedStore
	secondHandler, err := NewAssetPinHandler(AssetPinHandlerOptions{
		Verifier: rig.verifier,
		Store:    sharedStore,
		Capacity: rig.capacity,
		Pinner:   rig.pinner,
		Recovery: rig.recovery,
		// Separate gates model independent daemon processes and continue to
		// exercise the store's durable cross-process ordering guarantees.
		Gate:    assetpin.NewMutationGate(),
		Config:  rig.cfg,
		DataDir: rig.dataDir,
		Clock:   func() time.Time { return rig.now },
	})
	if err != nil {
		t.Fatalf("NewAssetPinHandler(second handler) error = %v", err)
	}

	candidateKey := "candidate-cross-handler-monotonic-time"
	stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
	rig.store.byCandidate[candidateKey] = stored
	rig.store.byReference[stored.ReferenceKey] = stored

	type result struct {
		name     string
		response *httptest.ResponseRecorder
	}
	type decisionRequest struct {
		name      string
		handler   *AssetPinHandler
		decidedAt time.Time
	}
	responses := make(chan result, 2)
	requests := []decisionRequest{
		{name: "earlier", handler: rig.handler, decidedAt: earlierDecisionAt},
		{name: "later", handler: secondHandler, decidedAt: laterDecisionAt},
	}
	for _, request := range requests {
		go func(request decisionRequest) {
			body := fmt.Sprintf(`{"candidateKey":%q,"decidedAt":%q,"decisionSha256":"","issueNumber":7170,"state":"review_open"}`, candidateKey, request.decidedAt.Format(time.RFC3339Nano))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer cross-handler-"+request.name+"-token")
			responses <- result{name: request.name, response: serveAssetPin(request.handler, req)}
		}(request)
	}
	statuses := make(map[string]int, 2)
	for i := 0; i < 2; i++ {
		response := <-responses
		statuses[response.name] = response.response.Code
	}
	if statuses["later"] != http.StatusOK || statuses["earlier"] != http.StatusConflict {
		t.Fatalf("cross-handler statuses = %#v, want later 200 and earlier 409", statuses)
	}

	rig.store.mu.Lock()
	durableAudits := len(rig.store.byEvent)
	persisted := rig.store.byCandidate[candidateKey]
	rig.store.mu.Unlock()
	if sharedStore.durableTransitionCount() != 1 || durableAudits != 1 {
		t.Fatalf("durable transitions/audits = %d/%d, want 1/1", sharedStore.durableTransitionCount(), durableAudits)
	}
	if !persisted.UpdatedAt.Equal(laterDecisionAt) {
		t.Fatalf("persisted updatedAt = %s, want later decision %s", persisted.UpdatedAt, laterDecisionAt)
	}
}

func TestAssetPinAuthenticationFailuresAreGenericAndDoNotReadBody(t *testing.T) {
	tests := []struct {
		name   string
		header string
		err    error
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "invalid", header: "Bearer bad", err: assetpin.ErrInvalidToken, status: http.StatusUnauthorized},
		{name: "replayed", header: "Bearer replayed", err: assetpin.ErrTokenReplay, status: http.StatusUnauthorized},
		{name: "receipt backend", header: "Bearer good", err: assetpin.ErrTokenReceipt, status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			rig.verifier.err = test.err
			reader := &trackingReader{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/pin", reader)
			if test.header != "" {
				req.Header.Set("Authorization", test.header)
			}
			response := serveAssetPin(rig.handler, req)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if reader.read {
				t.Fatal("authentication failure read request body")
			}
			if test.status == http.StatusUnauthorized && response.Body.String() != "{\"error\":{\"message\":\"unauthorized\"}}\n" {
				t.Fatalf("body = %q, want generic unauthorized", response.Body.String())
			}
		})
	}
}

func TestAssetPinRejectsMalformedMultipartShape(t *testing.T) {
	glb := testGLB([]byte("shape"))
	metadata := testCanonicalMetadata(glb, "candidate-shape")
	tests := []struct {
		name        string
		parts       []testMultipartPart
		contentType string
		wantStatus  int
	}{
		{name: "missing content type", parts: nil, contentType: "application/octet-stream", wantStatus: http.StatusBadRequest},
		{name: "missing metadata", parts: []testMultipartPart{{name: "file", filename: "asset.glb", data: glb}}, wantStatus: http.StatusBadRequest},
		{name: "missing file", parts: []testMultipartPart{{name: "metadata", data: metadata}}, wantStatus: http.StatusBadRequest},
		{name: "duplicate metadata", parts: []testMultipartPart{{name: "metadata", data: metadata}, {name: "metadata", data: metadata}, {name: "file", filename: "asset.glb", data: glb}}, wantStatus: http.StatusBadRequest},
		{name: "duplicate file", parts: []testMultipartPart{{name: "metadata", data: metadata}, {name: "file", filename: "one.glb", data: glb}, {name: "file", filename: "two.glb", data: glb}}, wantStatus: http.StatusBadRequest},
		{name: "unknown part", parts: []testMultipartPart{{name: "metadata", data: metadata}, {name: "extra", data: []byte("no")}, {name: "file", filename: "asset.glb", data: glb}}, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			var req *http.Request
			if test.contentType != "" {
				req = authorizedPinRequest(bytes.NewReader([]byte("not multipart")), test.contentType)
			} else {
				req = testMultipartRequest(t, test.parts)
			}
			response := serveAssetPin(rig.handler, req)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			assertNoAssetPinTempFiles(t, rig.dataDir)
		})
	}
}

func TestAssetPinEnforcesMetadataFileAndWholeBodyLimits(t *testing.T) {
	baseGLB := testGLB([]byte("limits"))
	oversizedMetadata := []byte(`{"attribution":"` + strings.Repeat("x", 65*1024) + `","candidateKey":"candidate-large-metadata","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + testSHA256(baseGLB) + `","sourceUrl":"https://example.test/large.glb"}`)

	t.Run("metadata above 64 KiB", func(t *testing.T) {
		rig := newAssetPinTestRig(t, func(options *AssetPinHandlerOptions) { options.Config.MaxUploadBytes = 100_000 })
		response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: oversizedMetadata}, {name: "file", filename: "asset.glb", data: baseGLB}}))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})

	t.Run("configured file cap", func(t *testing.T) {
		rig := newAssetPinTestRig(t, func(options *AssetPinHandlerOptions) { options.Config.MaxUploadBytes = 32 })
		glb := testGLB(bytes.Repeat([]byte{'x'}, 32))
		response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-file-cap")}, {name: "file", filename: "asset.glb", data: glb}}))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})

	t.Run("hard ten million byte cap", func(t *testing.T) {
		rig := newAssetPinTestRig(t, func(options *AssetPinHandlerOptions) { options.Config.MaxUploadBytes = 20_000_000 })
		glb := testGLB(bytes.Repeat([]byte{'z'}, 10_000_001-12))
		response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-hard-cap")}, {name: "file", filename: "asset.glb", data: glb}}))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})

	t.Run("whole body cap", func(t *testing.T) {
		rig := newAssetPinTestRig(t, func(options *AssetPinHandlerOptions) { options.Config.MaxUploadBytes = 32 })
		boundary := "asset-boundary"
		raw := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"metadata\"\r\nX-Pad: " + strings.Repeat("p", (1<<20)+1024) + "\r\n\r\n{}\r\n--" + boundary + "--\r\n"
		response := serveAssetPin(rig.handler, authorizedPinRequest(strings.NewReader(raw), "multipart/form-data; boundary="+boundary))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})
}

func TestAssetPinRejectsMetadataAndGLBIntegrityFailures(t *testing.T) {
	validGLB := testGLB([]byte("integrity"))
	wrongMagic := append([]byte(nil), validGLB...)
	copy(wrongMagic[:4], []byte("nope"))
	wrongVersion := append([]byte(nil), validGLB...)
	binary.LittleEndian.PutUint32(wrongVersion[4:8], 1)
	wrongLength := append([]byte(nil), validGLB...)
	binary.LittleEndian.PutUint32(wrongLength[8:12], uint32(len(wrongLength)+1))
	tests := []struct {
		name       string
		glb        []byte
		metadata   func([]byte) []byte
		wantStatus int
	}{
		{name: "noncanonical metadata", glb: validGLB, metadata: func(glb []byte) []byte {
			return append([]byte(" "), testCanonicalMetadata(glb, "candidate-noncanonical")...)
		}, wantStatus: http.StatusBadRequest},
		{name: "short header", glb: []byte("glTF"), metadata: func(glb []byte) []byte { return testCanonicalMetadata(glb, "candidate-short") }, wantStatus: http.StatusUnprocessableEntity},
		{name: "wrong magic", glb: wrongMagic, metadata: func(glb []byte) []byte { return testCanonicalMetadata(glb, "candidate-magic") }, wantStatus: http.StatusUnprocessableEntity},
		{name: "wrong version", glb: wrongVersion, metadata: func(glb []byte) []byte { return testCanonicalMetadata(glb, "candidate-version") }, wantStatus: http.StatusUnprocessableEntity},
		{name: "declared length mismatch", glb: wrongLength, metadata: func(glb []byte) []byte { return testCanonicalMetadata(glb, "candidate-length") }, wantStatus: http.StatusUnprocessableEntity},
		{name: "SHA mismatch", glb: validGLB, metadata: func(glb []byte) []byte {
			return bytes.Replace(testCanonicalMetadata(glb, "candidate-sha"), []byte(testSHA256(glb)), []byte(strings.Repeat("d", 64)), 1)
		}, wantStatus: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: test.metadata(test.glb)}, {name: "file", filename: "asset.glb", data: test.glb}}))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if rig.store.findCalls != 0 || rig.pinner.pinCalls != 0 {
				t.Fatalf("integrity failure reached store/pinner: find=%d pin=%d", rig.store.findCalls, rig.pinner.pinCalls)
			}
			assertNoAssetPinTempFiles(t, rig.dataDir)
		})
	}
}

func TestAssetPinCapacityChecksAreOverflowSafeAndPrecedeKubo(t *testing.T) {
	glb := testGLB([]byte("capacity"))
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-capacity")}, {name: "file", filename: "asset.glb", data: glb}}

	t.Run("file exceeds available bytes", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.capacity.available = uint64(len(glb) - 1)
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusInsufficientStorage {
			t.Fatalf("status = %d, want 507; body=%s", response.Code, response.Body.String())
		}
		if rig.pinner.pinCalls != 0 {
			t.Fatalf("pin calls = %d, want 0", rig.pinner.pinCalls)
		}
		if rig.capacity.path != rig.cfg.KuboRepoPath {
			t.Fatalf("capacity path = %q, want %q", rig.capacity.path, rig.cfg.KuboRepoPath)
		}
	})

	t.Run("free-space floor", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.capacity.available = uint64(len(glb)) + uint64(rig.cfg.EffectiveMinFreeBytes()) - 1
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusInsufficientStorage {
			t.Fatalf("status = %d, want 507; body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("statfs failure", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.capacity.err = errors.New("statfs unavailable")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
		}
		if rig.pinner.pinCalls != 0 {
			t.Fatalf("pin calls = %d, want 0", rig.pinner.pinCalls)
		}
	})

	t.Run("maximum uint64 capacity", func(t *testing.T) {
		rig := newAssetPinTestRig(t, func(options *AssetPinHandlerOptions) { options.Config.MinFreeBytes = math.MaxInt64 })
		rig.capacity.available = math.MaxUint64
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
	})
}

func TestAssetPinKuboAndLedgerFailureCleanup(t *testing.T) {
	glb := testGLB([]byte("cleanup"))
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-cleanup")}, {name: "file", filename: "asset.glb", data: glb}}

	t.Run("Kubo pin call failure", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		const backendError = "kubo socket failed at /private/backend.sock"
		rig.pinner.pinErr = errors.New(backendError)
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		assertAssetPinKuboUnavailable(t, response, backendError)
		if rig.pinner.unpinCalls != 0 || len(rig.store.upserts) != 0 {
			t.Fatalf("Kubo failure unpin/upsert = %d/%d, want 0/0", rig.pinner.unpinCalls, len(rig.store.upserts))
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})

	t.Run("ordinary ledger failure unpins newly pinned CID", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.store.upsertErr = errors.New("ledger failed")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
		}
		if rig.pinner.unpinCalls != 1 || rig.pinner.unpinned != testAssetCID {
			t.Fatalf("unpin = %d %q, want one %q", rig.pinner.unpinCalls, rig.pinner.unpinned, testAssetCID)
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})

	t.Run("recovery-required ledger failure leaves pin", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.store.upsertErr = fmt.Errorf("commit uncertain: %w", storage.ErrAssetPinLedgerRecoveryRequired)
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
		}
		if rig.pinner.unpinCalls != 0 {
			t.Fatalf("unpin calls = %d, want 0 when durable journal may replay", rig.pinner.unpinCalls)
		}
		assertNoAssetPinTempFiles(t, rig.dataDir)
	})
}

func TestAssetPinPlansDeterministicCIDBeforeMarkerAndPin(t *testing.T) {
	glb := testGLB([]byte("plan-before-pin"))
	candidateKey := "candidate-plan-before-pin"
	referenceKey := stableTestID("asset-pin-reference:v1\n" + candidateKey)
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, candidateKey)}, {name: "file", filename: "asset.glb", data: glb}}
	rig := newAssetPinTestRig(t, nil)
	rig.pinner.calculateFunc = func(_ context.Context, path string) (string, error) {
		if rig.capacity.calls != 1 {
			t.Fatalf("capacity calls before CID calculation = %d, want 1", rig.capacity.calls)
		}
		if _, exists := rig.recovery.marker(referenceKey); exists {
			t.Fatal("recovery marker existed before side-effect-free CID calculation")
		}
		if rig.pinner.pinCalls != 0 {
			t.Fatalf("pin calls before CID calculation = %d, want 0", rig.pinner.pinCalls)
		}
		if path == "" {
			t.Fatal("CID calculation received an empty staged path")
		}
		return testAssetCID, nil
	}
	rig.pinner.pinFunc = func(_ context.Context, path string) (string, error) {
		marker, exists := rig.recovery.marker(referenceKey)
		if !exists || marker.Phase != assetpin.AssetPinRecoveryIntent || marker.ExpectedCID != testAssetCID || marker.CID != "" {
			t.Fatalf("intent before pin = %+v/%v", marker, exists)
		}
		if rig.pinner.calculateCalls != 1 || path != rig.pinner.calculatePath {
			t.Fatalf("calculate calls/path before pin = %d/%q; pin path=%q", rig.pinner.calculateCalls, rig.pinner.calculatePath, path)
		}
		return testAssetCID, nil
	}
	response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	if rig.pinner.calculateCalls != 1 || rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 1 {
		t.Fatalf("calculate/pin/upsert = %d/%d/%d, want 1/1/1", rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.store.upserts))
	}
}

func TestAssetPinCIDPlanningFailureCreatesNoMarkerOrPin(t *testing.T) {
	glb := testGLB([]byte("plan-failure"))
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-plan-failure")}, {name: "file", filename: "asset.glb", data: glb}}
	tests := []struct {
		name       string
		cid        string
		err        error
		wantStatus int
	}{
		{name: "backend failure", err: errors.New("only-hash unavailable with backend detail"), wantStatus: http.StatusServiceUnavailable},
		{name: "noncanonical response", cid: "not-a-cid", wantStatus: http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			rig.pinner.calculatedCID = test.cid
			rig.pinner.calculateErr = test.err
			response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
			if test.err != nil {
				assertAssetPinKuboUnavailable(t, response, test.err.Error())
			} else if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if rig.pinner.calculateCalls != 1 || rig.pinner.pinCalls != 0 || len(rig.recovery.markers) != 0 || len(rig.store.upserts) != 0 {
				t.Fatalf("calculate/pin/markers/upserts = %d/%d/%d/%d, want 1/0/0/0", rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.recovery.markers), len(rig.store.upserts))
			}
		})
	}
}

func TestAssetPinRecoveryMarkerOutcomeTable(t *testing.T) {
	newRequest := func(t *testing.T, candidateKey string) ([]byte, []testMultipartPart, string) {
		t.Helper()
		glb := testGLB([]byte(candidateKey))
		parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, candidateKey)}, {name: "file", filename: "asset.glb", data: glb}}
		return glb, parts, stableTestID("asset-pin-reference:v1\n" + candidateKey)
	}

	t.Run("marker create failure prevents pin", func(t *testing.T) {
		_, parts, _ := newRequest(t, "candidate-marker-create-failure")
		rig := newAssetPinTestRig(t, nil)
		rig.recovery.createErr = errors.New("marker unavailable")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable || rig.pinner.calculateCalls != 1 || rig.pinner.pinCalls != 0 || len(rig.store.upserts) != 0 {
			t.Fatalf("status/calculate/pin/upsert = %d/%d/%d/%d; body=%s", response.Code, rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.store.upserts), response.Body.String())
		}
	})

	t.Run("Kubo uncertainty retains expected CID and blocks crash-equivalent retry", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-kubo-uncertain")
		rig := newAssetPinTestRig(t, nil)
		const backendError = "Kubo response lost after private dial detail"
		rig.pinner.pinErr = errors.New(backendError)
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		assertAssetPinKuboUnavailable(t, response, backendError)
		if !ok || marker.Phase != assetpin.AssetPinRecoveryIntent || marker.ExpectedCID != testAssetCID || marker.CID != "" {
			t.Fatalf("status/marker = %d/%+v/%v; body=%s", response.Code, marker, ok, response.Body.String())
		}
		response = serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable || rig.pinner.calculateCalls != 1 || rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 0 {
			t.Fatalf("retry status/calculate/pin/upsert = %d/%d/%d/%d; body=%s", response.Code, rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.store.upserts), response.Body.String())
		}
	})

	t.Run("mark-pinned failure unpins and removes after confirmation", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-mark-pinned-failure")
		rig := newAssetPinTestRig(t, nil)
		rig.recovery.markErr = errors.New("marker update failed")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		_, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 1 || ok || len(rig.store.upserts) != 0 {
			t.Fatalf("status/unpin/marker/upsert = %d/%d/%v/%d; body=%s", response.Code, rig.pinner.unpinCalls, ok, len(rig.store.upserts), response.Body.String())
		}
	})

	t.Run("mark-pinned failure and failed unpin retain expected CID", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-mark-pinned-unpin-failure")
		rig := newAssetPinTestRig(t, nil)
		rig.recovery.markErr = errors.New("marker update failed")
		rig.pinner.unpinErr = errors.New("unpin failed")
		reported := 0
		rig.handler.reportCleanupFailure = func() { reported++ }
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 1 || !ok || marker.Phase != assetpin.AssetPinRecoveryIntent || marker.ExpectedCID != testAssetCID || marker.CID != "" || reported != 1 || len(rig.store.upserts) != 0 {
			t.Fatalf("status/unpin/marker/reported/upsert = %d/%d/%+v/%v/%d/%d; body=%s", response.Code, rig.pinner.unpinCalls, marker, ok, reported, len(rig.store.upserts), response.Body.String())
		}
	})

	for _, unpinFails := range []bool{false, true} {
		name := "observed CID mismatch unpins and removes marker"
		if unpinFails {
			name = "observed CID mismatch retains both CIDs when unpin fails"
		}
		t.Run(name, func(t *testing.T) {
			_, parts, referenceKey := newRequest(t, "candidate-observed-mismatch-"+fmt.Sprint(unpinFails))
			rig := newAssetPinTestRig(t, nil)
			rig.pinner.cid = testAssetAlternateCID
			if unpinFails {
				rig.pinner.unpinErr = errors.New("unpin failed")
			}
			reported := 0
			rig.handler.reportCleanupFailure = func() { reported++ }
			response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
			marker, ok := rig.recovery.marker(referenceKey)
			if response.Code != http.StatusBadGateway || rig.pinner.unpinCalls != 1 || rig.pinner.unpinned != testAssetAlternateCID || len(rig.store.upserts) != 0 {
				t.Fatalf("status/unpin/CID/upsert = %d/%d/%q/%d; body=%s", response.Code, rig.pinner.unpinCalls, rig.pinner.unpinned, len(rig.store.upserts), response.Body.String())
			}
			if unpinFails {
				if !ok || marker.Phase != assetpin.AssetPinRecoveryPinnedUncommitted || marker.ExpectedCID != testAssetCID || marker.CID != testAssetAlternateCID || reported != 1 {
					t.Fatalf("retained mismatch marker/reported = %+v/%v/%d", marker, ok, reported)
				}
			} else if ok || reported != 0 {
				t.Fatalf("confirmed mismatch cleanup marker/reported = %+v/%v/%d", marker, ok, reported)
			}
		})
	}

	t.Run("ordinary ledger failure and confirmed unpin remove marker", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-ledger-failure-unpinned")
		rig := newAssetPinTestRig(t, nil)
		rig.store.upsertErr = errors.New("ledger failed")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		_, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 1 || ok {
			t.Fatalf("status/unpin/marker = %d/%d/%v; body=%s", response.Code, rig.pinner.unpinCalls, ok, response.Body.String())
		}
	})

	t.Run("unpin failure retains pinned marker", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-unpin-failure")
		rig := newAssetPinTestRig(t, nil)
		rig.store.upsertErr = errors.New("ledger failed")
		rig.pinner.unpinErr = errors.New("unpin failed")
		reported := 0
		rig.handler.reportCleanupFailure = func() { reported++ }
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 1 || !ok || marker.Phase != assetpin.AssetPinRecoveryPinnedUncommitted || marker.ExpectedCID != testAssetCID || marker.CID != testAssetCID || reported != 1 {
			t.Fatalf("status/unpin/marker/reported = %d/%d/%+v/%v/%d; body=%s", response.Code, rig.pinner.unpinCalls, marker, ok, reported, response.Body.String())
		}
	})

	t.Run("recovery-required retains marker without unpin", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-recovery-required-marker")
		rig := newAssetPinTestRig(t, nil)
		rig.store.upsertErr = fmt.Errorf("uncertain commit: %w", storage.ErrAssetPinLedgerRecoveryRequired)
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 0 || !ok || marker.Phase != assetpin.AssetPinRecoveryPinnedUncommitted || marker.ExpectedCID != testAssetCID || marker.CID != testAssetCID {
			t.Fatalf("status/unpin/marker = %d/%d/%+v/%v; body=%s", response.Code, rig.pinner.unpinCalls, marker, ok, response.Body.String())
		}
	})

	t.Run("ledger success with stale marker is retry-cleanable", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-stale-marker")
		rig := newAssetPinTestRig(t, nil)
		rig.recovery.removeErr = errors.New("remove failed")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable || rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 1 {
			t.Fatalf("first status/pin/upsert = %d/%d/%d; body=%s", response.Code, rig.pinner.pinCalls, len(rig.store.upserts), response.Body.String())
		}
		if marker, ok := rig.recovery.marker(referenceKey); !ok || marker.Phase != assetpin.AssetPinRecoveryPinnedUncommitted {
			t.Fatalf("stale marker = %+v/%v", marker, ok)
		}
		rig.recovery.removeErr = nil
		response = serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusCreated || rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 1 {
			t.Fatalf("retry status/pin/upsert = %d/%d/%d; body=%s", response.Code, rig.pinner.pinCalls, len(rig.store.upserts), response.Body.String())
		}
		if _, ok := rig.recovery.marker(referenceKey); ok {
			t.Fatal("exact candidate retry did not clear stale marker")
		}
	})
}

func TestAssetPinFirstPinStreamsTempAndPersistsStagedAudit(t *testing.T) {
	glb := testGLB([]byte("first-pin"))
	candidateKey := "candidate-first-pin"
	referenceKey := stableTestID("asset-pin-reference:v1\n" + candidateKey)
	metadata := testCanonicalMetadataWithAttribution(glb, candidateKey, "Artist <Name>")
	rig := newAssetPinTestRig(t, nil)
	requestContextKey := struct{}{}
	req := testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: metadata}, {name: "file", filename: "asset.glb", data: glb}})
	req = req.WithContext(context.WithValue(req.Context(), requestContextKey, "present"))
	rig.pinner.calculateFunc = func(ctx context.Context, path string) (string, error) {
		if ctx.Value(requestContextKey) != "present" {
			t.Fatal("request context was not preserved to CID calculator")
		}
		if path == "" {
			t.Fatal("CID calculator path was empty")
		}
		return testAssetCID, nil
	}
	rig.pinner.pinFunc = func(ctx context.Context, path string) (string, error) {
		if ctx.Value(requestContextKey) != "present" {
			t.Fatal("request context was not preserved to pinner")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("temp stat during pin: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("temp mode = %o, want 600", info.Mode().Perm())
		}
		pathAbs, _ := filepath.Abs(path)
		dataAbs, _ := filepath.Abs(rig.dataDir)
		if pathAbs == dataAbs || !strings.HasPrefix(pathAbs, dataAbs+string(os.PathSeparator)) {
			t.Fatalf("temp path %q is not below data dir %q", pathAbs, dataAbs)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read streamed temp: %v", err)
		}
		if !bytes.Equal(got, glb) {
			t.Fatalf("streamed temp differs from GLB")
		}
		marker, ok := rig.recovery.marker(referenceKey)
		if !ok || marker.ExpectedCID != testAssetCID || marker.CID != "" || marker.Phase != assetpin.AssetPinRecoveryIntent {
			t.Fatalf("pre-pin intent marker = %+v/%v", marker, ok)
		}
		return testAssetCID, nil
	}

	response := serveAssetPin(rig.handler, req)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	assertNoAssetPinTempFiles(t, rig.dataDir)
	if rig.verifier.calls != 1 || rig.verifier.token != "good-token" || rig.verifier.kind != assetpin.WorkflowPin {
		t.Fatalf("verification = calls %d token %q kind %q", rig.verifier.calls, rig.verifier.token, rig.verifier.kind)
	}
	if rig.pinner.calculateCalls != 1 || rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 1 || len(rig.store.events) != 1 {
		t.Fatalf("calculate/pin/upserts/events = %d/%d/%d/%d, want 1/1/1/1", rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.store.upserts), len(rig.store.events))
	}
	ref := rig.store.upserts[0]
	if ref.ReferenceKey != stableTestID("asset-pin-reference:v1\n"+candidateKey) ||
		ref.CandidateKey != candidateKey || ref.CID != testAssetCID ||
		ref.SHA256 != testSHA256(glb) || ref.ByteCount != int64(len(glb)) ||
		ref.State != storage.AssetReferenceStaged ||
		ref.SourceURL != "https://example.test/model.glb" || ref.LicenseName != "CC0-1.0" ||
		ref.Attribution != "Artist <Name>" || ref.MetadataJSON != string(metadata) ||
		ref.WorkflowRunID != rig.verifier.claims.RunID || !ref.CreatedAt.Equal(rig.now) ||
		!ref.UpdatedAt.Equal(rig.now) || !ref.ExpiresAt.Equal(rig.now.Add(90*24*time.Hour)) {
		t.Fatalf("reference = %+v, want complete staged provenance", ref)
	}
	event := rig.store.events[0]
	if event.EventID != stableTestID("asset-pin-upsert:v1\n"+ref.ReferenceKey) ||
		event.Kind != "asset_pin_upload" || event.Result != "pinned" ||
		event.TokenDigest != stableTestID("good-token") ||
		event.Repository != rig.verifier.claims.Repository || event.Ref != rig.verifier.claims.Ref ||
		event.WorkflowRef != rig.verifier.claims.WorkflowRef || event.Actor != rig.verifier.claims.Actor ||
		event.WorkflowRunID != rig.verifier.claims.RunID || event.RunAttempt != rig.verifier.claims.RunAttempt ||
		event.CommitSHA != rig.verifier.claims.SHA || event.CandidateKey != candidateKey ||
		event.ReferenceKey != ref.ReferenceKey || event.CID != testAssetCID ||
		event.SHA256 != testSHA256(glb) || event.ByteCount != int64(len(glb)) || !event.OccurredAt.Equal(rig.now) {
		t.Fatalf("audit event = %+v, want complete pinned outcome", event)
	}

	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 6 {
		t.Fatalf("response fields = %v, want exactly 6", result)
	}
	if result["cid"] != testAssetCID || result["sha256"] != testSHA256(glb) ||
		result["byteLength"] != float64(len(glb)) ||
		result["gatewayUrl"] != "https://sdn.spaceaware.io/ipfs/"+testAssetCID ||
		result["pinState"] != "staged" || result["alreadyExisted"] != false {
		t.Fatalf("response = %#v", result)
	}
}

func TestAssetPinCandidateRetryIsIdempotentOrConflictsDeterministically(t *testing.T) {
	glb := testGLB([]byte("candidate-retry"))
	candidateKey := "candidate-retry"
	metadata := testCanonicalMetadataWithAttribution(glb, candidateKey, "Artist")

	t.Run("exact staged retry", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		stored := testStoredAssetPinReference(glb, candidateKey, metadata, rig.now)
		rig.store.byCandidate[candidateKey] = stored
		response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: metadata}, {name: "file", filename: "asset.glb", data: glb}}))
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if result["alreadyExisted"] != true || result["cid"] != stored.CID || result["pinState"] != "staged" {
			t.Fatalf("retry response = %#v", result)
		}
		if rig.store.findCalls != 0 || rig.capacity.calls != 0 || rig.pinner.calculateCalls != 0 || rig.pinner.pinCalls != 0 || len(rig.store.upserts) != 0 {
			t.Fatalf("retry side effects find/capacity/calculate/pin/upsert = %d/%d/%d/%d/%d", rig.store.findCalls, rig.capacity.calls, rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.store.upserts))
		}
	})

	mismatches := []struct {
		name   string
		mutate func(*storage.AssetPinReference)
	}{
		{name: "reference key", mutate: func(ref *storage.AssetPinReference) { ref.ReferenceKey = strings.Repeat("a", 64) }},
		{name: "candidate identity", mutate: func(ref *storage.AssetPinReference) { ref.CandidateKey = "different-candidate" }},
		{name: "SHA", mutate: func(ref *storage.AssetPinReference) { ref.SHA256 = strings.Repeat("b", 64) }},
		{name: "byte count", mutate: func(ref *storage.AssetPinReference) { ref.ByteCount++ }},
		{name: "source", mutate: func(ref *storage.AssetPinReference) { ref.SourceURL = "https://example.test/other.glb" }},
		{name: "license", mutate: func(ref *storage.AssetPinReference) { ref.LicenseName = "Apache-2.0" }},
		{name: "attribution", mutate: func(ref *storage.AssetPinReference) { ref.Attribution = "Other Artist" }},
		{name: "metadata", mutate: func(ref *storage.AssetPinReference) { ref.MetadataJSON = "{}" }},
		{name: "lifecycle state", mutate: func(ref *storage.AssetPinReference) { ref.State = storage.AssetReferenceReviewOpen }},
		{name: "expired staged row", mutate: func(ref *storage.AssetPinReference) { ref.ExpiresAt = time.Unix(1, 0).UTC() }},
	}
	for _, mismatch := range mismatches {
		t.Run(mismatch.name+" conflict", func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			stored := testStoredAssetPinReference(glb, candidateKey, metadata, rig.now)
			mismatch.mutate(&stored)
			rig.store.byCandidate[candidateKey] = stored
			response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: metadata}, {name: "file", filename: "asset.glb", data: glb}}))
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", response.Code, response.Body.String())
			}
			if rig.pinner.pinCalls != 0 || len(rig.store.upserts) != 0 {
				t.Fatalf("conflict pin/upsert = %d/%d, want 0/0", rig.pinner.pinCalls, len(rig.store.upserts))
			}
		})
	}

	t.Run("corrupt stored CID", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		stored := testStoredAssetPinReference(glb, candidateKey, metadata, rig.now)
		stored.CID = "not-a-cid"
		rig.store.byCandidate[candidateKey] = stored
		response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: metadata}, {name: "file", filename: "asset.glb", data: glb}}))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
		}
	})
}

func TestAssetPinCandidateAbsentWithRecoveryMarkerDoesNotRepin(t *testing.T) {
	glb := testGLB([]byte("pending-recovery"))
	candidateKey := "candidate-pending-recovery"
	referenceKey := stableTestID("asset-pin-reference:v1\n" + candidateKey)
	rig := newAssetPinTestRig(t, nil)
	rig.recovery.markers[referenceKey] = assetpin.AssetPinRecoveryMarker{
		SchemaVersion: 1, Phase: assetpin.AssetPinRecoveryPinnedUncommitted,
		ReferenceKey: referenceKey, CID: testAssetCID,
	}
	response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, candidateKey)}, {name: "file", filename: "asset.glb", data: glb}}))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
	if rig.store.findCalls != 0 || rig.capacity.calls != 0 || rig.pinner.pinCalls != 0 || len(rig.store.upserts) != 0 {
		t.Fatalf("pending recovery side effects find/capacity/pin/upsert = %d/%d/%d/%d", rig.store.findCalls, rig.capacity.calls, rig.pinner.pinCalls, len(rig.store.upserts))
	}
}

func TestAssetPinConcurrentIdenticalCandidatePinsAndUpsertsOnce(t *testing.T) {
	glb := testGLB([]byte("concurrent-candidate"))
	candidateKey := "candidate-concurrent"
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, candidateKey)}, {name: "file", filename: "asset.glb", data: glb}}
	rig := newAssetPinTestRig(t, nil)
	requests := []*http.Request{testMultipartRequest(t, parts), testMultipartRequest(t, parts)}
	responses := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			responses[index] = serveAssetPin(rig.handler, requests[index])
		}(index)
	}
	close(start)
	wait.Wait()

	alreadyExisted := map[bool]int{}
	for _, response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
		var result struct {
			AlreadyExisted bool `json:"alreadyExisted"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		alreadyExisted[result.AlreadyExisted]++
	}
	if alreadyExisted[false] != 1 || alreadyExisted[true] != 1 {
		t.Fatalf("alreadyExisted results = %v, want one false and one true", alreadyExisted)
	}
	if rig.pinner.calculateCalls != 1 || rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 1 || len(rig.store.events) != 1 {
		t.Fatalf("calculate/pin/upsert/event = %d/%d/%d/%d, want 1/1/1/1", rig.pinner.calculateCalls, rig.pinner.pinCalls, len(rig.store.upserts), len(rig.store.events))
	}
	assertNoAssetPinTempFiles(t, rig.dataDir)
}

func TestAssetPinExistingSHADeduplicatesWithoutCapacityPinOrUnpin(t *testing.T) {
	glb := testGLB([]byte("deduplicated"))
	candidateKey := "candidate-deduplicated"
	rig := newAssetPinTestRig(t, nil)
	rig.store.found = true
	rig.store.foundRef = storage.AssetPinReference{CID: testAssetCID, SHA256: testSHA256(glb)}
	rig.store.upsertErr = errors.New("dedup ledger failure")
	response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, candidateKey)}, {name: "file", filename: "asset.glb", data: glb}}))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
	if rig.capacity.calls != 0 || rig.pinner.calculateCalls != 0 || rig.pinner.pinCalls != 0 || rig.pinner.unpinCalls != 0 {
		t.Fatalf("dedup capacity/calculate/pin/unpin = %d/%d/%d/%d, want 0/0/0/0", rig.capacity.calls, rig.pinner.calculateCalls, rig.pinner.pinCalls, rig.pinner.unpinCalls)
	}
	if rig.pinner.checkCalls != 1 {
		t.Fatalf("dedup pin checks = %d, want 1", rig.pinner.checkCalls)
	}
	if len(rig.store.events) != 1 || rig.store.events[0].Result != "deduplicated" {
		t.Fatalf("dedup event = %+v, want explicit deduplicated result", rig.store.events)
	}

	rig.store.upsertErr = nil
	response = serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-deduplicated-success")}, {name: "file", filename: "asset.glb", data: glb}}))
	if response.Code != http.StatusCreated {
		t.Fatalf("success status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 6 || result["alreadyExisted"] != true || result["cid"] != testAssetCID {
		t.Fatalf("dedup response = %#v", result)
	}
}

func TestAssetPinExistingSHAMissingAfterRetentionUnpinIsRepinned(t *testing.T) {
	glb := testGLB([]byte("retention-unpinned-delete-failed"))
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-repin")}, {name: "file", filename: "asset.glb", data: glb}}
	rig := newAssetPinTestRig(t, nil)
	// This durable row models retention successfully unpinning a CID and then
	// failing to delete its retry row. SHA deduplication must repair the pin.
	rig.store.found = true
	rig.store.foundRef = storage.AssetPinReference{CID: testAssetCID, SHA256: testSHA256(glb), ByteCount: int64(len(glb))}
	rig.pinner.pinned = false

	response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	if rig.pinner.checkCalls != 1 || rig.capacity.calls != 1 || rig.pinner.pinCalls != 1 || rig.pinner.unpinCalls != 0 {
		t.Fatalf("repair check/capacity/pin/unpin = %d/%d/%d/%d, want 1/1/1/0", rig.pinner.checkCalls, rig.capacity.calls, rig.pinner.pinCalls, rig.pinner.unpinCalls)
	}
	if len(rig.store.events) != 1 || rig.store.events[0].Result != "repinned" {
		t.Fatalf("repair audit = %+v, want repinned", rig.store.events)
	}
	if len(rig.recovery.calls) != 1 || rig.recovery.calls[0] != "load" {
		t.Fatalf("repair recovery calls = %v, want no new marker", rig.recovery.calls)
	}

	// The old ledger row remains the durable owner/recovery record, so a new
	// reference upsert failure must not compensate by removing the repaired pin.
	rig.store.upsertErr = errors.New("injected new reference failure")
	response = serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-repin-upsert-failure")}, {name: "file", filename: "asset.glb", data: glb}}))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failure status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
	if rig.pinner.unpinCalls != 0 {
		t.Fatalf("repaired durable pin was compensated after upsert failure: %d unpins", rig.pinner.unpinCalls)
	}
}

func TestAssetPinExistingSHAPinVerificationAndRepairFailClosed(t *testing.T) {
	glb := testGLB([]byte("dedup-repair-fail-closed"))
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-repair-failure")}, {name: "file", filename: "asset.glb", data: glb}}
	tests := []struct {
		name       string
		configure  func(*assetPinTestRig)
		wantStatus int
		wantPin    int
		wantUnpin  int
	}{
		{name: "lookup unavailable", wantStatus: http.StatusServiceUnavailable, configure: func(rig *assetPinTestRig) {
			rig.pinner.checkErr = errors.New("Kubo lookup unavailable")
		}},
		{name: "capacity unavailable", wantStatus: http.StatusServiceUnavailable, configure: func(rig *assetPinTestRig) {
			rig.pinner.pinned = false
			rig.capacity.err = errors.New("statfs unavailable")
		}},
		{name: "capacity floor", wantStatus: http.StatusInsufficientStorage, configure: func(rig *assetPinTestRig) {
			rig.pinner.pinned = false
			rig.capacity.available = 0
		}},
		{name: "repin unavailable", wantStatus: http.StatusServiceUnavailable, wantPin: 1, configure: func(rig *assetPinTestRig) {
			rig.pinner.pinned = false
			rig.pinner.pinErr = errors.New("Kubo pin unavailable")
		}},
		{name: "malformed repin CID", wantStatus: http.StatusBadGateway, wantPin: 1, configure: func(rig *assetPinTestRig) {
			rig.pinner.pinned = false
			rig.pinner.cid = "not-a-cid"
		}},
		{name: "mismatched repin CID may be shared", wantStatus: http.StatusBadGateway, wantPin: 1, wantUnpin: 0, configure: func(rig *assetPinTestRig) {
			rig.pinner.pinned = false
			rig.pinner.cid = testAssetAlternateCID
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newAssetPinTestRig(t, nil)
			rig.store.found = true
			rig.store.foundRef = storage.AssetPinReference{CID: testAssetCID, SHA256: testSHA256(glb), ByteCount: int64(len(glb))}
			test.configure(rig)
			response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if rig.pinner.checkCalls != 1 || rig.pinner.pinCalls != test.wantPin || rig.pinner.unpinCalls != test.wantUnpin || len(rig.store.upserts) != 0 {
				t.Fatalf("check/pin/unpin/upsert = %d/%d/%d/%d, want 1/%d/%d/0", rig.pinner.checkCalls, rig.pinner.pinCalls, rig.pinner.unpinCalls, len(rig.store.upserts), test.wantPin, test.wantUnpin)
			}
			if strings.Contains(response.Body.String(), "Kubo") || strings.Contains(response.Body.String(), "not-a-cid") {
				t.Fatalf("response leaked backend detail: %s", response.Body.String())
			}
		})
	}
}

func TestAssetPinMutationGateCoversUploadAndReferenceState(t *testing.T) {
	t.Run("upload", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		release, err := rig.handler.gate.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		glb := testGLB([]byte("gate-upload"))
		request := testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-gate-upload")}, {name: "file", filename: "asset.glb", data: glb}})
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- serveAssetPin(rig.handler, request)
		}()
		waitForAssetPinTempFile(t, rig.dataDir)
		select {
		case response := <-result:
			t.Fatalf("upload completed while mutation gate was held: status=%d body=%s", response.Code, response.Body.String())
		case <-time.After(20 * time.Millisecond):
		}
		rig.store.mu.Lock()
		storeCalls := rig.store.candidateFindCalls + rig.store.findCalls + len(rig.store.upserts)
		rig.store.mu.Unlock()
		if storeCalls != 0 || rig.pinner.pinCalls != 0 {
			t.Fatalf("upload mutated while gate held: store=%d pin=%d", storeCalls, rig.pinner.pinCalls)
		}
		release()
		if response := <-result; response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("reference state", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		candidateKey := "candidate-gate-state"
		stored := testAPIAssetReferenceForState(rig, candidateKey, storage.AssetReferenceStaged)
		rig.store.byCandidate[candidateKey] = stored
		rig.store.byReference[stored.ReferenceKey] = stored
		release, err := rig.handler.gate.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- serveAssetReferenceStateTestRequest(rig, candidateKey, storage.AssetReferenceReviewOpen, 42, "", rig.now)
		}()
		deadline := time.Now().Add(time.Second)
		verified := false
		for !verified && time.Now().Before(deadline) {
			rig.verifier.mu.Lock()
			verified = rig.verifier.calls > 0
			rig.verifier.mu.Unlock()
			if !verified {
				time.Sleep(time.Millisecond)
			}
		}
		if !verified {
			t.Fatal("reference-state authorization did not complete before gate assertion")
		}
		select {
		case response := <-result:
			t.Fatalf("reference state completed while mutation gate was held: status=%d body=%s", response.Code, response.Body.String())
		case <-time.After(20 * time.Millisecond):
		}
		rig.store.mu.Lock()
		findCalls := rig.store.candidateFindCalls
		rig.store.mu.Unlock()
		if findCalls != 0 {
			t.Fatalf("reference state read/mutated while gate held: finds=%d", findCalls)
		}
		release()
		if response := <-result; response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
		}
	})
}

func TestAssetPinFailsClosedOnInvalidCID(t *testing.T) {
	glb := testGLB([]byte("invalid-cid"))
	parts := []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-invalid-cid")}, {name: "file", filename: "asset.glb", data: glb}}
	t.Run("deduplicated store CID", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.store.found = true
		rig.store.foundRef = storage.AssetPinReference{CID: "not-a-cid", SHA256: testSHA256(glb)}
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusServiceUnavailable || len(rig.store.upserts) != 0 || rig.pinner.pinCalls != 0 {
			t.Fatalf("status/upserts/pins = %d/%d/%d; body=%s", response.Code, len(rig.store.upserts), rig.pinner.pinCalls, response.Body.String())
		}
	})
	t.Run("Kubo CID", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.pinner.cid = "not-a-cid"
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		referenceKey := stableTestID("asset-pin-reference:v1\ncandidate-invalid-cid")
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusBadGateway || len(rig.store.upserts) != 0 || !ok || marker.ExpectedCID != testAssetCID || marker.Phase != assetpin.AssetPinRecoveryIntent || marker.CID != "" {
			t.Fatalf("status/upserts/marker = %d/%d/%+v/%v; body=%s", response.Code, len(rig.store.upserts), marker, ok, response.Body.String())
		}
	})
}

func TestAssetPinBoundsAuthorizedConcurrentBodyStaging(t *testing.T) {
	glb := testGLB([]byte("upload-slot"))
	request := testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-upload-slot")}, {name: "file", filename: "asset.glb", data: glb}})
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read test multipart body: %v", err)
	}
	contentType := request.Header.Get("Content-Type")
	blocked := &blockingReader{reader: bytes.NewReader(body), entered: make(chan struct{}), release: make(chan struct{})}
	rig := newAssetPinTestRig(t, nil)
	rig.handler.uploadSlots = make(chan struct{}, 1)
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResult <- serveAssetPin(rig.handler, authorizedPinRequest(blocked, contentType))
	}()
	<-blocked.entered

	untouched := &trackingReader{}
	second := serveAssetPin(rig.handler, authorizedPinRequest(untouched, contentType))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated status = %d, want 503; body=%s", second.Code, second.Body.String())
	}
	if untouched.read {
		t.Fatal("saturated authorized request body was read")
	}
	close(blocked.release)
	if first := <-firstResult; first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201; body=%s", first.Code, first.Body.String())
	}
}

func TestAssetPinRejectsSymlinkedTempDirectory(t *testing.T) {
	glb := testGLB([]byte("symlink-temp"))
	rig := newAssetPinTestRig(t, nil)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(rig.dataDir, "asset-pins")); err != nil {
		t.Fatalf("create asset-pins symlink: %v", err)
	}
	response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-symlink-temp")}, {name: "file", filename: "asset.glb", data: glb}}))
	if response.Code != http.StatusServiceUnavailable || rig.pinner.pinCalls != 0 {
		t.Fatalf("status/pin = %d/%d, want 503/0; body=%s", response.Code, rig.pinner.pinCalls, response.Body.String())
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target was modified: %v", entries)
	}
}

func TestAssetPinTempRemovalFailureIsReportedWithoutPathLeak(t *testing.T) {
	glb := testGLB([]byte("cleanup-report"))
	rig := newAssetPinTestRig(t, nil)
	reported := 0
	rig.handler.removeTempFile = func(path string) error {
		_ = os.Remove(path)
		return errors.New("remove failed at " + path)
	}
	rig.handler.reportCleanupFailure = func() { reported++ }
	response := serveAssetPin(rig.handler, testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: testCanonicalMetadata(glb, "candidate-cleanup-report")}, {name: "file", filename: "asset.glb", data: glb}}))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	if reported != 1 {
		t.Fatalf("cleanup reports = %d, want 1", reported)
	}
	if strings.Contains(response.Body.String(), rig.dataDir) || strings.Contains(response.Body.String(), "remove failed") {
		t.Fatalf("response leaked cleanup detail: %s", response.Body.String())
	}
}

func TestAssetPinHandlerConstructorRequiresSafeDependencies(t *testing.T) {
	cfg := config.Default().AssetPins
	base := AssetPinHandlerOptions{
		Verifier: &fakeAssetPinVerifier{}, Store: &fakeAssetPinStore{},
		Capacity: &fakeAssetPinCapacity{}, Pinner: &fakeAssetPinner{},
		Gate:   assetpin.NewMutationGate(),
		Config: cfg, DataDir: t.TempDir(),
	}
	tests := []struct {
		name   string
		mutate func(*AssetPinHandlerOptions)
	}{
		{name: "verifier", mutate: func(o *AssetPinHandlerOptions) { o.Verifier = nil }},
		{name: "store", mutate: func(o *AssetPinHandlerOptions) { o.Store = nil }},
		{name: "pinner", mutate: func(o *AssetPinHandlerOptions) { o.Pinner = nil }},
		{name: "gate", mutate: func(o *AssetPinHandlerOptions) { o.Gate = nil }},
		{name: "data dir", mutate: func(o *AssetPinHandlerOptions) { o.DataDir = "" }},
		{name: "Kubo repo", mutate: func(o *AssetPinHandlerOptions) { o.Config.KuboRepoPath = "" }},
		{name: "gateway", mutate: func(o *AssetPinHandlerOptions) { o.Config.GatewayURL = "http://example.test/ipfs" }},
		{name: "hostless gateway", mutate: func(o *AssetPinHandlerOptions) { o.Config.GatewayURL = "https://:443" }},
		{name: "negative max upload", mutate: func(o *AssetPinHandlerOptions) { o.Config.MaxUploadBytes = -1 }},
		{name: "negative free floor", mutate: func(o *AssetPinHandlerOptions) { o.Config.MinFreeBytes = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.mutate(&options)
			if _, err := NewAssetPinHandler(options); err == nil {
				t.Fatal("NewAssetPinHandler() succeeded with unsafe options")
			}
		})
	}
}

type trackingReader struct{ read bool }

func (r *trackingReader) Read(_ []byte) (int, error) {
	r.read = true
	return 0, io.EOF
}

type blockingReader struct {
	reader  *bytes.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.reader.Read(buffer)
}

type testMultipartPart struct {
	name     string
	filename string
	data     []byte
}

func testMultipartRequest(t *testing.T, parts []testMultipartPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		var output io.Writer
		var err error
		if part.filename == "" {
			output, err = writer.CreateFormField(part.name)
		} else {
			output, err = writer.CreateFormFile(part.name, part.filename)
		}
		if err != nil {
			t.Fatalf("create multipart part: %v", err)
		}
		if _, err := output.Write(part.data); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	return authorizedPinRequest(&body, writer.FormDataContentType())
}

func authorizedPinRequest(body io.Reader, contentType string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/pin", body)
	req.Header.Set("Authorization", "Bearer good-token")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func serveAssetPin(handler *AssetPinHandler, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}

func assertAssetPinKuboUnavailable(t *testing.T, response *httptest.ResponseRecorder, backendError string) {
	t.Helper()
	const wantBody = "{\"error\":{\"message\":\"asset pin backend unavailable\"}}\n"
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	if response.Body.String() != wantBody {
		t.Fatalf("body = %q, want fixed sanitized JSON %q", response.Body.String(), wantBody)
	}
	if strings.Contains(response.Body.String(), backendError) {
		t.Fatalf("response leaked Kubo error detail %q: %s", backendError, response.Body.String())
	}
}

func testGLB(payload []byte) []byte {
	glb := make([]byte, 12+len(payload))
	copy(glb[:4], "glTF")
	binary.LittleEndian.PutUint32(glb[4:8], 2)
	binary.LittleEndian.PutUint32(glb[8:12], uint32(len(glb)))
	copy(glb[12:], payload)
	return glb
}

func testSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func testCanonicalMetadata(glb []byte, candidateKey string) []byte {
	return []byte(fmt.Sprintf(`{"candidateKey":%q,"licenseName":"CC0-1.0","schemaVersion":1,"sha256":"%s","sourceUrl":"https://example.test/model.glb"}`, candidateKey, testSHA256(glb)))
}

func testCanonicalMetadataWithAttribution(glb []byte, candidateKey, attribution string) []byte {
	return []byte(fmt.Sprintf(`{"attribution":%q,"candidateKey":%q,"licenseName":"CC0-1.0","schemaVersion":1,"sha256":"%s","sourceUrl":"https://example.test/model.glb"}`, attribution, candidateKey, testSHA256(glb)))
}

func stableTestID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func testStoredAssetPinReference(glb []byte, candidateKey string, metadata []byte, now time.Time) storage.AssetPinReference {
	return storage.AssetPinReference{
		ReferenceKey:  stableTestID("asset-pin-reference:v1\n" + candidateKey),
		CandidateKey:  candidateKey,
		CID:           testAssetCID,
		SHA256:        testSHA256(glb),
		ByteCount:     int64(len(glb)),
		State:         storage.AssetReferenceStaged,
		SourceURL:     "https://example.test/model.glb",
		LicenseName:   "CC0-1.0",
		Attribution:   extractTestAttribution(metadata),
		MetadataJSON:  string(metadata),
		WorkflowRunID: "original-workflow-run",
		CreatedAt:     now.Add(-time.Minute),
		UpdatedAt:     now.Add(-time.Minute),
		ExpiresAt:     now.Add(90*24*time.Hour - time.Minute),
	}
}

func extractTestAttribution(metadata []byte) string {
	var value struct {
		Attribution string `json:"attribution"`
	}
	_ = json.Unmarshal(metadata, &value)
	return value.Attribution
}

func waitForAssetPinTempFile(t *testing.T, dataDir string) {
	t.Helper()
	tempDir := filepath.Join(dataDir, "asset-pins", "tmp")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(tempDir)
		if err == nil && len(entries) > 0 {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read asset pin temp directory: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("asset upload did not finish staging before gate assertion")
}

func assertNoAssetPinTempFiles(t *testing.T, dataDir string) {
	t.Helper()
	tempDir := filepath.Join(dataDir, "asset-pins", "tmp")
	if _, err := os.Lstat(tempDir); errors.Is(err, os.ErrNotExist) {
		return
	}
	err := filepath.WalkDir(tempDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Fatalf("temporary file was not removed: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk data dir: %v", err)
	}
}
