// Package accessgate decides which HTTP requests a locked-down node serves
// without an admin. Everything is denied unless a rule opens it (owner order
// 2026-09-22: "lock the whole server API", with configurable public paths and
// the ones in use today configured open).
//
// A rule is written the way it is configured:
//
//	"/ipfs/"                         GET/HEAD, prefix (trailing slash or *)
//	"/api/node/info"                 GET/HEAD, exact path
//	"POST /api/auth/challenge"       one method, exact path
//	"GET,POST /api/v1/cellular/"     several methods, prefix
//
// A rule with no method admits reads only (GET, HEAD). OPTIONS is admitted
// wherever any method is, so browsers can preflight a public route.
package accessgate

import (
	"fmt"
	"net/http"
	"strings"
)

// DefaultPublic is what a node opens by default: what external consumers use
// today, and what the dashboard needs to show its sign-in screen. Found by
// survey 2026-09-22; each entry names who depends on it.
var DefaultPublic = []string{
	// SpaceAware console, OrbPro sandcastle: modules, content, terrain, data.
	"/.well-known/sdn/modules.pmm",
	"/modules/",
	"/ipfs/",
	"/api/module-delivery/provider",
	"/api/v1/terrain/",
	"/api/v1/cellular/",
	"POST /api/v1/cellular/aggregate",
	"/api/v1/data/index",
	"/api/v1/channels/",
	// spaceaware.io marketplace, sdn-js storefront client, payment webhook.
	"/api/storefront/listings",
	"/api/storefront/listings/",
	"POST /api/storefront/listings/search",
	"/api/storefront/trust/",
	"POST /api/storefront/payments/stripe/webhook",
	// Fleet self-update feed, discovery, health and identity probes.
	"/updates/",
	"/api/relay/status",
	"/api/node/info",
	"/api/v1/id",
	"/api/v1/data/health",
	"/health",
	"/ready",
	"/api/v1/health",
	"/api/v1/ready",
	"/ws/status",
	// The dashboard shell and its sign-in.
	"/",
	"/index.html",
	"/favicon.ico",
	"/fonts/",
	"/sdn-js/",
	"/wallet-wasm/",
	"/wallet-ui/",
	"/wallet/callback",
	"/wallet/callback/",
	"/wallet-callback.html",
	"/bootstrap.crt",
	"POST /api/auth/challenge",
	"POST /api/auth/verify",
	"POST /api/auth/delegate",
	"/api/auth/me",
	"POST /api/auth/logout",
}

// Rule is one parsed public-path entry.
type Rule struct {
	Methods map[string]bool
	Path    string
	Prefix  bool
}

// ParseRule parses one entry in the form documented on the package.
func ParseRule(entry string) (Rule, error) {
	entry = strings.TrimSpace(entry)
	methods, path := "", entry
	if i := strings.IndexByte(entry, ' '); i >= 0 {
		methods, path = entry[:i], strings.TrimSpace(entry[i+1:])
	}
	if !strings.HasPrefix(path, "/") {
		return Rule{}, fmt.Errorf("public path %q must start with /", entry)
	}
	rule := Rule{Methods: map[string]bool{}, Path: path}
	if strings.HasSuffix(path, "*") {
		rule.Path, rule.Prefix = strings.TrimSuffix(path, "*"), true
	} else if strings.HasSuffix(path, "/") && path != "/" {
		rule.Prefix = true
	}
	if methods == "" {
		rule.Methods[http.MethodGet] = true
		rule.Methods[http.MethodHead] = true
	} else {
		for _, m := range strings.Split(methods, ",") {
			m = strings.ToUpper(strings.TrimSpace(m))
			if m == "" {
				continue
			}
			rule.Methods[m] = true
			if m == http.MethodGet {
				rule.Methods[http.MethodHead] = true
			}
		}
	}
	return rule, nil
}

func (r Rule) matches(method, path string) bool {
	if !r.Methods[method] && !(method == http.MethodOptions && len(r.Methods) > 0) {
		return false
	}
	if r.Prefix {
		return strings.HasPrefix(path, r.Path)
	}
	return path == r.Path
}

// Policy is the effective public set.
type Policy struct {
	rules []Rule
	deny  []Rule
	// Extra admits requests a static rule cannot express (per-schema reads,
	// flow-declared anonymous routes). May be nil.
	Extra func(method, path string) bool
}

// New builds a policy from the defaults plus configured additions, minus
// configured removals. A removal wins over every other source.
func New(defaults, add, deny []string) (*Policy, error) {
	p := &Policy{}
	for _, list := range [][]string{defaults, add} {
		for _, entry := range list {
			rule, err := ParseRule(entry)
			if err != nil {
				return nil, err
			}
			p.rules = append(p.rules, rule)
		}
	}
	for _, entry := range deny {
		rule, err := ParseRule(entry)
		if err != nil {
			return nil, err
		}
		// A removal applies to every method on its path.
		rule.Methods = map[string]bool{}
		p.deny = append(p.deny, rule)
	}
	return p, nil
}

// Public reports whether method+path is served without an admin.
func (p *Policy) Public(method, path string) bool {
	if p == nil {
		return false
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	for _, rule := range p.deny {
		if (rule.Prefix && strings.HasPrefix(path, rule.Path)) || (!rule.Prefix && path == rule.Path) {
			return false
		}
	}
	for _, rule := range p.rules {
		if rule.matches(method, path) {
			return true
		}
	}
	return p.Extra != nil && p.Extra(method, path)
}
