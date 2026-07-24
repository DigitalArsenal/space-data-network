package node

import (
	"encoding/json"
	"errors"
	"testing"
)

// Boot check (task sdn-licensing-module-load): a failed module load must be
// recorded on the node so it stays visible after boot (node-info API), not
// just a log line that scrolls away.
func TestRecordModuleLoadFailureIsRetainedAndVisible(t *testing.T) {
	t.Parallel()

	n := &Node{}
	if failures := n.ModuleLoadFailures(); len(failures) != 0 {
		t.Fatalf("fresh node ModuleLoadFailures() = %+v, want empty", failures)
	}

	n.recordModuleLoadFailure("catalog-load", licensingModuleID,
		errors.New(`module capability policy: module "licensing" requests unapproved sensitive capabilities [wallet_sign]`))
	n.recordModuleLoadFailure("fallback-read", "/opt/spacedatanetwork/wasm/licensing-module.wasm",
		errors.New("read failed"))

	failures := n.ModuleLoadFailures()
	if len(failures) != 2 {
		t.Fatalf("ModuleLoadFailures() = %d entries, want 2: %+v", len(failures), failures)
	}
	if failures[0].Stage != "catalog-load" || failures[0].Ref != licensingModuleID {
		t.Fatalf("failures[0] = %+v, want stage=catalog-load ref=licensing", failures[0])
	}
	if failures[0].At == "" || failures[1].At == "" {
		t.Fatal("failure timestamps must be set")
	}

	// The accessor returns a copy — callers must not be able to mutate the
	// node's ledger through it.
	failures[0].Stage = "mutated"
	if got := n.ModuleLoadFailures()[0].Stage; got != "catalog-load" {
		t.Fatalf("ledger mutated through accessor copy: stage = %q", got)
	}
}

// The node-info API surfaces the ledger as an API-synthesized field, so its
// JSON keys are lowercase by convention (SDS-record keys match IDL
// capitalization; synthesized fields stay lowercase).
func TestModuleLoadFailureJSONKeysAreLowercase(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(ModuleLoadFailure{Stage: "s", Ref: "r", Error: "e", At: "t"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"stage":"s","ref":"r","error":"e","at":"t"}`
	if string(raw) != want {
		t.Fatalf("ModuleLoadFailure JSON = %s, want %s", raw, want)
	}
}
