package format4

import (
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// FormatEnv is the environment switch for the store format: unset is format
// 1, "2" is format 2, "4" or "sqlite" (any case) is format 4.
const FormatEnv = "SDN_STORE_FORMAT"

// Selected reports whether this process runs store format 4.
func Selected() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(FormatEnv)))
	return v == "4" || v == "sqlite"
}

// ReadMarkers reads what dataRoot says about format 4 (= marker.Read).
func ReadMarkers(dataRoot string) (marker.Markers, error) { return marker.Read(dataRoot) }

// TypeOf is the engine's type name of an SDS schema: "OMM.fbs" -> "OMM"
// (sds.SchemaNameToTable).
func TypeOf(schemaName string) (string, error) { return sds.SchemaNameToTable(schemaName) }
