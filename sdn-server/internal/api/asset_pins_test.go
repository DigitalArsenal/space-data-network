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

const testAssetCID = "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e"

type fakeAssetPinVerifier struct {
	mu     sync.Mutex
	claims assetpin.Claims
	err    error
	calls  int
	token  string
	kind   assetpin.WorkflowKind
}

func (f *fakeAssetPinVerifier) VerifyAndConsume(_ context.Context, token string, kind assetpin.WorkflowKind) (assetpin.Claims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.token = token
	f.kind = kind
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
	events             []storage.AssetPinAuditEvent
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
	defer f.mu.Unlock()
	f.candidateFindCalls++
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
	mu         sync.Mutex
	cid        string
	pinErr     error
	unpinErr   error
	pinCalls   int
	unpinCalls int
	path       string
	unpinned   string
	pinFunc    func(context.Context, string) (string, error)
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
	pinner := &fakeAssetPinner{cid: testAssetCID}
	recovery := &fakeAssetPinRecoveryStore{markers: make(map[string]assetpin.AssetPinRecoveryMarker)}
	dataDir := t.TempDir()
	options := AssetPinHandlerOptions{
		Verifier: verifier,
		Store:    store,
		Capacity: capacity,
		Pinner:   pinner,
		Recovery: recovery,
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

	t.Run("Kubo failure", func(t *testing.T) {
		rig := newAssetPinTestRig(t, nil)
		rig.pinner.pinErr = errors.New("kubo failed")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502; body=%s", response.Code, response.Body.String())
		}
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
		if response.Code != http.StatusServiceUnavailable || rig.pinner.pinCalls != 0 || len(rig.store.upserts) != 0 {
			t.Fatalf("status/pin/upsert = %d/%d/%d; body=%s", response.Code, rig.pinner.pinCalls, len(rig.store.upserts), response.Body.String())
		}
	})

	t.Run("Kubo uncertainty retains intent", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-kubo-uncertain")
		rig := newAssetPinTestRig(t, nil)
		rig.pinner.pinErr = errors.New("Kubo response lost")
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusBadGateway || !ok || marker.Phase != assetpin.AssetPinRecoveryIntent || marker.CID != "" {
			t.Fatalf("status/marker = %d/%+v/%v; body=%s", response.Code, marker, ok, response.Body.String())
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
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 1 || !ok || marker.Phase != assetpin.AssetPinRecoveryPinnedUncommitted || marker.CID != testAssetCID {
			t.Fatalf("status/unpin/marker = %d/%d/%+v/%v; body=%s", response.Code, rig.pinner.unpinCalls, marker, ok, response.Body.String())
		}
	})

	t.Run("recovery-required retains marker without unpin", func(t *testing.T) {
		_, parts, referenceKey := newRequest(t, "candidate-recovery-required-marker")
		rig := newAssetPinTestRig(t, nil)
		rig.store.upsertErr = fmt.Errorf("uncertain commit: %w", storage.ErrAssetPinLedgerRecoveryRequired)
		response := serveAssetPin(rig.handler, testMultipartRequest(t, parts))
		marker, ok := rig.recovery.marker(referenceKey)
		if response.Code != http.StatusServiceUnavailable || rig.pinner.unpinCalls != 0 || !ok || marker.Phase != assetpin.AssetPinRecoveryPinnedUncommitted {
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
	metadata := testCanonicalMetadataWithAttribution(glb, candidateKey, "Artist <Name>")
	rig := newAssetPinTestRig(t, nil)
	requestContextKey := struct{}{}
	req := testMultipartRequest(t, []testMultipartPart{{name: "metadata", data: metadata}, {name: "file", filename: "asset.glb", data: glb}})
	req = req.WithContext(context.WithValue(req.Context(), requestContextKey, "present"))
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
	if len(rig.store.upserts) != 1 || len(rig.store.events) != 1 {
		t.Fatalf("upserts/events = %d/%d, want 1/1", len(rig.store.upserts), len(rig.store.events))
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
		if rig.store.findCalls != 0 || rig.capacity.calls != 0 || rig.pinner.pinCalls != 0 || len(rig.store.upserts) != 0 {
			t.Fatalf("retry side effects find/capacity/pin/upsert = %d/%d/%d/%d", rig.store.findCalls, rig.capacity.calls, rig.pinner.pinCalls, len(rig.store.upserts))
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
	if rig.pinner.pinCalls != 1 || len(rig.store.upserts) != 1 || len(rig.store.events) != 1 {
		t.Fatalf("pin/upsert/event = %d/%d/%d, want 1/1/1", rig.pinner.pinCalls, len(rig.store.upserts), len(rig.store.events))
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
	if rig.capacity.calls != 0 || rig.pinner.pinCalls != 0 || rig.pinner.unpinCalls != 0 {
		t.Fatalf("dedup capacity/pin/unpin = %d/%d/%d, want 0/0/0", rig.capacity.calls, rig.pinner.pinCalls, rig.pinner.unpinCalls)
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
		if response.Code != http.StatusBadGateway || len(rig.store.upserts) != 0 {
			t.Fatalf("status/upserts = %d/%d; body=%s", response.Code, len(rig.store.upserts), response.Body.String())
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
		Config: cfg, DataDir: t.TempDir(),
	}
	tests := []struct {
		name   string
		mutate func(*AssetPinHandlerOptions)
	}{
		{name: "verifier", mutate: func(o *AssetPinHandlerOptions) { o.Verifier = nil }},
		{name: "store", mutate: func(o *AssetPinHandlerOptions) { o.Store = nil }},
		{name: "pinner", mutate: func(o *AssetPinHandlerOptions) { o.Pinner = nil }},
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
