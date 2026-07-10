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

	"github.com/spacedatanetwork/sdn-server/plugins"
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
	// UI is the app's web UI entry, if it has one. Optional — an app with
	// no UI entry (e.g. a headless data pipeline) leaves this nil, and
	// resolution/UIDescriptor wiring is a no-op for it, exactly like a
	// module with no UI today.
	UI *UIEntry `json:"ui,omitempty"`
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
}

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
	if len(a.Modules) == 0 {
		return errors.New("app manifest: at least one module is required")
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
	// UI is nil when the manifest declares no UI entry (a headless app),
	// exactly mirroring a module with no UIProvider today.
	UI *ResolvedUI
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
