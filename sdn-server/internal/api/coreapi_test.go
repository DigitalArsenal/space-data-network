package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
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
	for _, field := range []string{"connected_peers", "total_records", "total_bytes", "schemas", "sources"} {
		if _, ok := body[field]; !ok {
			t.Errorf("field %q missing from stats response", field)
		}
	}
	// sources is always present (possibly empty) so generic clients can rely on
	// it without a shape guard.
	if _, ok := body["sources"].([]interface{}); !ok {
		t.Errorf("sources field is not an array, got %T", body["sources"])
	}
}

// TestCoreAPI_Stats_PeersBlockSplitsSDNFromIPFS proves the anonymous stats
// surface reports the two peer populations separately: peers.ipfs is the raw
// libp2p/DHT swarm connection count, peers.sdn is the count of connected peers
// that are actual Space Data Network nodes (with peers.sdn_known covering the
// wider observed set, incl. advertisement-discovered nodes not connected now).
// The legacy connected_peers field must keep meaning the IPFS swarm count.
func TestCoreAPI_Stats_PeersBlockSplitsSDNFromIPFS(t *testing.T) {
	// A real two-host swarm: the client holds exactly one libp2p connection,
	// so the IPFS count is 1 and comes from the host, not from the SDN source.
	server, err := libp2p.New()
	if err != nil {
		t.Fatalf("create server host: %v", err)
	}
	defer server.Close()
	client, err := libp2p.New()
	if err != nil {
		t.Fatalf("create client host: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}

	h := &CoreAPIHandler{
		peerID:  client.ID(),
		h2pHost: client,
		rl:      newRateLimiter(),
	}
	// The node's real SDN peer state (epm.CountSDNPeers) is injected here; the
	// swarm peer is a plain libp2p host, so a truthful counter reports 0 SDN
	// peers connected while the IPFS count is 1.
	h.SetSDNPeerCounter(func() SDNPeerCounts { return SDNPeerCounts{Connected: 0, Known: 2} })

	mux := newTestCoreAPIMux(h)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ConnectedPeers int `json:"connected_peers"`
		Peers          struct {
			IPFS     *int `json:"ipfs"`
			SDN      *int `json:"sdn"`
			SDNKnown *int `json:"sdn_known"`
		} `json:"peers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Peers.IPFS == nil || body.Peers.SDN == nil || body.Peers.SDNKnown == nil {
		t.Fatalf("peers block missing ipfs/sdn/sdn_known: %+v", body.Peers)
	}
	if *body.Peers.IPFS != 1 {
		t.Errorf("peers.ipfs = %d, want 1 (the live swarm connection)", *body.Peers.IPFS)
	}
	if body.ConnectedPeers != *body.Peers.IPFS {
		t.Errorf("connected_peers = %d, must stay equal to peers.ipfs = %d (backward compat)", body.ConnectedPeers, *body.Peers.IPFS)
	}
	if *body.Peers.SDN != 0 {
		t.Errorf("peers.sdn = %d, want 0 (the swarm peer is not an SDN node)", *body.Peers.SDN)
	}
	if *body.Peers.SDNKnown != 2 {
		t.Errorf("peers.sdn_known = %d, want 2 (from the injected SDN peer state)", *body.Peers.SDNKnown)
	}
}

// TestCoreAPI_Stats_PeersBlockWithoutSDNSource proves the peers block is always
// present and zero-valued when no SDN peer-state source is wired (e.g. a node
// without a peer registry), so the board never has to shape-guard it.
func TestCoreAPI_Stats_PeersBlockWithoutSDNSource(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Peers struct {
			IPFS     *int `json:"ipfs"`
			SDN      *int `json:"sdn"`
			SDNKnown *int `json:"sdn_known"`
		} `json:"peers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Peers.IPFS == nil || body.Peers.SDN == nil || body.Peers.SDNKnown == nil {
		t.Fatalf("peers block missing ipfs/sdn/sdn_known: %+v", body.Peers)
	}
	if *body.Peers.IPFS != 0 || *body.Peers.SDN != 0 || *body.Peers.SDNKnown != 0 {
		t.Errorf("peers = %+v, want all zero with no host and no SDN source", body.Peers)
	}
}

// TestCoreAPI_Stats_IncludesSourceBatchProgress proves the anonymous stats
// surface carries per-(schema, provider, source, batch) live pipeline progress:
// a rising record count plus first/last record arrival timestamps, without
// adding schema-specific interpretations.
func TestCoreAPI_Stats_IncludesSourceBatchProgress(t *testing.T) {
	store, _, validator := newDataAPITestStoreWithBasePath(t)
	storeDataAPITestOMMWithBatch(t, store, 25544, "ISS (ZARYA)", "2026-05-05", "batch-alpha")
	storeDataAPITestOMMWithBatch(t, store, 56775, "SATELLITE-6292", "2026-05-06", "batch-alpha")

	h := &CoreAPIHandler{
		peerID:    peer.ID("12D3KooWTestPeerID"),
		store:     store,
		validator: validator,
		rl:        newRateLimiter(),
	}
	mux := newTestCoreAPIMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		TotalRecords int64 `json:"total_records"`
		Sources      []struct {
			Schema     string `json:"schema"`
			ProviderID string `json:"provider_id"`
			SourceName string `json:"source_name"`
			BatchID    string `json:"batch_id"`
			Count      int64  `json:"count"`
			FirstSeen  string `json:"first_seen"`
			LastSeen   string `json:"last_seen"`
			UpdatedAt  string `json:"updated_at"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var raw struct {
		Sources []map[string]interface{} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw sources: %v", err)
	}
	for _, source := range raw.Sources {
		for _, field := range []string{"objects", "latest_epoch", "mean_rms", "min_rms", "max_rms"} {
			if _, ok := source[field]; ok {
				t.Errorf("generic source progress unexpectedly exposes schema-specific field %q: %+v", field, source)
			}
		}
	}
	if body.TotalRecords < 2 {
		t.Fatalf("total_records = %d, want >= 2", body.TotalRecords)
	}

	var found bool
	for _, s := range body.Sources {
		if s.Schema == "OMM.fbs" && s.ProviderID == "space-data-network-02" &&
			s.SourceName == "catalogfixture-gp" && s.BatchID == "batch-alpha" {
			found = true
			if s.Count != 2 {
				t.Fatalf("OMM batch-alpha count = %d, want 2 (rising pull progress)", s.Count)
			}
			if s.FirstSeen == "" || s.LastSeen == "" {
				t.Fatalf("progress row missing timestamps: first_seen=%q last_seen=%q", s.FirstSeen, s.LastSeen)
			}
			if _, err := time.Parse(time.RFC3339, s.FirstSeen); err != nil {
				t.Fatalf("first_seen %q is not RFC3339: %v", s.FirstSeen, err)
			}
			if _, err := time.Parse(time.RFC3339, s.LastSeen); err != nil {
				t.Fatalf("last_seen %q is not RFC3339: %v", s.LastSeen, err)
			}
		}
	}
	if !found {
		t.Fatalf("no OMM batch-alpha progress row in sources: %+v", body.Sources)
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

// ---------------------------------------------------------------------------
// G.2: flow-claimed peer routes — the native read surface yields to the
// mounted discovery flow; the admin control plane stays native via
// method-scoped patterns (no mux conflict with the flow's subtree).
// ---------------------------------------------------------------------------

func TestCoreAPI_RegisterRoutes_FlowClaimsPeers(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := http.NewServeMux()

	// The flow mount owns the peers surface (subtree + exact alias), as
	// RegisterFlowMounts does for a /api/v1/peers/ config mount.
	flowHits := 0
	flowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flowHits++
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/api/v1/peers/", flowHandler)
	mux.Handle("/api/v1/peers", flowHandler)

	// Must NOT panic on duplicate patterns and must keep the admin verbs.
	h.RegisterRoutesWithFlowMounts(mux, func(path string) bool {
		return path == "/api/v1/peers" || path == "/api/v1/peers/"
	})

	// GET peers goes to the flow.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil))
	if rec.Code != http.StatusOK || flowHits != 1 {
		t.Fatalf("GET peers: code=%d flowHits=%d", rec.Code, flowHits)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/peers/16Uiu2X", nil))
	if rec.Code != http.StatusOK || flowHits != 2 {
		t.Fatalf("GET peers/{id}: code=%d flowHits=%d", rec.Code, flowHits)
	}

	// POST connect stays native (h2pHost is nil -> 503 from the native
	// handler, proving the native route matched, not the flow).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/peers/connect", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST connect: code=%d (want native 503), flowHits=%d", rec.Code, flowHits)
	}

	// DELETE peers/{id} stays native (h2pHost is nil in this fixture, so
	// the native disconnect handler answers 503 — the flow would have 200'd).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/peers/not-a-peer-id", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE peers/{id}: code=%d (want native 503)", rec.Code)
	}
	if flowHits != 2 {
		t.Fatalf("admin verbs leaked to the flow: flowHits=%d", flowHits)
	}
}

func TestCoreAPI_RegisterRoutes_NoFlowKeepsLegacyPeers(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutesWithFlowMounts(mux, nil)

	// Legacy native listing answers (empty host -> {"peers":[]}).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET peers: code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"peers"`) {
		t.Fatalf("legacy envelope missing: %q", rec.Body.String())
	}
}
