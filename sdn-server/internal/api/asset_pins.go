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
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"golang.org/x/sys/unix"

	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	assetPinHardMaxUploadBytes      int64 = 10_000_000
	assetPinBodyOverheadBytes       int64 = 1 << 20
	assetPinMetadataMaxBytes        int64 = 64 << 10
	assetPinStagedLifetime                = 90 * 24 * time.Hour
	assetPinCleanupTimeout                = 10 * time.Second
	assetPinConcurrentUploads             = 2
	assetReferenceStateBodyMaxBytes       = 4 << 10
	assetReferenceDecisionLifetime        = 30 * 24 * time.Hour
)

// AssetPinVerifier is the upload handler's narrow OIDC security dependency.
type AssetPinVerifier interface {
	VerifyAndConsume(context.Context, string, assetpin.WorkflowKind) (assetpin.Claims, error)
}

// AssetPinStore is the upload handler's journaled reference-ledger dependency.
type AssetPinStore interface {
	FindAssetPinReferenceByCandidateKey(context.Context, string) (storage.AssetPinReference, bool, error)
	FindAssetBySHA256(context.Context, string) (storage.AssetPinReference, bool, error)
	UpsertAssetPinReference(context.Context, storage.AssetPinReference, storage.AssetPinAuditEvent) error
	TransitionAssetPinReference(context.Context, storage.AssetPinReferenceTransition, storage.AssetPinAuditEvent) error
}

// AssetPinCapacity reports bytes available to the daemon on a filesystem.
type AssetPinCapacity interface {
	AvailableBytes(path string) (uint64, error)
}

// AssetPinPinner plans, pins, and unpins deterministic asset UnixFS files.
type AssetPinPinner interface {
	IsAssetCIDPinned(context.Context, string) (bool, error)
	CalculateAssetGLBCID(context.Context, string) (string, error)
	PinAssetGLB(context.Context, string) (string, error)
	UnpinAssetCID(context.Context, string) error
}

// AssetPinRecoveryStore durably bridges a new Kubo pin to its ledger upsert.
type AssetPinRecoveryStore interface {
	CreateIntent(assetpin.AssetPinRecoveryMarker) error
	MarkPinned(referenceKey, cidValue string) error
	Load(referenceKey string) (assetpin.AssetPinRecoveryMarker, bool, error)
	Remove(referenceKey string) error
}

// AssetPinHandlerOptions supplies the bounded upload handler dependencies.
type AssetPinHandlerOptions struct {
	Verifier AssetPinVerifier
	Store    AssetPinStore
	Capacity AssetPinCapacity
	Pinner   AssetPinPinner
	Recovery AssetPinRecoveryStore
	Gate     *assetpin.MutationGate
	Config   config.AssetPinConfig
	DataDir  string
	Clock    func() time.Time
}

// AssetPinHandler accepts one authenticated, bounded asset upload at a time.
// Serializing the ledger lookup/pin/upsert section prevents same-SHA races from
// pinning twice and then unpinning content retained by a concurrent reference.
type AssetPinHandler struct {
	verifier             AssetPinVerifier
	store                AssetPinStore
	capacity             AssetPinCapacity
	pinner               AssetPinPinner
	recovery             AssetPinRecoveryStore
	gate                 *assetpin.MutationGate
	config               config.AssetPinConfig
	dataDir              string
	gatewayURL           string
	maxUploadBytes       int64
	minFreeBytes         uint64
	clock                func() time.Time
	uploadSlots          chan struct{}
	removeTempFile       func(string) error
	reportCleanupFailure func()
	mutationMu           sync.Mutex
}

