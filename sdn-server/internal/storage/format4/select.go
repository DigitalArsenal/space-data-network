package format4

import (
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// FormatEnv is the environment switch for the store format. Unset (or
// empty) is the default, format 4; "1" is format 1, the opt-out; "2" is
// format 2; "4" or "sqlite" (any case) is format 4. Under the default, a
// store an earlier format holds stays on it until it is migrated (storage's
// NewFlatSQLStore; store-migrate --to 4 migrates a format-1 store).
const FormatEnv = "SDN_STORE_FORMAT"

// Requested is the store format FormatEnv names: 0 when it is unset (the
// default), else 1, 2 or 4. Any other value is an error.
func Requested() (int, error) {
	raw := os.Getenv(FormatEnv)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return 0, nil
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	case "4", "sqlite":
		return 4, nil
	}
	return 0, fmt.Errorf("%s=%q: the store formats are 1, 2 and 4 (alias sqlite); unset is format 4", FormatEnv, raw)
}

// Runnable reports whether this process can run format 4, and why not: the
// engine is embedded, the native host I/O module is built for this platform,
// and the linked WasmEdge is the SDN-patched runtime (the engine's threads
// lose wake-ups on the upstream one; the release and the image link the
// patched static prefix).
func Runnable() (bool, string) {
	if len(flatsqlrt.P4ThreadsWasm()) == 0 {
		return false, "no format-4 engine is embedded in this build"
	}
	if !flatsqlrt.NativeHostIOSupported() {
		return false, "the native host I/O module is not built for this platform"
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		return false, fmt.Sprintf("the linked WasmEdge %s lacks the SDN runtime patches", rep.RuntimeVersion)
	}
	return true, ""
}

// ReadMarkers reads what dataRoot says about format 4 (= marker.Read).
func ReadMarkers(dataRoot string) (marker.Markers, error) { return marker.Read(dataRoot) }

// TypeOf is the engine's type name of an SDS schema: "OMM.fbs" -> "OMM"
// (sds.SchemaNameToTable).
func TypeOf(schemaName string) (string, error) { return sds.SchemaNameToTable(schemaName) }
