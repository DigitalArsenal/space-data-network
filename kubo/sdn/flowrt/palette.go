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

	"github.com/ipfs/kubo/sdn/flowcc"
)

// PalettePort is one draggable-node port with its SDS schema type(s). Types are
// the normalized SDS type tokens (e.g. "OMM", "CDM") the port produces (output)
// or accepts (input); Any marks an acceptsAnyFlatbuffer wildcard. A port with
// neither is undeclared and the editor treats it as "any" — it wires to anything.
type PalettePort struct {
	PortID string   `json:"portId"`
	Types  []string `json:"types,omitempty"`
	Any    bool     `json:"any,omitempty"`
}

// PaletteMethod is one draggable node type: a module/capability method plus its
// typed ports. Phase 2 carries each port's SDS type set (Types/Any) so the editor
// can reject a wire whose producer output type is not in the consumer input's
// accepted set. A staged module's ports come from its manifest (via the staged
// metadata's MethodPorts); a capability method's ports are untyped ("any").
type PaletteMethod struct {
	MethodID    string        `json:"methodId"`
	Description string        `json:"description,omitempty"`
	InputPorts  []PalettePort `json:"inputPorts"`
	OutputPorts []PalettePort `json:"outputPorts"`
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

// StagedModule is one guest-link module staged for baking, with the methods its
// metadata.json exports an entry symbol for (each carrying its schema-typed
// ports from the staged metadata's MethodPorts).
type StagedModule struct {
	PluginID string
	Methods  []StagedMethod
}

// StagedMethod is one bakeable method plus its schema-typed ports. Ports are nil
// when the module was staged without a manifest (the editor treats those as
// untyped "any").
type StagedMethod struct {
	MethodID    string
	InputPorts  []flowcc.PortSchema
	OutputPorts []flowcc.PortSchema
}

// StagedModules enumerates the guest-link modules staged under the baker's
// flowcc home. Each staged module directory is keyed by its (path-sanitized)
// pluginId; for the dotted java-style pluginIds SDN modules use, the directory
// name is the pluginId verbatim. The methods come from the staged metadata.json
// — the same map the bake resolves entry symbols from — so every method listed
// here is one this node can bake; each method's typed ports come from the same
// metadata's MethodPorts (the manifest schema captured at stage time). A missing
// modules/ dir (no modules staged) returns an empty slice, not an error.
func (b *Baker) StagedModules() ([]StagedModule, error) {
	return stagedModulesFromHome(b.home)
}

// stagedModulesFromHome is the home-scoped enumerator (Baker-independent so the
// palette can list staged modules without holding a Baker reference).
func stagedModulesFromHome(home flowcc.Home) ([]StagedModule, error) {
	entries, err := os.ReadDir(home.ModulesDir())
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
		meta, err := home.LoadModuleMetadata(pluginID)
		if err != nil {
			continue // a dir without a readable metadata.json is not a staged module
		}
		// Index the manifest-derived port schema by methodId for O(1) lookup.
		portsByMethod := make(map[string]flowcc.MethodPortSchema, len(meta.MethodPorts))
		for _, mp := range meta.MethodPorts {
			portsByMethod[mp.MethodID] = mp
		}
		methods := make([]StagedMethod, 0, len(meta.MethodSymbols))
		for m := range meta.MethodSymbols {
			sm := StagedMethod{MethodID: m}
			if mp, ok := portsByMethod[m]; ok {
				sm.InputPorts = mp.InputPorts
				sm.OutputPorts = mp.OutputPorts
			}
			methods = append(methods, sm)
		}
		sort.Slice(methods, func(i, j int) bool { return methods[i].MethodID < methods[j].MethodID })
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
				pal.Modules = append(pal.Modules, stagedModulePalette(sm))
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pal)
}

// stagedModulePalette adapts one staged module into a palette module with its
// methods' schema-typed ports. A method with no manifest-derived ports falls
// back to the generic linked-direct in/out ports (untyped "any") so the editor
// still renders + wires it.
func stagedModulePalette(sm StagedModule) PaletteModule {
	methods := make([]PaletteMethod, 0, len(sm.Methods))
	for _, m := range sm.Methods {
		in := portSchemaToPalette(m.InputPorts)
		out := portSchemaToPalette(m.OutputPorts)
		if len(in) == 0 {
			in = []PalettePort{{PortID: "in", Any: true}}
		}
		if len(out) == 0 {
			out = []PalettePort{{PortID: "out", Any: true}}
		}
		methods = append(methods, PaletteMethod{
			MethodID:    m.MethodID,
			InputPorts:  in,
			OutputPorts: out,
		})
	}
	return PaletteModule{PluginID: sm.PluginID, Kind: "module", Methods: methods}
}

// portSchemaToPalette maps the staged (flowcc) port schema onto the palette wire
// shape.
func portSchemaToPalette(ports []flowcc.PortSchema) []PalettePort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]PalettePort, 0, len(ports))
	for _, p := range ports {
		out = append(out, PalettePort{PortID: p.PortID, Types: p.Types, Any: p.Any})
	}
	return out
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
			mod.Methods = append(mod.Methods, PaletteMethod{
				MethodID:    m.MethodID,
				Description: m.Description,
				InputPorts:  capabilityPorts(m.InputPorts),
				OutputPorts: capabilityPorts(m.OutputPorts),
			})
		}
		out = append(out, mod)
	}
	return out
}

// capabilityPorts wraps a capability descriptor's string port ids as untyped
// palette ports (Any: true) — host-capability ports carry no SDS schema yet, so
// they accept/produce anything. Always returns a non-nil (possibly empty) slice.
func capabilityPorts(ids []string) []PalettePort {
	out := make([]PalettePort, 0, len(ids))
	for _, id := range ids {
		out = append(out, PalettePort{PortID: id, Any: true})
	}
	return out
}
