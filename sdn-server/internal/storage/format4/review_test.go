package format4

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

func TestEmbeddedLayoutStampMatchesCapability(t *testing.T) {
	ctx := context.Background()
	e, err := Open(ctx, Options{DataRoot: t.TempDir(), Create: CreateFresh, Wasm: flatsqlrt.P4ThreadsWasm(), CompileOnMiss: true, AOTCacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(ctx)
	layout := 1
	if probe, ok := e.ctl.(interface{ HasFunction(string) bool }); ok && probe.HasFunction("flatsql_capabilities") {
		bits, err := e.ctl.ControlCall("flatsql_capabilities")
		if err != nil || bits < 0 {
			t.Fatalf("capabilities: %d %v", bits, err)
		}
		if bits&1 != 0 {
			layout = 2
		}
	}
	if versioninfo.MaxStoreLayout != layout {
		t.Fatalf("stamp %d, embedded capability requires %d", versioninfo.MaxStoreLayout, layout)
	}
}

func TestMigrationCreatesEngineRoot(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "absent", "target")
	wasm := flatsqlrt.P4ThreadsWasm()
	if path := os.Getenv("SDN_P4_WASM"); path != "" {
		var err error
		wasm, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	e, err := Open(ctx, Options{DataRoot: root, Create: CreateForMigration, Wasm: wasm, CompileOnMiss: true, AOTCacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterType(CIDOnlyTypeSpec("OPAQUE", [4]byte{'$', 'O', 'P', 'Q'})); err != nil {
		e.Close(ctx)
		t.Fatal(err)
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "fsql4")); err != nil || !info.IsDir() {
		t.Fatalf("engine root: %v %v", info, err)
	}
}
