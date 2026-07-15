// Package appmanifest defines the SDN "app" manifest: a documented
// COMPOSITION over existing SDS records, not a new SDS FlatBuffer type.
//
// Background (H1 loop): an "app" is a collection of module-sdk WASM modules
// + a manifest + a UI, runnable in desktop or browser (see the SpaceAware.io
// / "SDN Console" design's APPS_CATALOG). Today the SDS $PLG record
// (third_party/spacedatastandards-go/PLG) and the module-sdk manifest
// (internal/modulert.Manifest, parsed from an embedded $PLG or legacy
// "PMAN" FlatBuffer — see internal/modulert/manifest.go) each describe
// exactly one module. Neither has a notion of "N modules + data + sources +
// UI, grouped as one launchable app". Minting a new .fbs type for that is
// explicitly out of scope for this loop (owner-gated, cross-repo,
// version-bump-heavy — see spacedatastandards.org).
//
// AppManifest instead REFERENCES existing module identities rather than
// embedding or redefining them:
//
//   - ModuleRef.PluginID matches $PLG's PLUGIN_ID field / modulert.Manifest.
//     PluginID exactly — the same identifier a module already advertises in
//     its own $PLG manifest.
//   - ModuleRef.ContentHash matches the lowercase hex SHA-256 the runtime
//     already computes for every loaded module (modulert.Module.
//     ContentHash(), the capability-policy identity) — no new hash scheme.
//   - DataRef.SDSType names an existing spacedatastandards.org schema (e.g.
//     "OMM", "EPM") by its established name; AppManifest carries no SDS
//     schema definitions of its own.
//   - SourceRef and UIEntry reference ModuleRef.ID (an app-local pointer)
//     rather than duplicating module identity.
//
// Serialization: AppManifest's canonical form is deterministic JSON — see
// MarshalCanonicalJSON. There is no map-typed field anywhere in the struct
// graph, so encoding/json's struct-field-order output is stable across runs
// for equal content (no non-determinism to canonicalize away). For the
// "round-trips as a FlatBuffer" requirement, ToMBL/FromMBL (mbl.go) wrap
// that same canonical JSON as a single MANIFEST-role entry inside an
// existing $MBL (Module Bundle Listing) FlatBuffer container — see that
// file's doc comment for why $MBL, specifically, is the right existing
// container to reuse instead of minting a new one.
package appmanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ipfs/kubo/sdn/plugins"
)

