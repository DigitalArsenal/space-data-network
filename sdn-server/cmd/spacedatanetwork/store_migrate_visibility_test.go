package main

import (
	"context"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"testing"
)

func TestMigrate4RegistrationKeepsBoundedLayout(t *testing.T) {
	requirePSEngine(t)
	root := t.TempDir()
	spec, err := format4.TypeSpecFor("OCM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if spec.UnlimitedVisibility || spec.A18Bound == 0 {
		t.Fatal("migration must require a separate explicit opt-in")
	}
	m := migrator4{root: root, rep: &migrate4Report{}, opt: migrate4Options{CompileOnMiss: true, AOTCacheDir: t.TempDir()},
		schemas: []string{"OCM.fbs"}, specs: map[string]format4.TypeSpec{"OCM.fbs": spec}}
	for _, mode := range []format4.CreateMode{format4.CreateFresh, format4.OpenExisting} {
		if err := m.openTarget(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		if err := m.closeTarget(context.Background()); err != nil {
			t.Fatal(err)
		}
		state, err := marker.Read(root)
		if err != nil || state.Layout != 1 {
			t.Fatalf("migration layout: %+v %v", state, err)
		}
	}
}

func TestMigrate4VerificationOpensPromotedLayout(t *testing.T) {
	root := t.TempDir()
	spec, err := format4.TypeSpecFor("OCM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	m := migrator4{root: root, rep: &migrate4Report{}, opt: migrate4Options{CompileOnMiss: true, AOTCacheDir: t.TempDir(), Wasm: engineWasm(t)},
		schemas: []string{"OCM.fbs"}, specs: map[string]format4.TypeSpec{"OCM.fbs": spec}}
	ctx := context.Background()
	if err := m.openTarget(ctx, format4.CreateFresh); err != nil {
		t.Fatal(err)
	}
	spec.UnlimitedVisibility = true
	if err := m.api.RegisterType(spec); err != nil {
		m.closeTarget(ctx)
		t.Fatal(err)
	}
	if err := m.closeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.openTarget(ctx, format4.OpenExisting); err != nil {
		t.Fatal(err)
	}
	if err := m.closeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := marker.Read(root)
	if err != nil || state.Layout != 2 {
		t.Fatalf("promoted verification: %+v %v", state, err)
	}
}