// NewAssetPinHandler validates and binds the upload capability dependencies.
func NewAssetPinHandler(options AssetPinHandlerOptions) (*AssetPinHandler, error) {
	if options.Verifier == nil {
		return nil, errors.New("asset pin verifier is required")
	}
	if options.Store == nil {
		return nil, errors.New("asset pin store is required")
	}
	if options.Pinner == nil {
		return nil, errors.New("asset pin pinner is required")
	}
	if options.Gate == nil {
		return nil, errors.New("asset pin mutation gate is required")
	}
	if options.Capacity == nil {
		options.Capacity = statFSAssetPinCapacity{}
	}
	if options.Config.MaxUploadBytes < 0 {
		return nil, errors.New("asset pin upload limit must be non-negative")
	}
	if options.Config.MinFreeBytes < 0 {
		return nil, errors.New("asset pin free-space floor must be non-negative")
	}
	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir == "" {
		return nil, errors.New("asset pin data directory is required")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve asset pin data directory: %w", err)
	}
	dataDir, err = filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve asset pin data directory symlinks: %w", err)
	}
	dataInfo, err := os.Stat(dataDir)
	if err != nil || !dataInfo.IsDir() {
		return nil, errors.New("asset pin data directory must exist")
	}
	if options.Recovery == nil {
		options.Recovery, err = assetpin.NewFileAssetPinRecoveryStore(dataDir)
		if err != nil {
			return nil, fmt.Errorf("create asset pin recovery store: %w", err)
		}
	}
	if strings.TrimSpace(options.Config.KuboRepoPath) == "" {
		return nil, errors.New("asset pin Kubo repository path is required")
	}
	gatewayURL, err := validateAssetGatewayURL(options.Config.GatewayURL)
	if err != nil {
		return nil, err
	}
	maxUploadBytes := options.Config.EffectiveMaxUploadBytes()
	if maxUploadBytes > assetPinHardMaxUploadBytes {
		maxUploadBytes = assetPinHardMaxUploadBytes
	}
	if maxUploadBytes <= 0 {
		return nil, errors.New("asset pin upload limit must be positive")
	}
	minFreeBytes := options.Config.EffectiveMinFreeBytes()
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &AssetPinHandler{
		verifier:       options.Verifier,
		store:          options.Store,
		capacity:       options.Capacity,
		pinner:         options.Pinner,
		recovery:       options.Recovery,
		gate:           options.Gate,
		config:         options.Config,
		dataDir:        dataDir,
		gatewayURL:     gatewayURL,
		maxUploadBytes: maxUploadBytes,
		minFreeBytes:   uint64(minFreeBytes),
		clock:          clock,
		uploadSlots:    make(chan struct{}, assetPinConcurrentUploads),
		removeTempFile: os.Remove,
		reportCleanupFailure: func() {
			log.Warn("Asset pin cleanup failed; reconciliation is required")
		},
	}, nil
}

// RegisterRoutes registers both narrow asset capability endpoints.
// Daemon-level authentication bypass composition remains the caller's responsibility.
func (h *AssetPinHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/v1/assets/pin", h)
	mux.HandleFunc("POST /api/v1/assets/reference-state", h.handleAssetReferenceState)
}

type assetReferenceStateRequest struct {
	CandidateKey   string `json:"candidateKey"`
	DecidedAt      string `json:"decidedAt"`
	DecisionSHA256 string `json:"decisionSha256"`
	IssueNumber    int64  `json:"issueNumber"`
	State          string `json:"state"`
}

type assetReferenceStateResponse struct {
	CandidateKey string                      `json:"candidateKey"`
	CID          string                      `json:"cid"`
	State        storage.AssetReferenceState `json:"state"`
	ExpiresAt    *time.Time                  `json:"expiresAt"`
}

type assetReferenceStateDetail struct {
	DecisionSHA256 string                      `json:"decisionSha256"`
	IssueNumber    int64                       `json:"issueNumber"`
	State          storage.AssetReferenceState `json:"state"`
}

