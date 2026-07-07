package api

// Gateway API docs (loop G.1, docs/gateway-api.md): OpenAPI generated FROM
// THE MOUNTED FLOWS + a Scalar reference UI, both served by the daemon.
//
// The generator's inputs are the flow bundles ACTUALLY mounted on this node
// (their flow.json "api" extensions, read at mount time by
// internal/flowrt) — the published spec cannot drift from the running
// surface because it is derived from it. Three clearly-separated sources
// feed the spec:
//
//	x-sdn-served-by: "flow"    — real, mounted flow routes (authoritative)
//	x-sdn-served-by: "native"  — a SMALL static set of Go routes (health,
//	                             node info, this docs surface). Marked
//	                             native because they predate the flows-only
//	                             directive and migrate to flows later.
//	x-sdn-status:    "planned" — Phase G routes designed but not yet
//	                             mounted (G.2–G.5). Summaries carry a
//	                             "[PLANNED Gn]" prefix so no reader can
//	                             mistake them for a live surface.
//
// The Scalar UI is fully self-hosted: the standalone bundle is vendored in
// this repo (assets/scalar.standalone.js + SCALAR-LICENSE.md) and the docs
// page carries a strict CSP so it can never fetch anything off-node.
//
// KNOWN TEMPORARY NATIVE ROUTES: /api/v1/docs, /api/v1/docs/scalar.js and
// /api/v1/openapi.json are served natively because the generator needs the
// host-side mount table (which flows cannot see without a dedicated
// capability node). Documented as a bootstrap exception in
// docs/gateway-api.md §6 — they migrate to a docs flow once a mount-table
// capability node exists.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
)

//go:embed assets/scalar.standalone.js
var scalarStandaloneJS []byte

// Media types of the two response encodings every record-stream route
// serves (docs/gateway-api.md §3).
const (
	ContentTypeFlatBufferStream = "application/vnd.sdn.flatbuffers.stream"
	ContentTypeJSON             = "application/json"
)

// FlowDocSource is the slice of a mounted flow the generator reads.
// *flowrt.MountedFlow satisfies it; tests use fakes.
type FlowDocSource interface {
	ProgramID() string
	FlowVersion() string
	MountPath() string
	APIDoc() *flowrt.FlowAPIDoc
}

// DocsHandlerOptions configures the docs surface.
type DocsHandlerOptions struct {
	// Version is the daemon build version stamped into info.version.
	Version string

	// Flows are the flow modules mounted on this node's HTTP listener.
	// Only flows whose bundle declares an "api" extension contribute
	// operations.
	Flows []FlowDocSource

	// EffectiveAnonymous reports whether the node's REAL anonymous-access
	// allowlist admits (method, path) without authentication — the same
	// predicate the HTTP auth wall evaluates, so the spec's
	// x-sdn-anonymous stamps cannot drift from enforcement. Nil omits the
	// stamps.
	EffectiveAnonymous func(method, path string) bool
}

// DocsHandler serves /api/v1/openapi.json, /api/v1/docs and the vendored
// Scalar bundle. The spec is generated once at construction: the mount
// table is fixed for the life of the process.
type DocsHandler struct {
	specJSON  []byte
	specETag  string
	pageHTML  []byte
	scalarTag string
}

// NewDocsHandler generates the OpenAPI document from the mounted flows and
// prepares the docs page.
func NewDocsHandler(opts DocsHandlerOptions) (*DocsHandler, error) {
	spec, err := GenerateOpenAPI(opts)
	if err != nil {
		return nil, err
	}
	h := &DocsHandler{
		specJSON:  spec,
		specETag:  fnv1aETag(spec),
		pageHTML:  []byte(docsPageHTML),
		scalarTag: fnv1aETag(scalarStandaloneJS),
	}
	return h, nil
}

// RegisterRoutes binds the docs surface on the mux.
func (h *DocsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/openapi.json", h.handleSpec)
	mux.HandleFunc("/api/v1/docs", h.handleDocsPage)
	mux.HandleFunc("/api/v1/docs/", h.handleDocsAsset)
}

