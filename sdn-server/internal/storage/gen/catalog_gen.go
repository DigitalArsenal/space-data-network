// Command gen regenerates internal/storage/engine_standard_catalog.go from the
// embedded SDS IDLs.
//
//	go generate ./sdn-server/internal/storage/...
//
// It is a build-time tool on purpose. Parsing 228 IDLs at every daemon boot
// would put a regex pass on the storage open path for a result that only
// changes when the SDS pin moves, and a COMMITTED artifact is reviewable:
// the diff of an SDS bump shows exactly which columns the engine gained.
// TestGeneratedEngineCatalogIsUpToDate re-runs this and byte-compares, so the
// artifact can never drift from the IDLs it was derived from.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

func main() {
	schemaDir := flag.String("schemas", "", "directory holding the embedded .fbs IDLs")
	out := flag.String("out", "", "generated Go file to write")
	flag.Parse()

	if *schemaDir == "" || *out == "" {
		root, err := os.Getwd()
		if err != nil {
			fatal(err)
		}
		if *schemaDir == "" {
			*schemaDir = filepath.Join(root, "..", "sds", "schemas")
		}
		if *out == "" {
			*out = filepath.Join(root, "engine_standard_catalog.go")
		}
	}

	catalog, err := enginecatalog.Build(*schemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, catalog.Render(), 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "engine catalog: %d routed standards, %d not routed -> %s\n",
		len(catalog.Bindings), len(catalog.Skipped), *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "engine catalog generation failed:", err)
	os.Exit(1)
}
