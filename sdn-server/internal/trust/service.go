package trust

// WS11.3 — on-the-fly trust recompute. The Service owns the DAG + funds and
// maintains, for each tracked evaluator, a cached (score, trusted) status per
// subject. Every mutation (edge set/remove, node removal, funds update)
// recomputes ONLY the affected (evaluator, subject) pairs — derived from the
// DAG structure — and returns the trust-status flips, which WS11.4 fans out
// as pub/sub events to the flipped subject's web-of-trust neighborhood.
//
// Affected-set derivation:
//   - funds change on X → X's own-funds component (X as subject) and X's
//     truster-funds contribution (every direct trustee of X as subject);
//   - edge change T→S → S's truster set (S as subject for every evaluator),
//     plus, for each evaluator whose WEB OF TRUST reachability changed
//     (web = evaluator + transitive trustees), every direct trustee of a
//     node that entered/left that web (their among-trusted splits moved);
//   - node removal → union of the edge effects for every removed edge.

import (
	"sync"
	"time"
)

// Status is one cached (evaluator, subject) trust evaluation.
type Status struct {
	Score   float64
	Trusted bool
}

// StatusChange records a trust-status flip produced by a mutation.
type StatusChange struct {
	Evaluator  string
	Subject    string
	OldScore   float64
	NewScore   float64
	OldTrusted bool
	NewTrusted bool
	AtMs       int64
}

// Service coordinates the DAG, funds, scoring, and cached statuses with
// incremental recompute. Safe for concurrent use.
type Service struct {
	mu    sync.Mutex
	graph *Graph
	funds MemoryFundsProvider
	eval  *Evaluator

	// status[evaluator][subject], complete over all graph nodes for every
	// tracked evaluator (rebuilt incrementally).
	status map[string]map[string]Status
	// web[evaluator] = evaluator + its transitive trustees (snapshot used to
	// detect reachability deltas).
	web map[string]map[string]struct{}

	nowMs func() int64
}

// NewService creates a Service over a graph and an initial funds map (copied).
func NewService(g *Graph, funds map[string][]FundHolding) *Service {
	fp := MemoryFundsProvider{}
	for k, v := range funds {
		fp[k] = append([]FundHolding(nil), v...)
	}
	s := &Service{
		graph:  g,
		funds:  fp,
		status: map[string]map[string]Status{},
		web:    map[string]map[string]struct{}{},
		nowMs:  func() int64 { return time.Now().UnixMilli() },
	}
	s.eval = NewEvaluator(g, fp)
	return s
}

// Evaluator exposes the underlying evaluator (config/score-fn tuning).
func (s *Service) Evaluator() *Evaluator { return s.eval }

// TrackEvaluator registers a perspective to maintain. The baseline snapshot
// emits no events.
func (s *Service) TrackEvaluator(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.status[id]; ok {
		return
	}
	s.web[id] = s.webOf(id)
	snap := map[string]Status{}
	for _, subject := range s.graph.Nodes() {
		if subject == id {
			continue
		}
		score := s.eval.Score(id, subject)
		snap[subject] = Status{Score: score, Trusted: score >= s.eval.Config.TrustThreshold}
	}
	s.status[id] = snap
}

// Status returns the cached evaluation for (evaluator, subject).
func (s *Service) Status(evaluator, subject string) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.status[evaluator][subject]
	return st, ok
}

// Statuses returns a copy of the evaluator's full cached status map.
func (s *Service) Statuses(evaluator string) map[string]Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Status{}
	for k, v := range s.status[evaluator] {
		out[k] = v
	}
	return out
}

// SetEdge mutates the DAG (acyclicity enforced) and incrementally recomputes
// affected statuses, returning the trust flips.
func (s *Service) SetEdge(e Edge) ([]StatusChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.graph.SetEdge(e); err != nil {
		return nil, err
	}
	return s.recomputeAfterStructuralChange(map[string]struct{}{e.Trustee: {}}), nil
}

// RemoveEdge mutates the DAG and incrementally recomputes affected statuses.
func (s *Service) RemoveEdge(truster, trustee string) ([]StatusChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.graph.RemoveEdge(truster, trustee); err != nil {
		return nil, err
	}
	return s.recomputeAfterStructuralChange(map[string]struct{}{trustee: {}}), nil
}

// RemoveNode deletes an identity (and its edges) and recomputes.
func (s *Service) RemoveNode(id string) ([]StatusChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Everyone the node directly trusted loses a truster.
	affected := map[string]struct{}{}
	for _, t := range s.graph.Trustees(id) {
		affected[t] = struct{}{}
	}
	if err := s.graph.RemoveNode(id); err != nil {
		return nil, err
	}
	for _, snap := range s.status {
		delete(snap, id)
	}
	delete(s.status, id)
	delete(s.web, id)
	return s.recomputeAfterStructuralChange(affected), nil
}