func (h *DocsHandler) handleSpec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("ETag", h.specETag)
	if match := r.Header.Get("If-None-Match"); match != "" && match == h.specETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(h.specJSON)
}

func (h *DocsHandler) handleDocsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Strict CSP: the page may only use same-origin resources (the
	// vendored Scalar bundle + the spec) — no CDN, no external fonts.
	// 'unsafe-inline' covers the one-line bootstrap script and Scalar's
	// injected styles; worker-src blob: is required by Scalar's syntax
	// highlighter.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; "+
			"frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(h.pageHTML)
}

func (h *DocsHandler) handleDocsAsset(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/api/v1/docs/") {
	case "scalar.js":
		w.Header().Set("ETag", h.scalarTag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == h.scalarTag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(scalarStandaloneJS)
	case "":
		h.handleDocsPage(w, r)
	default:
		http.NotFound(w, r)
	}
}

const docsPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Space Data Network — Gateway API</title>
<style>html,body{margin:0;padding:0}</style>
</head>
<body>
<div id="sdn-api-reference"></div>
<script src="/api/v1/docs/scalar.js"></script>
<script>
  Scalar.createApiReference('#sdn-api-reference', {
    url: '/api/v1/openapi.json',
    withDefaultFonts: false,   // never touch fonts.scalar.com
    agent: { disabled: true }, // never touch api.scalar.com (Ask AI/registry)
    hideClientButton: true,
    hideDarkModeToggle: false,
  });
