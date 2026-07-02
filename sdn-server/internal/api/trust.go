package api

// WS11.5 — HTTP API for complex trust queries and trust-relationship
// management over the internal/trust subsystem.
//
// Read surface:
//   GET  /api/v1/trust/score?evaluator=E&subject=S     one evaluation + raw inputs
//   GET  /api/v1/trust/statuses?evaluator=E            full cached status map
//   GET  /api/v1/trust/rank?evaluator=E&limit=N        ranked subjects
//   GET  /api/v1/trust/neighborhood?node=X&depth=D     web-of-trust audience
//   POST /api/v1/trust/query                           predicate query (see TrustQuery)
//
// Mutation surface (wrap with the node's auth in production via Protect):
//   POST   /api/v1/trust/edges                         {truster,trustee,weight}
//   DELETE /api/v1/trust/edges?truster=T&trustee=S
//   PUT    /api/v1/trust/funds?node=X                  [{type,location,amount}...]
//
// Mutations return the trust-status flips they caused; when an
// EventPublisher is wired the flips also fan out to the web-of-trust
// gossipsub topics (WS11.4), and when a Store is wired edges persist.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// TrustHandler exposes the trust subsystem over the node HTTP API.
type TrustHandler struct {
	Service *trust.Service
	// Store, when set, persists edge mutations (trust_nodes/trust_edges).
	Store *trust.Store
	// Events, when set, fans mutation flips out to gossipsub (WS11.4).
	Events *trust.EventPublisher
	// Protect wraps the mutation endpoints (wire the node's admin auth
	// here in production). nil = unprotected (tests/local).
	Protect func(http.HandlerFunc) http.HandlerFunc
}

// NewTrustHandler creates a handler over a trust service.
func NewTrustHandler(svc *trust.Service) *TrustHandler {
	return &TrustHandler{Service: svc}
}

// RegisterRoutes registers the trust API routes.
func (h *TrustHandler) RegisterRoutes(mux *http.ServeMux) {
	protect := h.Protect
	if protect == nil {
		protect = func(f http.HandlerFunc) http.HandlerFunc { return f }
	}
	mux.HandleFunc("/api/v1/trust/score", h.handleScore)
	mux.HandleFunc("/api/v1/trust/statuses", h.handleStatuses)
	mux.HandleFunc("/api/v1/trust/rank", h.handleRank)
	mux.HandleFunc("/api/v1/trust/neighborhood", h.handleNeighborhood)
	mux.HandleFunc("/api/v1/trust/query", h.handleQuery)
	mux.HandleFunc("/api/v1/trust/edges", protect(h.handleEdges))
	mux.HandleFunc("/api/v1/trust/funds", protect(h.handleFunds))
}

func writeTrustJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func trustError(w http.ResponseWriter, status int, msg string) {
	writeTrustJSON(w, status, map[string]string{"error": msg})
}

type scoreResponse struct {
	Evaluator string            `json:"evaluator"`
	Subject   string            `json:"subject"`
	Score     float64           `json:"score"`
	Trusted   bool              `json:"trusted"`
	Inputs    trust.ScoreInputs `json:"inputs"`
}

func (h *TrustHandler) handleScore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		trustError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	evaluator := r.URL.Query().Get("evaluator")
	subject := r.URL.Query().Get("subject")
	if evaluator == "" || subject == "" {
		trustError(w, http.StatusBadRequest, "evaluator and subject are required")
		return
	}
	ev := h.Service.Evaluator()
	in := ev.Inputs(evaluator, subject)
	score := ev.Score(evaluator, subject)
	writeTrustJSON(w, http.StatusOK, scoreResponse{
		Evaluator: evaluator,
		Subject:   subject,
		Score:     score,
		Trusted:   score >= ev.Config.TrustThreshold,
		Inputs:    in,
	})
}

func (h *TrustHandler) handleStatuses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		trustError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	evaluator := r.URL.Query().Get("evaluator")
	if evaluator == "" {
		trustError(w, http.StatusBadRequest, "evaluator is required")
		return
	}
	writeTrustJSON(w, http.StatusOK, h.Service.Statuses(evaluator))
}

