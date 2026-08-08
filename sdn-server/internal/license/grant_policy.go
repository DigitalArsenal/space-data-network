package license

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Grant policy — the per-module entitlement rule the provider applies before it
// hands out a module-delivery grant.
//
// WHY THIS EXISTS (graph/tasks/sdn-allowed-xpubs-not-enforced.md, P1): the only
// entitlement control on the wire was PLG.ALLOWED_XPUBS, and the licensing key
// server reads an EMPTY allowlist as "unrestricted". Every module in the live
// catalog was published with an empty allowlist, so a throwaway anonymous
// identity was granted com.orbpro.hpop. The allowlist alone cannot express the
// difference between "nobody bothered to set one" and "this module really is
// open", so it fails open on the exact case that matters.
//
// The policy names that difference. It is declared per module and resolved on
// the HOST, at the admit point where the host provisions publication key
// material into the licensing runtime. The host makes no grant decision — it
// decides only whether a module's content key is provisioned at all, which is
// capability provision, fail-closed, and squarely a connector concern.
const (
	// GrantPolicyAllowlist restricts grants to the module's ALLOWED_XPUBS.
	// It is the DEFAULT and it is fail-closed: an allowlist policy with an
	// EMPTY allowlist publishes nothing, because "restricted to nobody" is
	// the only honest reading of "restricted, list unset".
	GrantPolicyAllowlist = "allowlist"

	// GrantPolicyOpen grants to any identity that passes the licensing
	// challenge (proven ed25519 signing key, live nonce, domain and timeout
	// within policy). It is an explicit, auditable declaration — never a
	// default, never inferred from an empty allowlist.
	GrantPolicyOpen = "open"

	// GrantPolicyLinkKey is GrantPolicyOpen with its reason recorded: the
	// credential is a capability URL whose UUID derives the requester's key
	// (owner directive 2026-08-07, the private sandcastle gallery link). It
	// behaves exactly like "open" on the wire and exists so the "for now" is
	// greppable, is listed on every boot, and can be flipped to "allowlist"
	// by editing one file.
	GrantPolicyLinkKey = "link-key"
)

// GrantPolicyFileName is the operator-owned policy file, read from the plugin
// root beside catalog.json. It is deliberately NOT a compiled-in table and NOT
// part of the node YAML: flipping a module between open and allowlist must be
// an edit-and-restart, never a code change or a rebuild.
const GrantPolicyFileName = "grant-policy.json"

// GrantPolicyRule maps a module-ID pattern to a policy. Match supports a single
// trailing "*" wildcard ("com.orbpro.rf-*"), which is what a module FAMILY
// needs; anything richer invites the ambiguity this file exists to remove.
type GrantPolicyRule struct {
	Match  string `json:"match"`
	Policy string `json:"policy"`
	Note   string `json:"note,omitempty"`
}

// GrantPolicyConfig is the on-disk grant-policy.json format.
type GrantPolicyConfig struct {
	// DefaultPolicy applies to modules that match no rule and declare no
	// policy of their own. Unset means GrantPolicyAllowlist.
	DefaultPolicy string `json:"default_policy,omitempty"`

	// EnforceAllowlistOnly is the operator lockdown switch: when true EVERY
	// module resolves to allowlist regardless of catalog entry or rule. It
	// is the one-line way to end an "open for now" era.
	EnforceAllowlistOnly bool `json:"enforce_allowlist_only,omitempty"`

	// Modules are evaluated in order; the FIRST match wins, so specific
	// module IDs belong above family wildcards.
	Modules []GrantPolicyRule `json:"modules,omitempty"`
}

// GrantPolicySourceBuiltIn and friends name where an effective policy came
// from. They are audit strings, printed verbatim in the boot ledger.
const (
	GrantPolicySourceBuiltIn       = "built-in-default"
	GrantPolicySourceConfigDefault = "grant-policy.json:default_policy"
	GrantPolicySourceCatalog       = "catalog.json:grant_policy"
	GrantPolicySourceLockdown      = "grant-policy.json:enforce_allowlist_only"
	grantPolicySourceRulePrefix    = "grant-policy.json:modules[" //  + match + "]"
)

