// Package trust implements the SDN web-of-trust subsystem (WS11): a directed
// ACYCLIC graph of trust assertions between node identities, with computed
// trust scores and live re-evaluation layered on top.
//
// This file is the DAG core (WS11.1): the node/edge model, acyclicity
// enforcement (an edge that would create a cycle is rejected at insert),
// deterministic topological ordering, and transitive traversal (the "web of
// trust neighborhood" primitive that scoring and pub/sub eventing build on).
package trust

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Edge is one directed trust assertion: Truster vouches for Trustee.
// Weight is the truster-assigned base weight in [0,1]; the computed trust
// score machinery (WS11.2) combines it with funds/fund-type/graph inputs.
type Edge struct {
	Truster     string
	Trustee     string
	Weight      float64
	UpdatedAtMs int64
}

var (
	// ErrCycle is returned when inserting an edge would create a cycle.
	ErrCycle = errors.New("trust: edge would create a cycle")
	// ErrSelfTrust is returned for self-referential edges.
	ErrSelfTrust = errors.New("trust: a node cannot trust itself")
	// ErrNotFound is returned when a referenced node or edge does not exist.
	ErrNotFound = errors.New("trust: not found")
)

// Graph is an in-memory trust DAG. All mutating operations preserve the
// acyclic invariant. Safe for concurrent use.
type Graph struct {
	mu    sync.RWMutex
	nodes map[string]struct{}
	out   map[string]map[string]*Edge // truster -> trustee -> edge
	in    map[string]map[string]*Edge // trustee -> truster -> edge
}

// NewGraph creates an empty trust DAG.
func NewGraph() *Graph {
	return &Graph{
		nodes: map[string]struct{}{},
		out:   map[string]map[string]*Edge{},
		in:    map[string]map[string]*Edge{},
	}
}

