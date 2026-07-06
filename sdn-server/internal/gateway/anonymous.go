// Package gateway implements the network-gateway policy pieces that sit
// between mounted flows and the daemon's HTTP surface.
//
// Anonymous-access policy (docs/gateway-api.md §4.4, gateway loop G.2):
// a flow's manifest REQUESTS anonymous placement per route
// (api.routes[].anonymous); the HOST decides. Since G.2 the decision is
// mechanical: a mounted route is admitted anonymously iff it declares
// anonymous: true AND node config does not veto it (gateway.anonymous.deny);
// config may also extend the allowlist (gateway.anonymous.allow). The same
// predicate feeds both the auth wall and the OpenAPI generator's
// x-sdn-anonymous stamp, so the published spec cannot drift from
// enforcement.
package gateway

import (
	"net/http"
	"strings"
)

// RouteDecl is one mounted-flow route with its joined full path template
// (mount path + route suffix; "{param}" segments) and the flow's REQUESTED
// anonymous placement.
type RouteDecl struct {
	Method    string
	Path      string
	Anonymous bool
}

// JoinMountPath joins a config mount path with a route suffix into the full
// path template — the same rule the OpenAPI generator applies (docs.go
// joinMountPath), duplicated here so enforcement and spec agree by
// construction (asserted by tests).
func JoinMountPath(mountPath, routePath string) string {
	mount := strings.TrimSuffix(mountPath, "/")
	route := strings.TrimPrefix(strings.TrimSpace(routePath), "/")
	if route == "" {
		if mount == "" {
			return "/"
		}
		return mount
	}
	return mount + "/" + route
}

// templateMatch reports whether a concrete request path (or a literal
// template string — the OpenAPI generator stamps template paths) matches a
// path template: segments compare literally except "{param}" which matches
// exactly one non-empty segment. A single trailing slash on the request is
// tolerated (API paths never redirect, so /peers/ must answer like /peers).
func templateMatch(template, path string) bool {
	if template == path {
		return true
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
		if template == path {
			return true
		}
	}
	tSegs := strings.Split(strings.Trim(template, "/"), "/")
	pSegs := strings.Split(strings.Trim(path, "/"), "/")
	if len(tSegs) != len(pSegs) {
		return false
	}
	for i, tSeg := range tSegs {
		if len(tSeg) >= 2 && strings.HasPrefix(tSeg, "{") && strings.HasSuffix(tSeg, "}") {
			if pSegs[i] == "" {
				return false
			}
			continue
		}
		if tSeg != pSegs[i] {
			return false
		}
	}
	return true
}

// configEntryMatch reports whether a gateway.anonymous.allow/deny entry
// matches a request path. Entries ending in "/" are prefixes; other entries
// match exactly or as a "{param}" template.
func configEntryMatch(entry, path string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	if strings.HasSuffix(entry, "/") && len(entry) > 1 {
		return strings.HasPrefix(path, entry) || path == strings.TrimSuffix(entry, "/")
	}
	return templateMatch(entry, path)
}

// AnonymousPolicy decides whether (method, path) is served without a
// session. It combines the static host allowlist, the mounted flows'
// declared anonymity, and the operator's config veto/extension.
type AnonymousPolicy struct {
	static func(method, path string) bool
	routes []RouteDecl
	allow  []string
	deny   []string
}

// NewAnonymousPolicy builds the effective predicate.
//
//	static — the host's built-in allowlist (may be nil).
//	routes — mounted-flow route declarations (method + full path template +
//	         requested anonymity).
//	allow  — gateway.anonymous.allow config entries (host extensions).
//	deny   — gateway.anonymous.deny config entries (operator veto — wins
//	         over everything, including the static list).
func NewAnonymousPolicy(static func(method, path string) bool, routes []RouteDecl, allow, deny []string) *AnonymousPolicy {
	normalized := make([]RouteDecl, 0, len(routes))
	for _, route := range routes {
		method := strings.ToUpper(strings.TrimSpace(route.Method))
		if method == "" {
			method = http.MethodGet
		}
		normalized = append(normalized, RouteDecl{Method: method, Path: route.Path, Anonymous: route.Anonymous})
	}
	return &AnonymousPolicy{static: static, routes: normalized, allow: allow, deny: deny}
}

// Anonymous is the effective decision — the SAME predicate the auth wall
// enforces and the OpenAPI generator stamps as x-sdn-anonymous.
func (p *AnonymousPolicy) Anonymous(method, path string) bool {
	if p == nil {
		return false
	}
	for _, entry := range p.deny {
		if configEntryMatch(entry, path) {
			return false
		}
	}
	if p.static != nil && p.static(method, path) {
		return true
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	requestMethod := method
	if requestMethod == http.MethodHead || requestMethod == http.MethodOptions {
		// HEAD/OPTIONS on an anonymous GET route are anonymous reads.
		requestMethod = http.MethodGet
	}
	for _, route := range p.routes {
		if !route.Anonymous || route.Method != requestMethod {
			continue
		}
		if templateMatch(route.Path, path) {
			return true
		}
	}
	for _, entry := range p.allow {
		if (method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions) &&
			configEntryMatch(entry, path) {
			return true
		}
	}
	return false
}
