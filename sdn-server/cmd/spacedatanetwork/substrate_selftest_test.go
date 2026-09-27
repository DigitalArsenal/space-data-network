package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The command prints a parseable report; on the release build (patched static
// prefix, SDN_WASM_REQUIRE_PATCHED=1) --require-patched must succeed.
func TestSubstrateSelftestCommand(t *testing.T) {
	requireCommand(t, []string{"substrate-selftest"}, "substrate-selftest")
	var out bytes.Buffer
	substrateSelftestCmd.SetOut(&out)
	defer substrateSelftestCmd.SetOut(nil)
	substrateRequirePatched = os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1"
	defer func() { substrateRequirePatched = false }()
	if err := runSubstrateSelftest(substrateSelftestCmd, nil); err != nil {
		t.Fatal(err)
	}
	var rep substrateSelftestReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("report %q: %v", out.String(), err)
	}
	if rep.RuntimeVersion == "" || rep.ThreadsSpawned != 4 {
		t.Fatalf("report %+v", rep)
	}
	t.Logf("%s", out.String())
}
