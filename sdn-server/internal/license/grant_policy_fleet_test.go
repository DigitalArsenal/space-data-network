package license

import (
	"os"
	"path/filepath"
	"testing"
)

// liveCatalogModuleIDs is the catalog audited on host-01 on 2026-08-07 — the
// only box in the fleet carrying one. All 43 entries had allowed_xpubs=0, so
// all 43 were unrestricted. The deployed policy file must rule on every one of
// them deliberately; a module that falls through to the fail-closed default is
// a module that stops being served, and this test is what catches that before
// a roll rather than after.
var liveCatalogModuleIDs = []string{
	"com.digitalarsenal.examples.remote-sdn-live-e2e",
	"com.digitalarsenal.flows.data-retrieval",
	"com.orbpro.access",
	"com.orbpro.analysis.orbit-determination",
	"com.orbpro.fastest-path",
	"com.orbpro.hpop",
	"com.orbpro.maneuver",
	"com.orbpro.rf-antenna-pattern",
	"com.orbpro.rf-atmospheric-gaseous",
	"com.orbpro.rf-ber-modulation",
	"com.orbpro.rf-cloud-fog",
	"com.orbpro.rf-diffraction",
	"com.orbpro.rf-doppler-fresnel",
	"com.orbpro.rf-empirical",
	"com.orbpro.rf-fspl",
	"com.orbpro.rf-link-budget",
	"com.orbpro.rf-longley-rice",
	"com.orbpro.rf-rain",
	"com.orbpro.sensor-shaders",
	"com.orbpro.sensor-shaders.glsl-bundle",
	"com.orbpro.sgp4",
	"com.orbpro.viewshed-shader",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-0",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-1",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-2",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-3",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-4",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-5",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-6",
	"com.orbpro.viewshed-shader.frustum-fragment.fragment-7",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-0",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-1",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-2",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-3",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-4",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-5",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-6",
	"com.orbpro.viewshed-shader.frustum-vertex.fragment-7",
	"com.orbpro.viewshed-shader.uniforms",
	"com.orbpro.wasm-engine",
	"com.spaceaware.test.add-two",
	"licensing",
	"org.spacedata.analysis.conjunction.assessment",
}

// loadFleetPolicy reads the policy file that ships to the fleet, from the repo,
// through the SAME loader the node uses.
func loadFleetPolicy(t *testing.T) *GrantPolicyConfig {
	t.Helper()
	source := filepath.Join("..", "..", "..", "deployment", "license", "grant-policy.json")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read deployment/license/grant-policy.json: %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, GrantPolicyFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGrantPolicyConfig(root)
	if err != nil {
		t.Fatalf("the file that ships to the fleet does not load: %v", err)
	}
	return cfg
}

func TestFleetPolicyClosesHpopAndOnlyHpop(t *testing.T) {
	cfg := loadFleetPolicy(t)

	refused := make([]string, 0, 4)
	for _, id := range liveCatalogModuleIDs {
		// Every live entry has an EMPTY allowlist — that is the audited fact.
		decision := EvaluatePublication(&PluginAsset{ID: id, Version: "1.0.0"}, cfg)
		if !decision.Publish {
			refused = append(refused, id)
		}
	}

	if len(refused) != 1 || refused[0] != "com.orbpro.hpop" {
		t.Fatalf("the fleet policy refuses %v; want exactly [com.orbpro.hpop] — anything else means the roll "+
			"stops serving a module that is in use today", refused)
	}
}

func TestFleetPolicyKeepsEveryRFGalleryModuleOpen(t *testing.T) {
	cfg := loadFleetPolicy(t)

	for _, id := range liveCatalogModuleIDs {
		if len(id) < 14 || id[:14] != "com.orbpro.rf-" {
			continue
		}
		resolved := cfg.Resolve(id, "")
		if resolved.Policy != GrantPolicyLinkKey {
			t.Fatalf("%s resolved to %q, want %q — the owner's private gallery link derives its identity from "+
				"the URL UUID and cannot be on an allowlist", id, resolved.Policy, GrantPolicyLinkKey)
		}
	}
}

// The licensing client module is load-bearing: close it and no client can run
// the challenge that every other policy depends on.
func TestFleetPolicyKeepsTheLicensingClientReachable(t *testing.T) {
	cfg := loadFleetPolicy(t)

	if !EvaluatePublication(&PluginAsset{ID: "licensing", Version: "0.1.0"}, cfg).Publish {
		t.Fatal("the licensing client module was refused; that closes the entire delivery lane")
	}
}

// The file must NOT carry a blanket wildcard: a module published tomorrow has
// to be entitled or declared, not silently adopted into the "for now".
func TestFleetPolicyDoesNotBlanketWildcardNewModules(t *testing.T) {
	cfg := loadFleetPolicy(t)

	decision := EvaluatePublication(&PluginAsset{ID: "com.orbpro.some-future-paid-module", Version: "1.0.0"}, cfg)

	if decision.Publish {
		t.Fatal("a module that did not exist at audit time was admitted; the policy file has a blanket wildcard " +
			"and new modules inherit an openness nobody declared for them")
	}
}

// The owner's future flip, exercised against the real file.
func TestFleetPolicyLockdownSwitchClosesEverything(t *testing.T) {
	cfg := loadFleetPolicy(t)
	cfg.EnforceAllowlistOnly = true

	for _, id := range liveCatalogModuleIDs {
		if EvaluatePublication(&PluginAsset{ID: id}, cfg).Publish {
			t.Fatalf("%s survived enforce_allowlist_only", id)
		}
	}
}
