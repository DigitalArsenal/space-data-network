package license

import "testing"

func TestNormalizeDependencies(t *testing.T) {
	got := normalizeDependencies([]PluginDependencyRef{
		{PluginID: "  com.orbpro.validator ", MinVersion: " 1.2.0 ", MaxVersion: ""},
		{PluginID: ""}, // dropped: empty plugin ID
		{PluginID: "com.orbpro.parser", MinVersion: "0.1.0"},
		{PluginID: "com.orbpro.validator", MinVersion: "1.3.0"}, // dedupe: last declaration wins
	})

	if len(got) != 2 {
		t.Fatalf("normalizeDependencies() len = %d, want 2 (%+v)", len(got), got)
	}
	// Sorted by plugin ID: parser precedes validator.
	if got[0].PluginID != "com.orbpro.parser" || got[0].MinVersion != "0.1.0" {
		t.Errorf("got[0] = %+v, want {com.orbpro.parser 0.1.0}", got[0])
	}
	// Trimmed + last declaration's version bound wins.
	if got[1].PluginID != "com.orbpro.validator" || got[1].MinVersion != "1.3.0" || got[1].MaxVersion != "" {
		t.Errorf("got[1] = %+v, want {com.orbpro.validator 1.3.0 (empty max)}", got[1])
	}

	if normalizeDependencies(nil) != nil {
		t.Error("normalizeDependencies(nil) should return nil")
	}
	if normalizeDependencies([]PluginDependencyRef{{PluginID: "   "}}) != nil {
		t.Error("normalizeDependencies() of all-empty IDs should return nil")
	}
}

func TestCloneDependenciesDetaches(t *testing.T) {
	src := []PluginDependencyRef{{PluginID: "a", MinVersion: "1.0.0"}}
	cp := cloneDependencies(src)
	cp[0].MinVersion = "9.9.9"
	if src[0].MinVersion != "1.0.0" {
		t.Errorf("cloneDependencies did not detach: source mutated to %q", src[0].MinVersion)
	}
	if cloneDependencies(nil) != nil {
		t.Error("cloneDependencies(nil) should return nil")
	}
}
