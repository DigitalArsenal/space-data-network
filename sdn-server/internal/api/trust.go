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
// Mutation surface (wrapped with the node's auth in production via Protect):
//   POST   /api/v1/trust/edges                         one signed $TRE frame
//   DELETE /api/v1/trust/edges?truster=T&trustee=S
//   PUT    /api/v1/trust/funds?node=X                  [{type,location,amount}...]
//
// POST returns the accepted $TRE frame. The legacy DELETE response still
// returns trust-status flips; when an EventPublisher is wired flips fan out to
// the web-of-trust gossipsub topics (WS11.4), and when a Store is wired edges
// persist.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// TrustPeerRegistry is the direct trust projection shared with the legacy
// peer-management API. Keeping it as a small interface makes the frame lane
// independently testable and prevents trust state from diverging between the
// two operator surfaces.
type TrustPeerRegistry interface {
	ListPeers() []*peers.TrustedPeer
	SetTrustLevel(peer.ID, peers.TrustLevel) error
	IsTrusted(peer.ID) bool
}

// TrustHandler exposes the trust subsystem over the node HTTP API.
type TrustHandler struct {
	Service *trust.Service
	// Store, when set, persists edge mutations as SDS TNR/TRE records.
	Store *trust.Store
	// Events, when set, fans mutation flips out to gossipsub (WS11.4).
	Events *trust.EventPublisher
	// Protect wraps the mutation endpoints (wire the node's admin auth
	// here in production). nil = unprotected (tests/local).
	Protect func(http.HandlerFunc) http.HandlerFunc
	// ResolveEPM returns the held signed profile for a TRE provider. POST uses
	// its Signing keys to verify PROVIDER_SIGNATURE before changing the graph.
	ResolveEPM func(peerID string) []byte
	// SelfPeerID and SigningKey let an authenticated operator submit an
	// unsigned local edge. The node fills the signer fields and signs the same
	// canonical payload used for presigned network records.
	SelfPeerID   string
	SigningKey   ed25519.PrivateKey
	PeerRegistry TrustPeerRegistry
	Now          func() time.Time

	// Policies, Verdicts and Engine wire the `$TRP` rules engine
	// (sdn-trust-rules-engine). nil = the rules surface answers 503.
	Policies *trust.PolicyStore
	Verdicts *trust.VerdictStore
	Engine   *trust.Engine
	// SaveInterval persists the runtime evaluation interval (0 = each
	// policy's own cadence) so a restart keeps the operator's setting.
	SaveInterval func(ms uint32) error

	protectFn func(http.HandlerFunc) http.HandlerFunc
}

// NewTrustHandler creates a handler over a trust service.
func NewTrustHandler(svc *trust.Service) *TrustHandler {
	return &TrustHandler{Service: svc, Now: time.Now}
}

// RegisterRoutes registers the trust API routes.
func (h *TrustHandler) RegisterRoutes(mux *http.ServeMux) {
	h.RegisterEdgeRoutes(mux)
	h.RegisterEngineRoutes(mux)
}

// RegisterEdgeRoutes mounts the node-first $TRE lane independently of the
// optional trust rules engine.
func (h *TrustHandler) RegisterEdgeRoutes(mux *http.ServeMux) {
	protect := h.Protect
	if protect == nil {
		protect = func(f http.HandlerFunc) http.HandlerFunc { return f }
	}
	h.protectFn = protect
	mux.HandleFunc("/api/v1/trust/edges", h.handleEdges)
}