// AddNode registers an identity with no edges (idempotent).
func (g *Graph) AddNode(id string) error {
	if id == "" {
		return errors.New("trust: node id required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes[id] = struct{}{}
	return nil
}

// SetEdge inserts or updates the Truster→Trustee edge. Inserting a NEW edge
// that would close a cycle fails with ErrCycle (updating an existing edge's
// weight cannot create one). Nodes are auto-registered.
func (g *Graph) SetEdge(e Edge) error {
	if e.Truster == "" || e.Trustee == "" {
		return errors.New("trust: edge endpoints required")
	}
	if e.Truster == e.Trustee {
		return ErrSelfTrust
	}
	if e.Weight < 0 || e.Weight > 1 {
		return fmt.Errorf("trust: weight %v outside [0,1]", e.Weight)
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.out[e.Truster][e.Trustee]; !exists {
		// New edge Truster→Trustee closes a cycle iff Truster is reachable
		// FROM Trustee along existing edges.
		if g.reachesLocked(e.Trustee, e.Truster) {
			return ErrCycle
		}
	}
	g.nodes[e.Truster] = struct{}{}
	g.nodes[e.Trustee] = struct{}{}
	stored := e
	if g.out[e.Truster] == nil {
		g.out[e.Truster] = map[string]*Edge{}
	}
	if g.in[e.Trustee] == nil {
		g.in[e.Trustee] = map[string]*Edge{}
	}
	g.out[e.Truster][e.Trustee] = &stored
	g.in[e.Trustee][e.Truster] = &stored
	return nil
}

// RemoveEdge deletes the Truster→Trustee edge.
func (g *Graph) RemoveEdge(truster, trustee string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.out[truster][trustee]; !ok {
		return ErrNotFound
	}
	delete(g.out[truster], trustee)
	delete(g.in[trustee], truster)
	return nil
}

// RemoveNode deletes an identity and every edge touching it.
func (g *Graph) RemoveNode(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.nodes[id]; !ok {
		return ErrNotFound
	}
	for trustee := range g.out[id] {
		delete(g.in[trustee], id)
	}
	for truster := range g.in[id] {
		delete(g.out[truster], id)
	}
	delete(g.out, id)
	delete(g.in, id)
	delete(g.nodes, id)
	return nil
}

// Edge returns the Truster→Trustee edge, if present.
func (g *Graph) Edge(truster, trustee string) (Edge, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if e, ok := g.out[truster][trustee]; ok {
		return *e, true
	}
	return Edge{}, false
}

// Nodes returns all identities, sorted.
func (g *Graph) Nodes() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Edges returns all edges, sorted by (truster, trustee).
func (g *Graph) Edges() []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Edge, 0)
	for _, m := range g.out {
		for _, e := range m {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Truster != out[j].Truster {
			return out[i].Truster < out[j].Truster
		}
		return out[i].Trustee < out[j].Trustee
	})
	return out
}

// Trustees returns the identities id directly trusts, sorted.
func (g *Graph) Trustees(id string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedKeys(g.out[id])
}

// Trusters returns the identities that directly trust id, sorted.
func (g *Graph) Trusters(id string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedKeys(g.in[id])
}

// TransitiveTrustees returns every identity reachable FROM id along trust
// edges within maxDepth hops (maxDepth <= 0 means unbounded), excluding id.
func (g *Graph) TransitiveTrustees(id string, maxDepth int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.bfsLocked(id, maxDepth, g.out)
}

// TransitiveTrusters returns every identity that transitively trusts id
// within maxDepth hops (maxDepth <= 0 means unbounded), excluding id.
func (g *Graph) TransitiveTrusters(id string, maxDepth int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.bfsLocked(id, maxDepth, g.in)
}

// Neighborhood returns id's web-of-trust neighborhood within maxDepth hops in
// EITHER direction (trusters + trustees), excluding id — the audience for
// trust-status pub/sub events (WS11.4).
func (g *Graph) Neighborhood(id string, maxDepth int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, n := range g.bfsLocked(id, maxDepth, g.out) {
		seen[n] = struct{}{}
	}
	for _, n := range g.bfsLocked(id, maxDepth, g.in) {
		seen[n] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TopoOrder returns a deterministic topological ordering of all nodes
// (trusters before their trustees; ties broken lexically). The acyclic
// invariant guarantees this always succeeds.
func (g *Graph) TopoOrder() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	indegree := map[string]int{}
	for id := range g.nodes {
		indegree[id] = len(g.in[id])
	}
	ready := make([]string, 0)
	for id, d := range indegree {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(g.nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		next := sortedKeys(g.out[id])
		inserted := false
		for _, t := range next {
			indegree[t]--
			if indegree[t] == 0 {
				ready = append(ready, t)
				inserted = true
			}
		}
		if inserted {
			sort.Strings(ready)
		}
	}
	return order
}

// reachesLocked reports whether `to` is reachable from `from` (callers hold
// the lock). DFS over out-edges.
func (g *Graph) reachesLocked(from, to string) bool {
	if from == to {
		return true
	}
	stack := []string{from}
	seen := map[string]struct{}{from: {}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for next := range g.out[cur] {
			if next == to {
				return true
			}
			if _, ok := seen[next]; !ok {
				seen[next] = struct{}{}
				stack = append(stack, next)
			}
		}
	}
	return false
}

func (g *Graph) bfsLocked(start string, maxDepth int, adj map[string]map[string]*Edge) []string {
	type qe struct {
		id    string
		depth int
	}
	seen := map[string]struct{}{start: {}}
	queue := []qe{{start, 0}}
	out := make([]string, 0)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if maxDepth > 0 && cur.depth >= maxDepth {
			continue
		}
		for next := range adj[cur.id] {
			if _, ok := seen[next]; ok {
				continue
			}
			seen[next] = struct{}{}
			out = append(out, next)
			queue = append(queue, qe{next, cur.depth + 1})
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]*Edge) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
