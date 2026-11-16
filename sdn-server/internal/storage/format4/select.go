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
// empty) is the default, format 5; "1" is format 1, the opt-out; "2" is
// format 2; "4", "5" or "sqlite" (any case) select format 5. Under the
// default, format-4 stores convert on first boot; format-1 stores stay on
// format 1 until explicitly migrated with store-migrate --to 5.
const FormatEnv = "SDN_STORE_FORMAT"

// Requested is the store format FormatEnv names: 0 when it is unset (the
// default), else 1, 2 or 5. Any other value is an error.
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
		return 5, nil
	case "5":
		return 5, nil
	}
	return 0, fmt.Errorf("%s=%q: the store formats are 1, 2 and 5 (alias sqlite); unset is format 5", FormatEnv, raw)
}

// Runnable reports whether this process can run format 5, and why not: the
// engine is embedded, the native host I/O module is built for this platform,
// and the linked WasmEdge is the SDN-patched runtime (the engine's threads
// lose wake-ups on the upstream one; the release and the image link the
// patched static prefix).
func Runnable() (bool, string) {
	ok, _, why := RunnableState()
	return ok, why
}

// RunnableState is Runnable, and whether its "no" is only an inconclusive
// runtime self-test (unconfirmed): a busy machine, not a missing patch. The
// next call runs the self-test again.
func RunnableState() (ok, unconfirmed bool, why string) {
	if len(flatsqlrt.P4ThreadsWasm()) == 0 {
		return false, false, "no format-5 engine is embedded in this build"
	}
	if !flatsqlrt.NativeHostIOSupported() {
		return false, false, "the native host I/O module is not built for this platform"
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		return false, rep.Inconclusive, fmt.Sprintf("the linked WasmEdge %s: %s", rep.RuntimeVersion, rep.FailureReason())
	}
	return true, false, ""
}

// ReadMarkers reads what dataRoot says about format 5 (= marker.Read).
func ReadMarkers(dataRoot string) (marker.Markers, error) { return marker.Read(dataRoot) }

// TypeOf is the engine's type name of an SDS schema: "OMM.fbs" -> "OMM"
// (sds.SchemaNameToTable).
func TypeOf(schemaName string) (string, error) { return sds.SchemaNameToTable(schemaName) }