// RegisterEngineRoutes mounts the evaluator/rules surfaces that only exist
// when the optional trust engine starts. The edge lane is deliberately absent
// here because RegisterEdgeRoutes already mounted it during baseline node
// setup.
func (h *TrustHandler) RegisterEngineRoutes(mux *http.ServeMux) {
	protect := h.Protect
	if protect == nil {
		protect = func(f http.HandlerFunc) http.HandlerFunc { return f }
	}
	h.protectFn = protect
	mux.HandleFunc("/api/v1/trust/score", h.handleScore)
	mux.HandleFunc("/api/v1/trust/statuses", h.handleStatuses)
	mux.HandleFunc("/api/v1/trust/rank", h.handleRank)
	mux.HandleFunc("/api/v1/trust/neighborhood", h.handleNeighborhood)
	mux.HandleFunc("/api/v1/trust/query", h.handleQuery)
	mux.HandleFunc("/api/v1/trust/funds", protect(h.handleFunds))
	// Rules engine: policies and verdicts read openly (a policy is the
	// evaluator's published rule; a verdict is its signed public opinion),
	// mutations and settings behind the same admin gate as edges.
	mux.HandleFunc("/api/v1/trust/policies", h.handlePolicies)
	mux.HandleFunc("/api/v1/trust/verdicts", h.handleVerdicts)
	mux.HandleFunc("/api/v1/trust/settings", h.handleSettings)
	mux.HandleFunc("/api/v1/trust/evaluate", protect(h.handleEvaluate))
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

func (h *TrustHandler) handleEdges(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleEdgeFrames(w)
	case http.MethodPost:
		h.protectMutation(h.handleEdgeFramePost)(w, r)
	case http.MethodDelete:
		h.protectMutation(h.handleLegacyEdgeDelete)(w, r)
	default:
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or POST for trust edges.", 0)
	}
}

func (h *TrustHandler) protectMutation(next http.HandlerFunc) http.HandlerFunc {
	if h != nil && h.protectFn != nil {
		return h.protectFn(next)
	}
	return next
}

func (h *TrustHandler) handleEdgeFrames(w http.ResponseWriter) {
	recordsByID := make(map[string]trust.EdgeRecord)
	// A service-only handler (unit tests and older embedders) still exposes its
	// graph. Production also has Store and PeerRegistry, whose fuller durable
	// projections below replace duplicates by stable EDGE_ID.
	if h.Service != nil && h.Service.Evaluator() != nil && h.Service.Evaluator().Graph != nil {
		for _, edge := range h.Service.Evaluator().Graph.Edges() {
			record := trust.EdgeRecord{
				EdgeID:         trust.EdgeRecordID(edge.Truster, edge.Trustee),
				Edge:           edge,
				ProviderPeerID: edge.Truster,
			}
			recordsByID[record.EdgeID] = record
		}
	}
	// The peer registry is the source of truth for direct operator trust and
	// config trusted peers. Project each positive marginal/full assignment as
	// this node's weight-1 edge, timestamped at the peer's original AddedAt.
	for _, known := range h.registryEdgeRecords() {
		if _, exists := recordsByID[known.EdgeID]; !exists {
			recordsByID[known.EdgeID] = known
		}
	}
	if h.Store != nil {
		records, err := h.Store.EdgeRecords()
		if err != nil {
			WriteErrorFrame(w, http.StatusServiceUnavailable, "trust_unavailable", "Trust edges are not available right now.", 5*time.Second)
			return
		}
		for _, record := range records {
			recordsByID[record.EdgeID] = record
		}
	}
	ids := make([]string, 0, len(recordsByID))
	for id := range recordsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	frames := make([][]byte, 0, len(ids))
	for _, id := range ids {
		frame, err := trust.EncodeEdgeFrame(recordsByID[id])
		if err == nil {
			frames = append(frames, frame)
		}
	}
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{
		StreamSchemaHeader: "TRE.fbs",
		SelfPeerHeaderName: strings.TrimSpace(h.SelfPeerID),
	})
}

