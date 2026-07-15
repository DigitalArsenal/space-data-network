package flowrt

import (
	"strings"
	"testing"
)

func TestCompiledRuntimeABINamesUseSDKPrefix(t *testing.T) {
	retiredPrefix := strings.Join([]string{"sdn", "flow"}, "_") + "_"
	for _, name := range compiledRuntimeExportNames {
		if strings.Contains(name, retiredPrefix) {
			t.Fatalf("compiled runtime export %q uses retired SDN flow prefix", name)
		}
		if !strings.HasPrefix(name, "space_data_module_runtime_") {
			t.Fatalf("compiled runtime export %q does not use SDK runtime prefix", name)
		}
	}
}

func TestUnderscoreRuntimeExportNameUsesSDKSymbol(t *testing.T) {
	got := underscoreRuntimeExportName(runtimeExportBeginInvocation)
	want := "_space_data_module_runtime_begin_node_invocation"
	if got != want {
		t.Fatalf("underscore fallback = %q, want %q", got, want)
	}
}