func (h *AssetPinHandler) handleAssetReferenceState(w http.ResponseWriter, r *http.Request) {
	token, ok := assetBearerToken(r.Header)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	request, decidedAt, err := parseCanonicalAssetReferenceStateRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset reference state request")
		return
	}
	target := storage.AssetReferenceState(request.State)
	workflow := assetpin.WorkflowDecision
	if target == storage.AssetReferenceReviewOpen {
		workflow = assetpin.WorkflowPin
	}
	claims, err := h.verifier.VerifyAndConsume(r.Context(), token, workflow)
	if err != nil {
		if errors.Is(err, assetpin.ErrMissingToken) ||
			errors.Is(err, assetpin.ErrInvalidToken) ||
			errors.Is(err, assetpin.ErrTokenReplay) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "asset reference authorization unavailable")
		return
	}

	release, err := h.gate.Acquire(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin mutation unavailable")
		return
	}
	defer release()
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	ref, found, err := h.store.FindAssetPinReferenceByCandidateKey(r.Context(), request.CandidateKey)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "asset candidate not found")
		return
	}
	if !assetReferenceStateStoredValid(ref, request.CandidateKey) {
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	if decidedAt.Before(ref.UpdatedAt) {
		writeError(w, http.StatusConflict, "asset reference state conflicts with the requested transition")
		return
	}
	if ref.State == target {
		if ref.GitHubIssue != request.IssueNumber || ref.DecisionSHA256 != request.DecisionSHA256 {
			writeError(w, http.StatusConflict, "asset reference state conflicts with the requested transition")
			return
		}
		h.writeAssetReferenceStateSuccess(w, ref.CandidateKey, ref.CID, ref.State, ref.ExpiresAt)
		return
	}
	transition := storage.AssetPinReferenceTransition{
		ReferenceKey:   ref.ReferenceKey,
		FromState:      ref.State,
		ToState:        target,
		GitHubIssue:    request.IssueNumber,
		DecisionSHA256: request.DecisionSHA256,
		UpdatedAt:      decidedAt,
	}
	switch {
	case ref.State == storage.AssetReferenceStaged && target == storage.AssetReferenceReviewOpen && !ref.ExpiresAt.Before(decidedAt):
		transition.ExpiresAt = ref.ExpiresAt
	case ref.State == storage.AssetReferenceReviewOpen && target == storage.AssetReferenceApproved && ref.GitHubIssue == request.IssueNumber:
		transition.ExpiresAt = time.Time{}
	case ref.State == storage.AssetReferenceReviewOpen && target == storage.AssetReferenceRejected && ref.GitHubIssue == request.IssueNumber:
		transition.ExpiresAt = decidedAt.Add(assetReferenceDecisionLifetime)
	case ref.State == storage.AssetReferenceApproved && target == storage.AssetReferenceSuperseded && ref.GitHubIssue == request.IssueNumber && ref.DecisionSHA256 != request.DecisionSHA256:
		transition.ExpiresAt = decidedAt.Add(assetReferenceDecisionLifetime)
	default:
		writeError(w, http.StatusConflict, "asset reference state conflicts with the requested transition")
		return
	}
	if !transition.ExpiresAt.IsZero() && !time.Unix(0, transition.ExpiresAt.UnixNano()).Equal(transition.ExpiresAt) {
		writeError(w, http.StatusConflict, "asset reference state conflicts with the requested transition")
		return
	}
	detail, err := marshalCanonicalAssetReferenceStateDetail(assetReferenceStateDetail{
		DecisionSHA256: request.DecisionSHA256,
		IssueNumber:    request.IssueNumber,
		State:          target,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset reference transition unavailable")
		return
	}
	event := storage.AssetPinAuditEvent{
		EventID:       assetReferenceStateEventID(ref.CandidateKey, ref.State, target, request.IssueNumber, request.DecisionSHA256),
		Kind:          "asset_reference_state",
		Result:        string(target),
		TokenDigest:   stableAssetPinID(token),
		Repository:    claims.Repository,
		Ref:           claims.Ref,
		WorkflowRef:   claims.WorkflowRef,
		Actor:         claims.Actor,
		WorkflowRunID: claims.RunID,
		RunAttempt:    claims.RunAttempt,
		CommitSHA:     claims.SHA,
		CandidateKey:  ref.CandidateKey,
		ReferenceKey:  ref.ReferenceKey,
		CID:           ref.CID,
		SHA256:        ref.SHA256,
		ByteCount:     ref.ByteCount,
		Detail:        detail,
		OccurredAt:    decidedAt,
	}
	if err := h.store.TransitionAssetPinReference(r.Context(), transition, event); err != nil {
		if errors.Is(err, storage.ErrAssetPinReferenceNotFound) {
			writeError(w, http.StatusNotFound, "asset candidate not found")
			return
		}
		if errors.Is(err, storage.ErrAssetPinReferenceConflict) || errors.Is(err, storage.ErrAssetPinAuditConflict) {
			h.writeAssetReferenceStateConflictResult(w, r, request, target, decidedAt)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	h.writeAssetReferenceStateSuccess(w, ref.CandidateKey, ref.CID, target, transition.ExpiresAt)
}

func (h *AssetPinHandler) writeAssetReferenceStateConflictResult(w http.ResponseWriter, r *http.Request, request assetReferenceStateRequest, target storage.AssetReferenceState, decidedAt time.Time) {
	ref, found, err := h.store.FindAssetPinReferenceByCandidateKey(r.Context(), request.CandidateKey)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "asset reference state conflicts with the requested transition")
		return
	}
	if !assetReferenceStateStoredValid(ref, request.CandidateKey) {
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	if decidedAt.Before(ref.UpdatedAt) || ref.State != target || ref.GitHubIssue != request.IssueNumber || ref.DecisionSHA256 != request.DecisionSHA256 {
		writeError(w, http.StatusConflict, "asset reference state conflicts with the requested transition")
		return
	}
	h.writeAssetReferenceStateSuccess(w, ref.CandidateKey, ref.CID, ref.State, ref.ExpiresAt)
}

func parseCanonicalAssetReferenceStateRequest(body io.Reader) (assetReferenceStateRequest, time.Time, error) {
	raw, err := io.ReadAll(io.LimitReader(body, assetReferenceStateBodyMaxBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > assetReferenceStateBodyMaxBytes {
		return assetReferenceStateRequest{}, time.Time{}, errors.New("invalid bounded request body")
	}
	var request assetReferenceStateRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return assetReferenceStateRequest{}, time.Time{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return assetReferenceStateRequest{}, time.Time{}, errors.New("trailing JSON data")
	}
	canonical, err := marshalCanonicalAssetReferenceStateRequest(request)
	if err != nil || !bytes.Equal(raw, canonical) {
		return assetReferenceStateRequest{}, time.Time{}, errors.New("request is not canonical JSON")
	}
	if request.CandidateKey == "" || strings.TrimSpace(request.CandidateKey) != request.CandidateKey || request.IssueNumber <= 0 {
		return assetReferenceStateRequest{}, time.Time{}, errors.New("invalid candidate or issue")
	}
	state := storage.AssetReferenceState(request.State)
	switch state {
	case storage.AssetReferenceReviewOpen:
		if request.DecisionSHA256 != "" {
			return assetReferenceStateRequest{}, time.Time{}, errors.New("review request contains a decision digest")
		}
	case storage.AssetReferenceApproved, storage.AssetReferenceRejected, storage.AssetReferenceSuperseded:
		if !isLowerAssetSHA256(request.DecisionSHA256) {
			return assetReferenceStateRequest{}, time.Time{}, errors.New("invalid decision digest")
		}
	default:
		return assetReferenceStateRequest{}, time.Time{}, errors.New("unsupported asset reference state")
	}
	decidedAt, err := time.Parse(time.RFC3339Nano, request.DecidedAt)
	if err != nil || decidedAt.Location() != time.UTC || decidedAt.Format(time.RFC3339Nano) != request.DecidedAt ||
		!time.Unix(0, decidedAt.UnixNano()).Equal(decidedAt) {
		return assetReferenceStateRequest{}, time.Time{}, errors.New("invalid decision time")
	}
	return request, decidedAt, nil
}

func marshalCanonicalAssetReferenceStateRequest(request assetReferenceStateRequest) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(request); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func marshalCanonicalAssetReferenceStateDetail(detail assetReferenceStateDetail) (string, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(detail); err != nil {
		return "", err
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}

func assetReferenceStateEventID(candidateKey string, from, to storage.AssetReferenceState, issue int64, digest string) string {
	identity := strconv.Itoa(len(candidateKey)) + ":" + candidateKey + "\n" + string(from) + "\n" + string(to) + "\n" + strconv.FormatInt(issue, 10) + "\n" + digest
	return stableAssetPinID("asset-reference-state:v1\n" + identity)
}

func isLowerAssetSHA256(value string) bool {
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

func assetReferenceStateStoredValid(ref storage.AssetPinReference, candidateKey string) bool {
	if ref.CandidateKey != candidateKey ||
		ref.ReferenceKey != stableAssetPinID("asset-pin-reference:v1\n"+candidateKey) ||
		ref.ByteCount <= 0 || !isLowerAssetSHA256(ref.SHA256) ||
		ref.CreatedAt.IsZero() || ref.UpdatedAt.IsZero() || ref.UpdatedAt.Before(ref.CreatedAt) ||
		!time.Unix(0, ref.CreatedAt.UnixNano()).Equal(ref.CreatedAt) ||
		!time.Unix(0, ref.UpdatedAt.UnixNano()).Equal(ref.UpdatedAt) {
		return false
	}
	if _, err := canonicalAssetCID(ref.CID); err != nil {
		return false
	}
	expiryValid := func() bool {
		return !ref.ExpiresAt.IsZero() &&
			time.Unix(0, ref.ExpiresAt.UnixNano()).Equal(ref.ExpiresAt) &&
			!ref.ExpiresAt.Before(ref.UpdatedAt)
	}
	switch ref.State {
	case storage.AssetReferenceStaged:
		return ref.GitHubIssue == 0 && ref.DecisionSHA256 == "" && expiryValid()
	case storage.AssetReferenceReviewOpen:
		return ref.GitHubIssue > 0 && ref.DecisionSHA256 == "" && expiryValid()
	case storage.AssetReferenceApproved:
		return ref.GitHubIssue > 0 && isLowerAssetSHA256(ref.DecisionSHA256) && ref.ExpiresAt.IsZero()
	case storage.AssetReferenceRejected, storage.AssetReferenceSuperseded:
		if ref.GitHubIssue <= 0 || !isLowerAssetSHA256(ref.DecisionSHA256) || !expiryValid() {
			return false
		}
		wantExpiry := ref.UpdatedAt.Add(assetReferenceDecisionLifetime)
		return time.Unix(0, wantExpiry.UnixNano()).Equal(wantExpiry) && ref.ExpiresAt.Equal(wantExpiry)
	case storage.AssetReferenceAbandoned:
		return ref.GitHubIssue >= 0 && ref.DecisionSHA256 == "" && expiryValid()
	default:
		return false
	}
}

func (h *AssetPinHandler) writeAssetReferenceStateSuccess(w http.ResponseWriter, candidateKey, cidValue string, state storage.AssetReferenceState, expiresAt time.Time) {
	var expiry *time.Time
	if !expiresAt.IsZero() {
		normalized := normalizeAssetPinHandlerTime(expiresAt)
		expiry = &normalized
	}
	writeJSON(w, http.StatusOK, assetReferenceStateResponse{
		CandidateKey: candidateKey,
		CID:          cidValue,
		State:        state,
		ExpiresAt:    expiry,
	})
}

// ServeHTTP implements the bounded upload endpoint.
func (h *AssetPinHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	token, ok := assetBearerToken(r.Header)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	claims, err := h.verifier.VerifyAndConsume(r.Context(), token, assetpin.WorkflowPin)
	if err != nil {
		if errors.Is(err, assetpin.ErrMissingToken) ||
			errors.Is(err, assetpin.ErrInvalidToken) ||
			errors.Is(err, assetpin.ErrTokenReplay) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "asset pin authorization unavailable")
		return
	}
	select {
	case h.uploadSlots <- struct{}{}:
		defer func() { <-h.uploadSlots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "asset upload capacity unavailable")
		return
	}

	upload, status, err := h.readUpload(w, r)
	if err != nil {
		writeError(w, status, assetPinUploadErrorMessage(status))
		return
	}
	defer h.cleanupTempFile(upload.tempPath)

	metadata, canonicalMetadata, err := assetpin.ParseCanonicalMetadata(upload.metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset metadata")
		return
	}
	if !validAssetGLB(upload.header, upload.byteCount) || metadata.SHA256 != upload.sha256 {
		writeError(w, http.StatusUnprocessableEntity, "asset integrity validation failed")
		return
	}

	release, err := h.gate.Acquire(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin mutation unavailable")
		return
	}
	defer release()
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	now := normalizeAssetPinHandlerTime(h.clock())
	referenceKey := stableAssetPinID("asset-pin-reference:v1\n" + metadata.CandidateKey)
	eventID := stableAssetPinID("asset-pin-upsert:v1\n" + referenceKey)
	tokenDigest := stableAssetPinID(token)

	candidate, candidateExists, err := h.store.FindAssetPinReferenceByCandidateKey(r.Context(), metadata.CandidateKey)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	if candidateExists {
		cidValue, cidErr := canonicalAssetCID(candidate.CID)
		if cidErr != nil {
			writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
			return
		}
		if candidate.State != storage.AssetReferenceStaged || !assetPinCandidateMatches(
			candidate, referenceKey, metadata, canonicalMetadata, upload, now,
		) {
			writeError(w, http.StatusConflict, "asset candidate conflicts with an existing submission")
			return
		}
		if _, markerExists, markerErr := h.recovery.Load(referenceKey); markerErr != nil {
			writeError(w, http.StatusServiceUnavailable, "asset pin recovery unavailable")
			return
		} else if markerExists {
			if err := h.recovery.Remove(referenceKey); err != nil {
				h.reportCleanupFailure()
				writeError(w, http.StatusServiceUnavailable, "asset pin recovery unavailable")
				return
			}
		}
		h.writeAssetPinSuccess(w, cidValue, upload, true)
		return
	}
	if _, markerExists, markerErr := h.recovery.Load(referenceKey); markerErr != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin recovery unavailable")
		return
	} else if markerExists {
		writeError(w, http.StatusServiceUnavailable, "asset pin recovery pending")
		return
	}

	existing, alreadyExisted, err := h.store.FindAssetBySHA256(r.Context(), upload.sha256)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}

	cidValue := ""
	newlyPinned := false
	repinned := false
	if alreadyExisted {
		cidValue, err = canonicalAssetCID(existing.CID)
		if err != nil || (existing.SHA256 != "" && existing.SHA256 != upload.sha256) ||
			(existing.ByteCount != 0 && existing.ByteCount != upload.byteCount) {
			writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
			return
		}
		pinned, pinLookupErr := h.pinner.IsAssetCIDPinned(r.Context(), cidValue)
		if pinLookupErr != nil {
			writeError(w, http.StatusServiceUnavailable, "asset pin backend unavailable")
			return
		}
		if !pinned {
			available, capacityErr := h.capacity.AvailableBytes(h.config.KuboRepoPath)
			if capacityErr != nil {
				writeError(w, http.StatusServiceUnavailable, "asset capacity unavailable")
				return
			}
			if !assetPinCapacityAvailable(available, upload.byteCount, h.minFreeBytes) {
				writeError(w, http.StatusInsufficientStorage, "insufficient asset pin storage")
				return
			}
			observedCID, pinErr := h.pinner.PinAssetGLB(r.Context(), upload.tempPath)
			if pinErr != nil {
				writeError(w, http.StatusServiceUnavailable, "asset pin backend unavailable")
				return
			}
			observedCID, err = canonicalAssetCID(observedCID)
			if err != nil {
				writeError(w, http.StatusBadGateway, "asset pin backend failed")
				return
			}
			if observedCID != cidValue {
				// The observed CID may already be owned by an unrelated reference,
				// so this fail-closed path must not unpin it. The existing ledger row
				// still protects the expected CID; the mismatch is non-destructive
				// reconciliation work rather than a data-loss condition.
				writeError(w, http.StatusBadGateway, "asset pin backend failed")
				return
			}
			repinned = true
		}
	} else {
		available, capacityErr := h.capacity.AvailableBytes(h.config.KuboRepoPath)
		if capacityErr != nil {
			writeError(w, http.StatusServiceUnavailable, "asset capacity unavailable")
			return
		}
		if !assetPinCapacityAvailable(available, upload.byteCount, h.minFreeBytes) {
			writeError(w, http.StatusInsufficientStorage, "insufficient asset pin storage")
			return
		}
		expectedCID, calculateErr := h.pinner.CalculateAssetGLBCID(r.Context(), upload.tempPath)
		if calculateErr != nil {
			writeError(w, http.StatusServiceUnavailable, "asset pin backend unavailable")
			return
		}
		expectedCID, err = canonicalAssetCID(expectedCID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "asset pin backend failed")
			return
		}
		intent := assetpin.AssetPinRecoveryMarker{
			SchemaVersion: 1,
			Phase:         assetpin.AssetPinRecoveryIntent,
			ReferenceKey:  referenceKey,
			EventID:       eventID,
			CandidateKey:  metadata.CandidateKey,
			ExpectedCID:   expectedCID,
			SHA256:        upload.sha256,
			ByteCount:     upload.byteCount,
			SourceURL:     metadata.SourceURL,
			LicenseName:   metadata.LicenseName,
			Attribution:   metadata.Attribution,
			MetadataJSON:  string(canonicalMetadata),
			TokenDigest:   tokenDigest,
			Repository:    claims.Repository,
			Ref:           claims.Ref,
			WorkflowRef:   claims.WorkflowRef,
			Actor:         claims.Actor,
			WorkflowRunID: claims.RunID,
			RunAttempt:    claims.RunAttempt,
			CommitSHA:     claims.SHA,
			CreatedAt:     now,
			UpdatedAt:     now,
			ExpiresAt:     now.Add(assetPinStagedLifetime),
		}
		if err := h.recovery.CreateIntent(intent); err != nil {
			writeError(w, http.StatusServiceUnavailable, "asset pin recovery unavailable")
			return
		}
		observedCID, pinErr := h.pinner.PinAssetGLB(r.Context(), upload.tempPath)
		if pinErr != nil {
			writeError(w, http.StatusServiceUnavailable, "asset pin backend unavailable")
			return
		}
		observedCID, err = canonicalAssetCID(observedCID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "asset pin backend failed")
			return
		}
		if err := h.recovery.MarkPinned(referenceKey, observedCID); err != nil {
			if h.compensateAssetUnpin(r.Context(), observedCID) {
				if removeErr := h.recovery.Remove(referenceKey); removeErr != nil {
					h.reportCleanupFailure()
				}
			}
			writeError(w, http.StatusServiceUnavailable, "asset pin recovery unavailable")
			return
		}
		if observedCID != expectedCID {
			if h.compensateAssetUnpin(r.Context(), observedCID) {
				if removeErr := h.recovery.Remove(referenceKey); removeErr != nil {
					h.reportCleanupFailure()
				}
			}
			writeError(w, http.StatusBadGateway, "asset pin backend failed")
			return
		}
		cidValue = observedCID
		newlyPinned = true
	}

	result := "deduplicated"
	if newlyPinned {
		result = "pinned"
	} else if repinned {
		result = "repinned"
	}
	reference := storage.AssetPinReference{
		ReferenceKey:  referenceKey,
		CandidateKey:  metadata.CandidateKey,
		CID:           cidValue,
		SHA256:        upload.sha256,
		ByteCount:     upload.byteCount,
		State:         storage.AssetReferenceStaged,
		SourceURL:     metadata.SourceURL,
		LicenseName:   metadata.LicenseName,
		Attribution:   metadata.Attribution,
		MetadataJSON:  string(canonicalMetadata),
		WorkflowRunID: claims.RunID,
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(assetPinStagedLifetime),
	}
	event := storage.AssetPinAuditEvent{
		EventID:       eventID,
		Kind:          "asset_pin_upload",
		Result:        result,
		TokenDigest:   tokenDigest,
		Repository:    claims.Repository,
		Ref:           claims.Ref,
		WorkflowRef:   claims.WorkflowRef,
		Actor:         claims.Actor,
		WorkflowRunID: claims.RunID,
		RunAttempt:    claims.RunAttempt,
		CommitSHA:     claims.SHA,
		CandidateKey:  metadata.CandidateKey,
		ReferenceKey:  referenceKey,
		CID:           cidValue,
		SHA256:        upload.sha256,
		ByteCount:     upload.byteCount,
		OccurredAt:    now,
	}
	if err := h.store.UpsertAssetPinReference(r.Context(), reference, event); err != nil {
		if newlyPinned && !errors.Is(err, storage.ErrAssetPinLedgerRecoveryRequired) {
			if h.compensateAssetUnpin(r.Context(), cidValue) {
				if removeErr := h.recovery.Remove(referenceKey); removeErr != nil {
					h.reportCleanupFailure()
				}
			}
		}
		writeError(w, http.StatusServiceUnavailable, "asset pin ledger unavailable")
		return
	}
	if newlyPinned {
		if err := h.recovery.Remove(referenceKey); err != nil {
			h.reportCleanupFailure()
			writeError(w, http.StatusServiceUnavailable, "asset pin recovery unavailable")
			return
		}
	}
	h.writeAssetPinSuccess(w, cidValue, upload, alreadyExisted)
}

func (h *AssetPinHandler) writeAssetPinSuccess(w http.ResponseWriter, cidValue string, upload assetPinUpload, alreadyExisted bool) {
	gatewayURL, err := url.JoinPath(h.gatewayURL, cidValue)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset gateway unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, assetPinResponse{
		CID:            cidValue,
		SHA256:         upload.sha256,
		ByteLength:     upload.byteCount,
		GatewayURL:     gatewayURL,
		PinState:       string(storage.AssetReferenceStaged),
		AlreadyExisted: alreadyExisted,
	})
}

