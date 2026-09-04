package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdstre "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRE"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// fixture: eve -> alice -> dave; bob -> dave; stranger -> dave.
func newTrustFixture(t *testing.T) *trust.Service {
	t.Helper()
	g := trust.NewGraph()
	for _, e := range []trust.Edge{
		{Truster: "eve", Trustee: "alice", Weight: 0.9},
		{Truster: "alice", Trustee: "dave", Weight: 0.6},
		{Truster: "bob", Trustee: "dave", Weight: 0.5},
		{Truster: "stranger", Trustee: "dave", Weight: 1.0},
	} {
		if err := g.SetEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	svc := trust.NewService(g, map[string][]trust.FundHolding{
		"dave":     {{Type: trust.FundStablecoin, Location: "0xdave", Amount: 50_000}},
		"alice":    {{Type: trust.FundStablecoin, Location: "0xalice", Amount: 100_000}},
		"stranger": {{Type: trust.FundStablecoin, Location: "0xs", Amount: 1_000_000}},
	})
	svc.TrackEvaluator("eve")
	return svc
}

func newTrustServer(t *testing.T, h *TrustHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func signedTREFixture(t *testing.T, record trust.EdgeRecord, privateKey ed25519.PrivateKey) []byte {
	t.Helper()
	payload, err := trust.EdgeSigningPayload(record)
	if err != nil {
		t.Fatal(err)
	}
	record.ProviderSignature = ed25519.Sign(privateKey, payload)
	frame, err := trust.EncodeEdgeFrame(record)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func unsignedTREFixture(record trust.EdgeRecord) []byte {
	b := flatbuffers.NewBuilder(256)
	edgeID := b.CreateString(record.EdgeID)
	truster := b.CreateString(record.Truster)
	trustee := b.CreateString(record.Trustee)
	provider := b.CreateString(record.ProviderPeerID)
	sdstre.TREStart(b)
	sdstre.TREAddEDGE_ID(b, edgeID)
	sdstre.TREAddTRUSTER_ID(b, truster)
	sdstre.TREAddTRUSTEE_ID(b, trustee)
	sdstre.TREAddWEIGHT(b, record.Weight)
	sdstre.TREAddUPDATED_AT(b, uint64(record.UpdatedAtMs))
	sdstre.TREAddDELETED(b, record.Deleted)
	sdstre.TREAddPROVIDER_PEER_ID(b, provider)
	root := sdstre.TREEnd(b)
	sdstre.FinishSizePrefixedTREBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

type fakeTrustPeerRegistry struct {
	peers map[peer.ID]*peers.TrustedPeer
}

func (r *fakeTrustPeerRegistry) ListPeers() []*peers.TrustedPeer {
	out := make([]*peers.TrustedPeer, 0, len(r.peers))
	for _, known := range r.peers {
		copy := *known
		out = append(out, &copy)
	}
	return out
}

func (r *fakeTrustPeerRegistry) SetTrustLevel(id peer.ID, level peers.TrustLevel) error {
	known, ok := r.peers[id]
	if !ok {
		return peers.ErrPeerNotFound
	}
	known.TrustLevel = level
	return nil
}

func (r *fakeTrustPeerRegistry) IsTrusted(id peer.ID) bool {
	known, ok := r.peers[id]
	return ok && known.TrustLevel >= peers.Full
}

func postTRE(t *testing.T, url string, frame []byte, operator bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", StreamContentType)
	if operator {
		req.Header.Set("X-Test-Operator", "yes")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp
}

func TestTrustScoreAndRankEndpoints(t *testing.T) {
	srv := newTrustServer(t, NewTrustHandler(newTrustFixture(t)))

	var score struct {
		Score   float64           `json:"score"`
		Trusted bool              `json:"trusted"`
		Inputs  trust.ScoreInputs `json:"inputs"`
	}
	getJSON(t, srv.URL+"/api/v1/trust/score?evaluator=eve&subject=dave", &score)
	if score.Score <= 0 || score.Score >= 1 {
		t.Fatalf("score out of range: %v", score.Score)
	}
	if score.Inputs.TrusterCountTotal != 3 {
		t.Fatalf("TrusterCountTotal = %d, want 3", score.Inputs.TrusterCountTotal)
	}
	if score.Inputs.TrusterCountAmongTrusted != 1 { // only alice is in eve's web
		t.Fatalf("TrusterCountAmongTrusted = %d, want 1", score.Inputs.TrusterCountAmongTrusted)
	}

	var rank []struct {
		Subject string  `json:"subject"`
		Score   float64 `json:"score"`
	}
	getJSON(t, srv.URL+"/api/v1/trust/rank?evaluator=eve&limit=2", &rank)
	if len(rank) != 2 {
		t.Fatalf("rank returned %d entries, want 2", len(rank))
	}
	if rank[0].Score < rank[1].Score {
		t.Fatal("rank not descending")
	}

	var hood []string
	getJSON(t, srv.URL+"/api/v1/trust/neighborhood?node=dave&depth=1", &hood)
	want := map[string]bool{"alice": true, "bob": true, "stranger": true}
	for _, id := range hood {
		if !want[id] {
			t.Fatalf("unexpected depth-1 neighbor %s", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("neighborhood missing %v", want)
	}

	// Missing params → 400.
	if resp := getJSON(t, srv.URL+"/api/v1/trust/score?evaluator=eve", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing subject: status %d", resp.StatusCode)
	}
}

func TestTrustComplexQuery(t *testing.T) {
	srv := newTrustServer(t, NewTrustHandler(newTrustFixture(t)))

	post := func(body string) []struct {
		Subject string            `json:"subject"`
		Inputs  trust.ScoreInputs `json:"inputs"`
	} {
		t.Helper()
		resp, err := http.Post(srv.URL+"/api/v1/trust/query", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("query status %d", resp.StatusCode)
		}
		var out []struct {
			Subject string            `json:"subject"`
			Inputs  trust.ScoreInputs `json:"inputs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Subjects with >=1 in-web truster: only dave (alice's truster is eve
	// himself — eve counts as in-web for alice too). Check precisely.
	got := post(`{"evaluator":"eve","where":{"minTrusterCountAmongTrusted":1}}`)
	subjects := map[string]bool{}
	for _, m := range got {
		subjects[m.Subject] = true
	}
	if !subjects["dave"] || !subjects["alice"] {
		t.Fatalf("in-web truster query missed dave/alice: %v", subjects)
	}
	if subjects["stranger"] || subjects["bob"] {
		t.Fatalf("in-web truster query matched unendorsed nodes: %v", subjects)
	}

	// Funds predicate: own weighted funds >= 500k → only stranger.
	got = post(`{"evaluator":"eve","where":{"minOwnWeightedFunds":500000}}`)
	if len(got) != 1 || got[0].Subject != "stranger" {
		t.Fatalf("funds query = %+v, want [stranger]", got)
	}

	// Web membership predicate: inside eve's web (alice, dave).
	got = post(`{"evaluator":"eve","where":{"inWebOfTrust":true}}`)
	subjects = map[string]bool{}
	for _, m := range got {
		subjects[m.Subject] = true
	}
	if !subjects["alice"] || !subjects["dave"] || len(subjects) != 2 {
		t.Fatalf("inWebOfTrust query = %v, want alice+dave", subjects)
	}

	// AND semantics + limit: in-web AND >=3 total trusters → dave only.
	got = post(`{"evaluator":"eve","where":{"inWebOfTrust":true,"minTrusterCount":3},"limit":5}`)
	if len(got) != 1 || got[0].Subject != "dave" {
		t.Fatalf("combined query = %+v, want [dave]", got)
	}
}

func TestTrustMutationsFlipsAndFanOut(t *testing.T) {
	svc := newTrustFixture(t)
	// Pin the threshold just above dave's score so a funds boost flips him.
	base := svc.Evaluator().Score("eve", "dave")
	svc2 := newTrustFixture(t)
	svc2.Evaluator().Config.TrustThreshold = base + 0.01

	_, priv, _ := ed25519.GenerateKey(nil)
	published := map[string]int{}
	h := &TrustHandler{
		Service: svc2,
		Events: &trust.EventPublisher{
			SenderPriv: priv,
			Publish: func(topic string, data []byte) error {
				published[topic]++
				return nil
			},
		},
	}
	eveEPM, evePrivateKey := signedNodeEPMFixture(t, "eve", "Eve")
	daveEPM, davePrivateKey := signedNodeEPMFixture(t, "dave", "Dave")
	h.ResolveEPM = func(peerID string) []byte {
		switch peerID {
		case "eve":
			return eveEPM
		case "dave":
			return daveEPM
		default:
			return nil
		}
	}
	// Rebuild the tracked baseline under the pinned threshold.
	srv := newTrustServer(t, h)

	// Funds boost → flip + fan-out to dave's neighborhood topics.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/trust/funds?node=dave",
		bytes.NewReader([]byte(`[{"Type":"stablecoin","Location":"0xdave","Amount":10000000}]`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var mut struct {
		Flips     []trust.StatusChange `json:"flips"`
		Delivered int                  `json:"delivered"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mut); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(mut.Flips) == 0 {
		t.Fatal("funds boost produced no flips")
	}
	if mut.Delivered == 0 || len(published) == 0 {
		t.Fatalf("no fan-out: delivered=%d topics=%v", mut.Delivered, published)
	}
	if _, ok := published[trust.TrustTopic("alice")]; !ok {
		t.Fatalf("fan-out missed alice's topic: %v", published)
	}

	// Cycle via API → 409.
	cycle := signedTREFixture(t, trust.EdgeRecord{
		EdgeID:         trust.EdgeRecordID("dave", "eve"),
		Edge:           trust.Edge{Truster: "dave", Trustee: "eve", Weight: 0.5, UpdatedAtMs: 1_788_000_001},
		ProviderPeerID: "dave",
	}, davePrivateKey)
	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", cycle, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cycle edge: status %d, want 409", resp.StatusCode)
	}

	// Legal edge insert → 200.
	insert := signedTREFixture(t, trust.EdgeRecord{
		EdgeID:         trust.EdgeRecordID("eve", "bob"),
		Edge:           trust.Edge{Truster: "eve", Trustee: "bob", Weight: 0.9, UpdatedAtMs: 1_788_000_002},
		ProviderPeerID: "eve",
	}, evePrivateKey)
	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", insert, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge insert: status %d", resp.StatusCode)
	}

	// Delete it again.
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/trust/edges?truster=eve&trustee=bob", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge delete: status %d", resp.StatusCode)
	}
}

func TestTrustMutationProtection(t *testing.T) {
	h := NewTrustHandler(newTrustFixture(t))
	h.Protect = func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}
	srv := newTrustServer(t, h)

	// Mutations blocked.
	resp, err := http.Post(srv.URL+"/api/v1/trust/edges", "application/json",
		strings.NewReader(`{"truster":"eve","trustee":"bob","weight":0.9}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("protected mutation: status %d, want 403", resp.StatusCode)
	}
	// Reads stay open.
	resp = getJSON(t, srv.URL+"/api/v1/trust/rank?evaluator=eve", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read blocked: status %d", resp.StatusCode)
	}
}

func TestTrustEdgeFrameAuthPersistenceAndPublicRead(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flat.Close() })
	edgeStore, err := trust.NewStoreWithFlatSQL(flat)
	if err != nil {
		t.Fatal(err)
	}
	profile, privateKey := signedNodeEPMFixture(t, "self", "Self Node")
	h := NewTrustHandler(trust.NewService(trust.NewGraph(), nil))
	h.Store = edgeStore
	h.ResolveEPM = func(peerID string) []byte {
		if peerID == "self" {
			return profile
		}
		return nil
	}
	h.Protect = func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Operator") != "yes" {
				http.Error(w, "operator required", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	srv := newTrustServer(t, h)
	record := trust.EdgeRecord{
		EdgeID:         trust.EdgeRecordID("self", "peer-a"),
		Edge:           trust.Edge{Truster: "self", Trustee: "peer-a", Weight: 1, UpdatedAtMs: 1_788_000_010},
		ProviderPeerID: "self",
	}
	frame := signedTREFixture(t, record, privateKey)

	resp := postTRE(t, srv.URL+"/api/v1/trust/edges", frame, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST = %d, want 401", resp.StatusCode)
	}
	if records, err := edgeStore.EdgeRecords(); err != nil || len(records) != 0 {
		t.Fatalf("unauthenticated edge was persisted: records=%d err=%v", len(records), err)
	}

	tampered, err := trust.DecodeEdgeFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	tampered.ProviderSignature[0] ^= 0xff
	tamperedFrame, err := trust.EncodeEdgeFrame(tampered)
	if err != nil {
		t.Fatal(err)
	}
	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", tamperedFrame, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid signature POST = %d, want 400", resp.StatusCode)
	}
	if records, err := edgeStore.EdgeRecords(); err != nil || len(records) != 0 {
		t.Fatalf("invalid signed edge was persisted: records=%d err=%v", len(records), err)
	}

	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", frame, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator POST = %d", resp.StatusCode)
	}
	records, err := edgeStore.EdgeRecords()
	posted, decodeErr := trust.DecodeEdgeFrame(frame)
	if err != nil || decodeErr != nil || len(records) != 1 || records[0].Deleted || !bytes.Equal(records[0].ProviderSignature, posted.ProviderSignature) {
		t.Fatalf("persisted edge = %+v, err=%v", records, err)
	}

	resp, err = http.Get(srv.URL + "/api/v1/trust/edges")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public GET = %d", resp.StatusCode)
	}
	frames, err := SplitFrames(body.Bytes())
	if err != nil || len(frames) != 1 {
		t.Fatalf("GET frames=%d err=%v", len(frames), err)
	}
	decoded, err := trust.DecodeEdgeFrame(frames[0])
	if err != nil || decoded.Truster != "self" || decoded.Trustee != "peer-a" || decoded.Deleted {
		t.Fatalf("decoded GET edge = %+v, err=%v", decoded, err)
	}

	record.Deleted = true
	record.UpdatedAtMs++
	tombstone := signedTREFixture(t, record, privateKey)
	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", tombstone, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator tombstone POST = %d", resp.StatusCode)
	}
	records, err = edgeStore.EdgeRecords()
	if err != nil || len(records) != 1 || !records[0].Deleted {
		t.Fatalf("persisted tombstone = %+v, err=%v", records, err)
	}
}

func TestTrustEdgesWithoutEngineDeriveRegistryAndSignOperatorMutations(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flat.Close() })
	edgeStore, err := trust.NewStoreWithFlatSQL(flat)
	if err != nil {
		t.Fatal(err)
	}
	storedEdge := trust.EdgeRecord{
		EdgeID: "remote-a->remote-b",
		Edge: trust.Edge{
			Truster:     "remote-a",
			Trustee:     "remote-b",
			Weight:      0.75,
			UpdatedAtMs: 1_788_000_000_050,
		},
		ProviderPeerID:    "remote-a",
		ProviderSignature: []byte("held-signature"),
	}
	if err := edgeStore.StoreEdgeRecord(storedEdge); err != nil {
		t.Fatal(err)
	}
	const (
		fullPeer      = "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
		marginalPeer  = "12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p"
		untrustedPeer = "12D3KooWP5MYTnN8DcQDw7aDUFZY2vQAhvMwZZZ1XN3U9Wh3mJUW"
	)
	fullID, _ := peer.Decode(fullPeer)
	marginalID, _ := peer.Decode(marginalPeer)
	untrustedID, _ := peer.Decode(untrustedPeer)
	fullAdded := time.UnixMilli(1_788_000_000_111)
	marginalAdded := time.UnixMilli(1_788_000_000_222)
	registry := &fakeTrustPeerRegistry{peers: map[peer.ID]*peers.TrustedPeer{
		fullID:      {ID: fullID, TrustLevel: peers.Full, AddedAt: fullAdded},
		marginalID:  {ID: marginalID, TrustLevel: peers.Marginal, AddedAt: marginalAdded},
		untrustedID: {ID: untrustedID, TrustLevel: peers.Unknown, AddedAt: time.UnixMilli(1_788_000_000_333)},
	}}
	profile, signingKey := signedNodeEPMFixture(t, "self", "Self Node")
	h := NewTrustHandler(trust.NewService(trust.NewGraph(), nil))
	h.Store = edgeStore
	h.SelfPeerID = "self"
	h.SigningKey = signingKey
	h.PeerRegistry = registry
	h.ResolveEPM = func(peerID string) []byte {
		if peerID == "self" {
			return profile
		}
		return nil
	}
	h.Now = func() time.Time { return time.UnixMilli(1_788_000_999_000) }
	srv := newTrustServer(t, h)

	resp, err := http.Get(srv.URL + "/api/v1/trust/edges")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	frames, err := SplitFrames(body.Bytes())
	if err != nil || len(frames) != 3 {
		t.Fatalf("registry GET frames=%d err=%v", len(frames), err)
	}
	derived := map[string]trust.EdgeRecord{}
	storedSeen := false
	for _, frame := range frames {
		record, decodeErr := trust.DecodeEdgeFrame(frame)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if record.EdgeID == storedEdge.EdgeID {
			storedSeen = record.Truster == storedEdge.Truster && record.Trustee == storedEdge.Trustee
			continue
		}
		derived[record.Trustee] = record
		if record.Truster != "self" || record.ProviderPeerID != "self" || record.Weight != 1 || len(record.ProviderSignature) == 0 || !h.verifyEdgeSigner(record) {
			t.Fatalf("derived registry edge is not a signed local weight-1 edge: %+v", record)
		}
	}
	if derived[fullPeer].UpdatedAtMs != fullAdded.UnixMilli() || derived[marginalPeer].UpdatedAtMs != marginalAdded.UnixMilli() {
		t.Fatalf("derived timestamps = full:%d marginal:%d", derived[fullPeer].UpdatedAtMs, derived[marginalPeer].UpdatedAtMs)
	}
	if _, exists := derived[untrustedPeer]; exists {
		t.Fatal("an untrusted registry peer produced a trust edge")
	}
	if !storedSeen {
		t.Fatal("GET omitted the stored trust edge")
	}

	unsigned := unsignedTREFixture(trust.EdgeRecord{Edge: trust.Edge{Trustee: untrustedPeer}})
	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", unsigned, false)
	body.Reset()
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unsigned operator POST = %d: %s", resp.StatusCode, body.String())
	}
	postedFrames, err := SplitFrames(body.Bytes())
	if err != nil || len(postedFrames) != 1 {
		t.Fatalf("signed response frames=%d err=%v", len(postedFrames), err)
	}
	posted, err := trust.DecodeEdgeFrame(postedFrames[0])
	if err != nil || posted.Truster != "self" || posted.ProviderPeerID != "self" || posted.EdgeID == "" || posted.Weight != 1 || posted.UpdatedAtMs != 1_788_000_999_000 || len(posted.ProviderSignature) == 0 || !h.verifyEdgeSigner(posted) {
		t.Fatalf("server-signed edge = %+v err=%v", posted, err)
	}
	if got := registry.peers[untrustedID].TrustLevel; got != peers.Full {
		t.Fatalf("registry trust after edge = %s, want full", got)
	}
	stored, err := edgeStore.EdgeRecords()
	if err != nil || len(stored) != 2 || stored[1].Deleted || !bytes.Equal(stored[1].ProviderSignature, posted.ProviderSignature) {
		t.Fatalf("stored signed edge=%+v err=%v", stored, err)
	}

	h.Now = func() time.Time { return time.UnixMilli(1_788_001_000_000) }
	tombstone := unsignedTREFixture(trust.EdgeRecord{Edge: trust.Edge{Trustee: untrustedPeer, Weight: 1}, Deleted: true})
	resp = postTRE(t, srv.URL+"/api/v1/trust/edges", tombstone, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unsigned tombstone POST = %d", resp.StatusCode)
	}
	if got := registry.peers[untrustedID].TrustLevel; got != peers.Unknown {
		t.Fatalf("registry trust after tombstone = %s, want unknown", got)
	}
	stored, err = edgeStore.EdgeRecords()
	if err != nil || len(stored) != 2 || !stored[1].Deleted || stored[1].UpdatedAtMs != 1_788_001_000_000 || len(stored[1].ProviderSignature) == 0 {
		t.Fatalf("stored tombstone=%+v err=%v", stored, err)
	}
}

// ---- `$TRP` rules engine surface -----------------------------------------

func newRulesHandler(t *testing.T, protect func(http.HandlerFunc) http.HandlerFunc) (*TrustHandler, *http.ServeMux, *uint32) {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flat.Close() })
	_, key, _ := ed25519.GenerateKey(nil)
	svc := trust.NewService(trust.NewGraph(), nil)
	svc.TrackEvaluator("me")
	if _, err := svc.SetEdge(trust.Edge{Truster: "eve", Trustee: "alice", Weight: 0.9, UpdatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	policies, err := trust.NewPolicyStore(flat, "me", key)
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err := trust.NewVerdictStore(flat, "me")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := trust.NewEngine(trust.EngineConfig{Policies: policies, Verdicts: verdicts, Service: svc, Key: key, PeerID: "me"})
	if err != nil {
		t.Fatal(err)
	}
	var saved uint32
	h := NewTrustHandler(svc)
	h.Policies, h.Verdicts, h.Engine, h.Protect = policies, verdicts, engine, protect
	h.SaveInterval = func(ms uint32) error { saved = ms; return nil }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, &saved
}

func rulesPolicyBody() string {
	return `{"NAME":"one truster","ACTIVE":true,"ROOT":{"GROUP_ID":"root","COMBINATOR":"All","PREDICATES":[{"PREDICATE_ID":"c1","KIND":"TrustedConnections","REQUIRED_COUNT":1}]}}`
}

func TestTrustPolicyPostListsSignedAndVerdictsFollow(t *testing.T) {
	h, mux, _ := newRulesHandler(t, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/trust/policies", strings.NewReader(rulesPolicyBody())))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST policy: %d %s", rec.Code, rec.Body.String())
	}
	var stored map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	policyID, _ := stored["POLICY_ID"].(string)
	if !strings.HasPrefix(policyID, "trp-") || stored["EVALUATOR_SIGNATURE"] == nil || stored["EVALUATOR_PEER_ID"] != "me" {
		t.Fatalf("stored policy is id-stamped and signed by the evaluator: %v", stored)
	}
	if rec.Header().Get("X-SDN-Record-CID") == "" {
		t.Fatal("the stored record's CID is reported")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/policies", nil))
	var list struct{ Policies []trust.Policy }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Policies) != 1 || list.Policies[0].ID != policyID {
		t.Fatalf("GET policies: %d %s", rec.Code, rec.Body.String())
	}

	h.Engine.RunOnce(context.Background(), "test")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/verdicts?subject=alice", nil))
	var got struct {
		Source   string
		Verdicts []trust.Verdict
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Source != "engine" || len(got.Verdicts) != 1 {
		t.Fatalf("GET verdicts: %d %s", rec.Code, rec.Body.String())
	}
	if v := got.Verdicts[0]; !v.Passed || v.PolicyID != policyID || v.SubjectID != "alice" || v.EvaluatorPeerID != "me" {
		t.Fatalf("alice has one truster so the policy passes: %+v", v)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/verdicts?subject=alice&history=1", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Source != "history" || len(got.Verdicts) != 1 || !got.Verdicts[0].Passed {
		t.Fatalf("the flip was persisted as a $TRV record: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTrustSettingsAndEvaluateEndpoints(t *testing.T) {
	h, mux, saved := newRulesHandler(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/trust/settings", strings.NewReader(`{"EVALUATION_INTERVAL_MS":5000}`)))
	if rec.Code != http.StatusOK || *saved != 5000 || h.Engine.IntervalOverride() != 5000 {
		t.Fatalf("PUT settings: %d %s saved=%d", rec.Code, rec.Body.String(), *saved)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trust/settings", nil))
	var s map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || s["EVALUATION_INTERVAL_MS"].(float64) != 5000 || s["MIN_INTERVAL_MS"].(float64) != float64(trust.MinEvaluationIntervalMs) {
		t.Fatalf("GET settings: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/trust/settings", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a PUT without the interval is refused: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/trust/evaluate", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST evaluate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTrustRulesMutationsHonourProtect(t *testing.T) {
	deny := func(http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusUnauthorized) }
	}
	_, mux, _ := newRulesHandler(t, deny)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/trust/policies", rulesPolicyBody()},
		{http.MethodPut, "/api/v1/trust/settings", `{"EVALUATION_INTERVAL_MS":5000}`},
		{http.MethodPost, "/api/v1/trust/evaluate", ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s bypassed Protect: %d", tc.method, tc.path, rec.Code)
		}
	}
	for _, path := range []string{"/api/v1/trust/policies", "/api/v1/trust/verdicts", "/api/v1/trust/settings"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s is open: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestTrustRulesSurfaceAnswers503WhenUnwired(t *testing.T) {
	h := NewTrustHandler(trust.NewService(trust.NewGraph(), nil))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, path := range []string{"/api/v1/trust/policies", "/api/v1/trust/verdicts"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s without stores: %d", path, rec.Code)
		}
	}
}