// UpdateFunds replaces a node's holdings and incrementally recomputes: the
// node itself (own funds) and its direct trustees (truster-funds component).
func (s *Service) UpdateFunds(nodeID string, holdings []FundHolding) []StatusChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.funds[nodeID] = append([]FundHolding(nil), holdings...)
	affected := map[string]struct{}{nodeID: {}}
	for _, t := range s.graph.Trustees(nodeID) {
		affected[t] = struct{}{}
	}
	// Funds changes don't alter reachability, so webs are stable.
	return s.rescore(affected, nil)
}

// webOf computes evaluator + transitive trustees.
func (s *Service) webOf(id string) map[string]struct{} {
	web := map[string]struct{}{id: {}}
	for _, n := range s.graph.TransitiveTrustees(id, 0) {
		web[n] = struct{}{}
	}
	return web
}

// recomputeAfterStructuralChange refreshes web snapshots, folds reachability
// deltas into the affected-subject set per evaluator, and rescores.
// Callers hold s.mu. baseAffected = subjects whose truster set changed.
func (s *Service) recomputeAfterStructuralChange(baseAffected map[string]struct{}) []StatusChange {
	perEvaluatorExtra := map[string]map[string]struct{}{}
	for evaluator := range s.status {
		oldWeb := s.web[evaluator]
		newWeb := s.webOf(evaluator)
		s.web[evaluator] = newWeb
		delta := map[string]struct{}{}
		for n := range oldWeb {
			if _, ok := newWeb[n]; !ok {
				delta[n] = struct{}{}
			}
		}
		for n := range newWeb {
			if _, ok := oldWeb[n]; !ok {
				delta[n] = struct{}{}
			}
		}
		if len(delta) == 0 {
			continue
		}
		extra := map[string]struct{}{}
		for n := range delta {
			// n's membership in this evaluator's web changed → every subject
			// n directly trusts has its among-trusted splits moved.
			for _, t := range s.graph.Trustees(n) {
				extra[t] = struct{}{}
			}
			// n itself gains/loses "new subjects appear" — cover n too (its
			// direct-edge / cache row may be new to this evaluator).
			extra[n] = struct{}{}
		}
		perEvaluatorExtra[evaluator] = extra
	}
	return s.rescore(baseAffected, perEvaluatorExtra)
}

// rescore recomputes (evaluator, subject) pairs = (all tracked evaluators ×
// base) ∪ per-evaluator extras, updates the cache, and returns flips. Also
// inserts cache rows for subjects that newly appeared in the graph.
// Callers hold s.mu.
func (s *Service) rescore(base map[string]struct{}, perEvaluatorExtra map[string]map[string]struct{}) []StatusChange {
	changes := []StatusChange{}
	now := s.nowMs()
	inGraph := map[string]struct{}{}
	for _, id := range s.graph.Nodes() {
		inGraph[id] = struct{}{}
	}
	for evaluator, snap := range s.status {
		subjects := map[string]struct{}{}
		for id := range base {
			subjects[id] = struct{}{}
		}
		for id := range perEvaluatorExtra[evaluator] {
			subjects[id] = struct{}{}
		}
		// Subjects new to the graph since the snapshot get a row.
		for id := range inGraph {
			if id == evaluator {
				continue
			}
			if _, ok := snap[id]; !ok {
				subjects[id] = struct{}{}
			}
		}
		for subject := range subjects {
			if subject == evaluator {
				continue
			}
			// A removed node can re-enter via web deltas — drop its stale
			// row instead of rescoring a ghost.
			if _, ok := inGraph[subject]; !ok {
				delete(snap, subject)
				continue
			}
			score := s.eval.Score(evaluator, subject)
			trusted := score >= s.eval.Config.TrustThreshold
			old, had := snap[subject]
			snap[subject] = Status{Score: score, Trusted: trusted}
			if had && old.Trusted != trusted {
				changes = append(changes, StatusChange{
					Evaluator:  evaluator,
					Subject:    subject,
					OldScore:   old.Score,
					NewScore:   score,
					OldTrusted: old.Trusted,
					NewTrusted: trusted,
					AtMs:       now,
				})
			} else if !had && trusted {
				// A brand-new subject that immediately clears the threshold
				// is a flip from the implicit untrusted default.
				changes = append(changes, StatusChange{
					Evaluator:  evaluator,
					Subject:    subject,
					NewScore:   score,
					OldTrusted: false,
					NewTrusted: true,
					AtMs:       now,
				})
			}
		}
	}
	return changes
}

// NeighborhoodOf exposes the DAG neighborhood for event fan-out (WS11.4):
// the audience for a subject's status change.
func (s *Service) NeighborhoodOf(id string, maxDepth int) []string {
	return s.graph.Neighborhood(id, maxDepth)
}
