package flowrt

// palette.go is the flow editor's LOCAL node-type source (Phase 1): the set of
// draggable node types a flow can be composed from on THIS node, with no network
// discovery. It has two parts:
//
//   - capabilities — the host capability nodes the node always provides (IPFS,
//     FlatSQL), with their declared input/output ports. These dispatch through
//     the host, not a baked guest-link object.
//   - modules — every guest-link module the node has STAGED for baking (under the
//     flowcc home's modules/ dir). These are the linked-direct node types a bake
//     actually composes; each module's methods come from its staged metadata.json
//     (the authoritative (method -> entry symbol) map the bake resolves against),
//     so a method that appears here is one the node can bake.
//
// Phase 2 (network module discovery + schema-typed ports) replaces/augments the
// `modules` source with content-addressed modules advertised across the SDN
// network and carries each port's SDS schema so the editor can reject
// incompatible wires. The editor consumes this single GET /api/v1/flows/palette
// endpoint regardless of where the node types come from, so that swap is
// server-side only.

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
)

// PaletteMethod is one draggable node type: a module/capability method plus the
// port ids a wire can attach to. In Phase 1 a staged module's method carries the
// generic linked-direct ports ("in"/"out"); a capability method carries the
// ports declared in its descriptor. Phase 2 adds an SDS schema per port here.
type PaletteMethod struct {
	MethodID    string   `json:"methodId"`
	Description string   `json:"description,omitempty"`
	InputPorts  []string `json:"inputPorts"`
	OutputPorts []string `json:"outputPorts"`
}

// PaletteModule is one locally-available node source and its methods. Kind is
// "module" for a staged guest-link module (linked-direct, bakeable) or
// "capability" for a host capability node.
type PaletteModule struct {
	PluginID     string          `json:"pluginId"`
	Name         string          `json:"name,omitempty"`
	PluginFamily string          `json:"pluginFamily,omitempty"`
	Kind         string          `json:"kind"`
	Methods      []PaletteMethod `json:"methods"`
}

// Palette is the editor's node-type catalog for this node.
type Palette struct {
	Capabilities []PaletteModule `json:"capabilities"`
	Modules      []PaletteModule `json:"modules"`
}

// StagedModule is one guest-link module staged for baking, with the method ids
// its metadata.json exports an entry symbol for.
type StagedModule struct {
	PluginID string
	Methods  []string
}

// StagedModules enumerates the guest-link modules staged under the baker's
// flowcc home. Each staged module directory is keyed by its (path-sanitized)
// pluginId; for the dotted java-style pluginIds SDN modules use, the directory
// name is the pluginId verbatim. The methods come from the staged metadata.json
// — the same map the bake resolves entry symbols from — so every method listed
// here is one this node can bake. A missing modules/ dir (no modules staged)
// returns an empty slice, not an error.
func (b *Baker) StagedModules() ([]StagedModule, error) {
	entries, err := os.ReadDir(b.home.ModulesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]StagedModule, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginID := e.Name()
		meta, err := b.home.LoadModuleMetadata(pluginID)
		if err != nil {
			continue // a dir without a readable metadata.json is not a staged module
		}
		methods := make([]string, 0, len(meta.MethodSymbols))
		for m := range meta.MethodSymbols {
			methods = append(methods, m)
		}
		sort.Strings(methods)
		out = append(out, StagedModule{PluginID: pluginID, Methods: methods})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out, nil
}

// handlePalette serves GET /api/v1/flows/palette: the node's local node-type
// catalog. Host capabilities are always present; staged guest-link modules are
// present only when a baker is attached (a node with the flowcc toolchain
// staged). A node with no toolchain still returns the capability palette so the
// editor renders — Deploy on such a node returns the bake path's clean 501.
func handlePalette(w http.ResponseWriter, r *http.Request, mgr *FlowManager) {
	pal := Palette{
		Capabilities: capabilityPalette(),
		Modules:      []PaletteModule{},
	}
	if b := mgr.Baker(); b != nil {
		if staged, err := b.StagedModules(); err == nil {
			for _, sm := range staged {
				methods := make([]PaletteMethod, 0, len(sm.Methods))
				for _, m := range sm.Methods {
					methods = append(methods, PaletteMethod{
						MethodID:    m,
						InputPorts:  []string{"in"},
						OutputPorts: []string{"out"},
					})
				}
				pal.Modules = append(pal.Modules, PaletteModule{
					PluginID: sm.PluginID,
					Kind:     "module",
					Methods:  methods,
				})
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pal)
}

// capabilityPalette adapts the host capability descriptors into palette modules.
// It reuses AvailableCapabilities (the same descriptors /api/v1/flows/capabilities
// serves) so the palette and the capability endpoint never drift.
func capabilityPalette() []PaletteModule {
	var out []PaletteModule
	for _, raw := range AvailableCapabilities() {
		var d struct {
			PluginID     string `json:"pluginId"`
			Name         string `json:"name"`
			PluginFamily string `json:"pluginFamily"`
			Methods      []struct {
				MethodID    string   `json:"methodId"`
				Description string   `json:"description"`
				InputPorts  []string `json:"inputPorts"`
				OutputPorts []string `json:"outputPorts"`
				TriggerKind string   `json:"triggerKind"`
			} `json:"methods"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		mod := PaletteModule{
			PluginID:     d.PluginID,
			Name:         d.Name,
			PluginFamily: d.PluginFamily,
			Kind:         "capability",
		}
		for _, m := range d.Methods {
			in := m.InputPorts
			if in == nil {
				in = []string{}
			}
			outp := m.OutputPorts
			if outp == nil {
				outp = []string{}
			}
			mod.Methods = append(mod.Methods, PaletteMethod{
				MethodID:    m.MethodID,
				Description: m.Description,
				InputPorts:  in,
				OutputPorts: outp,
			})
		}
		out = append(out, mod)
	}
	return out
}
