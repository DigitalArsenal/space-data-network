package license

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The defect this file guards (graph/tasks/sdn-allowed-xpubs-not-enforced.md):
// a throwaway anonymous identity was granted com.orbpro.hpop because the only
// entitlement control on the wire — ALLOWED_XPUBS — reads EMPTY as
// "unrestricted". None of these cases had a test before; that is exactly why
// the hole stayed invisible until someone tried it.

func TestEvaluatePublicationRefusesAllowlistPolicyWithEmptyAllowlist(t *testing.T) {
	asset := &PluginAsset{ID: "com.orbpro.hpop", Version: "1.0.0"}

	decision := EvaluatePublication(asset, &GrantPolicyConfig{})

	if decision.Publish {
		t.Fatal("a module with the default allowlist policy and NO allowed xpubs was published; " +
			"its content key would reach the key server, which grants freely on an empty allowlist")
	}
	if decision.Policy != GrantPolicyAllowlist {
		t.Fatalf("Policy = %q, want %q (allowlist is the fail-closed default)", decision.Policy, GrantPolicyAllowlist)
	}
	if decision.Source != GrantPolicySourceBuiltIn {
		t.Fatalf("Source = %q, want %q", decision.Source, GrantPolicySourceBuiltIn)
	}
	if !strings.Contains(decision.Reason, "REFUSING to provision the content key") {
		t.Fatalf("refusal reason does not say what was refused: %q", decision.Reason)
	}
}

func TestEvaluatePublicationPublishesAllowlistPolicyWithEntries(t *testing.T) {
	asset := &PluginAsset{
		ID:           "com.orbpro.hpop",
		Version:      "1.0.0",
		AllowedXpubs: []string{"xpubENTITLED"},
	}

	decision := EvaluatePublication(asset, &GrantPolicyConfig{})

	if !decision.Publish {
		t.Fatalf("an entitled module was refused: %s", decision.Reason)
	}
	if decision.AllowedXpubCount != 1 {
		t.Fatalf("AllowedXpubCount = %d, want 1", decision.AllowedXpubCount)
	}
}

// The owner directive of 2026-08-07: the private sandcastle gallery link
// derives a fresh identity from the URL UUID, so the RF modules must grant to
// an xpub nobody has ever seen. That is a DECLARED policy, not a default.
func TestEvaluatePublicationKeepsTheLinkKeyLaneOpen(t *testing.T) {
	cfg := &GrantPolicyConfig{
		Modules: []GrantPolicyRule{
			{Match: "com.orbpro.rf-*", Policy: GrantPolicyLinkKey, Note: "gallery link key, for now"},
		},
	}
	asset := &PluginAsset{ID: "com.orbpro.rf-fspl", Version: "0.1.0"}

	decision := EvaluatePublication(asset, cfg)

	if !decision.Publish {
		t.Fatalf("the link-key RF lane was refused, which breaks the owner's gallery link: %s", decision.Reason)
	}
	if decision.Policy != GrantPolicyLinkKey {
		t.Fatalf("Policy = %q, want %q", decision.Policy, GrantPolicyLinkKey)
	}
	if decision.Note != "gallery link key, for now" {
		t.Fatalf("Note = %q, want the rule's note carried into the audit", decision.Note)
	}
}

func TestEvaluatePublicationNarrowsAnOpenModuleThatDeclaresAnAllowlist(t *testing.T) {
	cfg := &GrantPolicyConfig{DefaultPolicy: GrantPolicyOpen}
	asset := &PluginAsset{ID: "com.orbpro.hpop", AllowedXpubs: []string{"xpubENTITLED"}}

	decision := EvaluatePublication(asset, cfg)

	if decision.Policy != GrantPolicyAllowlist {
		t.Fatalf("Policy = %q, want %q — a declared allowlist is a restriction and must win", decision.Policy, GrantPolicyAllowlist)
	}
	if !decision.Publish {
		t.Fatalf("narrowing must still publish: %s", decision.Reason)
	}
}

