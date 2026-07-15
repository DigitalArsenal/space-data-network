package flatsqlrt

import (
	"strconv"
	"strings"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// RuntimeVersion returns the version string of the libwasmedge the process is
// linked against (e.g. "0.14.0", "0.16.4"). This is the RUNTIME version, not
// the Go binding version — the v0.14.0 binding builds and runs against
// libwasmedge 0.16.x unchanged (verified loop C.9; 0.17.0 removed the
// by-value WasmEdge_Limit C API the binding uses, so it needs a binding
// port).
func RuntimeVersion() string { return wasmedge.GetVersion() }

// RuntimeHasLinkedAOTFix reports whether the linked libwasmedge is free of
// the two 0.14 AOT executor defects worked around in loops C.5b/C.7:
//
//  1. nested AOT-in-AOT execution on one OS thread corrupting the suspended
//     outer frame's per-thread executor state (false "out of bounds memory
//     access" on return — docs/wasmedge-aot-nested-execution.md), and
//  2. AOT-compiled ENGINE-LINKED flows falsely trapping in the linked drain
//     (docs/flatsql-component-linkage.md §8.2).
//
// Both were retested and PROVEN FIXED on libwasmedge 0.16.4 (loop C.9:
// standalone nested-execution matrix green including the AOT-in-AOT
// same-thread case, and SDN_C7_FORCE_LINKED_AOT=1 TestAOTMountRepro passes).
// Versions between 0.14.0 and 0.16.4 were not tested, so the threshold is
// the verified 0.16.4. Callers use this to drop the linked-mount
// force-interpret mitigation; wasmrt.WithDedicatedThread stays regardless
// (it also serializes engine execution and keeps 0.14 builds safe).
func RuntimeHasLinkedAOTFix() bool {
	return runtimeVersionAtLeast(RuntimeVersion(), 0, 16, 4)
}

// runtimeVersionAtLeast parses a leading "MAJOR.MINOR.PATCH" from v
// (suffixes like "-rc.1" ignored) and compares against the given minimum.
// Unparseable versions report false (treat unknown runtimes as unfixed).
func runtimeVersionAtLeast(v string, major, minor, patch int) bool {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 3 {
		return false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		// Strip any non-numeric suffix (e.g. "4-rc" -> "4").
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		if end == 0 {
			return false
		}
		n, err := strconv.Atoi(p[:end])
		if err != nil {
			return false
		}
		nums[i] = n
	}
	want := [3]int{major, minor, patch}
	for i := 0; i < 3; i++ {
		if nums[i] != want[i] {
			return nums[i] > want[i]
		}
	}
	return true
}