type rankEntry struct {
	Subject string  `json:"subject"`
	Score   float64 `json:"score"`
	Trusted bool    `json:"trusted"`
}

func (h *TrustHandler) handleRank(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		trustError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	evaluator := r.URL.Query().Get("evaluator")
	if evaluator == "" {
		trustError(w, http.StatusBadRequest, "evaluator is required")
		return
	}
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, _ = strconv.Atoi(s)
	}
	ev := h.Service.Evaluator()
	scores := ev.ScoreAll(evaluator)
	ranked := ev.RankedSubjects(evaluator)
	if limit > 0 && limit < len(ranked) {
		ranked = ranked[:limit]
	}
	out := make([]rankEntry, 0, len(ranked))
	for _, id := range ranked {
		out = append(out, rankEntry{Subject: id, Score: scores[id], Trusted: scores[id] >= ev.Config.TrustThreshold})
	}
	writeTrustJSON(w, http.StatusOK, out)
}

func (h *TrustHandler) handleNeighborhood(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		trustError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	node := r.URL.Query().Get("node")
	if node == "" {
		trustError(w, http.StatusBadRequest, "node is required")
		return
	}
	depth := 0
	if s := r.URL.Query().Get("depth"); s != "" {
		depth, _ = strconv.Atoi(s)
	}
	writeTrustJSON(w, http.StatusOK, h.Service.NeighborhoodOf(node, depth))
}

// TrustQuery is the complex-predicate query body: every set predicate must
// hold (AND semantics) for a subject to match.
type TrustQuery struct {
	Evaluator string `json:"evaluator"`
	Where     struct {
		MinScore                    *float64 `json:"minScore"`
		MaxScore                    *float64 `json:"maxScore"`
		Trusted                     *bool    `json:"trusted"`
		MinOwnWeightedFunds         *float64 `json:"minOwnWeightedFunds"`
		MinTrusterCount             *int     `json:"minTrusterCount"`
		MinTrusterCountAmongTrusted *int     `json:"minTrusterCountAmongTrusted"`
		MinTrusterFunds             *float64 `json:"minTrusterFunds"`
		MinTrusterFundsAmongTrusted *float64 `json:"minTrusterFundsAmongTrusted"`
		// InWebOfTrust keeps only subjects inside (true) or outside (false)
		// the evaluator's transitive web of trust.
		InWebOfTrust *bool `json:"inWebOfTrust"`
	} `json:"where"`
	Limit int `json:"limit"`
}

type queryMatch struct {
	Subject string            `json:"subject"`
	Score   float64           `json:"score"`
	Trusted bool              `json:"trusted"`
	Inputs  trust.ScoreInputs `json:"inputs"`
}

func (h *TrustHandler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		trustError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var q TrustQuery
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		trustError(w, http.StatusBadRequest, "invalid query body: "+err.Error())
		return
	}
	if q.Evaluator == "" {
		trustError(w, http.StatusBadRequest, "evaluator is required")
		return
	}
	ev := h.Service.Evaluator()

	web := map[string]struct{}{q.Evaluator: {}}
	for _, id := range ev.Graph.TransitiveTrustees(q.Evaluator, 0) {
		web[id] = struct{}{}
	}

	matches := []queryMatch{}
	for _, subject := range ev.Graph.Nodes() {
		if subject == q.Evaluator {
			continue
		}
		in := ev.Inputs(q.Evaluator, subject)
		score := ev.Score(q.Evaluator, subject)
		trusted := score >= ev.Config.TrustThreshold
		wq := q.Where
		if wq.MinScore != nil && score < *wq.MinScore {
			continue
		}
		if wq.MaxScore != nil && score > *wq.MaxScore {
			continue
		}
		if wq.Trusted != nil && trusted != *wq.Trusted {
			continue
		}
		if wq.MinOwnWeightedFunds != nil && in.OwnWeightedFunds < *wq.MinOwnWeightedFunds {
			continue
		}
		if wq.MinTrusterCount != nil && in.TrusterCountTotal < *wq.MinTrusterCount {
			continue
		}
		if wq.MinTrusterCountAmongTrusted != nil && in.TrusterCountAmongTrusted < *wq.MinTrusterCountAmongTrusted {
			continue
		}
		if wq.MinTrusterFunds != nil && in.TrusterFundsTotal < *wq.MinTrusterFunds {
			continue
		}
		if wq.MinTrusterFundsAmongTrusted != nil && in.TrusterFundsAmongTrusted < *wq.MinTrusterFundsAmongTrusted {
			continue
		}
		if wq.InWebOfTrust != nil {
			_, inWeb := web[subject]
			if inWeb != *wq.InWebOfTrust {
				continue
			}
		}
		matches = append(matches, queryMatch{Subject: subject, Score: score, Trusted: trusted, Inputs: in})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Subject < matches[j].Subject
	})
	if q.Limit > 0 && q.Limit < len(matches) {
		matches = matches[:q.Limit]
	}
	writeTrustJSON(w, http.StatusOK, matches)
}

