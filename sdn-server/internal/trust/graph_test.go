package trust

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func mustSetEdge(t *testing.T, g *Graph, truster, trustee string, w float64) {
	t.Helper()
	if err := g.SetEdge(Edge{Truster: truster, Trustee: trustee, Weight: w}); err != nil {
		t.Fatalf("SetEdge %s->%s: %v", truster, trustee, err)
	}
}

func TestAcyclicityEnforced(t *testing.T) {
	g := NewGraph()
	mustSetEdge(t, g, "a", "b", 0.9)
	mustSetEdge(t, g, "b", "c", 0.8)
	mustSetEdge(t, g, "a", "c", 0.7) // diamond edge, still acyclic

	// Closing the cycle c->a must be rejected.
	if err := g.SetEdge(Edge{Truster: "c", Trustee: "a", Weight: 0.5}); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
	// Direct back-edge b->a also rejected.
	if err := g.SetEdge(Edge{Truster: "b", Trustee: "a", Weight: 0.5}); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle for back-edge, got %v", err)
	}
	// Self-trust rejected.
	if err := g.SetEdge(Edge{Truster: "a", Trustee: "a", Weight: 1}); !errors.Is(err, ErrSelfTrust) {
		t.Fatalf("expected ErrSelfTrust, got %v", err)
	}
	// Weight bounds.
	if err := g.SetEdge(Edge{Truster: "a", Trustee: "d", Weight: 1.5}); err == nil {
		t.Fatal("expected weight-range error")
	}

	// Updating an EXISTING edge is always fine (cannot create a cycle).
	if err := g.SetEdge(Edge{Truster: "a", Trustee: "b", Weight: 0.4}); err != nil {
		t.Fatalf("weight update rejected: %v", err)
	}
	if e, _ := g.Edge("a", "b"); e.Weight != 0.4 {
		t.Fatalf("weight not updated: %v", e.Weight)
	}

	// Removing b->c breaks the path a→…→c, so c->a becomes legal... it does
	// NOT: a->c still exists. Removing that too makes c->a legal.
	if err := g.RemoveEdge("b", "c"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEdge(Edge{Truster: "c", Trustee: "a", Weight: 0.5}); !errors.Is(err, ErrCycle) {
		t.Fatalf("a->c still present; expected ErrCycle, got %v", err)
	}
	if err := g.RemoveEdge("a", "c"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEdge(Edge{Truster: "c", Trustee: "a", Weight: 0.5}); err != nil {
		t.Fatalf("c->a should be legal after path removal: %v", err)
	}
}

func TestTopoOrderDeterministic(t *testing.T) {
	build := func() *Graph {
		g := NewGraph()
		mustSetEdge(t, g, "root", "mid1", 1)
		mustSetEdge(t, g, "root", "mid2", 1)
		mustSetEdge(t, g, "mid1", "leaf", 1)
		mustSetEdge(t, g, "mid2", "leaf", 1)
		_ = g.AddNode("island")
		return g
	}
	g := build()
	order := g.TopoOrder()
	if len(order) != 5 {
		t.Fatalf("topo covers %d nodes, want 5", len(order))
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, e := range g.Edges() {
		if pos[e.Truster] >= pos[e.Trustee] {
			t.Fatalf("topo violation: %s (%d) not before %s (%d)", e.Truster, pos[e.Truster], e.Trustee, pos[e.Trustee])
		}
	}
	// Deterministic across rebuilds.
	for i := 0; i < 3; i++ {
		if got := build().TopoOrder(); !reflect.DeepEqual(got, order) {
			t.Fatalf("topo order not deterministic: %v vs %v", got, order)
		}
	}
}

func TestTraversalAndNeighborhood(t *testing.T) {
	g := NewGraph()
	// a -> b -> c -> d ;  x -> b
	mustSetEdge(t, g, "a", "b", 1)
	mustSetEdge(t, g, "b", "c", 1)
	mustSetEdge(t, g, "c", "d", 1)
	mustSetEdge(t, g, "x", "b", 1)

	if got := g.Trustees("b"); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("Trustees(b) = %v", got)
	}
	if got := g.Trusters("b"); !reflect.DeepEqual(got, []string{"a", "x"}) {
		t.Fatalf("Trusters(b) = %v", got)
	}
	if got := g.TransitiveTrustees("a", 0); !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Fatalf("TransitiveTrustees(a) = %v", got)
	}
	if got := g.TransitiveTrustees("a", 2); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("TransitiveTrustees(a, depth 2) = %v", got)
	}
	if got := g.TransitiveTrusters("d", 0); !reflect.DeepEqual(got, []string{"a", "b", "c", "x"}) {
		t.Fatalf("TransitiveTrusters(d) = %v", got)
	}
	// b's neighborhood both directions, depth 1: trusters {a,x} + trustees {c}.
	if got := g.Neighborhood("b", 1); !reflect.DeepEqual(got, []string{"a", "c", "x"}) {
		t.Fatalf("Neighborhood(b,1) = %v", got)
	}
	// Unbounded: everyone but b.
	if got := g.Neighborhood("b", 0); !reflect.DeepEqual(got, []string{"a", "c", "d", "x"}) {
		t.Fatalf("Neighborhood(b) = %v", got)
	}
}

