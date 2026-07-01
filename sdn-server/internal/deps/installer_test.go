package deps

import (
	"errors"
	"testing"
)

// recordInstaller records install order and can fail on a chosen plugin.
type recordInstaller struct {
	order  []string
	failOn string
}

func (r *recordInstaller) fn(step PlanStep) error {
	if step.PluginID == r.failOn {
		return errors.New("boom")
	}
	r.order = append(r.order, step.PluginID)
	return nil
}

func TestInstallLinearChainDepsFirstThenRoot(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "c", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "c", Version: "1.0.0"},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}

	rec := &recordInstaller{}
	done, err := Install(root, nil, catalog, rec.fn)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	// c (deepest dep) -> b -> a (root, last).
	want := []string{"c", "b", "a"}
	if got := rec.order; !equalStrings(got, want) {
		t.Fatalf("install order = %v, want %v", got, want)
	}
	if len(done) != 3 || done[2].PluginID != "a" {
		t.Errorf("done = %+v, want root last", done)
	}
}

func TestInstallSkipsInstalledDepsAndRoot(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0"},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}

	// b and a both already installed -> nothing to install.
	rec := &recordInstaller{}
	done, err := Install(root, MapInstalled{"a": "1.0.0", "b": "1.0.0"}, catalog, rec.fn)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(done) != 0 || len(rec.order) != 0 {
		t.Fatalf("expected no installs, got %v", rec.order)
	}
}

func TestInstallOnlyMissingDep(t *testing.T) {
	catalog := NewMapCatalog(Manifest{PluginID: "b", Version: "1.0.0"})
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}

	// Root already installed, dep b missing -> install just b (repair).
	rec := &recordInstaller{}
	done, err := Install(root, MapInstalled{"a": "1.0.0"}, catalog, rec.fn)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if !equalStrings(rec.order, []string{"b"}) {
		t.Fatalf("install order = %v, want [b]", rec.order)
	}
	if len(done) != 1 {
		t.Errorf("done = %+v", done)
	}
}

func TestInstallDiamondEachOnce(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "d", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "c", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "d", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "d", Version: "1.0.0"},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{
		{PluginID: "b", MinVersion: "1.0.0"},
		{PluginID: "c", MinVersion: "1.0.0"},
	}}

	rec := &recordInstaller{}
	if _, err := Install(root, nil, catalog, rec.fn); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	// d installed exactly once, before b and c; a last.
	if count(rec.order, "d") != 1 {
		t.Errorf("d installed %d times, want 1 (%v)", count(rec.order, "d"), rec.order)
	}
	if idx(rec.order, "d") > idx(rec.order, "b") || idx(rec.order, "d") > idx(rec.order, "c") {
		t.Errorf("d must install before b and c: %v", rec.order)
	}
	if rec.order[len(rec.order)-1] != "a" {
		t.Errorf("root a must install last: %v", rec.order)
	}
}

func TestInstallAbortsOnFailure(t *testing.T) {
	catalog := NewMapCatalog(
		Manifest{PluginID: "b", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "c", MinVersion: "1.0.0"}}},
		Manifest{PluginID: "c", Version: "1.0.0"},
	)
	root := Manifest{PluginID: "a", Version: "1.0.0", Dependencies: []Dependency{{PluginID: "b", MinVersion: "1.0.0"}}}

	rec := &recordInstaller{failOn: "b"}
	done, err := Install(root, nil, catalog, rec.fn)
	if err == nil {
		t.Fatal("expected install error")
	}
	// c installed before the failure at b; a never reached.
	if !equalStrings(planIDs(done), []string{"c"}) {
		t.Errorf("done = %v, want [c] before failure", planIDs(done))
	}
	if contains(rec.order, "a") {
		t.Error("root must not install after a dependency fails")
	}
}

func TestInstallRequiresFunc(t *testing.T) {
	if _, err := Install(Manifest{PluginID: "a"}, nil, NewMapCatalog(), nil); err == nil {
		t.Error("expected error for nil install func")
	}
}

func TestInstallPropagatesResolveError(t *testing.T) {
	root := Manifest{PluginID: "a", Dependencies: []Dependency{{PluginID: "ghost", MinVersion: "1.0.0"}}}
	rec := &recordInstaller{}
	if _, err := Install(root, nil, NewMapCatalog(), rec.fn); !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("error = %v, want ErrDependencyNotFound", err)
	}
	if len(rec.order) != 0 {
		t.Error("nothing should install when resolution fails")
	}
}

// --- small helpers ---

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool { return idx(s, v) >= 0 }

func idx(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func count(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