// NormalizeGrantPolicy canonicalizes a declared policy. An empty value means
// "not declared" and is returned as "" so callers can fall through to the next
// source; an unknown value is an ERROR, never a silent downgrade.
func NormalizeGrantPolicy(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return "", nil
	case GrantPolicyAllowlist, GrantPolicyOpen, GrantPolicyLinkKey:
		return normalized, nil
	default:
		return "", fmt.Errorf(
			"unknown grant policy %q (want %q, %q or %q)",
			value, GrantPolicyAllowlist, GrantPolicyOpen, GrantPolicyLinkKey,
		)
	}
}

// IsOpenGrantPolicy reports whether a policy grants to any identity that passes
// the licensing challenge.
func IsOpenGrantPolicy(policy string) bool {
	return policy == GrantPolicyOpen || policy == GrantPolicyLinkKey
}

// LoadGrantPolicyConfig reads grant-policy.json from a plugin root. A MISSING
// file is not an error and is not permissive: it yields the fail-closed
// built-in default. A PRESENT but malformed file IS an error — a policy file
// the operator meant to be read must never be silently ignored.
func LoadGrantPolicyConfig(rootPath string) (*GrantPolicyConfig, error) {
	root := strings.TrimSpace(rootPath)
	if root == "" {
		return &GrantPolicyConfig{}, nil
	}
	data, err := os.ReadFile(filepath.Join(root, GrantPolicyFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &GrantPolicyConfig{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", GrantPolicyFileName, err)
	}
	cfg := &GrantPolicyConfig{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", GrantPolicyFileName, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", GrantPolicyFileName, err)
	}
	return cfg, nil
}

// Validate rejects unknown policy names and empty match patterns up front, so a
// typo in the policy file fails the boot loudly instead of resolving to
// something the operator did not write.
func (c *GrantPolicyConfig) Validate() error {
	if c == nil {
		return nil
	}
	if _, err := NormalizeGrantPolicy(c.DefaultPolicy); err != nil {
		return fmt.Errorf("default_policy: %w", err)
	}
	for i, rule := range c.Modules {
		if strings.TrimSpace(rule.Match) == "" {
			return fmt.Errorf("modules[%d]: match is required", i)
		}
		policy, err := NormalizeGrantPolicy(rule.Policy)
		if err != nil {
			return fmt.Errorf("modules[%d] (%s): %w", i, rule.Match, err)
		}
		if policy == "" {
			return fmt.Errorf("modules[%d] (%s): policy is required", i, rule.Match)
		}
	}
	return nil
}

// ResolvedGrantPolicy is the effective policy for one module plus the audit
// trail that produced it.
type ResolvedGrantPolicy struct {
	ModuleID string
	Policy   string
	Source   string
	Note     string
}

// Resolve computes the effective policy for a module. Precedence, highest
// first:
//
//  1. enforce_allowlist_only — the operator lockdown switch.
//  2. the module's own catalog entry (the publisher's declaration).
//  3. the first matching rule in grant-policy.json.
//  4. default_policy in grant-policy.json.
//  5. the built-in default, allowlist.
//
// The publisher outranks the rule list on purpose: a publisher that declared a
// restriction must not be loosened by a wildcard someone else wrote. The
// operator can still override everything at (1).
func (c *GrantPolicyConfig) Resolve(moduleID, catalogPolicy string) ResolvedGrantPolicy {
	resolved := ResolvedGrantPolicy{
		ModuleID: strings.TrimSpace(moduleID),
		Policy:   GrantPolicyAllowlist,
		Source:   GrantPolicySourceBuiltIn,
	}
	if c != nil && c.EnforceAllowlistOnly {
		resolved.Source = GrantPolicySourceLockdown
		return resolved
	}
	if declared, err := NormalizeGrantPolicy(catalogPolicy); err == nil && declared != "" {
		resolved.Policy = declared
		resolved.Source = GrantPolicySourceCatalog
		return resolved
	}
	if c != nil {
		for _, rule := range c.Modules {
			if !matchModulePattern(rule.Match, resolved.ModuleID) {
				continue
			}
			policy, err := NormalizeGrantPolicy(rule.Policy)
			if err != nil || policy == "" {
				continue
			}
			resolved.Policy = policy
			resolved.Source = grantPolicySourceRulePrefix + strings.TrimSpace(rule.Match) + "]"
			resolved.Note = strings.TrimSpace(rule.Note)
			return resolved
		}
		if fallback, err := NormalizeGrantPolicy(c.DefaultPolicy); err == nil && fallback != "" {
			resolved.Policy = fallback
			resolved.Source = GrantPolicySourceConfigDefault
			return resolved
		}
	}
	return resolved
}

// matchModulePattern implements exact match plus a single trailing "*".
func matchModulePattern(pattern, moduleID string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" || moduleID == "" {
		return false
	}
	if p == "*" {
		return true
	}
	if strings.HasSuffix(p, "*") {
		return strings.HasPrefix(moduleID, strings.TrimSuffix(p, "*"))
	}
	return p == moduleID
}