func (h *TrustHandler) registryEdgeRecords() []trust.EdgeRecord {
	if h == nil || h.PeerRegistry == nil || strings.TrimSpace(h.SelfPeerID) == "" {
		return nil
	}
	self := strings.TrimSpace(h.SelfPeerID)
	out := make([]trust.EdgeRecord, 0)
	for _, known := range h.PeerRegistry.ListPeers() {
		if known == nil || strings.TrimSpace(known.ID.String()) == "" {
			continue
		}
		if known.TrustLevel != peers.Marginal && known.TrustLevel < peers.Full {
			continue
		}
		record := trust.EdgeRecord{
			EdgeID: trust.EdgeRecordID(self, known.ID.String()),
			Edge: trust.Edge{
				Truster:     self,
				Trustee:     known.ID.String(),
				Weight:      1,
				UpdatedAtMs: known.AddedAt.UTC().UnixMilli(),
			},
			ProviderPeerID: self,
		}
		if len(h.SigningKey) == ed25519.PrivateKeySize {
			if payload, err := trust.EdgeSigningPayload(record); err == nil {
				record.ProviderSignature = ed25519.Sign(h.SigningKey, payload)
			}
		}
		out = append(out, record)
	}
	return out
}

func (h *TrustHandler) handleEdgeFramePost(w http.ResponseWriter, r *http.Request) {
	frames, err := ReadFrames(r.Body, MaxRequestFrameBytes)
	if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != "$TRE" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_trust_edge", "The request body must be exactly one size-prefixed $TRE frame.", 0)
		return
	}
	draft, err := trust.DecodeEdgeDraftFrame(frames[0])
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_trust_edge", "The trust edge frame could not be decoded.", 0)
		return
	}
	var record trust.EdgeRecord
	if len(draft.ProviderSignature) == 0 {
		record, err = h.signUnsignedEdge(draft)
	} else {
		record, err = trust.DecodeEdgeFrame(frames[0])
	}
	if err != nil || record.UpdatedAtMs <= 0 || record.ProviderPeerID == "" || record.ProviderPeerID != record.Truster || len(record.ProviderSignature) == 0 {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_trust_edge", "The trust edge is missing required signer, timestamp, or signature fields.", 0)
		return
	}
	if !h.verifyEdgeSigner(record) {
		WriteErrorFrame(w, http.StatusBadRequest, "invalid_signature", "The trust edge signature does not verify against the truster's held profile.", 0)
		return
	}

	if h.Service == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "trust_unavailable", "Trust edges are not available right now.", 5*time.Second)
		return
	}
	var changes []trust.StatusChange
	if record.Deleted {
		changes, err = h.Service.RemoveEdge(record.Truster, record.Trustee)
		if errors.Is(err, trust.ErrNotFound) {
			err = nil // an idempotent tombstone still needs to be persisted
		}
	} else {
		changes, err = h.Service.SetEdge(record.Edge)
	}
	if err != nil {
		WriteErrorFrame(w, http.StatusConflict, "invalid_trust_edge", err.Error(), 0)
		return
	}
	if h.Store != nil {
		if err := h.Store.StoreEdgeRecord(record); err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "not_persisted", "The trust edge could not be stored.", 0)
			return
		}
	}
	if err := h.syncPeerRegistry(record); err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "peer_trust_not_updated", "The peer trust registry could not be updated.", 0)
		return
	}
	h.trigger("edge-changed")
	_ = h.fanOut(changes)
	frame, err := trust.EncodeEdgeFrame(record)
	if err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "trust_edge_encode_failed", "The trust edge could not be returned.", 0)
		return
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{frame}, map[string]string{StreamSchemaHeader: "TRE.fbs"})
}

func (h *TrustHandler) signUnsignedEdge(record trust.EdgeRecord) (trust.EdgeRecord, error) {
	if h == nil || strings.TrimSpace(h.SelfPeerID) == "" || len(h.SigningKey) != ed25519.PrivateKeySize {
		return trust.EdgeRecord{}, errors.New("this node cannot sign trust edges")
	}
	self := strings.TrimSpace(h.SelfPeerID)
	if truster := strings.TrimSpace(record.Truster); truster != "" && truster != self {
		return trust.EdgeRecord{}, errors.New("an unsigned edge cannot name another truster")
	}
	if provider := strings.TrimSpace(record.ProviderPeerID); provider != "" && provider != self {
		return trust.EdgeRecord{}, errors.New("an unsigned edge cannot name another provider")
	}
	record.Truster = self
	record.Trustee = strings.TrimSpace(record.Trustee)
	record.ProviderPeerID = self
	record.ProviderSignature = nil
	if !record.Deleted {
		record.Weight = 1
	}
	if record.EdgeID == "" {
		record.EdgeID = trust.EdgeRecordID(self, record.Trustee)
	}
	now := h.Now
	if now == nil {
		now = time.Now
	}
	record.UpdatedAtMs = now().UTC().UnixMilli()
	payload, err := trust.EdgeSigningPayload(record)
	if err != nil {
		return trust.EdgeRecord{}, err
	}
	record.ProviderSignature = ed25519.Sign(h.SigningKey, payload)
	if !h.verifyEdgeSigner(record) {
		return trust.EdgeRecord{}, errors.New("this node's signed profile does not verify the trust edge")
	}
	return record, nil
}

