package node

import (
	"context"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestLoadFlatcConverterLoadsEmbeddedArtifact(t *testing.T) {
	n := &Node{ctx: context.Background()}
	fm := n.loadFlatcConverter(wasm.NewEmbeddedFlatcModule)
	if fm == nil {
		t.Fatalf("embedded converter did not load: %+v", n.ModuleLoadFailures())
	}
	defer fm.Close(context.Background())
	if failures := n.ModuleLoadFailures(); len(failures) != 0 {
		t.Fatalf("load failures recorded on success: %+v", failures)
	}
}

// A converter that does not load is a boot-check failure the node reports,
// not a nil it quietly runs without.
func TestLoadFlatcConverterFailureIsRecorded(t *testing.T) {
	n := &Node{ctx: context.Background()}
	fm := n.loadFlatcConverter(func(ctx context.Context) (*wasm.FlatcModule, error) {
		return wasm.NewFlatcModuleFromBytes(ctx, []byte("not a wasm module"))
	})
	if fm != nil {
		t.Fatal("a failed load returned a converter")
	}
	failures := n.ModuleLoadFailures()
	if len(failures) != 1 {
		t.Fatalf("recorded %d failures, want 1: %+v", len(failures), failures)
	}
	f := failures[0]
	if f.Stage != "flatc-converter" || !strings.Contains(f.Ref, wasm.FlatcWasiPackage) || !strings.Contains(f.Error, "instantiate") {
		t.Fatalf("failure record = %+v", f)
	}
}
