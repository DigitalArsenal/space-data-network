package sdnservices

import (
	"bytes"
	"context"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

type blockingOpaqueGetDatastore struct {
	ds.Datastore
	mu      stdsync.Mutex
	armed   bool
	blocked bool
	entered chan struct{}
	release chan struct{}
}

func (store *blockingOpaqueGetDatastore) Get(ctx context.Context, key ds.Key) ([]byte, error) {
	value, err := store.Datastore.Get(ctx, key)
	store.mu.Lock()
	shouldBlock := store.armed && !store.blocked
	if shouldBlock {
		store.blocked = true
		close(store.entered)
	}
	store.mu.Unlock()
	if shouldBlock {
		select {
		case <-store.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return value, err
}

func TestOpaqueStateRoundTripsAppendsReplacesAndReloadsBinaryBytes(t *testing.T) {
	ctx := context.Background()
	backing := sync.MutexWrap(ds.NewMapDatastore())
	state, err := NewOpaqueStateStore(backing)
	if err != nil {
		t.Fatalf("NewOpaqueStateStore() error = %v", err)
	}
	scope := OpaqueStateScope{ArtifactHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NodeID: "node-a", Namespace: "primary"}

	first := []byte{0, 1, 2, 0, 255}
	second := []byte{9, 0, 8}
	if err := state.Replace(ctx, scope, "snapshot.bin", first); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if err := state.Append(ctx, scope, "snapshot.bin", second); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := state.Sync(ctx, scope); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// A fresh adapter over the same datastore models process restart/reload.
	reloaded, err := NewOpaqueStateStore(backing)
	if err != nil {
		t.Fatalf("NewOpaqueStateStore(reload) error = %v", err)
	}
	got, found, err := reloaded.Read(ctx, scope, "snapshot.bin")
	if err != nil || !found {
		t.Fatalf("Read() = (%x, %v, %v), want found", got, found, err)
	}
	want := append(append([]byte(nil), first...), second...)
	if !bytes.Equal(got, want) {
		t.Fatalf("Read() bytes = %v, want %v", got, want)
	}
	keys, err := reloaded.List(ctx, scope)
	if err != nil || len(keys) != 1 || keys[0] != "snapshot.bin" {
		t.Fatalf("List() = (%v, %v), want [snapshot.bin]", keys, err)
	}
	if err := reloaded.Delete(ctx, scope, "snapshot.bin"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, found, err := reloaded.Read(ctx, scope, "snapshot.bin"); err != nil || found {
		t.Fatalf("Read(after delete) found/error = %v/%v, want false/nil", found, err)
	}
}

func TestBuildServicesWiresOpaqueStateIntoStorageAdapter(t *testing.T) {
	backing := sync.MutexWrap(ds.NewMapDatastore())
	services, err := BuildServices(Deps{
		Blockstore: blockstore.NewBlockstore(backing),
		Datastore:  backing,
		Schemas: sdnstore.SchemaProviderFunc(func(string) (string, string, string, bool) {
			return "", "", "", false
		}),
	})
	if err != nil {
		t.Fatalf("BuildServices() error = %v", err)
	}
	defer services.Close()
	if services.OpaqueState == nil {
		t.Fatal("BuildServices() did not expose the generic opaque state adapter")
	}
	if services.Wakeups == nil {
		t.Fatal("BuildServices() did not expose the generic wakeup broker")
	}
	if _, ok := services.CapReg.Lookup("storage_adapter"); !ok {
		t.Fatal("BuildServices() did not register storage_adapter")
	}
	if _, ok := services.CapReg.Lookup("timers"); !ok {
		t.Fatal("BuildServices() did not register the generic timers adapter")
	}
}

func TestOpaqueStateIsolatesArtifactNodeAndNamespace(t *testing.T) {
	ctx := context.Background()
	state, err := NewOpaqueStateStore(sync.MutexWrap(ds.NewMapDatastore()))
	if err != nil {
		t.Fatal(err)
	}
	base := OpaqueStateScope{ArtifactHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", NodeID: "node-a", Namespace: "primary"}
	otherNode := base
	otherNode.NodeID = "node-b"
	otherNamespace := base
	otherNamespace.Namespace = "secondary"
	for _, item := range []struct {
		scope OpaqueStateScope
		data  []byte
	}{{base, []byte("a")}, {otherNode, []byte("b")}, {otherNamespace, []byte("c")}} {
		if err := state.Replace(ctx, item.scope, "same-key", item.data); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		scope OpaqueStateScope
		want  []byte
	}{{base, []byte("a")}, {otherNode, []byte("b")}, {otherNamespace, []byte("c")}} {
		got, found, err := state.Read(ctx, item.scope, "same-key")
		if err != nil || !found || !bytes.Equal(got, item.want) {
			t.Fatalf("isolated Read(%+v) = (%q,%v,%v), want %q", item.scope, got, found, err, item.want)
		}
	}
}

func TestOpaqueStateRejectsUnsafeSegments(t *testing.T) {
	state, err := NewOpaqueStateStore(sync.MutexWrap(ds.NewMapDatastore()))
	if err != nil {
		t.Fatal(err)
	}
	valid := OpaqueStateScope{ArtifactHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", NodeID: "node-a", Namespace: "primary"}
	for _, mutate := range []func(*OpaqueStateScope){
		func(scope *OpaqueStateScope) { scope.ArtifactHash = "../artifact" },
		func(scope *OpaqueStateScope) { scope.NodeID = "node/a" },
		func(scope *OpaqueStateScope) { scope.Namespace = ".." },
	} {
		scope := valid
		mutate(&scope)
		if err := state.Replace(context.Background(), scope, "key", []byte{1}); err == nil {
			t.Fatalf("Replace(%+v) = nil error, want unsafe-segment rejection", scope)
		}
	}
	if err := state.Replace(context.Background(), valid, "../key", []byte{1}); err == nil {
		t.Fatal("Replace(unsafe key) = nil error, want rejection")
	}
}

func TestOpaqueStateEnforcesValueScopeAndKeyQuotasWithoutMutation(t *testing.T) {
	ctx := t.Context()
	state, err := NewOpaqueStateStore(sync.MutexWrap(ds.NewMapDatastore()))
	if err != nil {
		t.Fatal(err)
	}
	state.maxValueBytes = 8
	state.maxScopeBytes = 10
	state.maxScopeKeys = 2
	scope := OpaqueStateScope{ArtifactHash: strings.Repeat("e", 64), NodeID: "node-a", Namespace: "primary"}

	first := []byte("12345678")
	if err := state.Replace(ctx, scope, "first", first); err != nil {
		t.Fatalf("initial Replace() error = %v", err)
	}
	if err := state.Append(ctx, scope, "first", []byte("9")); err == nil || !strings.Contains(err.Error(), "value quota") {
		t.Fatalf("oversized Append() error = %v, want value quota", err)
	}
	if got, found, err := state.Read(ctx, scope, "first"); err != nil || !found || !bytes.Equal(got, first) {
		t.Fatalf("value after rejected append = (%q,%v,%v)", got, found, err)
	}
	if err := state.Replace(ctx, scope, "second", []byte("abc")); err == nil || !strings.Contains(err.Error(), "scope byte quota") {
		t.Fatalf("scope-overflow Replace() error = %v, want scope quota", err)
	}
	if err := state.Replace(ctx, scope, "second", []byte("ab")); err != nil {
		t.Fatalf("within-scope Replace() error = %v", err)
	}
	if err := state.Replace(ctx, scope, "third", nil); err == nil || !strings.Contains(err.Error(), "key quota") {
		t.Fatalf("third-key Replace() error = %v, want key quota", err)
	}
}

func TestOpaqueStateSerializesAppendWithReplace(t *testing.T) {
	backing := &blockingOpaqueGetDatastore{
		Datastore: sync.MutexWrap(ds.NewMapDatastore()),
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	state, err := NewOpaqueStateStore(backing)
	if err != nil {
		t.Fatal(err)
	}
	scope := OpaqueStateScope{ArtifactHash: strings.Repeat("d", 64), NodeID: "node-a", Namespace: "primary"}
	if err := state.Replace(t.Context(), scope, "snapshot", []byte("old")); err != nil {
		t.Fatal(err)
	}
	backing.mu.Lock()
	backing.armed = true
	backing.mu.Unlock()

	appendDone := make(chan error, 1)
	go func() { appendDone <- state.Append(t.Context(), scope, "snapshot", []byte("+append")) }()
	select {
	case <-backing.entered:
	case <-time.After(time.Second):
		t.Fatal("Append() did not reach controlled read")
	}
	replaceDone := make(chan error, 1)
	go func() { replaceDone <- state.Replace(t.Context(), scope, "snapshot", []byte("replacement")) }()
	earlyReplace := false
	select {
	case err := <-replaceDone:
		if err != nil {
			t.Fatal(err)
		}
		earlyReplace = true
	case <-time.After(25 * time.Millisecond):
	}
	close(backing.release)
	if err := <-appendDone; err != nil {
		t.Fatal(err)
	}
	if !earlyReplace {
		if err := <-replaceDone; err != nil {
			t.Fatal(err)
		}
	}
	if earlyReplace {
		t.Fatal("Replace() ran concurrently with an in-flight Append()")
	}
	got, found, err := state.Read(t.Context(), scope, "snapshot")
	if err != nil || !found || string(got) != "replacement" {
		t.Fatalf("serialized final value = (%q, %v, %v), want replacement", got, found, err)
	}
}