// AppManifest composes a runnable SDN app out of references to existing
// module/data/source records. It never embeds a $PLG record or an SDS data
// schema — only stable identifiers into them (see package doc).
type AppManifest struct {
	// ID is a stable, node-local identifier for this app (e.g.
	// "spaceaware-console"). Required.
	ID string `json:"id"`
	// Name is the human-readable app name shown in launchers/catalogs.
	Name string `json:"name"`
	// Version is the app manifest's own version (independent of any member
	// module's version).
	Version string `json:"version"`
	// Description is an optional human-readable summary.
	Description string `json:"description,omitempty"`
	// Modules is the set of member modules that make up this app,
	// referencing existing $PLG / module-sdk manifest identities. Required,
	// non-empty.
	Modules []ModuleRef `json:"modules"`
	// Data is the set of SDS data types this app produces and/or consumes.
	// Optional.
	Data []DataRef `json:"data,omitempty"`
	// Sources is the set of upstream data sources this app depends on.
	// Optional.
	Sources []SourceRef `json:"sources,omitempty"`
	// Dataflow is the app's declarative data contract, mirroring schema/APP's
	// DATAFLOW:[APPDataflow] field-for-field: what data enters and leaves the
	// running page, the SDS standard it carries, how it is transported, and —
	// when applicable — the loaded module method port bound to it. Optional; a
	// headless or contract-less app leaves this nil. Each entry survives the
	// $APP FlatBuffer round-trip (ToAPP/FromAPP) with all fields intact.
	Dataflow []DataflowEntry `json:"dataflow,omitempty"`
	// UI is the app's legacy single web UI entry, if it has one. Optional.
	// DEPRECATED in favor of Pages (below), which mirrors schema/APP's
	// UI:[APPUIPage] list field-for-field and additionally supports inline,
	// self-contained pages. UI is retained so pre-Pages manifests keep
	// parsing/validating/resolving unchanged (the JSON/MBL back-compat lane).
	// New apps use Pages. The $APP FlatBuffer lane (ToAPP/FromAPP) is
	// canonical on Pages and does not carry this legacy field — see app_fb.go.
	UI *UIEntry `json:"ui,omitempty"`
	// Pages is the app's UI page list, mirroring schema/APP's UI:[APPUIPage]
	// field-for-field. Each page is EITHER inline (self-contained Content in
	// the string form named by Encoding, with ContentSHA256 over the decoded
	// bytes) OR module-served (ModuleID+URL). Exactly one page is the Entry
	// when Pages is non-empty. Optional — a headless app leaves this nil.
	Pages []UIPage `json:"pages,omitempty"`
	// CreatedAt / UpdatedAt mirror schema/APP CREATED_AT / UPDATED_AT: RFC 3339
	// UTC fixed-millisecond timestamps. Optional; left empty in the canonical
	// deterministic record (a release-signing step stamps them) so the record
	// stays byte-stable for the drift gate.
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ModuleRef identifies one member module by referencing its existing $PLG /
// module-sdk manifest identity rather than embedding it.
type ModuleRef struct {
	// ID is an app-local stable reference used by DataRef.ModuleID,
	// SourceRef.Ref (when Kind == SourceKindModule), and UIEntry.ModuleID
	// to point back at this module without repeating PluginID/ContentHash.
	// Required, unique within the manifest.
	ID string `json:"id"`
	// PluginID matches the $PLG PLUGIN_ID field / modulert.Manifest.
	// PluginID the module itself advertises. Required.
	PluginID string `json:"pluginId"`
	// ContentHash is the lowercase hex SHA-256 of the module's wasm
	// artifact — the same identity modulert.Module.ContentHash() reports.
	// Optional (an app manifest authored before a specific build is
	// published may omit it and pin PluginID/Version instead), but strongly
	// recommended for reproducible app installs.
	ContentHash string `json:"contentHash,omitempty"`
	// Version is the member module's own semantic version.
	Version string `json:"version,omitempty"`
	// Role is a free-text hint for the launcher, e.g. "primary",
	// "worker", "ui-host". Optional.
	Role string `json:"role,omitempty"`
	// Description is an optional human-readable summary.
	Description string `json:"description,omitempty"`
	// RuntimeTarget mirrors schema/APP's APPModuleRef.RUNTIME_TARGET
	// (appRuntimeTarget): where this member module is instantiated —
	// RuntimeTargetNode (server only), RuntimeTargetPage (browser only, bytes
	// resolved by ContentHash over IPFS through the same module-sdk harness),
	// or RuntimeTargetBoth. Optional; empty normalizes to the .fbs default
	// NODE on the $APP round-trip (see runtimeToFBName in app_fb.go).
	RuntimeTarget RuntimeTarget `json:"runtimeTarget,omitempty"`
}

// RuntimeTarget mirrors schema/APP's appRuntimeTarget value-for-value: where a
// member module is instantiated. The JSON values are the lowercase of the .fbs
// enum names so the $APP round-trip is a mechanical name map (see
// runtimeToFBName / runtimeFromFBName in app_fb.go):
//
//	node <-> NODE
//	page <-> PAGE
//	both <-> BOTH
type RuntimeTarget string

const (
	// RuntimeTargetNode loads only in the desktop/server node runtime. This is
	// the .fbs default, so an omitted RuntimeTarget round-trips to it.
	RuntimeTargetNode RuntimeTarget = "node"
	// RuntimeTargetPage loads in the browser through the isomorphic module-sdk
	// harness; the page resolves the module bytes by ContentHash over IPFS.
	RuntimeTargetPage RuntimeTarget = "page"
	// RuntimeTargetBoth loads in both hosts from the same content-addressed
	// bytes and ABI.
	RuntimeTargetBoth RuntimeTarget = "both"
)

// DataDirection describes whether an app produces, consumes, or both
// produces and consumes a referenced SDS data type.
type DataDirection string

const (
	DataDirectionProduces DataDirection = "produces"
	DataDirectionConsumes DataDirection = "consumes"
	DataDirectionBoth     DataDirection = "both"
)

// DataRef references an SDS record type the app produces and/or consumes.
// It names an existing spacedatastandards.org schema by its established
// name (e.g. "OMM", "EPM") — AppManifest defines no SDS schemas of its own.
type DataRef struct {
	// ID is an app-local stable reference for this data binding. Required,
	// unique within the manifest.
	ID string `json:"id"`
	// SDSType is the existing SDS schema/record name, e.g. "OMM", "EPM".
	// Required.
	SDSType string `json:"sdsType"`
	// Direction describes the data flow relative to the app. Optional,
	// defaults to DataDirectionProduces semantics when empty (most
	// permissive assumption for older manifests).
	Direction DataDirection `json:"direction,omitempty"`
	// ModuleID, if set, must match a ModuleRef.ID in the same manifest —
	// the member module responsible for this data binding.
	ModuleID string `json:"moduleId,omitempty"`
	// Description is an optional human-readable summary.
	Description string `json:"description,omitempty"`
}

// SourceKind enumerates the kinds of upstream source a SourceRef can name.
type SourceKind string

const (
	// SourceKindModule means Ref is a ModuleRef.ID in the same manifest —
	// one member module is itself the source for another.
	SourceKindModule SourceKind = "module"
	// SourceKindExternalAPI means Ref is a URL or endpoint identifier
	// outside the app (e.g. a CelesTrak/Space-Track endpoint).
	SourceKindExternalAPI SourceKind = "external-api"
	// SourceKindDataset means Ref is a dataset/catalog identifier.
	SourceKindDataset SourceKind = "dataset"
)

// SourceRef references an upstream data source the app depends on.
type SourceRef struct {
	// ID is an app-local stable reference for this source. Required,
	// unique within the manifest.
	ID string `json:"id"`
	// Kind classifies Ref. Required.
	Kind SourceKind `json:"kind"`
	// Ref is the source identifier: a ModuleRef.ID when Kind ==
	// SourceKindModule, otherwise a URL/dataset identifier. Required.
	Ref string `json:"ref"`
	// Description is an optional human-readable summary.
	Description string `json:"description,omitempty"`
}

// FlowDirection mirrors schema/APP's appFlowDirection value-for-value: which
// way an APPDataflow payload crosses the running-page boundary. Distinct from
// DataDirection, which is producer/consumer relative to the app as a whole;
// this is page-relative. The JSON values are the lowercase of the .fbs enum
// names so the $APP round-trip is a mechanical name map (see flowDirToFBName /
// flowDirFromFBName in app_fb.go):
//
//	to_page       <-> TO_PAGE
//	from_page     <-> FROM_PAGE
//	bidirectional <-> BIDIRECTIONAL
type FlowDirection string

const (
	// FlowDirectionToPage delivers data into the page for display or module
	// input. This is the .fbs default, so an omitted DIRECTION round-trips to it.
	FlowDirectionToPage FlowDirection = "to_page"
	// FlowDirectionFromPage emits data from the page (a module output or user
	// action) for publication or upstream consumption.
	FlowDirectionFromPage FlowDirection = "from_page"
	// FlowDirectionBidirectional crosses data in both directions over one channel.
	FlowDirectionBidirectional FlowDirection = "bidirectional"
)

// valid reports whether d is a recognized direction. An empty direction is
// valid: it normalizes to the .fbs default TO_PAGE on the $APP round-trip.
func (d FlowDirection) valid() bool {
	switch d {
	case "", FlowDirectionToPage, FlowDirectionFromPage, FlowDirectionBidirectional:
		return true
	default:
		return false
	}
}

// FlowTransport mirrors schema/APP's appFlowTransport value-for-value: the
// transport that moves an APPDataflow payload. Locators are content-addressed
// and IPFS-first. The JSON values are the lowercase of the .fbs enum names so
// the $APP round-trip is a mechanical name map (see flowTransportToFBName /
// flowTransportFromFBName in app_fb.go):
//
//	ipfs_cid      <-> IPFS_CID
//	pubsub_topic  <-> PUBSUB_TOPIC
//	gateway_route <-> GATEWAY_ROUTE
type FlowTransport string

const (
	// FlowTransportIPFSCID: Locator is a CID; the page fetches the SDS record
	// bytes by content. This is the .fbs default, so an omitted TRANSPORT
	// round-trips to it.
	FlowTransportIPFSCID FlowTransport = "ipfs_cid"
	// FlowTransportPubsubTopic: Locator is a gossip topic name; live SDS records
	// arrive on the topic via the node's pubsub bus.
	FlowTransportPubsubTopic FlowTransport = "pubsub_topic"
	// FlowTransportGatewayRoute: Locator is a same-origin HTTP route template
	// served by the node that serves the page.
	FlowTransportGatewayRoute FlowTransport = "gateway_route"
)

// valid reports whether t is a recognized transport. An empty transport is
// valid: it normalizes to the .fbs default IPFS_CID on the $APP round-trip.
func (t FlowTransport) valid() bool {
	switch t {
	case "", FlowTransportIPFSCID, FlowTransportPubsubTopic, FlowTransportGatewayRoute:
		return true
	default:
		return false
	}
}

// DataflowEntry is one unit of an app's declarative data contract, mirroring
// schema/APP's APPDataflow field-for-field so the $APP FlatBuffer round-trip
// (app_fb.go) is a mechanical field copy. A flow names an existing SDS schema
// it carries (it defines no data shape of its own), a direction and transport,
// where to fetch/reach the payload (Locator, interpreted per Transport), and —
// when bound to a loaded module — the ModuleID/MethodID/PortId method port.
//
// JSON key <-> schema/APP .fbs field mapping (keys stay camelCase to match the
// rest of this package; the field SET and semantics mirror the .fbs exactly):
//
//	name            <-> NAME
//	direction       <-> DIRECTION
//	sdsSchema       <-> SDS_SCHEMA
//	transport       <-> TRANSPORT
//	locator         <-> LOCATOR
//	moduleId        <-> MODULE_ID
//	methodId        <-> METHOD_ID
//	portId          <-> PORT_ID
//	contentEncoding <-> CONTENT_ENCODING
//	description     <-> DESCRIPTION
type DataflowEntry struct {
	// Name is an app-local stable name for this flow. Required, unique within
	// Dataflow.
	Name string `json:"name"`
	// Direction is which way the payload crosses the page boundary. Empty
	// normalizes to the .fbs default TO_PAGE on the $APP round-trip.
	Direction FlowDirection `json:"direction,omitempty"`
	// SDSSchema is the existing SDS schema code the flow carries (e.g. "OMM").
	// Required.
	SDSSchema string `json:"sdsSchema"`
	// Transport moves the payload. Empty normalizes to the .fbs default
	// IPFS_CID on the $APP round-trip.
	Transport FlowTransport `json:"transport,omitempty"`
	// Locator is where to fetch/reach the payload, interpreted per Transport: a
	// CID for ipfs_cid, a gossip topic name for pubsub_topic, or a same-origin
	// route template for gateway_route. Optional.
	Locator string `json:"locator,omitempty"`
	// ModuleID, when set, must equal a Modules[].ID — the loaded module that
	// produces or consumes this flow. Binds the flow to a specific module
	// method port together with MethodID and PortId.
	ModuleID string `json:"moduleId,omitempty"`
	// MethodID, when set, is the PLG method id on ModuleID this flow binds to.
	MethodID string `json:"methodId,omitempty"`
	// PortId, when set, is the PLG port id on MethodID this flow binds to.
	PortId string `json:"portId,omitempty"`
	// ContentEncoding is the string/compression form of the payload as it
	// crosses the channel, reusing the page content-encoding vocabulary. Empty
	// normalizes to the .fbs default UTF8 on the $APP round-trip.
	ContentEncoding UIContentEncoding `json:"contentEncoding,omitempty"`
	// Description is an optional human-readable summary.
	Description string `json:"description,omitempty"`
}

// UIEntry declares which member module serves the app's web UI and how its
// plugins.UIDescriptor should be populated. Title/Description/Icon/Color/
// TextColor left empty fall back to the app's own Name/Description (see
// Resolve) — only ModuleID and URL are meaningfully required.
type UIEntry struct {
	// ModuleID must match a ModuleRef.ID in the same manifest — the member
	// module that serves the UI page at URL. Required.
	ModuleID string `json:"moduleId"`
	// URL is the path/entrypoint to the UI page (e.g. "/apps/spaceaware/"
	// or a module-served static asset path). Required — this is the value
	// that ends up in plugins.UIDescriptor.URL, the field that is never
	// populated today (see internal/modulert.Module.UIDescriptor,
	// plugins/manager.go).
	URL string `json:"url"`
	// Title overrides plugins.UIDescriptor.Title. Falls back to the app's
	// Name when empty.
	Title string `json:"title,omitempty"`
	// Description overrides plugins.UIDescriptor.Description. Falls back
	// to the app's Description when empty.
	Description string `json:"description,omitempty"`
	// Icon overrides plugins.UIDescriptor.Icon.
	Icon string `json:"icon,omitempty"`
	// Color overrides plugins.UIDescriptor.Color.
	Color string `json:"color,omitempty"`
	// TextColor overrides plugins.UIDescriptor.TextColor.
	TextColor string `json:"textColor,omitempty"`
}

// UIPage is one UI page of an app, mirroring schema/APP's APPUIPage
// field-for-field so the $APP FlatBuffer round-trip (app_fb.go) is a
// mechanical field copy. Exactly one of the two delivery mechanisms must be
// populated per page:
//
//   - inline: Content (in the string form named by Encoding) is non-empty and
//     ModuleID+URL are empty. ContentSHA256 must equal the lowercase hex
//     SHA-256 of the DECODED bytes (verified in Validate).
//   - module-served: ModuleID (a Modules[].ID) and URL are set and Content is
//     empty.
//
// JSON key <-> schema/APP .fbs field mapping (keys stay camelCase to match the
// rest of this package; the field SET and semantics mirror the .fbs exactly):
//
//	id            <-> ID
//	title         <-> TITLE
//	description   <-> DESCRIPTION
//	icon          <-> ICON
//	color         <-> COLOR
//	textColor     <-> TEXT_COLOR
//	content       <-> CONTENT
//	encoding      <-> ENCODING
//	mediaType     <-> MEDIA_TYPE
//	contentSha256 <-> CONTENT_SHA256
//	entry         <-> ENTRY
//	moduleId      <-> MODULE_ID
//	url           <-> URL
type UIPage struct {
	// ID is an app-local stable reference for this page. Required, unique
	// within Pages.
	ID string `json:"id"`
	// Title falls back to the app Name when empty.
	Title string `json:"title,omitempty"`
	// Description falls back to the app Description when empty.
	Description string `json:"description,omitempty"`
	// Icon is a launcher icon identifier or inline data URI.
	Icon string `json:"icon,omitempty"`
	// Color is the launcher accent color (CSS color syntax).
	Color string `json:"color,omitempty"`
	// TextColor is the launcher text color (CSS color syntax).
	TextColor string `json:"textColor,omitempty"`
	// Content is the inlined, self-contained page in the string form declared
	// by Encoding. Empty when the page is module-served via ModuleID+URL.
	Content string `json:"content,omitempty"`
	// Encoding is the string form of Content. Empty normalizes to utf8.
	Encoding UIContentEncoding `json:"encoding,omitempty"`
	// MediaType is the IANA media type of the decoded page bytes (e.g.
	// text/html).
	MediaType string `json:"mediaType,omitempty"`
	// ContentSHA256 is the lowercase hex SHA-256 of the DECODED page bytes.
	// Required for inline pages; verified in Validate.
	ContentSHA256 string `json:"contentSha256,omitempty"`
	// Entry is true for the page the launcher opens first. Exactly one page in
	// Pages must set this.
	Entry bool `json:"entry,omitempty"`
	// ModuleID, when module-served, must equal a Modules[].ID.
	ModuleID string `json:"moduleId,omitempty"`
	// URL, when module-served, is the path/entrypoint the module serves the
	// page at.
	URL string `json:"url,omitempty"`
}

// IsInline reports whether the page carries its content inline (as opposed to
// being served by a member module).
func (p UIPage) IsInline() bool {
	return strings.TrimSpace(p.Content) != ""
}

// DecodedContent returns the decoded page bytes for an inline page (the bytes
// ContentSHA256 covers). It errors for a module-served page or an unsupported
// encoding.
func (p UIPage) DecodedContent() ([]byte, error) {
	if !p.IsInline() {
		return nil, fmt.Errorf("app manifest: page %q has no inline content to decode", p.ID)
	}
	return p.Encoding.decodeContent(p.Content)
}

// MarshalCanonicalJSON serializes the manifest to its canonical wire form:
// encoding/json's default struct-field-order encoding. There is no map
// field anywhere in AppManifest's graph, so this is already deterministic —
// no separate canonicalization pass (e.g. key sorting) is needed.
func (a *AppManifest) MarshalCanonicalJSON() ([]byte, error) {
	if a == nil {
		return nil, errors.New("app manifest is nil")
	}
	return json.Marshal(a)
}

// Parse decodes a canonical-JSON app manifest and validates it. Use
// Resolve instead when the caller (e.g. the H2 launcher) also needs
// cross-referenced Module/Data/Source/UI lookups.
func Parse(data []byte) (*AppManifest, error) {
	var manifest AppManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse app manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate checks required fields and referential integrity: every
// DataRef.ModuleID, SourceRef.Ref (Kind == SourceKindModule), and
// UIEntry.ModuleID must resolve to a ModuleRef.ID declared in Modules. It
// also rejects duplicate IDs within Modules, Data, and Sources — each list
// is keyed by ID for O(1) lookup during Resolve.
func (a *AppManifest) Validate() error {
	if a == nil {
		return errors.New("app manifest is nil")
	}
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("app manifest: id is required")
	}
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("app manifest: name is required")
	}
	if strings.TrimSpace(a.Version) == "" {
		return errors.New("app manifest: version is required")
	}
	// An app must contain something to run or show. Modules are no longer
	// mandatory on their own (schema/APP makes MODULES optional): a pure-UI
	// app — like the conjunction app — declares zero modules and one or more
	// UI pages instead. The historical "at least one module is required"
	// wording is preserved for the no-modules-no-pages case so pre-Pages
	// callers/tests see the same message.
	if len(a.Modules) == 0 && len(a.Pages) == 0 {
		return errors.New("app manifest: at least one module is required (or at least one UI page for a pure-UI app)")
	}

	moduleIDs := make(map[string]bool, len(a.Modules))
	for i, module := range a.Modules {
		id := strings.TrimSpace(module.ID)
		if id == "" {
			return fmt.Errorf("app manifest: modules[%d]: id is required", i)
		}
		if moduleIDs[id] {
			return fmt.Errorf("app manifest: modules[%d]: duplicate module id %q", i, id)
		}
		moduleIDs[id] = true
		if strings.TrimSpace(module.PluginID) == "" {
			return fmt.Errorf("app manifest: modules[%d] (%s): pluginId is required", i, id)
		}
	}

	dataIDs := make(map[string]bool, len(a.Data))
	for i, data := range a.Data {
		id := strings.TrimSpace(data.ID)
		if id == "" {
			return fmt.Errorf("app manifest: data[%d]: id is required", i)
		}
		if dataIDs[id] {
			return fmt.Errorf("app manifest: data[%d]: duplicate data id %q", i, id)
		}
		dataIDs[id] = true
		if strings.TrimSpace(data.SDSType) == "" {
			return fmt.Errorf("app manifest: data[%d] (%s): sdsType is required", i, id)
		}
		if data.ModuleID != "" && !moduleIDs[data.ModuleID] {
			return fmt.Errorf("app manifest: data[%d] (%s): moduleId %q does not match any modules[].id", i, id, data.ModuleID)
		}
	}

	sourceIDs := make(map[string]bool, len(a.Sources))
	for i, source := range a.Sources {
		id := strings.TrimSpace(source.ID)
		if id == "" {
			return fmt.Errorf("app manifest: sources[%d]: id is required", i)
		}
		if sourceIDs[id] {
			return fmt.Errorf("app manifest: sources[%d]: duplicate source id %q", i, id)
		}
		sourceIDs[id] = true
		if strings.TrimSpace(string(source.Kind)) == "" {
			return fmt.Errorf("app manifest: sources[%d] (%s): kind is required", i, id)
		}
		if strings.TrimSpace(source.Ref) == "" {
			return fmt.Errorf("app manifest: sources[%d] (%s): ref is required", i, id)
		}
		if source.Kind == SourceKindModule && !moduleIDs[source.Ref] {
			return fmt.Errorf("app manifest: sources[%d] (%s): ref %q does not match any modules[].id", i, id, source.Ref)
		}
	}

	flowNames := make(map[string]bool, len(a.Dataflow))
	for i, flow := range a.Dataflow {
		name := strings.TrimSpace(flow.Name)
		if name == "" {
			return fmt.Errorf("app manifest: dataflow[%d]: name is required", i)
		}
		if flowNames[name] {
			return fmt.Errorf("app manifest: dataflow[%d]: duplicate flow name %q", i, name)
		}
		flowNames[name] = true
		if strings.TrimSpace(flow.SDSSchema) == "" {
			return fmt.Errorf("app manifest: dataflow[%d] (%s): sdsSchema is required", i, name)
		}
		if !flow.Direction.valid() {
			return fmt.Errorf("app manifest: dataflow[%d] (%s): unknown direction %q", i, name, flow.Direction)
		}
		if !flow.Transport.valid() {
			return fmt.Errorf("app manifest: dataflow[%d] (%s): unknown transport %q", i, name, flow.Transport)
		}
		if !flow.ContentEncoding.valid() {
			return fmt.Errorf("app manifest: dataflow[%d] (%s): unknown content encoding %q", i, name, flow.ContentEncoding)
		}
		// Referential integrity (schema/APP rule): every MODULE_ID here must
		// resolve into MODULES.
		if flow.ModuleID != "" && !moduleIDs[flow.ModuleID] {
			return fmt.Errorf("app manifest: dataflow[%d] (%s): moduleId %q does not match any modules[].id", i, name, flow.ModuleID)
		}
	}

	if a.UI != nil {
		if strings.TrimSpace(a.UI.ModuleID) == "" {
			return errors.New("app manifest: ui.moduleId is required when ui is present")
		}
		if !moduleIDs[a.UI.ModuleID] {
			return fmt.Errorf("app manifest: ui.moduleId %q does not match any modules[].id", a.UI.ModuleID)
		}
		if strings.TrimSpace(a.UI.URL) == "" {
			return errors.New("app manifest: ui.url is required when ui is present")
		}
	}

	if len(a.Pages) > 0 {
		pageIDs := make(map[string]bool, len(a.Pages))
		entryCount := 0
		for i, page := range a.Pages {
			id := strings.TrimSpace(page.ID)
			if id == "" {
				return fmt.Errorf("app manifest: pages[%d]: id is required", i)
			}
			if pageIDs[id] {
				return fmt.Errorf("app manifest: pages[%d]: duplicate page id %q", i, id)
			}
			pageIDs[id] = true

			inline := page.IsInline()
			moduleServed := strings.TrimSpace(page.ModuleID) != "" || strings.TrimSpace(page.URL) != ""
			// Exactly-one-of: inline Content XOR module-served (ModuleID+URL).
			if inline == moduleServed {
				return fmt.Errorf("app manifest: pages[%d] (%s): exactly one of inline content or moduleId+url must be set", i, id)
			}

			if inline {
				if !page.Encoding.valid() {
					return fmt.Errorf("app manifest: pages[%d] (%s): unknown content encoding %q", i, id, page.Encoding)
				}
				decoded, err := page.Encoding.decodeContent(page.Content)
				if err != nil {
					return fmt.Errorf("app manifest: pages[%d] (%s): %w", i, id, err)
				}
				if strings.TrimSpace(page.ContentSHA256) == "" {
					return fmt.Errorf("app manifest: pages[%d] (%s): contentSha256 is required for an inline page", i, id)
				}
				if want, got := strings.ToLower(strings.TrimSpace(page.ContentSHA256)), sha256Hex(decoded); want != got {
					return fmt.Errorf("app manifest: pages[%d] (%s): contentSha256 mismatch: declares %s, decoded content hashes to %s", i, id, want, got)
				}
			} else {
				if strings.TrimSpace(page.ModuleID) == "" {
					return fmt.Errorf("app manifest: pages[%d] (%s): moduleId is required for a module-served page", i, id)
				}
				if strings.TrimSpace(page.URL) == "" {
					return fmt.Errorf("app manifest: pages[%d] (%s): url is required for a module-served page", i, id)
				}
				if !moduleIDs[page.ModuleID] {
					return fmt.Errorf("app manifest: pages[%d] (%s): moduleId %q does not match any modules[].id", i, id, page.ModuleID)
				}
			}

			if page.Entry {
				entryCount++
			}
		}
		if entryCount != 1 {
			return fmt.Errorf("app manifest: exactly one UI page must be marked entry (found %d)", entryCount)
		}
	}

	return nil
}