</script>
</body>
</html>
`

// fnv1aETag mirrors the deterministic FNV-1a 64 strong ETag convention the
// data-retrieval flow uses for record streams (docs/gateway-api.md §3).
func fnv1aETag(b []byte) string {
	h := fnv.New64a()
	h.Write(b)
	return fmt.Sprintf("\"%016x\"", h.Sum64())
}

// ---------------------------------------------------------------------------
// OpenAPI generation
// ---------------------------------------------------------------------------

type openAPIObj = map[string]interface{}

// GenerateOpenAPI emits the gateway OpenAPI 3.1 document. Exported for
// tests and the CLI; the daemon uses NewDocsHandler.
func GenerateOpenAPI(opts DocsHandlerOptions) ([]byte, error) {
	paths := openAPIObj{}
	tagSet := map[string]string{}

	// 1. REAL: operations from the mounted flows' api extensions.
	for _, mf := range opts.Flows {
		doc := mf.APIDoc()
		if doc == nil {
			continue
		}
		tag := strings.TrimSpace(doc.Tag)
		if tag == "" {
			tag = mf.ProgramID()
		}
		if _, ok := tagSet[tag]; !ok {
			tagSet[tag] = doc.TagDescription
		}
		for i := range doc.Routes {
			route := &doc.Routes[i]
			fullPath := joinMountPath(mf.MountPath(), route.Path)
			op := operationFromFlowRoute(mf, route, tag, opts.EffectiveAnonymous, fullPath)
			addOperation(paths, fullPath, route.Method, op)
		}
	}

	// 2. NATIVE: the small static set of Go routes documented in the spec.
	for _, decl := range nativeRouteDecls() {
		if pathHasOperation(paths, decl.path, decl.method) {
			continue // a mounted flow claimed it — the flow is authoritative
		}
		op := decl.operation
		op["x-sdn-served-by"] = "native"
		stampAnonymous(op, opts.EffectiveAnonymous, decl.method, decl.path)
		addOperation(paths, decl.path, decl.method, op)
		if _, ok := tagSet[decl.tag]; !ok {
			tagSet[decl.tag] = decl.tagDescription
		}
	}

	// 3. PLANNED: Phase G routes designed but not yet mounted. Skipped the
	// moment a real mount claims the path, so the spec self-updates as the
	// loop lands G.2–G.5.
	for _, decl := range plannedRouteDecls() {
		if pathHasOperation(paths, decl.path, decl.method) {
			continue
		}
		op := decl.operation
		op["x-sdn-status"] = "planned"
		op["x-sdn-planned-in"] = decl.plannedIn
		op["summary"] = fmt.Sprintf("[PLANNED %s] %s", decl.plannedIn, op["summary"])
		addOperation(paths, decl.path, decl.method, op)
		if _, ok := tagSet[decl.tag]; !ok {
			tagSet[decl.tag] = decl.tagDescription
		}
	}

	tags := make([]openAPIObj, 0, len(tagSet))
	tagNames := make([]string, 0, len(tagSet))
	for name := range tagSet {
		tagNames = append(tagNames, name)
	}
	sort.Strings(tagNames)
	for _, name := range tagNames {
		tag := openAPIObj{"name": name}
		if desc := tagSet[name]; desc != "" {
			tag["description"] = desc
		}
		tags = append(tags, tag)
	}

	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "dev"
	}

	doc := openAPIObj{
		"openapi": "3.1.0",
		"info": openAPIObj{
			"title":   "Space Data Network — Gateway API",
			"version": version,
			"description": "Every SDN node is an HTTP gateway for the network. Endpoints are FLOWS — " +
				"wasm modules mounted on listener paths by node configuration — and this document is " +
				"generated at runtime from the manifests of the flows ACTUALLY mounted on this node, " +
				"plus a small static set of native routes (marked x-sdn-served-by: native) and the " +
				"designed-but-not-yet-mounted Phase G routes (marked x-sdn-status: planned).\n\n" +
				"Response conventions (all record streams): default encoding is an aligned " +
				"size-prefixed FlatBuffer stream (" + ContentTypeFlatBufferStream + "); " +
				"?format=json opts into a BARE top-level JSON array of records. Metadata never rides " +
				"in a body envelope — X-SDN-Record-Count and a content-derived ETag are response " +
				"headers on BOTH encodings, and conditional GET (If-None-Match → 304) works on both.\n\n" +
				"Property names: JSON renderings of SDS records use the spacedatastandards.org " +
				"IDL / JSON Schema capitalization EXACTLY (NORAD_CAT_ID, MEAN_MOTION, FILE_ID, " +
				"CID, DN, …) — never lowercased. API-synthesized envelope fields that are not " +
				"schema fields (signature_verified, attribution, peer_id, …) stay lowercase " +
				"snake_case: the case distinction separates schema data from API metadata.",
		},
		"servers": []openAPIObj{{"url": "/"}},
		"tags":    tags,
		"paths":   paths,
		"components": openAPIObj{
			"headers": openAPIObj{
				"XSDNRecordCount": openAPIObj{
					"description": "Number of records in the response: size-prefixed frames of the FlatBuffer stream, or top-level elements of the bare JSON array. Present on both encodings.",
					"schema":      openAPIObj{"type": "integer", "minimum": 0},
				},
				"ETag": openAPIObj{
					"description": "Strong, content-derived entity tag for the record stream (deterministic FNV-1a 64 over the stream bytes). Identical for both encodings' underlying stream; use with If-None-Match for 304 revalidation.",
					"schema":      openAPIObj{"type": "string"},
				},
			},
		},
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal openapi document: %w", err)
	}
	return append(out, '\n'), nil
}

// joinMountPath joins a config mount pattern ("/api/v1/data/") with a route
// suffix ("omm/bulk" or "/omm/bulk").
func joinMountPath(mountPath, routePath string) string {
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

func addOperation(paths openAPIObj, path, method string, op openAPIObj) {
	item, _ := paths[path].(openAPIObj)
	if item == nil {
		item = openAPIObj{}
		paths[path] = item
	}
	item[strings.ToLower(method)] = op
}

func pathHasOperation(paths openAPIObj, path, method string) bool {
	item, _ := paths[path].(openAPIObj)
	if item == nil {
		return false
	}
	_, ok := item[strings.ToLower(method)]
	return ok
}

func stampAnonymous(op openAPIObj, effective func(method, path string) bool, method, path string) {
	if effective == nil {
		return
	}
	anonymous := effective(method, path)
	op["x-sdn-anonymous"] = anonymous
	if anonymous {
		return
	}
	op["security"] = []openAPIObj{{"sdnSession": []string{}}}
}

// operationFromFlowRoute maps one flow-manifest route declaration to an
// OpenAPI operation object.
func operationFromFlowRoute(mf FlowDocSource, route *flowrt.FlowAPIRoute, tag string,
	effective func(method, path string) bool, fullPath string) openAPIObj {

	op := openAPIObj{
		"tags":               []string{tag},
		"x-sdn-served-by":    "flow",
		"x-sdn-flow":         mf.ProgramID(),
		"x-sdn-mount":        mf.MountPath(),
		"x-sdn-flow-version": mf.FlowVersion(),
	}
	if route.OperationID != "" {
		op["operationId"] = route.OperationID
	}
	if route.Summary != "" {
		op["summary"] = route.Summary
	}
	if route.Description != "" {
		op["description"] = route.Description
	}
	if route.Deprecated {
		op["deprecated"] = true
	}
	if len(route.Params) > 0 {
		params := make([]interface{}, 0, len(route.Params))
		for _, p := range route.Params {
			params = append(params, p)
		}
		op["parameters"] = params
	}
	if route.RequestBody != nil {
		op["requestBody"] = route.RequestBody
	}
	if len(route.Responses) > 0 {
		responses := openAPIObj{}
		codes := make([]string, 0, len(route.Responses))
		for code := range route.Responses {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		for _, code := range codes {
			responses[code] = responseFromFlowDecl(route.Responses[code])
		}
		op["responses"] = responses
	}
	// Declared vs effective anonymity: the flow REQUESTS placement, the
	// host allowlist DECIDES (docs/gateway-api.md §4).
	op["x-sdn-anonymous-requested"] = route.Anonymous
	stampAnonymous(op, effective, route.Method, fullPath)
	return op
}

func responseFromFlowDecl(decl flowrt.FlowAPIResponse) openAPIObj {
	resp := openAPIObj{}
	if decl.Description != "" {
		resp["description"] = decl.Description
	} else {
		resp["description"] = ""
	}
	headers := openAPIObj{}
	if decl.RecordStream {
		headers["X-SDN-Record-Count"] = openAPIObj{"$ref": "#/components/headers/XSDNRecordCount"}
		headers["ETag"] = openAPIObj{"$ref": "#/components/headers/ETag"}
	}
	for name, header := range decl.Headers {
		headers[name] = header
	}
	if len(headers) > 0 {
		resp["headers"] = headers
	}
	if len(decl.Content) > 0 {
		content := openAPIObj{}
		types := make([]string, 0, len(decl.Content))
		for mediaType := range decl.Content {
			types = append(types, mediaType)
		}
		sort.Strings(types)
		for _, mediaType := range types {
			c := decl.Content[mediaType]
			media := openAPIObj{}
			schema := c.Schema
			if c.Description != "" {
				if schema == nil {
					schema = map[string]interface{}{"description": c.Description}
				} else if _, ok := schema["description"]; !ok {
					// copy-on-write so the parsed decl stays untouched
					copied := make(map[string]interface{}, len(schema)+1)
					for k, v := range schema {
						copied[k] = v
					}
					copied["description"] = c.Description
					schema = copied
				}
			}
			if schema != nil {
				media["schema"] = schema
			}
			if c.SDS != nil {
				media["x-sds-schema"] = c.SDS
			}
			content[mediaType] = media
		}
		resp["content"] = content
	}
	return resp
}

// ---------------------------------------------------------------------------
// Static declarations: native routes + planned Phase G routes
// ---------------------------------------------------------------------------

type staticRouteDecl struct {
	path           string
	method         string
	tag            string
	tagDescription string
	plannedIn      string // planned decls only
	operation      openAPIObj
}

// recordStreamResponses builds the standard record-stream response set the
// header/format conventions demand (docs/gateway-api.md §3).
func recordStreamResponses(fbDescription, jsonItemDescription string, sds openAPIObj) openAPIObj {
	streamHeaders := openAPIObj{
		"X-SDN-Record-Count": openAPIObj{"$ref": "#/components/headers/XSDNRecordCount"},
		"ETag":               openAPIObj{"$ref": "#/components/headers/ETag"},
	}
	fbMedia := openAPIObj{"schema": openAPIObj{"description": fbDescription}}
	if sds != nil {
		fbMedia["x-sds-schema"] = sds
	}
	return openAPIObj{
		"200": openAPIObj{
			"description": "Record stream; X-SDN-Record-Count and ETag headers on both encodings.",
			"headers":     streamHeaders,
			"content": openAPIObj{
				ContentTypeFlatBufferStream: fbMedia,
				ContentTypeJSON: openAPIObj{
					"schema": openAPIObj{
						"type":        "array",
						"items":       openAPIObj{"type": "object"},
						"description": jsonItemDescription,
					},
				},
			},
		},
		"304": openAPIObj{"description": "Not modified (If-None-Match matched the stream ETag)."},
	}
}

func formatParam() openAPIObj {
	return openAPIObj{
		"name":        "format",
		"in":          "query",
		"required":    false,
		"schema":      openAPIObj{"type": "string", "enum": []string{"flatbuffer", "json"}, "default": "flatbuffer"},
		"description": "Response encoding: flatbuffer = aligned size-prefixed FlatBuffer stream (default); json = bare top-level JSON array.",
	}
}

// nativeRouteDecls is the SMALL static set of Go-served routes included in
// the spec. Everything else native stays out of the public gateway spec on
// purpose (docs/gateway-api.md §4 records where each native route lands).
func nativeRouteDecls() []staticRouteDecl {
	return []staticRouteDecl{
		{
			path: "/api/v1/data/health", method: "GET",
			tag: "node", tagDescription: "Node status and identity (native routes; migrate to flows later).",
			operation: openAPIObj{
				"operationId": "getDataHealth",
				"summary":     "Data API liveness",
				"description": "Liveness of the data surface. Native Go route; migrates to a flow with the rest of the native surface.",
				"responses": openAPIObj{
					"200": openAPIObj{
						"description": "Service is up.",
						"content":     openAPIObj{ContentTypeJSON: openAPIObj{"schema": openAPIObj{"type": "object"}}},
					},
				},
			},
		},
		{
			path: "/api/node/info", method: "GET",
			tag: "node", tagDescription: "Node status and identity (native routes; migrate to flows later).",
			operation: openAPIObj{
				"operationId": "getNodeInfo",
				"summary":     "Node identity and addresses",
				"description": "PeerID, listen addresses and node metadata. Native Go route; migrates to a flow (and to /api/v1/…) with the discovery surface.",
				"responses": openAPIObj{
					"200": openAPIObj{
						"description": "Node descriptor.",
						"content":     openAPIObj{ContentTypeJSON: openAPIObj{"schema": openAPIObj{"type": "object"}}},
					},
				},
			},
		},
		{
			path: "/api/v1/openapi.json", method: "GET",
			tag: "docs", tagDescription: "This documentation surface (temporary native bootstrap, docs/gateway-api.md §6).",
			operation: openAPIObj{
				"operationId": "getOpenAPI",
				"summary":     "This OpenAPI document",
				"description": "Generated at daemon start from the mounted flow manifests. Temporary native route (bootstrap): becomes a flow once a mount-table capability node exists.",
				"responses": openAPIObj{
					"200": openAPIObj{
						"description": "OpenAPI 3.1 document.",
						"headers":     openAPIObj{"ETag": openAPIObj{"$ref": "#/components/headers/ETag"}},
						"content":     openAPIObj{ContentTypeJSON: openAPIObj{"schema": openAPIObj{"type": "object"}}},
					},
					"304": openAPIObj{"description": "Not modified."},
				},
			},
		},
		{
			path: "/api/v1/docs", method: "GET",
			tag: "docs", tagDescription: "This documentation surface (temporary native bootstrap, docs/gateway-api.md §6).",
			operation: openAPIObj{
				"operationId": "getDocs",
				"summary":     "API reference UI (Scalar)",
				"description": "Self-hosted Scalar reference over this document. Fully self-contained: the vendored bundle is served by this daemon and the page's CSP forbids any external fetch.",
				"responses": openAPIObj{
					"200": openAPIObj{
						"description": "HTML page.",
						"content":     openAPIObj{"text/html": openAPIObj{"schema": openAPIObj{"type": "string"}}},
					},
				},
			},
		},
	}
}

// plannedRouteDecls are the Phase G gateway routes: designed shapes, not
// yet mounted. Each disappears from this list (shadowed automatically) the
// moment a mounted flow claims its path.
func plannedRouteDecls() []staticRouteDecl {
	peerIDParam := openAPIObj{
		"name": "peerId", "in": "path", "required": true,
		"schema":      openAPIObj{"type": "string"},
		"description": "libp2p peer ID of the provider node (e.g. 16Uiu2HAm…).",
	}
	discoveryTag := "discovery"
	discoveryDesc := "Peer and standards discovery (flows landing in G.2/G.3)."
	dataTag := "provider-data"
	dataDesc := "Provider-scoped dataset retrieval and public query (flows landing in G.4/G.5)."

	return []staticRouteDecl{
		{
			path: "/api/v1/peers", method: "GET", plannedIn: "G.2",
			tag: discoveryTag, tagDescription: discoveryDesc,
			operation: openAPIObj{
				"operationId": "listPeers",
				"summary":     "Known peers with published standards",
				"description": "Peers known to this node (peerstore + DHT + EPM profiles) with, per peer, the space-data standards it publishes. Replaces the current native connected-peers route at this path. Flow-served; fb stream carries $EPM records, JSON is a bare array of {peerId, name, standards[]} summaries.",
				"parameters":  []interface{}{formatParam()},
				"responses": recordStreamResponses(
					"Aligned size-prefixed $EPM FlatBuffer frames (one per known peer).",
					"Bare JSON array of peer summaries: {peerId, name, standards[]}.",
					openAPIObj{"schemaName": "EPM.fbs", "fileIdentifier": "$EPM", "rootTypeName": "EPM"},
				),
			},
		},
		{
			path: "/api/v1/peers/{peerId}", method: "GET", plannedIn: "G.2",
			tag: discoveryTag, tagDescription: discoveryDesc,
			operation: openAPIObj{
				"operationId": "getPeer",
				"summary":     "One peer's profile and published standards",
				"description": "The peer's EPM entity profile plus the standards it publishes. 404 when the peer is unknown to this node.",
				"parameters":  []interface{}{peerIDParam, formatParam()},
				"responses": mergeResponses(recordStreamResponses(
					"One aligned $EPM FlatBuffer frame.",
					"Bare JSON array with the single peer record.",
					openAPIObj{"schemaName": "EPM.fbs", "fileIdentifier": "$EPM", "rootTypeName": "EPM"},
				), openAPIObj{"404": openAPIObj{"description": "Peer unknown to this node."}}),
			},
		},
		{
			path: "/api/v1/standards", method: "GET", plannedIn: "G.2",
			tag: discoveryTag, tagDescription: discoveryDesc,
			operation: openAPIObj{
				"operationId": "listStandards",
				"summary":     "Standards published across known peers",
				"description": "Which space-data standards (OMM, CAT, SPW, …) are published on the network as seen by this node, with the publishing peers per standard.",
				"parameters":  []interface{}{formatParam()},
				"responses": recordStreamResponses(
					"Aligned FlatBuffer frames (standard advertisement records).",
					"Bare JSON array of {standard, peers[]} entries.",
					nil,
				),
			},
		},
		{
			path: "/api/v1/peers/{peerId}/pnm", method: "GET", plannedIn: "G.3",
			tag: discoveryTag, tagDescription: discoveryDesc,
			operation: openAPIObj{
				"operationId": "getPeerPNM",
				"summary":     "Signed publish notification messages (provenance)",
				"description": "The peer's newest signed PNMs (default limit=1 → newest only). PNMs are ed25519-signed and verifiable against the peer's published key: this is the provenance anchor for every dataset the peer serves.",
				"parameters": []interface{}{peerIDParam, openAPIObj{
					"name": "limit", "in": "query", "required": false,
					"schema":      openAPIObj{"type": "integer", "minimum": 1, "default": 1},
					"description": "Number of newest PNMs to return. Default 1.",
				}, formatParam()},
				"responses": mergeResponses(recordStreamResponses(
					"Aligned size-prefixed $PNM FlatBuffer frames, newest first.",
					"Bare JSON array of PNM records, newest first.",
					openAPIObj{"schemaName": "PNM.fbs", "fileIdentifier": "$PNM", "rootTypeName": "PNM"},
				), openAPIObj{"404": openAPIObj{"description": "Peer unknown or no PNMs recorded."}}),
			},
		},
		{
			path: "/api/v1/peers/{peerId}/{standard}/latest", method: "GET", plannedIn: "G.4",
			tag: dataTag, tagDescription: dataDesc,
			operation: openAPIObj{
				"operationId": "getPeerStandardLatest",
				"summary":     "Provider's newest published dataset for a standard",
				"description": "The newest dataset the provider peer published for the standard (e.g. celestrak.eth × OMM), served from this node's OPT-IN pin (gateway.pin config; each publish SUPERSEDES the previous pin). Not pinned and not locally available → honest 404/503 carrying the PNM pointer — v1 never silently proxies.",
				"parameters": []interface{}{peerIDParam, openAPIObj{
					"name": "standard", "in": "path", "required": true,
					"schema":      openAPIObj{"type": "string"},
					"description": "Space-data standard name (omm, cat, spw, …).",
				}, formatParam()},
				"responses": mergeResponses(recordStreamResponses(
					"Aligned size-prefixed FlatBuffer frames of the standard's record type.",
					"Bare JSON array of records.",
					nil,
				), openAPIObj{
					"404": openAPIObj{"description": "Unknown peer/standard, or dataset not pinned here — body carries the PNM pointer for direct retrieval."},
					"503": openAPIObj{"description": "Known dataset temporarily unavailable — body carries the PNM pointer."},
				}),
			},
		},
		{
			path: "/api/v1/query", method: "POST", plannedIn: "G.5",
			tag: dataTag, tagDescription: dataDesc,
			operation: openAPIObj{
				"operationId": "postQuery",
				"summary":     "Sandboxed public SQL query",
				"description": "Raw SELECT over this node's record store in a sandboxed READ-ONLY engine session: single-statement SELECT only (no pragma/attach/writes), statement timeout, row and byte caps. Supersedes the data-retrieval flow's internal query route and the native /api/v1/data/query.",
				"parameters":  []interface{}{formatParam()},
				"requestBody": openAPIObj{
					"required": true,
					"content": openAPIObj{
						ContentTypeJSON: openAPIObj{"schema": openAPIObj{
							"type":     "object",
							"required": []string{"sql"},
							"properties": openAPIObj{
								"sql":    openAPIObj{"type": "string", "description": "Single SELECT statement."},
								"params": openAPIObj{"type": "array", "description": "Positional parameters."},
								"limit":  openAPIObj{"type": "integer", "description": "Row cap override (never above the node's hard cap)."},
							},
						}},
					},
				},
				"responses": mergeResponses(recordStreamResponses(
					"Aligned size-prefixed FlatBuffer frames (all selected cells must be BLOB columns).",
					"Bare JSON array of result rows.",
					nil,
				), openAPIObj{
					"400": openAPIObj{"description": "Rejected: not a single SELECT statement, forbidden construct (pragma/attach/write), or malformed body."},
					"413": openAPIObj{"description": "Result exceeded the node's row/byte caps."},
					"504": openAPIObj{"description": "Statement timeout exceeded."},
				}),
			},
		},
	}
}

func mergeResponses(base openAPIObj, extra openAPIObj) openAPIObj {
	for code, resp := range extra {
		base[code] = resp
	}
	return base
}