func (h *AssetPinHandler) confirmAssetUnpin(ctx context.Context, cidValue string) bool {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), assetPinCleanupTimeout)
	defer cancel()
	return h.pinner.UnpinAssetCID(cleanupContext, cidValue) == nil
}

func (h *AssetPinHandler) compensateAssetUnpin(ctx context.Context, cidValue string) bool {
	if h.confirmAssetUnpin(ctx, cidValue) {
		return true
	}
	h.reportCleanupFailure()
	return false
}

func assetPinCandidateMatches(candidate storage.AssetPinReference, referenceKey string, metadata assetpin.Metadata, canonicalMetadata []byte, upload assetPinUpload, now time.Time) bool {
	return candidate.ReferenceKey == referenceKey &&
		candidate.CandidateKey == metadata.CandidateKey &&
		candidate.SHA256 == upload.sha256 &&
		candidate.ByteCount == upload.byteCount &&
		candidate.SourceURL == metadata.SourceURL &&
		candidate.LicenseName == metadata.LicenseName &&
		candidate.Attribution == metadata.Attribution &&
		candidate.MetadataJSON == string(canonicalMetadata) &&
		(candidate.ExpiresAt.IsZero() || candidate.ExpiresAt.After(now))
}

type assetPinUpload struct {
	metadata  []byte
	tempPath  string
	header    [12]byte
	byteCount int64
	sha256    string
}