func TestResolvePrecedence(t *testing.T) {
	cfg := &GrantPolicyConfig{
		DefaultPolicy: GrantPolicyOpen,
		Modules: []GrantPolicyRule{
			{Match: "com.orbpro.rf-*", Policy: GrantPolicyLinkKey},
		},
	}

	cases := []struct {
		name          string
		moduleID      string
		catalogPolicy string
		wantPolicy    string
		wantSource    string
	}{
		{"catalog outranks the rule list", "com.orbpro.rf-fspl", GrantPolicyAllowlist, GrantPolicyAllowlist, GrantPolicySourceCatalog},
		{"rule outranks the config default", "com.orbpro.rf-rain", "", GrantPolicyLinkKey, grantPolicySourceRulePrefix + "com.orbpro.rf-*]"},
		{"config default catches the rest", "com.orbpro.hpop", "", GrantPolicyOpen, GrantPolicySourceConfigDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.Resolve(tc.moduleID, tc.catalogPolicy)
			if got.Policy != tc.wantPolicy {
				t.Fatalf("Policy = %q, want %q", got.Policy, tc.wantPolicy)
			}
			if got.Source != tc.wantSource {
				t.Fatalf("Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

// The owner's one-line way to end an "open for now" era.
func TestEnforceAllowlistOnlyOverridesEverything(t *testing.T) {
	cfg := &GrantPolicyConfig{
		EnforceAllowlistOnly: true,
		DefaultPolicy:        GrantPolicyOpen,
		Modules:              []GrantPolicyRule{{Match: "*", Policy: GrantPolicyOpen}},
	}

	got := cfg.Resolve("com.orbpro.rf-fspl", GrantPolicyLinkKey)

	if got.Policy != GrantPolicyAllowlist {
		t.Fatalf("Policy = %q, want %q — the lockdown switch must outrank catalog, rules and default", got.Policy, GrantPolicyAllowlist)
	}
	if got.Source != GrantPolicySourceLockdown {
		t.Fatalf("Source = %q, want %q", got.Source, GrantPolicySourceLockdown)
	}
}

func TestLoadGrantPolicyConfigMissingFileIsFailClosed(t *testing.T) {
	cfg, err := LoadGrantPolicyConfig(t.TempDir())
	if err != nil {
		t.Fatalf("a missing policy file must not be an error: %v", err)
	}
	if got := cfg.Resolve("anything", "").Policy; got != GrantPolicyAllowlist {
		t.Fatalf("Policy = %q, want %q with no policy file present", got, GrantPolicyAllowlist)
	}
}

func TestLoadGrantPolicyConfigRejectsAnUnknownPolicyName(t *testing.T) {
	root := t.TempDir()
	body := `{"modules":[{"match":"com.orbpro.rf-*","policy":"opne"}]}`
	if err := os.WriteFile(filepath.Join(root, GrantPolicyFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadGrantPolicyConfig(root); err == nil {
		t.Fatal("a typo'd policy name loaded silently; a policy file the operator meant to be read must fail the boot")
	}
}

func TestLoadGrantPolicyConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	body := `{
      "default_policy": "allowlist",
      "modules": [{"match": "com.orbpro.rf-*", "policy": "link-key", "note": "owner 2026-08-07"}]
    }`
	if err := os.WriteFile(filepath.Join(root, GrantPolicyFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadGrantPolicyConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Resolve("com.orbpro.rf-doppler-fresnel", "").Policy; got != GrantPolicyLinkKey {
		t.Fatalf("Policy = %q, want %q", got, GrantPolicyLinkKey)
	}
	if got := cfg.Resolve("com.orbpro.hpop", "").Policy; got != GrantPolicyAllowlist {
		t.Fatalf("Policy = %q, want %q", got, GrantPolicyAllowlist)
	}
}

func TestNormalizeGrantPolicyRejectsUnknownValues(t *testing.T) {
	if _, err := NormalizeGrantPolicy("unrestricted"); err == nil {
		t.Fatal("an unknown policy name must be an error, never a silent downgrade")
	}
	for _, valid := range []string{"Allowlist", " open ", "LINK-KEY"} {
		if _, err := NormalizeGrantPolicy(valid); err != nil {
			t.Fatalf("NormalizeGrantPolicy(%q) = %v, want it accepted", valid, err)
		}
	}
}

func TestXpubFingerprintNeverLeaksTheXpub(t *testing.T) {
	const xpub = "xpub6CUGRUonZSQ4TWtTMmzXdrXDtypWKiKrhko4egpiMZbpiaQZY2s"

	got := XpubFingerprint(xpub)

	if strings.Contains(got, xpub) || strings.Contains(got, xpub[:12]) {
		t.Fatalf("fingerprint %q carries the xpub; xpubs are wallet-linkable across every module a party requests", got)
	}
	if got == XpubFingerprint(xpub+"x") {
		t.Fatal("two different xpubs produced the same fingerprint")
	}
	if XpubFingerprint("") != "xpub:none" {
		t.Fatalf("empty xpub = %q, want %q", XpubFingerprint(""), "xpub:none")
	}
}

func TestFormatPublicationAuditNamesModulePolicyAndOutcome(t *testing.T) {
	asset := &PluginAsset{ID: "com.orbpro.hpop"}
	line := FormatPublicationAudit(EvaluatePublication(asset, &GrantPolicyConfig{}))

	for _, needle := range []string{"module=com.orbpro.hpop", "policy=allowlist", "outcome=REFUSED", "allowed_xpubs=0"} {
		if !strings.Contains(line, needle) {
			t.Fatalf("audit line %q is missing %q", line, needle)
		}
	}
}

func TestMatchModulePattern(t *testing.T) {
	cases := map[string]bool{
		"com.orbpro.rf-*|com.orbpro.rf-fspl":  true,
		"com.orbpro.rf-*|com.orbpro.hpop":     false,
		"com.orbpro.hpop|com.orbpro.hpop":     true,
		"com.orbpro.hpop|com.orbpro.hpop.pro": false,
		"*|anything":                          true,
		"|com.orbpro.hpop":                    false,
	}
	for input, want := range cases {
		parts := strings.SplitN(input, "|", 2)
		if got := matchModulePattern(parts[0], parts[1]); got != want {
			t.Fatalf("matchModulePattern(%q, %q) = %v, want %v", parts[0], parts[1], got, want)
		}
	}
}
