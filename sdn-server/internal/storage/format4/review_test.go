package format4

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// reviewAOT is one compiled-engine cache folder for this test process,
// removed by TestMain: the engine compiles once for the tests here, and
// nothing is read from or left in the user's cache. The artifact's file name
// carries the engine's sha256 and the runtime's compile settings
// (flatsqlrt.ThreadedAOTPath), so two engines never share an entry.
var reviewAOT struct {
	once sync.Once
	dir  string
	err  error
}

func reviewAOTDir(t testing.TB) string {
	t.Helper()
	reviewAOT.once.Do(func() { reviewAOT.dir, reviewAOT.err = os.MkdirTemp("", "sdn-format4-review-aot-") })
	if reviewAOT.err != nil {
		t.Fatal(reviewAOT.err)
	}
	return reviewAOT.dir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if reviewAOT.dir != "" {
		os.RemoveAll(reviewAOT.dir)
	}
	os.Exit(code)
}

func TestEmbeddedLayoutStampMatchesCapability(t *testing.T) {
	ctx := context.Background()
	e, err := Open(ctx, Options{DataRoot: t.TempDir(), Create: CreateFresh, Wasm: flatsqlrt.P4ThreadsWasm(), CompileOnMiss: true, AOTCacheDir: reviewAOTDir(t)})
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
	// The node uses its CID-only fallback for the encrypted embedded standard.
	spec := CIDOnlyTypeSpec("KMF", [4]byte{'$', 'K', 'M', 'F'})
	if err := e.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	for _, sandbox := range []bool{false, true} {
		for _, projection := range []string{"COUNT(*)", "_cid", "_data", "*"} {
			_, err := e.SQL(ctx, SQLRequest{SQL: "SELECT " + projection + " FROM KMF", Sandbox: sandbox}, func([]byte) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "no such table") {
				t.Fatalf("shipped sealed SQL: %v", err)
			}
		}
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
	e, err := Open(ctx, Options{DataRoot: root, Create: CreateForMigration, Wasm: wasm, CompileOnMiss: true, AOTCacheDir: reviewAOTDir(t)})
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
	if info, err := os.Stat(filepath.Join(root, marker.Dir)); err != nil || !info.IsDir() {
		t.Fatalf("engine root: %v %v", info, err)
	}
}
