package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