// ResolvedData pairs a DataRef with its owning ModuleRef, when
// DataRef.ModuleID is set.
type ResolvedData struct {
	DataRef
	Module *ModuleRef
}

// ResolvedSource pairs a SourceRef with its owning ModuleRef, when
// Kind == SourceKindModule.
type ResolvedSource struct {
	SourceRef
	Module *ModuleRef
}

// ResolvedUI is the launcher-ready view of an app's UI entry: the member
// module that serves it plus a plugins.UIDescriptor with title/description
// defaulted from the app itself wherever the UI entry left them blank.
type ResolvedUI struct {
	UIEntry
	Module     ModuleRef
	Descriptor plugins.UIDescriptor
}

// Resolution is the fully cross-referenced, launcher-facing view of an
// AppManifest (see H1's task 3 — resolution helpers for the H2 launcher).
// Every Data/Source/UI reference to a member module has already been
// looked up, so callers never need to re-implement ID lookups.
type Resolution struct {
	Manifest   AppManifest
	Modules    []ModuleRef
	ModuleByID map[string]ModuleRef
	Data       []ResolvedData
	Sources    []ResolvedSource
	// UI is nil when the manifest declares no legacy UI entry (a headless app
	// or a Pages-based app), exactly mirroring a module with no UIProvider.
	UI *ResolvedUI
	// Pages is a copy of the manifest's UI page list (nil when none).
	Pages []UIPage
	// EntryPage points at the one Pages entry marked Entry (nil when Pages is
	// empty). Validate has already guaranteed exactly one when Pages is
	// non-empty, so a non-empty Pages always yields a non-nil EntryPage.
	EntryPage *UIPage
}

