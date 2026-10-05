package logservice

import (
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// TestMain runs this package's tests on store format 1 unless
// SDN_STORE_FORMAT names a format: they were written against format 1, and
// format 4, the default, has its own suites (SDN_STORE_FORMAT=4 runs these on
// it).
func TestMain(m *testing.M) {
	if _, set := os.LookupEnv(format4.FormatEnv); !set {
		os.Setenv(format4.FormatEnv, "1")
	}
	os.Exit(m.Run())
}
