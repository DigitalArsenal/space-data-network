package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// fakePublisher implements topicPublisher for tests.
type fakePublisher struct {
	lastSchema string
	lastTopic  string
	lastData   []byte
	err        error
}

func (f *fakePublisher) Publish(schema string, data []byte) error {
	f.lastSchema = schema
	f.lastTopic = sdnpubsub.TopicName(schema)
	f.lastData = data
	return f.err
}

func (f *fakePublisher) PublishToTopic(_ context.Context, topic string, data []byte) error {
	f.lastTopic = topic
	f.lastData = data
	return f.err
}

// newTestCoreAPIHandler creates a CoreAPIHandler with no real libp2p or node
// dependencies, using an in-memory store.
func newTestCoreAPIHandler(t *testing.T) (*CoreAPIHandler, *fakePublisher) {
	t.Helper()
	store, _, validator := newDataAPITestStoreWithBasePath(t)
	pub := &fakePublisher{}
	h := &CoreAPIHandler{
		peerID:      peer.ID("12D3KooWTestPeerID"),
		h2pHost:     nil, // tested in peer-specific tests
		pubsubSvc:   nil,
		publisher:   pub,
		store:       store,
		validator:   validator,
		cfg:         nil,
		authHandler: nil,
		rl:          newRateLimiter(),
		listenAddrs: func() []multiaddr.Multiaddr {
			ma, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/5000")
			return []multiaddr.Multiaddr{ma}
		},
	}
	return h, pub
}

// newTestCoreAPIMux registers routes without auth — suitable for public-endpoint tests.
func newTestCoreAPIMux(h *CoreAPIHandler) *http.ServeMux {
	mux := http.NewServeMux()
	// Register manually without auth wrappers for simplicity.
	mux.HandleFunc("/api/v1/id", h.withRL(h.handleID))
	mux.HandleFunc("/api/v1/version", h.withRL(h.handleVersion))
	mux.HandleFunc("/api/v1/stats", h.withRL(h.handleStats))
	mux.HandleFunc("/api/v1/pubsub/topics", h.withRL(h.handleTopics))
	mux.HandleFunc("/api/v1/pubsub/publish", h.withRL(h.handlePubSubPublish))
	mux.HandleFunc("/api/v1/pubsub/messages", h.withRL(h.handlePubSubMessages))
	mux.HandleFunc("/api/v1/peers", h.withRL(h.handlePeers))
	return mux
}

// ---------------------------------------------------------------------------
// GET /api/v1/id
// ---------------------------------------------------------------------------

func TestCoreAPI_ID_ReturnsExpectedFields(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got, ok := body["peer_id"]; !ok || got == "" {
		t.Error("peer_id missing or empty")
	}
	if got, ok := body["agent_version"]; !ok || got != versioninfo.AgentVersion {
		t.Errorf("agent_version = %v, want %q", got, versioninfo.AgentVersion)
	}
	if got, ok := body["suite_version"]; !ok || got != versioninfo.SuiteVersion {
		t.Errorf("suite_version = %v, want %q", got, versioninfo.SuiteVersion)
	}
	if got, ok := body["standards_version"]; !ok || got != versioninfo.SpaceDataStandardsVersion {
		t.Errorf("standards_version = %v, want %q", got, versioninfo.SpaceDataStandardsVersion)
	}
	if _, ok := body["listen_addresses"]; !ok {
		t.Error("listen_addresses missing")
	}
}

func TestCoreAPI_ID_MethodNotAllowed(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	assertCoreAPIErrorCode(t, rec.Body.Bytes(), "METHOD_NOT_ALLOWED")
}

// ---------------------------------------------------------------------------
// GET /api/v1/version
// ---------------------------------------------------------------------------

