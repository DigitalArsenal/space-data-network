//go:build stress
// +build stress

package stress

// PARTITION STORE (format 2) FIXTURE. The host-02-shaped legacy store that
// store-migrate is measured against (design §18: "the sdn-flatsql-stress
// fixture, re-scaled to the store-migrate --inventory of a host-02 copy").
// It is the production-shape store above, built through the daemon's own
// write path, kept as a template so every measurement clones it.
//
//	STRESS_SHAPE_TEMPLATE=<dir> STRESS_SHAPE_SCALE=<s> go test -tags stress \
//	  -run TestPartitionStoreFixture ./internal/stress/
//
// leaves <dir>/store populated (resumable: a rerun continues), and prints
// the shape it holds.

import (
	"os"
	"strings"
	"testing"
)

func TestPartitionStoreFixture(t *testing.T) {
	if strings.TrimSpace(os.Getenv("STRESS_SHAPE_TEMPLATE")) == "" {
		t.Skip("STRESS_SHAPE_TEMPLATE names the fixture directory")
	}
	parts := ScaleShape(Host02Shape(shapeEnvFloat("STRESS_SHAPE_IQC_MIRROR_FRACTION", 1)), shapeEnvFloat("STRESS_SHAPE_SCALE", 1))
	dir, stats := prepareShapeStore(t, t.TempDir(), parts)
	t.Logf("fixture %s: %d records, %d bytes, built in %s", dir, stats.Records, stats.Bytes, stats.Duration)
	for _, p := range parts {
		t.Logf("  %s x %d (%d B mean)", p.Lane(), p.Records, p.RecordBytes)
	}
}
