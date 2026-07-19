//go:build linux

package flowcc

// stage_modules_dist_test.go — verifies StageModulesFromDist stages ALL
// seven OD-flow modules from the REAL modules dist (the prod boot path
// maybeInstallOperatorOMMFlow depends on). Regression guard for the manifest
// layout (modules ship plugin-manifest.json inside dist/guest-link/).
// Env: SDN_ODSUP_MODULES_ROOT (space-data-network-modules root).

import (
	"os"
	"sort"
	"testing"
)

func TestStageModulesFromDistStagesODFlow(t *testing.T) {
	root := os.Getenv("SDN_ODSUP_MODULES_ROOT")
	if root == "" {
		t.Skip("set SDN_ODSUP_MODULES_ROOT")
	}
	home := HomeAt(t.TempDir())
	staged, err := StageModulesFromDist(home, root)
	if err != nil {
		t.Fatalf("StageModulesFromDist: %v", err)
	}
	sort.Strings(staged)
	t.Logf("staged pluginIds (%d): %v", len(staged), staged)
	want := []string{
		"com.orbpro.iss-source",
		"com.orbpro.glonass-source",
		"com.orbpro.spacex-starlink-source",
		"com.orbpro.intelsat-source",
		"com.orbpro.cpf-source",
		"orbit-determination",
		"com.digitalarsenal.hostcap.flatsql-store",
	}
	have := map[string]bool{}
	for _, s := range staged {
		have[s] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("OD-flow module NOT staged: %s", w)
		}
	}
}
