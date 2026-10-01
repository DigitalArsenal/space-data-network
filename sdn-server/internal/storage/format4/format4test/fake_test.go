package format4test

import (
	"context"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

func TestFakeConformance(t *testing.T) {
	Conformance(t, func(t *testing.T) format4.API {
		f := New()
		t.Cleanup(func() { _ = f.Close(context.Background()) })
		return f
	})
}

func TestFakeOpenMarkers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := Open(ctx, format4.Options{DataRoot: root, Create: format4.OpenExisting}); err == nil {
		t.Fatal("OpenExisting on an empty root succeeded")
	}
	mig, err := Open(ctx, format4.Options{DataRoot: root, Create: format4.CreateForMigration, GseqFloor: 1060922})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := marker.Read(root); m.Format4() {
		t.Fatalf("a migration target wrote markers before activation: %+v", m)
	}
	if err := mig.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := marker.Read(root)
	if err != nil || !m.Activated() || m.GseqFloor != 1060922 || m.MigratedFrom != 1 {
		t.Fatalf("after Activate: %+v %v", m, err)
	}
	if _, err := Open(ctx, format4.Options{DataRoot: root, Create: format4.CreateForMigration}); err == nil {
		t.Fatal("a migration target opened over an activated store")
	}
	if _, err := Open(ctx, format4.Options{DataRoot: root, Create: format4.OpenExisting}); err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	if _, err := Open(ctx, format4.Options{DataRoot: fresh, Create: format4.CreateFresh}); err != nil {
		t.Fatal(err)
	}
	if m, err := marker.Read(fresh); err != nil || !m.Activated() || m.MigratedFrom != 0 || m.GseqFloor != 1 {
		t.Fatalf("fresh markers: %+v %v", m, err)
	}
}

func TestProducerToken(t *testing.T) {
	for in, want := range map[string]string{"": "unattributed", "  ": "unattributed", "12D3KooWAbc": "12D3KooWAbc",
		" a-b.c ": "a_b_c", "é1": "_1"} {
		if got := ProducerToken(in); got != want {
			t.Fatalf("ProducerToken(%q) = %q, want %q", in, got, want)
		}
	}
}
