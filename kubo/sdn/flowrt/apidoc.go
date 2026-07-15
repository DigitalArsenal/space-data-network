package flowrt

// Flow-manifest API extension (loop G.1): a compiled flow bundle's flow.json
// MAY carry a top-level "api" block declaring the HTTP surface the flow
// serves — route paths (relative to the mount), methods, parameters and
// response schemas. The block is DECLARATIVE metadata authored next to the
// flow graph and copied verbatim into the bundle by the SDK flow compiler,
// so the OpenAPI generator (internal/api docs handler) reads it from the
// MOUNTED flow at runtime: the published spec cannot drift from what is
// actually mounted, because it is generated from the mounted artifact
// itself. Schema documented in docs/gateway-api.md §5.
//
// Nothing here changes request handling: the wasm flow remains the single
// authority for routing/params/format/ETag. A mismatch between the api
// block and the flow's real behavior is a bug in the flow bundle, exactly
// like a wrong method description in plugin-manifest.json.

import (
	"encoding/json"
	"strings"
)

// FlowAPIDoc is the parsed top-level "api" block of a bundle's flow.json.
type FlowAPIDoc struct {
	// BasePath is the flow author's canonical mount path hint (e.g.
	// "/api/v1/data"). Documentation only — the ACTUAL mount path comes from
	// the node's config.FlowMount and always wins in the generated spec.
	BasePath string `json:"basePath,omitempty"`

	// Tag groups the flow's operations in the generated spec (defaults to
	// the flow program ID when empty).
	Tag string `json:"tag,omitempty"`

	// TagDescription describes the tag in the generated spec.
	TagDescription string `json:"tagDescription,omitempty"`

	// Routes are the HTTP operations the flow serves under its mount.
	Routes []FlowAPIRoute `json:"routes"`
}

// FlowAPIRoute declares one HTTP operation served by the flow.
type FlowAPIRoute struct {
	// Path is the route suffix relative to the mount path (no leading
	// slash required; OpenAPI-style templates like "peers/{peerId}" are
	// allowed and pass through to the spec verbatim).
	Path string `json:"path"`

	// Method is the HTTP method (GET/POST/...). Defaults to GET.
	Method string `json:"method,omitempty"`

	OperationID string `json:"operationId,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty"`

	// Anonymous is the flow author's REQUESTED anonymous-access placement
	// for this route (docs/gateway-api.md §4). It is an input to the host's
	// allowlist policy, never a grant by itself; the generated spec stamps
	// the host's EFFECTIVE decision separately (x-sdn-anonymous).
	Anonymous bool `json:"anonymous,omitempty"`

	// Params are OpenAPI parameter objects (name/in/required/schema/
	// description pass through verbatim).
	Params []map[string]interface{} `json:"params,omitempty"`

	// RequestBody is an OpenAPI requestBody object, passed through
	// verbatim when present.
	RequestBody map[string]interface{} `json:"requestBody,omitempty"`

	// Responses maps status code -> response declaration.
	Responses map[string]FlowAPIResponse `json:"responses,omitempty"`
}

// FlowAPIResponse declares one response of a route.
type FlowAPIResponse struct {
	Description string `json:"description,omitempty"`

	// RecordStream marks the response as an SDN record stream: the
	// generator adds the standard metadata headers (X-SDN-Record-Count +
	// ETag) that ride on BOTH encodings (fb stream and bare-array JSON).
	// The header definitions are owned by the generator so every record
	// stream documents them identically.
	RecordStream bool `json:"recordStream,omitempty"`

	// Content maps media type -> content declaration.
	Content map[string]FlowAPIContent `json:"content,omitempty"`

	// Headers are additional OpenAPI header objects by header name,
	// passed through verbatim (merged after the recordStream standards).
	Headers map[string]map[string]interface{} `json:"headers,omitempty"`
}

// FlowAPIContent declares one media type of a response.
type FlowAPIContent struct {
	Description string `json:"description,omitempty"`

	// Schema is an OpenAPI schema object, passed through verbatim.
	Schema map[string]interface{} `json:"schema,omitempty"`

	// SDS names the space-data-standards FlatBuffer type carried by the
	// stream (schemaName / fileIdentifier / rootTypeName). Emitted as the
	// x-sds-schema extension in the generated spec.
	SDS map[string]interface{} `json:"sds,omitempty"`
}

// flowBundleAPIDoc is the flow.json subset holding the api extension.
type flowBundleAPIDoc struct {
	Version string      `json:"version"`
	API     *FlowAPIDoc `json:"api"`
}

// parseFlowAPIDoc extracts the "api" block (and the flow version) from raw
// bundle flow.json bytes. Returns (nil, version) when the flow declares no
// API surface — perfectly legal for non-HTTP flows.
func parseFlowAPIDoc(flowJSON []byte) (*FlowAPIDoc, string) {
	var doc flowBundleAPIDoc
	if err := json.Unmarshal(flowJSON, &doc); err != nil {
		log.Warnf("flow.json api extension: parse: %v", err)
		return nil, ""
	}
	if doc.API == nil || len(doc.API.Routes) == 0 {
		return nil, doc.Version
	}
	for i := range doc.API.Routes {
		if strings.TrimSpace(doc.API.Routes[i].Method) == "" {
			doc.API.Routes[i].Method = "GET"
		}
		doc.API.Routes[i].Method = strings.ToUpper(strings.TrimSpace(doc.API.Routes[i].Method))
	}
	return doc.API, doc.Version
}

// NOTE (kubo Phase 2c partial port): the three MountedFlow accessor methods
// that lived here in sdn-server — APIDoc(), FlowVersion(), MountPath() — were
// DEFERRED along with the MountedFlow type itself, which is defined in the
// deferred serving file httpmount.go (config/flatsqlrt/httpabi coupled). The
// declarative api-extension types above (FlowAPIDoc/Route/Response/Content)
// and parseFlowAPIDoc are the storage-free half and port cleanly; re-add the
// MountedFlow methods when httpmount.go is brought over. See doc.go.