// Resolve validates the manifest and cross-references every Modules[],
// Data[], Sources[], and UI reference into concrete lookups a launcher can
// consume directly, without re-walking AppManifest itself.
func (a *AppManifest) Resolve() (*Resolution, error) {
	if a == nil {
		return nil, errors.New("app manifest is nil")
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}

	moduleByID := make(map[string]ModuleRef, len(a.Modules))
	for _, module := range a.Modules {
		moduleByID[module.ID] = module
	}

	resolvedData := make([]ResolvedData, 0, len(a.Data))
	for _, data := range a.Data {
		entry := ResolvedData{DataRef: data}
		if data.ModuleID != "" {
			if module, ok := moduleByID[data.ModuleID]; ok {
				m := module
				entry.Module = &m
			}
		}
		resolvedData = append(resolvedData, entry)
	}

	resolvedSources := make([]ResolvedSource, 0, len(a.Sources))
	for _, source := range a.Sources {
		entry := ResolvedSource{SourceRef: source}
		if source.Kind == SourceKindModule {
			if module, ok := moduleByID[source.Ref]; ok {
				m := module
				entry.Module = &m
			}
		}
		resolvedSources = append(resolvedSources, entry)
	}

	resolution := &Resolution{
		Manifest:   *a,
		Modules:    append([]ModuleRef(nil), a.Modules...),
		ModuleByID: moduleByID,
		Data:       resolvedData,
		Sources:    resolvedSources,
	}

	if len(a.Pages) > 0 {
		resolution.Pages = append([]UIPage(nil), a.Pages...)
		for i := range resolution.Pages {
			if resolution.Pages[i].Entry {
				resolution.EntryPage = &resolution.Pages[i]
				break
			}
		}
	}

	if a.UI != nil {
		// Validate already confirmed a.UI.ModuleID resolves.
		module := moduleByID[a.UI.ModuleID]
		title := a.UI.Title
		if title == "" {
			title = a.Name
		}
		description := a.UI.Description
		if description == "" {
			description = a.Description
		}
		resolution.UI = &ResolvedUI{
			UIEntry: *a.UI,
			Module:  module,
			Descriptor: plugins.UIDescriptor{
				Title:       title,
				Description: description,
				Icon:        a.UI.Icon,
				Color:       a.UI.Color,
				TextColor:   a.UI.TextColor,
				URL:         a.UI.URL,
			},
		}
	}

	return resolution, nil
}

// Module returns the ModuleRef with the given app-local ID, if present.
func (a *AppManifest) Module(id string) (ModuleRef, bool) {
	if a == nil {
		return ModuleRef{}, false
	}
	for _, module := range a.Modules {
		if module.ID == id {
			return module, true
		}
	}
	return ModuleRef{}, false
}