type mutationResponse struct {
	Flips     []trust.StatusChange `json:"flips"`
	Delivered int                  `json:"delivered"`
}

// fanOut publishes flips when an EventPublisher is wired.
func (h *TrustHandler) fanOut(changes []trust.StatusChange) int {
	if h.Events == nil || len(changes) == 0 {
		return 0
	}
	delivered, _ := h.Events.FanOut(h.Service, changes)
	return delivered
}

type edgeRequest struct {
	Truster string  `json:"truster"`
	Trustee string  `json:"trustee"`
	Weight  float64 `json:"weight"`
}

func (h *TrustHandler) handleEdges(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req edgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			trustError(w, http.StatusBadRequest, "invalid edge body: "+err.Error())
			return
		}
		e := trust.Edge{Truster: req.Truster, Trustee: req.Trustee, Weight: req.Weight, UpdatedAtMs: time.Now().UnixMilli()}
		changes, err := h.Service.SetEdge(e)
		if err != nil {
			trustError(w, http.StatusConflict, err.Error())
			return
		}
		if h.Store != nil {
			if err := h.Store.UpsertEdge(e); err != nil {
				trustError(w, http.StatusInternalServerError, "persist: "+err.Error())
				return
			}
		}
		writeTrustJSON(w, http.StatusOK, mutationResponse{Flips: changes, Delivered: h.fanOut(changes)})
	case http.MethodDelete:
		truster := r.URL.Query().Get("truster")
		trustee := r.URL.Query().Get("trustee")
		if truster == "" || trustee == "" {
			trustError(w, http.StatusBadRequest, "truster and trustee are required")
			return
		}
		changes, err := h.Service.RemoveEdge(truster, trustee)
		if err != nil {
			trustError(w, http.StatusNotFound, err.Error())
			return
		}
		if h.Store != nil {
			if err := h.Store.DeleteEdge(truster, trustee); err != nil {
				trustError(w, http.StatusInternalServerError, "persist: "+err.Error())
				return
			}
		}
		writeTrustJSON(w, http.StatusOK, mutationResponse{Flips: changes, Delivered: h.fanOut(changes)})
	default:
		trustError(w, http.StatusMethodNotAllowed, "POST or DELETE")
	}
}

func (h *TrustHandler) handleFunds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		trustError(w, http.StatusMethodNotAllowed, "PUT only")
		return
	}
	node := r.URL.Query().Get("node")
	if node == "" {
		trustError(w, http.StatusBadRequest, "node is required")
		return
	}
	var holdings []trust.FundHolding
	if err := json.NewDecoder(r.Body).Decode(&holdings); err != nil {
		trustError(w, http.StatusBadRequest, "invalid holdings body: "+err.Error())
		return
	}
	changes := h.Service.UpdateFunds(node, holdings)
	writeTrustJSON(w, http.StatusOK, mutationResponse{Flips: changes, Delivered: h.fanOut(changes)})
}
