package deps

import (
	"errors"
	"testing"
)

func planIDs(steps []PlanStep) []string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.PluginID
	}
	return ids
}

// indexOf returns the position of id in steps, or -1.
func indexOf(steps []PlanStep, id string) int {
	for i, s := range steps {
		if s.PluginID == id {
			return i
		}
	}
	return -1
}

func TestResolveClosureLinearChainTopoOrder(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "c", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "c", Version: "1.2.0"},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}

	plan, err := ResolveClosure(root, nil, catalog)
	if err != nil {
		t.Fatalf("ResolveClosure() error = %v", err)
	}
	// c must precede b; root a is not in the plan.
	if got := planIDs(plan); len(got) != 2 || got[0] != "c" || got[1] != "b" {
		t.Fatalf("plan = %v, want [c b]", got)
	}
	if plan[0].Version != "1.2.0" {
		t.Errorf("c version = %q, want 1.2.0 (highest satisfying)", plan[0].Version)
	}
}

func TestResolveClosureDiamondDedup(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "d", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "c", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "d", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "d", Version: "1.5.0"},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{
		{PluginID: "b", MinVersion: "1.0.0"},
		{PluginID: "c", MinVersion: "1.0.0"},
	}}

	plan, err := ResolveClosure(root, nil, catalog)
	if err != nil {
		t.Fatalf("ResolveClosure() error = %v", err)
	}
	if len(plan) != 3 {
		t.Fatalf("plan = %v, want 3 unique steps", planIDs(plan))
	}
	// d must appear exactly once and before both b and c.
	dIdx, bIdx, cIdx := indexOf(plan, "d"), indexOf(plan, "b"), indexOf(plan, "c")
	if dIdx < 0 || bIdx < 0 || cIdx < 0 {
		t.Fatalf("plan missing a node: %v", planIDs(plan))
	}
	if !(dIdx < bIdx && dIdx < cIdx) {
		t.Errorf("plan = %v, want d before b and c", planIDs(plan))
	}
}

func TestResolveClosureSkipsSatisfiedInstalled(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "c", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "c", Version: "1.0.0"},
	)
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}

	// b already installed at a satisfying version -> not pulled, and its subtree
	// (c) is trusted complete and not walked.
	plan, err := ResolveClosure(root, MapInstalled{"b": "1.4.0"}, catalog)
	if err != nil {
		t.Fatalf("ResolveClosure() error = %v", err)
	}
	if len(plan) != 0 {
		t.Fatalf("plan = %v, want empty (b installed, subtree trusted)", planIDs(plan))
	}
}

func TestResolveClosureInstalledConflict(t *testing.T) {
	catalog := NewMapCatalog(Manifest{PluginID: "b", Version: "2.0.0"})
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "b", MinVersion: "2.0.0"}}}

	_, err := ResolveClosure(root, MapInstalled{"b": "1.0.0"}, catalog)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("error = %v, want ErrVersionConflict", err)
	}
}

func TestResolveClosureMissingDependency(t *testing.T) {
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "ghost", MinVersion: "1.0.0"}}}
	_, err := ResolveClosure(root, nil, NewMapCatalog())
	if !errors.Is(err, ErrDependencyNotFound) {
		t.Fatalf("error = %v, want ErrDependencyNotFound", err)
	}
}

func TestResolveClosureNoSatisfyingVersion(t *testing.T) {
	catalog := NewMapCatalog(Manifest{PluginID: "b", Version: "1.0.0"})
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "b", MinVersion: "2.0.0"}}}
	_, err := ResolveClosure(root, nil, catalog)
	if !errors.Is(err, ErrNoSatisfyingVersion) {
		t.Fatalf("error = %v, want ErrNoSatisfyingVersion", err)
	}
}