func (h *TrustHandler) syncPeerRegistry(record trust.EdgeRecord) error {
	if h == nil || h.PeerRegistry == nil || strings.TrimSpace(record.Truster) != strings.TrimSpace(h.SelfPeerID) {
		return nil
	}
	peerID, err := peer.Decode(strings.TrimSpace(record.Trustee))
	if err != nil {
		return err
	}
	level := peers.Full
	if record.Deleted {
		level = peers.Unknown
	}
	return h.PeerRegistry.SetTrustLevel(peerID, level)
}

func (h *TrustHandler) verifyEdgeSigner(record trust.EdgeRecord) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if h == nil || h.ResolveEPM == nil {
		return false
	}
	profile := h.ResolveEPM(record.ProviderPeerID)
	if len(profile) == 0 || epm.VerifyEPMSignature(profile) != nil {
		return false
	}
	peerID, err := epm.PeerIDFromEPM(profile)
	if err != nil || strings.TrimSpace(peerID) != record.ProviderPeerID {
		return false
	}
	payload, err := trust.EdgeSigningPayload(record)
	return err == nil && epm.VerifyDetachedSignature(profile, payload, record.ProviderSignature) == nil
}

func (h *TrustHandler) handleLegacyEdgeDelete(w http.ResponseWriter, r *http.Request) {
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
	h.trigger("edge-changed")
	writeTrustJSON(w, http.StatusOK, mutationResponse{Flips: changes, Delivered: h.fanOut(changes)})
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
	h.trigger("funds-changed")
	writeTrustJSON(w, http.StatusOK, mutationResponse{Flips: changes, Delivered: h.fanOut(changes)})
}

// ---- `$TRP` rules engine surface ---------------------------------------

func (h *TrustHandler) trigger(source string) {
	if h.Engine != nil {
		h.Engine.Trigger(source)
	}
}

func (h *TrustHandler) rulesWired(w http.ResponseWriter) bool {
	if h.Policies == nil || h.Verdicts == nil {
		trustError(w, http.StatusServiceUnavailable, "the trust rules engine is not wired on this node")
		return false
	}
	return true
}

// handlePolicies: GET lists every policy (latest record per POLICY_ID);
// POST stores a new or updated policy as a signed `$TRP` record.
func (h *TrustHandler) handlePolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.rulesWired(w) {
			return
		}
		list, err := h.Policies.List()
		if err != nil {
			trustError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if list == nil {
			list = []trust.Policy{}
		}
		writeTrustJSON(w, http.StatusOK, map[string]any{"policies": list})
	case http.MethodPost:
		protect := h.protectFn
		if protect == nil {
			protect = func(f http.HandlerFunc) http.HandlerFunc { return f }
		}
		protect(h.postPolicy)(w, r)
	default:
		trustError(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func newPolicyID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "trp-" + hex.EncodeToString(b[:])
}

func (h *TrustHandler) postPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.rulesWired(w) {
		return
	}
	var p trust.Policy
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		trustError(w, http.StatusBadRequest, "invalid policy body: "+err.Error())
		return
	}
	if p.ID == "" {
		p.ID = newPolicyID()
	}
	if p.EvaluationIntervalMs == 0 {
		p.EvaluationIntervalMs = trust.DefaultEvaluationIntervalMs
	}
	if err := p.Validate(); err != nil {
		trustError(w, http.StatusBadRequest, err.Error())
		return
	}
	cid, signedJSON, err := h.Policies.Put(p)
	if err != nil {
		trustError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.trigger("policy-changed")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-SDN-Record-CID", cid)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(signedJSON)
}