func (h *AssetPinHandler) readUpload(w http.ResponseWriter, r *http.Request) (upload assetPinUpload, status int, resultErr error) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(parameters["boundary"]) == "" {
		return upload, http.StatusBadRequest, errors.New("invalid multipart content type")
	}
	maxBodyBytes := h.maxUploadBytes + assetPinBodyOverheadBytes
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	reader := multipart.NewReader(body, parameters["boundary"])
	seenMetadata := false
	seenFile := false
	success := false
	defer func() {
		if !success && upload.tempPath != "" {
			h.cleanupTempFile(upload.tempPath)
		}
	}()

	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return upload, assetPinReadErrorStatus(nextErr, http.StatusBadRequest), nextErr
		}
		name := part.FormName()
		switch name {
		case "metadata":
			if seenMetadata || part.FileName() != "" {
				_ = part.Close()
				return upload, http.StatusBadRequest, errors.New("duplicate or invalid metadata part")
			}
			seenMetadata = true
			metadata, readErr := io.ReadAll(io.LimitReader(part, assetPinMetadataMaxBytes+1))
			closeErr := part.Close()
			if readErr != nil {
				return upload, assetPinReadErrorStatus(readErr, http.StatusBadRequest), readErr
			}
			if closeErr != nil {
				return upload, assetPinReadErrorStatus(closeErr, http.StatusBadRequest), closeErr
			}
			if int64(len(metadata)) > assetPinMetadataMaxBytes {
				return upload, http.StatusRequestEntityTooLarge, errors.New("metadata exceeds limit")
			}
			upload.metadata = metadata
		case "file":
			if seenFile || strings.TrimSpace(part.FileName()) == "" {
				_ = part.Close()
				return upload, http.StatusBadRequest, errors.New("duplicate or invalid file part")
			}
			seenFile = true
			tempDir, tempDirErr := assetpin.SecureAssetPinTempDir(h.dataDir)
			if tempDirErr != nil {
				_ = part.Close()
				return upload, http.StatusServiceUnavailable, tempDirErr
			}
			file, createErr := os.CreateTemp(tempDir, "asset-*.glb")
			if createErr != nil {
				_ = part.Close()
				return upload, http.StatusServiceUnavailable, createErr
			}
			upload.tempPath = file.Name()
			if chmodErr := file.Chmod(0o600); chmodErr != nil {
				_ = file.Close()
				_ = part.Close()
				return upload, http.StatusServiceUnavailable, chmodErr
			}
			hasher := sha256.New()
			count, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(part, h.maxUploadBytes+1))
			partCloseErr := part.Close()
			if count >= 12 {
				_, _ = file.ReadAt(upload.header[:], 0)
			}
			fileCloseErr := file.Close()
			if copyErr != nil {
				return upload, assetPinReadErrorStatus(copyErr, http.StatusServiceUnavailable), copyErr
			}
			if partCloseErr != nil {
				return upload, assetPinReadErrorStatus(partCloseErr, http.StatusBadRequest), partCloseErr
			}
			if fileCloseErr != nil {
				return upload, http.StatusServiceUnavailable, fileCloseErr
			}
			if count > h.maxUploadBytes {
				return upload, http.StatusRequestEntityTooLarge, errors.New("file exceeds limit")
			}
			upload.byteCount = count
			upload.sha256 = hex.EncodeToString(hasher.Sum(nil))
		default:
			_ = part.Close()
			return upload, http.StatusBadRequest, errors.New("unknown multipart part")
		}
	}
	if !seenMetadata || !seenFile {
		return upload, http.StatusBadRequest, errors.New("required multipart part is missing")
	}
	success = true
	return upload, 0, nil
}