func TestRemoveNode(t *testing.T) {
	g := NewGraph()
	mustSetEdge(t, g, "a", "b", 1)
	mustSetEdge(t, g, "b", "c", 1)
	if err := g.RemoveNode("b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Edge("a", "b"); ok {
		t.Fatal("edge a->b survived node removal")
	}
	if _, ok := g.Edge("b", "c"); ok {
		t.Fatal("edge b->c survived node removal")
	}
	if got := g.Nodes(); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("Nodes = %v", got)
	}
	if err := g.RemoveNode("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	// With b gone, c->a is legal (no path a→…→c anymore).
	if err := g.SetEdge(Edge{Truster: "c", Trustee: "a", Weight: 1}); err != nil {
		t.Fatalf("c->a after removal: %v", err)
	}
}

func TestStorePersistenceRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}

	g := NewGraph()
	edges := []Edge{
		{Truster: "a", Trustee: "b", Weight: 0.9, UpdatedAtMs: 100},
		{Truster: "b", Trustee: "c", Weight: 0.8, UpdatedAtMs: 200},
		{Truster: "a", Trustee: "c", Weight: 0.7, UpdatedAtMs: 300},
	}
	for _, e := range edges {
		if err := g.SetEdge(e); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	_ = g.AddNode("island")
	if err := store.UpsertNode("island"); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadGraph()
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if !reflect.DeepEqual(loaded.Nodes(), g.Nodes()) {
		t.Fatalf("nodes: %v vs %v", loaded.Nodes(), g.Nodes())
	}
	if !reflect.DeepEqual(loaded.Edges(), g.Edges()) {
		t.Fatalf("edges: %v vs %v", loaded.Edges(), g.Edges())
	}

	// Upsert updates in place.
	upd := Edge{Truster: "a", Trustee: "b", Weight: 0.5, UpdatedAtMs: 400}
	if err := store.UpsertEdge(upd); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := reloaded.Edge("a", "b"); e.Weight != 0.5 || e.UpdatedAtMs != 400 {
		t.Fatalf("upsert not applied: %+v", e)
	}

	// Delete edge + node.
	if err := store.DeleteEdge("a", "c"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode("c"); err != nil {
		t.Fatal(err)
	}
	final, err := store.LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := final.Edge("b", "c"); ok {
		t.Fatal("edge b->c survived DeleteNode(c)")
	}

	// A corrupted (cyclic) row image must fail loudly on load.
	if _, err := db.Exec(`INSERT INTO trust_edges(truster, trustee, weight, updated_at) VALUES ('b','a',1,500)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadGraph(); err == nil {
		t.Fatal("cyclic row image loaded silently")
	}
}
