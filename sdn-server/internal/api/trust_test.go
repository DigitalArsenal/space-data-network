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
	resp, err = http.Post(srv.URL+"/api/v1/trust/edges", "application/json",
		strings.NewReader(`{"truster":"dave","trustee":"eve","weight":0.5}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cycle edge: status %d, want 409", resp.StatusCode)
	}

	// Legal edge insert → 200.
	resp, err = http.Post(srv.URL+"/api/v1/trust/edges", "application/json",
		strings.NewReader(`{"truster":"eve","trustee":"bob","weight":0.9}`))
	if err != nil {
		t.Fatal(err)
	}
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