func (h *AssetPinHandler) cleanupTempFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := h.removeTempFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.reportCleanupFailure()
	}
}

type assetPinResponse struct {
	CID            string `json:"cid"`
	SHA256         string `json:"sha256"`
	ByteLength     int64  `json:"byteLength"`
	GatewayURL     string `json:"gatewayUrl"`
	PinState       string `json:"pinState"`
	AlreadyExisted bool   `json:"alreadyExisted"`
}

func validAssetGLB(header [12]byte, byteCount int64) bool {
	return byteCount >= 12 &&
		string(header[0:4]) == "glTF" &&
		binary.LittleEndian.Uint32(header[4:8]) == 2 &&
		int64(binary.LittleEndian.Uint32(header[8:12])) == byteCount
}

func assetBearerToken(header http.Header) (string, bool) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	value := values[0]
	if len(value) <= len("Bearer ") || !strings.EqualFold(value[:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := value[len("Bearer "):]
	if strings.IndexAny(token, " \t\r\n\v\f") >= 0 {
		return "", false
	}
	return token, true
}

func assetPinCapacityAvailable(available uint64, byteCount int64, minFree uint64) bool {
	if byteCount < 0 || uint64(byteCount) > available {
		return false
	}
	return available-uint64(byteCount) >= minFree
}

func canonicalAssetCID(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := cid.Decode(value)
	if err != nil || parsed.Version() != 1 || parsed.String() != value {
		return "", errors.New("invalid canonical CIDv1")
	}
	return value, nil
}

func stableAssetPinID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeAssetPinHandlerTime(value time.Time) time.Time {
	return time.Unix(0, value.UnixNano()).UTC()
}

func assetPinReadErrorStatus(err error, fallback int) int {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) || strings.Contains(err.Error(), "request body too large") {
		return http.StatusRequestEntityTooLarge
	}
	return fallback
}

