package flowrt

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// The cellular tile/aggregate lane reads its store through a node-configured
// statement. Its compiled-in fallback names the pre-migration blob layout, so
// a mount that cannot be told the modern statement answers empty on a node
// whose store is full.
func TestMountConfigReachesTheFlowNodeContext(t *testing.T) {
	shared := &modulert.NodeContext{}
	cfg := map[string]interface{}{
		"cell_cache_sql": "SELECT _data FROM TBS ORDER BY _rowid DESC LIMIT ?",
	}

	got := mountNodeContext(shared, cfg)
	if got == nil {
		t.Fatal("a configured mount must get a node context")
	}
	if got.Config["cell_cache_sql"] != cfg["cell_cache_sql"] {
		t.Fatalf("cell_cache_sql = %v, want the configured statement", got.Config["cell_cache_sql"])
	}
}

// One mount's config must never leak into the shared context, or into
// another mount that declared none.
func TestMountConfigDoesNotLeakIntoTheSharedContext(t *testing.T) {
	shared := &modulert.NodeContext{}
	first := mountNodeContext(shared, map[string]interface{}{"cell_cache_sql": "SELECT 1"})
	second := mountNodeContext(shared, map[string]interface{}{"cell_cache_sql": "SELECT 2"})

	if shared.Config != nil {
		t.Fatalf("the shared node context was mutated: %v", shared.Config)
	}
	if first.Config["cell_cache_sql"] == second.Config["cell_cache_sql"] {
		t.Fatal("two mounts must not share one config map")
	}
	if first == shared || second == shared {
		t.Fatal("a configured mount must get its OWN context, not the shared pointer")
	}
}

// Absence of configuration must stay indistinguishable from the behaviour
// before this existed: the same shared context, untouched.
func TestMountWithoutConfigKeepsTheSharedContextIdentity(t *testing.T) {
	shared := &modulert.NodeContext{}
	if got := mountNodeContext(shared, nil); got != shared {
		t.Fatal("an unconfigured mount must reuse the shared context unchanged")
	}
	if got := mountNodeContext(shared, map[string]interface{}{}); got != shared {
		t.Fatal("an EMPTY config block is not configuration")
	}
	if got := mountNodeContext(nil, nil); got != nil {
		t.Fatalf("a nil shared context stays nil, got %#v", got)
	}
}

// A nil shared context with real config still yields a usable one, so a mount
// is configurable on a node that has no node-wide context at all.
func TestMountConfigWorksWithoutASharedContext(t *testing.T) {
	got := mountNodeContext(nil, map[string]interface{}{"cell_tile_sql": "SELECT _data FROM TBS"})
	if got == nil || got.Config["cell_tile_sql"] != "SELECT _data FROM TBS" {
		t.Fatalf("got %#v, want a context carrying the configured statement", got)
	}
}