func TestCoreAPI_Version_ReturnsVersionFields(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"agent_version", "suite_version", "standards_version"} {
		if _, ok := body[field]; !ok {
			t.Errorf("field %q missing from version response", field)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats
// ---------------------------------------------------------------------------

func TestCoreAPI_Stats_EmptyStore(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"connected_peers", "total_records", "total_bytes", "schemas"} {
		if _, ok := body[field]; !ok {
			t.Errorf("field %q missing from stats response", field)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/pubsub/topics
// ---------------------------------------------------------------------------

func TestCoreAPI_Topics_NoPubSub_ReturnsEmptyList(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	// pubsubSvc is nil — should return empty list, not error.
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pubsub/topics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	topics, ok := body["topics"].([]interface{})
	if !ok {
		t.Fatalf("topics field is not array, got: %T", body["topics"])
	}
	if len(topics) != 0 {
		t.Errorf("expected empty topics, got %v", topics)
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/pubsub/publish
// ---------------------------------------------------------------------------

func TestCoreAPI_PubSubPublish_ValidData(t *testing.T) {
	h, pub := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	// Build a minimal valid OMM FlatBuffer.
	ommBytes := buildMinimalOMM(t)
	encoded := base64.StdEncoding.EncodeToString(ommBytes)

	body, _ := json.Marshal(map[string]string{
		"schema": "OMM.fbs",
		"data":   encoded,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pubsub/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if pub.lastTopic == "" {
		t.Error("expected pubsub publish to be called")
	}
	if pub.lastSchema != "OMM.fbs" {
		t.Errorf("schema publish = %q, want OMM.fbs", pub.lastSchema)
	}
	if pub.lastTopic != "/spacedatanetwork/sds/OMM.fbs" {
		t.Errorf("topic = %q, want /spacedatanetwork/sds/OMM.fbs", pub.lastTopic)
	}
}

func TestCoreAPI_PubSubPublish_MissingSchema(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	body, _ := json.Marshal(map[string]string{"data": "dGVzdA=="})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pubsub/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCoreAPIErrorCode(t, rec.Body.Bytes(), "INVALID_REQUEST")
}

func TestCoreAPI_PubSubPublish_InvalidBase64(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	body, _ := json.Marshal(map[string]string{"schema": "OMM.fbs", "data": "!!not-base64!!"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pubsub/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCoreAPIErrorCode(t, rec.Body.Bytes(), "INVALID_DATA")
}

// ---------------------------------------------------------------------------
// GET /api/v1/pubsub/messages
// ---------------------------------------------------------------------------

func TestCoreAPI_PubSubMessages_MissingSchema(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pubsub/messages", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCoreAPIErrorCode(t, rec.Body.Bytes(), "INVALID_REQUEST")
}

func TestCoreAPI_PubSubMessages_EmptyResult(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pubsub/messages?schema=OMM.fbs&limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["schema"] != "OMM.fbs" {
		t.Errorf("schema = %v, want OMM.fbs", body["schema"])
	}
	records, ok := body["records"].([]interface{})
	if !ok {
		t.Fatalf("records is not array: %T", body["records"])
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/peers
// ---------------------------------------------------------------------------

func TestCoreAPI_Peers_NoHost_ReturnsEmpty(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	peerList, ok := body["peers"].([]interface{})
	if !ok {
		t.Fatalf("peers is not array: %T", body["peers"])
	}
	if len(peerList) != 0 {
		t.Errorf("expected 0 peers, got %d", len(peerList))
	}
}

// ---------------------------------------------------------------------------
// Rate limiter
// ---------------------------------------------------------------------------

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := newRateLimiter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/id", nil)
	req.RemoteAddr = "192.168.1.1:12345"

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		if !rl.Allow(rec, req) {
			t.Fatalf("request %d was rate-limited unexpectedly", i+1)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := newRateLimiter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/id", nil)
	req.RemoteAddr = "10.0.0.1:9999"

	// Exhaust the bucket.
	for i := 0; i < readLimitPerMin; i++ {
		rec := httptest.NewRecorder()
		rl.Allow(rec, req)
	}

	// The next request should be blocked.
	rec := httptest.NewRecorder()
	if rl.Allow(rec, req) {
		t.Fatal("expected rate limiter to block request after exhausting tokens")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
}

func TestRateLimiter_XRateLimitHeaders(t *testing.T) {
	rl := newRateLimiter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/id", nil)
	req.RemoteAddr = "172.16.0.1:1234"

	rec := httptest.NewRecorder()
	if !rl.Allow(rec, req) {
		t.Fatal("first request should be allowed")
	}

	if got := rec.Header().Get("X-RateLimit-Limit"); got == "" {
		t.Error("X-RateLimit-Limit header missing")
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got == "" {
		t.Error("X-RateLimit-Remaining header missing")
	}
	if got := rec.Header().Get("X-RateLimit-Reset"); got == "" {
		t.Error("X-RateLimit-Reset header missing")
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.1")
	if got := clientIP(req); got != "203.0.113.1" {
		t.Errorf("clientIP = %q, want 203.0.113.1", got)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:9876"
	if got := clientIP(req); got != "192.168.1.50" {
		t.Errorf("clientIP = %q, want 192.168.1.50", got)
	}
}

// ---------------------------------------------------------------------------
// writeCoreAPIError
// ---------------------------------------------------------------------------

func TestWriteCoreAPIError_Format(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCoreAPIError(rec, http.StatusBadRequest, "INVALID_REQUEST", "something went wrong")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCoreAPIErrorCode(t, rec.Body.Bytes(), "INVALID_REQUEST")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// assertCoreAPIErrorCode parses a core-API error response and checks the code field.
func assertCoreAPIErrorCode(t *testing.T, body []byte, wantCode string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse error response: %v (body=%s)", err, body)
	}
	if resp.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q (message: %s)", resp.Error.Code, wantCode, resp.Error.Message)
	}
}

// buildMinimalOMM returns a valid OMM FlatBuffer (size-prefixed) for testing.
// Reuses the helper already available in the package-level test helpers.
func buildMinimalOMM(t *testing.T) []byte {
	t.Helper()
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	return storeDataAPITestOMM(t, store, 25544, "ISS (ZARYA)", "2026-05-05")
}