// PublicationDecision is the host's admit-point ruling for one module: whether
// its content key is provisioned into the licensing runtime at all, and under
// which policy the runtime will then decide individual grants.
type PublicationDecision struct {
	ResolvedGrantPolicy

	// Publish is false when the host refuses to provision the module. A
	// refused module's content key never enters guest memory, so the key
	// server cannot issue a grant for it under any circumstances — it
	// answers module_not_found, which is a refusal distinguishable from a
	// transport failure.
	Publish bool

	// AllowedXpubCount is the size of the allowlist that will be published.
	AllowedXpubCount int

	// Reason explains a refusal, or records a narrowing, in operator terms.
	Reason string
}

// EvaluatePublication rules on one catalog asset.
//
//	allowlist + non-empty list -> publish; the key server enforces membership
//	                              and the cross-curve EPM binding at proof time
//	allowlist + EMPTY list     -> REFUSE; nothing to enforce against, so the
//	                              only fail-closed answer is to publish nothing
//	open / link-key            -> publish with an empty allowlist; any
//	                              challenge-passing identity may be granted
//	open / link-key + a list   -> NARROWED to allowlist; a declared list is a
//	                              restriction and the restriction wins
func EvaluatePublication(asset *PluginAsset, cfg *GrantPolicyConfig) PublicationDecision {
	decision := PublicationDecision{}
	if asset == nil {
		decision.Policy = GrantPolicyAllowlist
		decision.Source = GrantPolicySourceBuiltIn
		decision.Reason = "no asset"
		return decision
	}
	decision.ResolvedGrantPolicy = cfg.Resolve(asset.ID, asset.GrantPolicy)
	decision.AllowedXpubCount = len(asset.AllowedXpubs)

	if IsOpenGrantPolicy(decision.Policy) && decision.AllowedXpubCount > 0 {
		decision.Reason = fmt.Sprintf(
			"policy %q was NARROWED to %q: the module declares %d allowed xpub(s), and a declared allowlist is a restriction",
			decision.Policy, GrantPolicyAllowlist, decision.AllowedXpubCount,
		)
		decision.Policy = GrantPolicyAllowlist
	}

	if IsOpenGrantPolicy(decision.Policy) {
		decision.Publish = true
		return decision
	}
	if decision.AllowedXpubCount == 0 {
		decision.Publish = false
		decision.Reason = fmt.Sprintf(
			"grant policy %q (from %s) with an EMPTY allowed_xpubs list: REFUSING to provision the content key, so no grant can be issued for this module. "+
				"Fix by entitling identities (catalog.json allowed_xpubs, or `spacedatanetwork module publish --allow-xpub <xpub>`) "+
				"or, if this module really is unrestricted, declare it explicitly (%s \"modules\": [{\"match\": %q, \"policy\": %q}])",
			decision.Policy, decision.Source, GrantPolicyFileName, asset.ID, GrantPolicyOpen,
		)
		return decision
	}
	decision.Publish = true
	return decision
}

// XpubFingerprint is the ONLY form an xpub takes in a log line: the first 8
// bytes of its SHA-256, hex, prefixed. Raw xpubs are wallet-linkable across
// every module a party ever requests, so they never reach the audit log.
func XpubFingerprint(xpub string) string {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return "xpub:none"
	}
	sum := sha256.Sum256([]byte(trimmed))
	return "xpub:" + hex.EncodeToString(sum[:8])
}
