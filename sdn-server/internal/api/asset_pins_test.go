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
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const testAssetCID = "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e"

type fakeAssetPinVerifier struct {
	claims assetpin.Claims
	err    error
	calls  int
	token  string
	kind   assetpin.WorkflowKind
}

func (f *fakeAssetPinVerifier) VerifyAndConsume(_ context.Context, token string, kind assetpin.WorkflowKind) (assetpin.Claims, error) {
	f.calls++
	f.token = token
	f.kind = kind
	return f.claims, f.err
}

type fakeAssetPinStore struct {
	foundRef  storage.AssetPinReference
	found     bool
	findErr   error
	upsertErr error
	findCalls int
	upserts   []storage.AssetPinReference
	events    []storage.AssetPinAuditEvent
}

func (f *fakeAssetPinStore) FindAssetBySHA256(_ context.Context, _ string) (storage.AssetPinReference, bool, error) {
	f.findCalls++
	return f.foundRef, f.found, f.findErr
}

func (f *fakeAssetPinStore) UpsertAssetPinReference(_ context.Context, ref storage.AssetPinReference, event storage.AssetPinAuditEvent) error {
	f.upserts = append(f.upserts, ref)
	f.events = append(f.events, event)
	return f.upsertErr
}

type fakeAssetPinCapacity struct {
	available uint64
	err       error
	calls     int
	path      string
}

func (f *fakeAssetPinCapacity) AvailableBytes(path string) (uint64, error) {
	f.calls++
	f.path = path
	return f.available, f.err
}

type fakeAssetPinner struct {
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
	f.pinCalls++
	f.path = path
	if f.pinFunc != nil {
		return f.pinFunc(ctx, path)
	}
	return f.cid, f.pinErr
}

func (f *fakeAssetPinner) UnpinAssetCID(_ context.Context, cid string) error {
	f.unpinCalls++
	f.unpinned = cid
	return f.unpinErr
}

type assetPinTestRig struct {
	handler  *AssetPinHandler
	verifier *fakeAssetPinVerifier
	store    *fakeAssetPinStore
	capacity *fakeAssetPinCapacity
	pinner   *fakeAssetPinner
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
	store := &fakeAssetPinStore{}
	capacity := &fakeAssetPinCapacity{available: 1 << 30}
	pinner := &fakeAssetPinner{cid: testAssetCID}
	dataDir := t.TempDir()
	options := AssetPinHandlerOptions{
		Verifier: verifier,
		Store:    store,
		Capacity: capacity,
		Pinner:   pinner,
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
		dataDir: dataDir, now: now,
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

func assertNoAssetPinTempFiles(t *testing.T, dataDir string) {
	t.Helper()
	err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
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
