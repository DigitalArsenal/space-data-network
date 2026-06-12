// Package abac implements attribute-based access control for the SDN server.
//
// The policy model is:
//   - Subject: the authenticated caller — xpub, trust_level, org, peer_id, and
//     arbitrary extra attributes (e.g. {"cui": "true"}).
//   - Action: one of read | publish | subscribe | admin.
//   - Resource: schema name, topic, classification label, and provider ID.
//
// Policies are an ordered list of Rules; evaluation is first-match-wins with a
// configurable default effect (allow or deny). The engine is pure/deterministic
// with no I/O: callers resolve subjects and resources before calling Evaluate.
package abac

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ----------------------------------------------------------------------------
// Attribute types
// ----------------------------------------------------------------------------

// Subject represents the authenticated caller.
type Subject struct {
	// XPub is the BIP-32 extended public key used for wallet authentication.
	XPub string
	// TrustLevel is the numeric trust ordinal (0=untrusted … 4=admin).
	TrustLevel int
	// Org is the organisation identifier resolved from EPM data (opaque string).
	Org string
	// PeerID is the libp2p peer ID string.
	PeerID string
	// Attrs holds additional attributes, e.g. {"cui": "true"}.
	Attrs map[string]string
}

// Action represents what the subject wants to do.
type Action string

const (
	ActionRead      Action = "read"
	ActionPublish   Action = "publish"
	ActionSubscribe Action = "subscribe"
	ActionAdmin     Action = "admin"
)

// Resource represents what is being accessed.
type Resource struct {
	// Schema is the FlatBuffer schema name, e.g. "OMM.fbs".
	Schema string
	// Topic is the pubsub topic name.
	Topic string
	// Classification is the data sensitivity label, e.g. "U", "CUI", "S".
	Classification string
	// ProviderID identifies the data provider.
	ProviderID string
}

// ----------------------------------------------------------------------------
// Policy document (YAML-serialisable)
// ----------------------------------------------------------------------------

// Effect is the outcome of a matching rule.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

// SubjectFilter matches subjects.  All non-empty fields must match (AND logic).
// A subject matches if it satisfies at least one non-empty list (OR within each
// field) and all field constraints.
type SubjectFilter struct {
	// MinTrust, when > 0, requires subject.TrustLevel >= MinTrust.
	MinTrust int `yaml:"min_trust,omitempty"`
	// XPubs is a list of exact xpub values; the subject matches if its XPub is
	// in the list (empty = no constraint).
	XPubs []string `yaml:"xpubs,omitempty"`
	// Orgs is a list of organisation identifiers; the subject matches if its Org
	// is in the list (empty = no constraint).
	Orgs []string `yaml:"orgs,omitempty"`
}

// ResourceFilter matches resources.  All non-empty fields must match.
type ResourceFilter struct {
	// Schemas is a list of schema glob patterns, e.g. ["OMM.fbs", "TLE.*"].
	// Glob characters: * matches any sequence of non-/ characters, ? matches one.
	Schemas []string `yaml:"schemas,omitempty"`
	// Classifications is a list of exact classification labels ("U", "CUI", "S"…).
	Classifications []string `yaml:"classifications,omitempty"`
	// Providers is a list of provider ID values.
	Providers []string `yaml:"providers,omitempty"`
}

// Rule is a single policy rule.
type Rule struct {
	// Effect is "allow" or "deny".
	Effect Effect `yaml:"effect"`
	// Subjects constrains the subjects this rule applies to.
	Subjects SubjectFilter `yaml:"subjects,omitempty"`
	// Actions is the list of actions covered by this rule (empty = all).
	Actions []Action `yaml:"actions,omitempty"`
	// Resources constrains the resources this rule applies to.
	Resources ResourceFilter `yaml:"resources,omitempty"`
	// Description is a human-readable annotation.
	Description string `yaml:"description,omitempty"`
}

// Policy is the top-level policy document.
type Policy struct {
	// DefaultEffect is "allow" or "deny".  Used when no rule matches.
	// Defaults to "deny" if empty.
	DefaultEffect Effect `yaml:"default_effect"`
	// Rules is the ordered list of rules.  First match wins.
	Rules []Rule `yaml:"rules"`
}

// ----------------------------------------------------------------------------
// Decision
// ----------------------------------------------------------------------------

// Decision is the result of evaluating a policy against a request.
type Decision struct {
	// Allowed is true when the action is permitted.
	Allowed bool
	// RuleIndex is the index of the matching rule (-1 = default effect applied).
	RuleIndex int
	// Reason is a human-readable description of why the decision was made.
	Reason string
}