// handleVerdicts serves the latest verdict per (policy, subject) from the
// engine when it runs, else the stored `$TRV` history.
func (h *TrustHandler) handleVerdicts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		trustError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if !h.rulesWired(w) {
		return
	}
	q := r.URL.Query()
	policyID, subject := q.Get("policy"), q.Get("subject")
	limit, _ := strconv.Atoi(q.Get("limit"))
	var (
		verdicts []trust.Verdict
		err      error
		source   = "history"
	)
	if q.Get("history") == "" && h.Engine != nil {
		verdicts = h.Engine.Latest(policyID, subject)
		source = "engine"
	} else {
		verdicts, err = h.Verdicts.List(policyID, subject, limit)
		if err != nil {
			trustError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	if verdicts == nil {
		verdicts = []trust.Verdict{}
	}
	writeTrustJSON(w, http.StatusOK, map[string]any{"source": source, "verdicts": verdicts})
}

type trustSettings struct {
	EvaluationIntervalMs uint32 `json:"EVALUATION_INTERVAL_MS"`
	MinIntervalMs        uint32 `json:"MIN_INTERVAL_MS"`
	DefaultIntervalMs    uint32 `json:"DEFAULT_INTERVAL_MS"`
	Runs                 uint64 `json:"RUNS"`
	LastRunMs            int64  `json:"LAST_RUN"`
}

func (h *TrustHandler) settings() trustSettings {
	s := trustSettings{MinIntervalMs: trust.MinEvaluationIntervalMs, DefaultIntervalMs: trust.DefaultEvaluationIntervalMs}
	if h.Engine != nil {
		s.EvaluationIntervalMs = h.Engine.IntervalOverride()
		s.Runs = h.Engine.Runs()
		if t := h.Engine.LastRun(); !t.IsZero() {
			s.LastRunMs = t.UnixMilli()
		}
	}
	return s
}

// handleSettings: GET reports the cadence in force; PUT sets the runtime
// override (EVALUATION_INTERVAL_MS, 0 = each policy's own) without restart.
func (h *TrustHandler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeTrustJSON(w, http.StatusOK, h.settings())
	case http.MethodPut:
		protect := h.protectFn
		if protect == nil {
			protect = func(f http.HandlerFunc) http.HandlerFunc { return f }
		}
		protect(h.putSettings)(w, r)
	default:
		trustError(w, http.StatusMethodNotAllowed, "GET or PUT")
	}
}

func (h *TrustHandler) putSettings(w http.ResponseWriter, r *http.Request) {
	if h.Engine == nil {
		trustError(w, http.StatusServiceUnavailable, "the trust rules engine is not running on this node")
		return
	}
	var req struct {
		EvaluationIntervalMs *uint32 `json:"EVALUATION_INTERVAL_MS"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EvaluationIntervalMs == nil {
		trustError(w, http.StatusBadRequest, "EVALUATION_INTERVAL_MS is required")
		return
	}
	h.Engine.SetIntervalOverride(*req.EvaluationIntervalMs)
	if h.SaveInterval != nil {
		if err := h.SaveInterval(h.Engine.IntervalOverride()); err != nil {
			trustError(w, http.StatusInternalServerError, "persist: "+err.Error())
			return
		}
	}
	writeTrustJSON(w, http.StatusOK, h.settings())
}

// handleEvaluate asks for one early pass now.
func (h *TrustHandler) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		trustError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if h.Engine == nil {
		trustError(w, http.StatusServiceUnavailable, "the trust rules engine is not running on this node")
		return
	}
	h.Engine.Trigger("manual")
	writeTrustJSON(w, http.StatusAccepted, map[string]any{"triggered": true, "runs": h.Engine.Runs()})
}
