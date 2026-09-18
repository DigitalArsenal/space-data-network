package libp2ptransports_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The interop fixture is built with a PLAIN `go build` by
// sdn-js/src/go-libp2p-interop.test.ts and
// sdn-js/e2e/browser-direct-interop.spec.ts. The moment its dependency graph
// reaches the WasmEdge cgo binding, that build fails with
// "wasmedge/wasmedge.h: No such file or directory" on every machine without
// the WasmEdge headers — which is a CI runner before the install step, and
// which a developer whose shell exports CGO_CFLAGS will never reproduce
// locally. That is exactly how it shipped once.
//
// Asserting the dependency graph catches it on any machine, in milliseconds,
// without needing a WasmEdge-free environment to test in.
func TestInteropFixtureDoesNotDependOnWasmEdge(t *testing.T) {
	for _, pkg := range []string{"../../cmd/js-interop-host", "."} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.Contains(strings.ToLower(dep), "wasmedge") {
				t.Errorf("%s depends on %s; a plain `go build` of the interop fixture then needs WasmEdge headers", pkg, dep)
			}
		}
	}
}