// ----------------------------------------------------------------------------
// Engine
// ----------------------------------------------------------------------------

// Engine evaluates access control policies.
type Engine struct {
	policy Policy
}

// NewEngine creates an Engine from an already-parsed Policy.
func NewEngine(p Policy) (*Engine, error) {
	if err := validatePolicy(p); err != nil {
		return nil, err
	}
	return &Engine{policy: p}, nil
}

// LoadFile parses a YAML policy file and returns an Engine.
func LoadFile(path string) (*Engine, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("abac: read policy file %q: %w", path, err)
	}
	return ParseYAML(data)
}

// ParseYAML parses a YAML-encoded policy and returns an Engine.
func ParseYAML(data []byte) (*Engine, error) {
	var p Policy
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // reject unknown fields
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("abac: parse policy YAML: %w", err)
	}
	return NewEngine(p)
}

// Evaluate returns the access-control Decision for the given triple.
func (e *Engine) Evaluate(sub Subject, action Action, res Resource) Decision {
	for i, rule := range e.policy.Rules {
		if !matchesSubject(rule.Subjects, sub) {
			continue
		}
		if !matchesAction(rule.Actions, action) {
			continue
		}
		if !matchesResource(rule.Resources, res) {
			continue
		}
		// Rule matched.
		desc := rule.Description
		if desc == "" {
			desc = fmt.Sprintf("rule %d", i)
		}
		return Decision{
			Allowed:   rule.Effect == EffectAllow,
			RuleIndex: i,
			Reason:    fmt.Sprintf("matched rule %d (%s): %s", i, rule.Effect, desc),
		}
	}
	// No rule matched — apply default.
	def := e.policy.DefaultEffect
	if def == "" {
		def = EffectDeny
	}
	return Decision{
		Allowed:   def == EffectAllow,
		RuleIndex: -1,
		Reason:    fmt.Sprintf("no rule matched; default effect: %s", def),
	}
}

// ----------------------------------------------------------------------------
// Matching helpers
// ----------------------------------------------------------------------------

func matchesSubject(f SubjectFilter, sub Subject) bool {
	// MinTrust
	if f.MinTrust > 0 && sub.TrustLevel < f.MinTrust {
		return false
	}
	// XPubs whitelist
	if len(f.XPubs) > 0 {
		found := false
		for _, x := range f.XPubs {
			if x == sub.XPub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Orgs whitelist
	if len(f.Orgs) > 0 {
		found := false
		for _, o := range f.Orgs {
			if o == sub.Org {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchesAction(actions []Action, action Action) bool {
	if len(actions) == 0 {
		return true // empty = wildcard
	}
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}

func matchesResource(f ResourceFilter, res Resource) bool {
	// Schema globs
	if len(f.Schemas) > 0 {
		matched := false
		for _, pattern := range f.Schemas {
			if globMatch(pattern, res.Schema) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	// Classifications
	if len(f.Classifications) > 0 {
		found := false
		for _, c := range f.Classifications {
			if strings.EqualFold(c, res.Classification) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Providers
	if len(f.Providers) > 0 {
		found := false
		for _, p := range f.Providers {
			if p == res.ProviderID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// globMatch implements simple glob matching where * matches any sequence of
// characters (including dots) and ? matches exactly one character.
func globMatch(pattern, s string) bool {
	return globMatchRunes([]rune(pattern), []rune(s))
}

func globMatchRunes(pattern, s []rune) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Consume consecutive stars
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true // trailing star matches everything
			}
			for i := 0; i <= len(s); i++ {
				if globMatchRunes(pattern, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		default:
			if len(s) == 0 || pattern[0] != s[0] {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		}
	}
	return len(s) == 0
}

// ----------------------------------------------------------------------------
// Validation
// ----------------------------------------------------------------------------

var validEffects = map[Effect]bool{EffectAllow: true, EffectDeny: true}
var validActions = map[Action]bool{
	ActionRead: true, ActionPublish: true, ActionSubscribe: true, ActionAdmin: true,
}

func validatePolicy(p Policy) error {
	if p.DefaultEffect != "" && !validEffects[p.DefaultEffect] {
		return fmt.Errorf("abac: invalid default_effect %q (must be allow|deny)", p.DefaultEffect)
	}
	for i, r := range p.Rules {
		if !validEffects[r.Effect] {
			return fmt.Errorf("abac: rule %d: invalid effect %q (must be allow|deny)", i, r.Effect)
		}
		for _, a := range r.Actions {
			if !validActions[a] {
				return fmt.Errorf("abac: rule %d: unknown action %q", i, a)
			}
		}
	}
	return nil
}
