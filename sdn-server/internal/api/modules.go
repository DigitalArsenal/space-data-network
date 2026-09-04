package api

// Installed-module $PMM projection for the node-first dashboard. $PMM has no
// runtime-state field, so each PMMModuleEntry.DESCRIPTION begins with the
// machine-readable text "runtime-state=<state>". ENTRY_STATE keeps its PMM
// marketplace meaning and is therefore not overloaded with process state.

import (
	"net/http"
	"sort"
	"strings"
	"time"

	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/cct"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

const (
	ModulesPath       = "/api/v1/modules"
	ModulesSchemaName = "PMM.fbs"
)

// ModulesHandler serves a live runtime snapshot as one size-prefixed $PMM.
type ModulesHandler struct {
	snapshot func() plugins.RuntimeSnapshot
}

// NewModulesHandler constructs the installed-module lane.
func NewModulesHandler(snapshot func() plugins.RuntimeSnapshot) *ModulesHandler {
	return &ModulesHandler{snapshot: snapshot}
}

// ServeHTTP writes the current installed-module manifest.
func (h *ModulesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read installed modules.", 0)
		return
	}
	snapshot := plugins.RuntimeSnapshot{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Modules: []plugins.RuntimeModuleEntry{}}
	if h != nil && h.snapshot != nil {
		snapshot = h.snapshot()
	}
	frame := BuildInstalledModulesFrame(snapshot)
	WriteFrameStream(w, http.StatusOK, [][]byte{frame}, map[string]string{StreamSchemaHeader: ModulesSchemaName})
}

// BuildInstalledModulesFrame maps the JSON runtime read model onto the fields
// $PMM already owns: name, version and family on each entry, with runtime state
// in DESCRIPTION because the pinned schema has no runtime-state field.
func BuildInstalledModulesFrame(snapshot plugins.RuntimeSnapshot) []byte {
	modules := append([]plugins.RuntimeModuleEntry(nil), snapshot.Modules...)
	sort.SliceStable(modules, func(i, j int) bool { return modules[i].ID < modules[j].ID })
	stamp := strings.TrimSpace(snapshot.GeneratedAt)
	if stamp == "" {
		stamp = time.Now().UTC().Format(time.RFC3339)
	}

	b := flatbuffers.NewBuilder(2048)
	offsets := make([]flatbuffers.UOffsetT, 0, len(modules))
	for i := range modules {
		module := &modules[i]
		moduleID := strings.TrimSpace(module.ID)
		pluginID := moduleID
		name := ""
		family := ""
		if module.Manifest != nil {
			if value := strings.TrimSpace(module.Manifest.PluginID); value != "" {
				pluginID = value
			}
			name = strings.TrimSpace(module.Manifest.Name)
			family = strings.TrimSpace(module.Manifest.PluginFamily)
		}
		if family == "" && module.Catalog != nil {
			family = strings.TrimSpace(module.Catalog.PluginType)
		}
		if name == "" && module.UI != nil {
			name = strings.TrimSpace(module.UI.Title)
		}
		if name == "" {
			name = moduleID
		}
		version := strings.TrimSpace(module.Version)
		if version == "" && module.Manifest != nil {
			version = strings.TrimSpace(module.Manifest.Version)
		}
		state := strings.TrimSpace(module.Status)
		if state == "" {
			state = "unknown"
		}
		description := "runtime-state=" + state
		if message := strings.TrimSpace(module.StatusMessage); message != "" {
			description += "; " + message
		}

		moduleIDOffset := b.CreateString(moduleID)
		pluginIDOffset := b.CreateString(pluginID)
		nameOffset := b.CreateString(name)
		descriptionOffset := b.CreateString(description)
		versionOffset := b.CreateString(version)
		updatedAtOffset := b.CreateString(stamp)
		pluginTypeName := pmmPluginTypeName(family)

		sdspmm.PMMModuleEntryStart(b)
		sdspmm.PMMModuleEntryAddMODULE_ID(b, moduleIDOffset)
		sdspmm.PMMModuleEntryAddPLUGIN_ID(b, pluginIDOffset)
		sdspmm.PMMModuleEntryAddNAME(b, nameOffset)
		sdspmm.PMMModuleEntryAddDESCRIPTION(b, descriptionOffset)
		sdspmm.PMMModuleEntryAddVERSION(b, versionOffset)
		sdspmm.PMMModuleEntryAddDEFAULT_ENABLED(b, strings.EqualFold(state, "running"))
		sdspmm.PMMModuleEntryAddENTRY_STATE(b, sdspmm.EnumValuespmmEntryState["ACTIVE"])
		sdspmm.PMMModuleEntryAddUPDATED_AT(b, updatedAtOffset)
		sdspmm.PMMModuleEntryAddPLUGIN_TYPE(b, sdspmm.EnumValuespluginCategory[pluginTypeName])
		sdspmm.PMMModuleEntryAddPRIMARY_CATEGORY(b, sdspmm.EnumValuescapabilityClass[cct.FromPluginType(pluginTypeName)])
		offsets = append(offsets, sdspmm.PMMModuleEntryEnd(b))
	}

	sdspmm.PMMStartMODULESVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	modulesVector := b.EndVector(len(offsets))
	description := b.CreateString("Installed module runtime snapshot; each entry DESCRIPTION carries runtime-state=<state>.")
	createdAt := b.CreateString(stamp)
	updatedAt := b.CreateString(stamp)

	sdspmm.PMMStart(b)
	sdspmm.PMMAddDESCRIPTION(b, description)
	sdspmm.PMMAddMODULES(b, modulesVector)
	sdspmm.PMMAddCREATED_AT(b, createdAt)
	sdspmm.PMMAddUPDATED_AT(b, updatedAt)
	root := sdspmm.PMMEnd(b)
	sdspmm.FinishSizePrefixedPMMBuffer(b, root)
	return b.FinishedBytes()
}

func pmmPluginTypeName(family string) string {
	for name := range sdspmm.EnumValuespluginCategory {
		if strings.EqualFold(strings.TrimSpace(family), name) {
			return name
		}
	}
	return "Unspecified"
}
