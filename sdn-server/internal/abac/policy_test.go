package abac_test

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/abac"
)

// helper: build an engine from inline YAML or fail the test.
func mustParseYAML(t *testing.T, src string) *abac.Engine {
	t.Helper()
	e, err := abac.ParseYAML([]byte(src))
	if err != nil {
		t.Fatalf("ParseYAML failed: %v", err)
	}
	return e
}

// standardSubject returns a typical authenticated peer.
func standardSubject() abac.Subject {
	return abac.Subject{
		XPub:       "xpubABCD",
		TrustLevel: 2, // standard
		Org:        "DIU",
		PeerID:     "QmPeer1",
		Attrs:      map[string]string{},
	}
}

// ----------------------------------------------------------------------------
// Table-driven core tests
// ----------------------------------------------------------------------------

func TestEvaluate(t *testing.T) {
	const basicPolicy = `
default_effect: deny
rules:
  - effect: allow
    description: "admins can do anything"
    subjects:
      min_trust: 4
    actions: [read, publish, subscribe, admin]
    resources: {}
  - effect: allow
    description: "standard peers may publish OMM"
    subjects:
      min_trust: 2
    actions: [publish]
    resources:
      schemas: ["OMM.fbs"]
  - effect: deny
    description: "CUI is blocked for subjects without cui attr (handled by explicit deny)"
    subjects: {}
    actions: [read, publish, subscribe]
    resources:
      classifications: ["CUI"]
  - effect: allow
    description: "org DIU may read all unclassified"
    subjects:
      orgs: ["DIU"]
    actions: [read]
    resources:
      classifications: ["U"]
`

	engine := mustParseYAML(t, basicPolicy)

	tests := []struct {
		name    string
		sub     abac.Subject
		action  abac.Action
		res     abac.Resource
		allowed bool
	}{
		{
			name:    "admin can publish anything",
			sub:     abac.Subject{TrustLevel: 4},
			action:  abac.ActionPublish,
			res:     abac.Resource{Schema: "OMM.fbs"},
			allowed: true,
		},
		{
			name:    "standard peer publishes OMM",
			sub:     abac.Subject{TrustLevel: 2},
			action:  abac.ActionPublish,
			res:     abac.Resource{Schema: "OMM.fbs"},
			allowed: true,
		},
		{
			name:    "standard peer publish non-OMM schema denied by default",
			sub:     abac.Subject{TrustLevel: 2},
			action:  abac.ActionPublish,
			res:     abac.Resource{Schema: "TLE.fbs"},
			allowed: false,
		},
		{
			name:    "limited peer cannot publish OMM (trust too low)",
			sub:     abac.Subject{TrustLevel: 1},
			action:  abac.ActionPublish,
			res:     abac.Resource{Schema: "OMM.fbs"},
			allowed: false,
		},
		{
			name:    "CUI resource denied regardless of trust",
			sub:     abac.Subject{TrustLevel: 3},
			action:  abac.ActionRead,
			res:     abac.Resource{Classification: "CUI"},
			allowed: false,
		},
		{
			name:    "admin can bypass CUI rule (earlier rule matches first)",
			sub:     abac.Subject{TrustLevel: 4},
			action:  abac.ActionRead,
			res:     abac.Resource{Classification: "CUI"},
			allowed: true, // admin rule is first
		},
		{
			name:    "DIU org reads unclassified",
			sub:     abac.Subject{TrustLevel: 2, Org: "DIU"},
			action:  abac.ActionRead,
			res:     abac.Resource{Classification: "U"},
			allowed: true,
		},
		{
			name:    "non-DIU org read unclassified — default deny",
			sub:     abac.Subject{TrustLevel: 2, Org: "other"},
			action:  abac.ActionRead,
			res:     abac.Resource{Classification: "U"},
			allowed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := engine.Evaluate(tc.sub, tc.action, tc.res)
			if d.Allowed != tc.allowed {
				t.Errorf("Evaluate() allowed=%v, want %v — reason: %s",
					d.Allowed, tc.allowed, d.Reason)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// Default effect
// ----------------------------------------------------------------------------

func TestDefaultEffectAllow(t *testing.T) {
	const src = `
default_effect: allow
rules: []
`
	engine := mustParseYAML(t, src)
	d := engine.Evaluate(standardSubject(), abac.ActionPublish, abac.Resource{Schema: "FOO.fbs"})
	if !d.Allowed {
		t.Fatalf("expected allow from default, got deny: %s", d.Reason)
	}
	if d.RuleIndex != -1 {
		t.Errorf("expected RuleIndex=-1 (default), got %d", d.RuleIndex)
	}
}

func TestDefaultEffectDeny(t *testing.T) {
	const src = `
default_effect: deny
rules: []
`
	engine := mustParseYAML(t, src)
	d := engine.Evaluate(standardSubject(), abac.ActionPublish, abac.Resource{})
	if d.Allowed {
		t.Fatalf("expected deny from default, got allow")
	}
}

func TestDefaultEffectEmptyIsDeny(t *testing.T) {
	const src = `
rules: []
`
	engine := mustParseYAML(t, src)
	d := engine.Evaluate(standardSubject(), abac.ActionRead, abac.Resource{})
	if d.Allowed {
		t.Fatalf("empty default_effect should default to deny")
	}
}

// ----------------------------------------------------------------------------
// Allow/deny precedence (first-match-wins)
// ----------------------------------------------------------------------------

func TestAllowDenyPrecedence(t *testing.T) {
	// Allow comes before deny — should allow.
	const allowFirst = `
default_effect: deny
rules:
  - effect: allow
    actions: [publish]
    resources:
      schemas: ["OMM.fbs"]
  - effect: deny
    actions: [publish]
    resources:
      schemas: ["OMM.fbs"]
`
	e := mustParseYAML(t, allowFirst)
	d := e.Evaluate(standardSubject(), abac.ActionPublish, abac.Resource{Schema: "OMM.fbs"})
	if !d.Allowed {
		t.Errorf("first-match-wins: first allow should win, got deny")
	}
	if d.RuleIndex != 0 {
		t.Errorf("expected RuleIndex=0, got %d", d.RuleIndex)
	}

	// Deny comes before allow — should deny.
	const denyFirst = `
default_effect: allow
rules:
  - effect: deny
    actions: [publish]
    resources:
      schemas: ["OMM.fbs"]
  - effect: allow
    actions: [publish]
    resources:
      schemas: ["OMM.fbs"]
`
	e2 := mustParseYAML(t, denyFirst)
	d2 := e2.Evaluate(standardSubject(), abac.ActionPublish, abac.Resource{Schema: "OMM.fbs"})
	if d2.Allowed {
		t.Errorf("first-match-wins: first deny should win, got allow")
	}
	if d2.RuleIndex != 0 {
		t.Errorf("expected RuleIndex=0, got %d", d2.RuleIndex)
	}
}

// ----------------------------------------------------------------------------
// Trust level threshold
// ----------------------------------------------------------------------------

func TestTrustLevelThreshold(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    subjects:
      min_trust: 3
    actions: [publish]
`
	e := mustParseYAML(t, src)

	for _, tc := range []struct {
		trust   int
		allowed bool
	}{
		{0, false},
		{1, false},
		{2, false},
		{3, true},
		{4, true},
	} {
		sub := abac.Subject{TrustLevel: tc.trust}
		d := e.Evaluate(sub, abac.ActionPublish, abac.Resource{})
		if d.Allowed != tc.allowed {
			t.Errorf("trust=%d: got allowed=%v, want %v", tc.trust, d.Allowed, tc.allowed)
		}
	}
}

// ----------------------------------------------------------------------------
// Schema glob matching
// ----------------------------------------------------------------------------

func TestSchemaGlobs(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    actions: [publish]
    resources:
      schemas: ["OMM.*", "TLE.fbs"]
`
	e := mustParseYAML(t, src)

	cases := []struct {
		schema  string
		allowed bool
	}{
		{"OMM.fbs", true},
		{"OMM.fbs2", true},
		{"OMM.", true},
		{"TLE.fbs", true},
		{"TLE.other", false},
		{"CAT.fbs", false},
		{"", false},
	}

	for _, tc := range cases {
		d := e.Evaluate(standardSubject(), abac.ActionPublish, abac.Resource{Schema: tc.schema})
		if d.Allowed != tc.allowed {
			t.Errorf("schema=%q: got allowed=%v, want %v — reason: %s",
				tc.schema, d.Allowed, tc.allowed, d.Reason)
		}
	}
}

func TestSchemaGlobStar(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    actions: [publish]
    resources:
      schemas: ["*"]
`
	e := mustParseYAML(t, src)
	d := e.Evaluate(standardSubject(), abac.ActionPublish, abac.Resource{Schema: "anything.fbs"})
	if !d.Allowed {
		t.Errorf("wildcard * should match any schema")
	}
}

func TestSchemaGlobQuestion(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    actions: [read]
    resources:
      schemas: ["?.fbs"]
`
	e := mustParseYAML(t, src)

	if !e.Evaluate(standardSubject(), abac.ActionRead, abac.Resource{Schema: "A.fbs"}).Allowed {
		t.Error("? should match single char 'A'")
	}
	if e.Evaluate(standardSubject(), abac.ActionRead, abac.Resource{Schema: "AB.fbs"}).Allowed {
		t.Error("? should not match two chars 'AB'")
	}
}

// ----------------------------------------------------------------------------
// Classification gating
// ----------------------------------------------------------------------------

func TestClassificationGating(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: deny
    description: "block CUI without cui attr (all subjects, since attr check is caller-side)"
    actions: [read, publish, subscribe]
    resources:
      classifications: ["CUI"]
  - effect: allow
    description: "allow unclassified reads for trusted+"
    subjects:
      min_trust: 3
    actions: [read]
    resources:
      classifications: ["U"]
`
	e := mustParseYAML(t, src)

	// CUI denied for any subject.
	d := e.Evaluate(abac.Subject{TrustLevel: 4}, abac.ActionRead, abac.Resource{Classification: "CUI"})
	if d.Allowed {
		t.Error("CUI should be denied")
	}

	// Unclassified allowed for trusted.
	d2 := e.Evaluate(abac.Subject{TrustLevel: 3}, abac.ActionRead, abac.Resource{Classification: "U"})
	if !d2.Allowed {
		t.Errorf("trusted should be allowed on U: %s", d2.Reason)
	}

	// Unclassified denied for standard (trust < 3).
	d3 := e.Evaluate(abac.Subject{TrustLevel: 2}, abac.ActionRead, abac.Resource{Classification: "U"})
	if d3.Allowed {
		t.Error("standard trust should be denied for U resource in this policy")
	}
}

// ----------------------------------------------------------------------------
// Org matching
// ----------------------------------------------------------------------------

func TestOrgMatching(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    subjects:
      orgs: ["DIU", "NRO"]
    actions: [publish]
    resources:
      schemas: ["*"]
`
	e := mustParseYAML(t, src)

	if !e.Evaluate(abac.Subject{Org: "DIU"}, abac.ActionPublish, abac.Resource{Schema: "OMM.fbs"}).Allowed {
		t.Error("DIU should be allowed")
	}
	if !e.Evaluate(abac.Subject{Org: "NRO"}, abac.ActionPublish, abac.Resource{Schema: "OMM.fbs"}).Allowed {
		t.Error("NRO should be allowed")
	}
	if e.Evaluate(abac.Subject{Org: "other"}, abac.ActionPublish, abac.Resource{Schema: "OMM.fbs"}).Allowed {
		t.Error("unknown org should be denied")
	}
}

// ----------------------------------------------------------------------------
// XPub whitelist
// ----------------------------------------------------------------------------

func TestXPubWhitelist(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    subjects:
      xpubs: ["xpubTrusted1", "xpubTrusted2"]
    actions: [admin]
`
	e := mustParseYAML(t, src)

	if !e.Evaluate(abac.Subject{XPub: "xpubTrusted1"}, abac.ActionAdmin, abac.Resource{}).Allowed {
		t.Error("whitelisted xpub should be allowed")
	}
	if e.Evaluate(abac.Subject{XPub: "xpubOther"}, abac.ActionAdmin, abac.Resource{}).Allowed {
		t.Error("non-whitelisted xpub should be denied")
	}
}

// ----------------------------------------------------------------------------
// Malformed policy rejection
// ----------------------------------------------------------------------------

func TestMalformedPolicyRejected(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "invalid effect in rule",
			yaml: `
rules:
  - effect: permit
    actions: [read]
`,
		},
		{
			name: "invalid default_effect",
			yaml: `
default_effect: maybe
rules: []
`,
		},
		{
			name: "unknown action",
			yaml: `
rules:
  - effect: allow
    actions: [delete]
`,
		},
		{
			name: "unknown field in policy",
			yaml: `
default_effect: deny
unknown_field: value
rules: []
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := abac.ParseYAML([]byte(tc.yaml))
			if err == nil {
				t.Errorf("expected error for malformed policy %q, got nil", tc.name)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// Decision fields
// ----------------------------------------------------------------------------

func TestDecisionFields(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    description: "test rule"
    actions: [read]
`
	e := mustParseYAML(t, src)

	d := e.Evaluate(standardSubject(), abac.ActionRead, abac.Resource{})
	if !d.Allowed {
		t.Fatal("should be allowed")
	}
	if d.RuleIndex != 0 {
		t.Errorf("expected RuleIndex=0, got %d", d.RuleIndex)
	}
	if d.Reason == "" {
		t.Error("Reason should not be empty")
	}

	// No match: default.
	d2 := e.Evaluate(standardSubject(), abac.ActionAdmin, abac.Resource{})
	if d2.Allowed {
		t.Fatal("admin should be denied by default")
	}
	if d2.RuleIndex != -1 {
		t.Errorf("expected RuleIndex=-1 for default, got %d", d2.RuleIndex)
	}
}

// ----------------------------------------------------------------------------
// Empty actions list = wildcard
// ----------------------------------------------------------------------------

func TestEmptyActionsMatchesAll(t *testing.T) {
	const src = `
default_effect: deny
rules:
  - effect: allow
    description: "all actions allowed for admin"
    subjects:
      min_trust: 4
`
	e := mustParseYAML(t, src)
	admin := abac.Subject{TrustLevel: 4}

	for _, act := range []abac.Action{abac.ActionRead, abac.ActionPublish, abac.ActionSubscribe, abac.ActionAdmin} {
		d := e.Evaluate(admin, act, abac.Resource{})
		if !d.Allowed {
			t.Errorf("action %q should be allowed for admin with empty actions list", act)
		}
	}
}