func TestResolveClosureSelectsWithinRange(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0"},
		Manifest{PluginID: "b", Version: "1.5.0"},
		Manifest{PluginID: "b", Version: "2.0.0"},
	)
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0", MaxVersion: "1.9.0"}}}
	plan, err := ResolveClosure(root, nil, catalog)
	if err != nil {
		t.Fatalf("ResolveClosure() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Version != "1.5.0" {
		t.Fatalf("plan = %+v, want single b@1.5.0 (highest within [1.0.0,1.9.0])", plan)
	}
}

func TestResolveClosureCycle(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "a", MinVersion: "1.0.0"}}},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}
	_, err := ResolveClosure(root, nil, catalog)
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("error = %v, want ErrDependencyCycle", err)
	}
}

func TestResolveClosureSelfCycle(t *testing.T) {
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "a", MinVersion: "1.0.0"}}}
	_, err := ResolveClosure(root, nil, NewMapCatalog())
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("error = %v, want ErrDependencyCycle", err)
	}
}

func TestResolveClosureDiamondVersionConflict(t *testing.T) {
	// b needs d>=2.0.0, c needs d<=1.9.0 -> no single d satisfies both.
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "d", MinVersion: "2.0.0"}}},
		Manifest{PluginID: "c", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "d", MaxVersion: "1.9.0"}}},
		Manifest{PluginID: "d", Version: "2.5.0"},
		Manifest{PluginID: "d", Version: "1.5.0"},
	)
	root := Manifest{PluginID: "a", Dependencies: []Dependency{
		{PluginID: "b", MinVersion: "1.0.0"},
		{PluginID: "c", MinVersion: "1.0.0"},
	}}
	_, err := ResolveClosure(root, nil, catalog)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("error = %v, want ErrVersionConflict", err)
	}
}

func TestResolveClosureInvalidRange(t *testing.T) {
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "b", MinVersion: "not-a-version"}}}
	_, err := ResolveClosure(root, nil, NewMapCatalog())
	if !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("error = %v, want ErrInvalidVersion", err)
	}

	root2 := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "b", MinVersion: "2.0.0", MaxVersion: "1.0.0"}}}
	if _, err := ResolveClosure(root2, nil, NewMapCatalog()); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("error = %v, want ErrInvalidVersion (min>max)", err)
	}
}

func TestResolveClosureNilCatalog(t *testing.T) {
	if _, err := ResolveClosure(Manifest{PluginID: "a"}, nil, nil); err == nil {
		t.Fatal("expected error for nil catalog")
	}
}

func TestResolveClosureNoDependencies(t *testing.T) {
	plan, err := ResolveClosure(Manifest{PluginID: "a", Version: "1.0.0"}, nil, NewMapCatalog())
	if err != nil {
		t.Fatalf("ResolveClosure() error = %v", err)
	}
	if len(plan) != 0 {
		t.Fatalf("plan = %v, want empty", planIDs(plan))
	}
}

func TestSatisfies(t *testing.T) {
	cases := []struct {
		version string
		dep     Dependency
		want    bool
	}{
		{"1.0.0", Dependency{MinVersion: "1.0.0"}, true},
		{"0.9.0", Dependency{MinVersion: "1.0.0"}, false},
		{"1.0.0", Dependency{MaxVersion: "1.0.0"}, true},
		{"1.0.1", Dependency{MaxVersion: "1.0.0"}, false},
		{"1.5.0", Dependency{MinVersion: "1.0.0", MaxVersion: "2.0.0"}, true},
		{"2.0.1", Dependency{MinVersion: "1.0.0", MaxVersion: "2.0.0"}, false},
		{"1.2.3", Dependency{}, true},                      // unbounded
		{"v1.2.3", Dependency{MinVersion: "v1.0.0"}, true}, // pre-normalized "v"
		{"garbage", Dependency{}, false},
	}
	for _, tc := range cases {
		if got := satisfies(tc.version, tc.dep); got != tc.want {
			t.Errorf("satisfies(%q, %+v) = %v, want %v", tc.version, tc.dep, got, tc.want)
		}
	}
}