func assetPinUploadErrorMessage(status int) string {
	switch status {
	case http.StatusRequestEntityTooLarge:
		return "asset upload exceeds limit"
	case http.StatusServiceUnavailable:
		return "asset upload storage unavailable"
	default:
		return "invalid asset upload"
	}
}

func validateAssetGatewayURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("asset pin gateway must be an HTTPS base URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

type statFSAssetPinCapacity struct{}

func (statFSAssetPinCapacity) AvailableBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, errors.New("statfs returned a non-positive block size")
	}
	blocks := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blocks > math.MaxUint64/blockSize {
		return math.MaxUint64, nil
	}
	return blocks * blockSize, nil
}

// KuboAssetPinner binds the generic asset pinner contract to one Kubo API.
type KuboAssetPinner struct {
	apiURL    string
	retention *assetpin.KuboRetentionClient
}

// NewKuboAssetPinner constructs the production storage-backed asset pinner.
func NewKuboAssetPinner(apiURL string) (*KuboAssetPinner, error) {
	parsed, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("Kubo API URL must be an absolute HTTP URL")
	}
	canonicalURL := strings.TrimRight(parsed.String(), "/")
	retention, err := assetpin.NewKuboRetentionClient(canonicalURL)
	if err != nil {
		return nil, err
	}
	return &KuboAssetPinner{apiURL: canonicalURL, retention: retention}, nil
}

func (p *KuboAssetPinner) IsAssetCIDPinned(ctx context.Context, cidValue string) (bool, error) {
	if p == nil || p.retention == nil {
		return false, errors.New("Kubo asset pinner is required")
	}
	return p.retention.IsAssetCIDPinned(ctx, cidValue)
}

func (p *KuboAssetPinner) PinAssetGLB(ctx context.Context, path string) (string, error) {
	return storage.PinAssetGLB(ctx, p.apiURL, path)
}

func (p *KuboAssetPinner) CalculateAssetGLBCID(ctx context.Context, path string) (string, error) {
	return storage.CalculateAssetGLBCID(ctx, p.apiURL, path)
}

func (p *KuboAssetPinner) UnpinAssetCID(ctx context.Context, cidValue string) error {
	return storage.UnpinAssetCID(ctx, p.apiURL, cidValue)
}
