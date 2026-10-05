package format4

import (
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// FormatEnv is the environment switch for the store format: unset is format
// 1, "1" is format 1, "2" is format 2, "4" or "sqlite" (any case) is format 4.
const FormatEnv = "SDN_STORE_FORMAT"

// Requested is the store format FormatEnv names: 0 when it names none (format
// 1), else 1, 2 or 4.
func Requested() (int, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(FormatEnv))) {
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	case "4", "sqlite":
		return 4, nil
	}
	return 0, nil
}

// ReadMarkers reads what dataRoot says about format 4 (= marker.Read).
func ReadMarkers(dataRoot string) (marker.Markers, error) { return marker.Read(dataRoot) }

// TypeOf is the engine's type name of an SDS schema: "OMM.fbs" -> "OMM"
// (sds.SchemaNameToTable).
func TypeOf(schemaName string) (string, error) { return sds.SchemaNameToTable(schemaName) }
