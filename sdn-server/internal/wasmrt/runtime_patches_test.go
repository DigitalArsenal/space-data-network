package wasmrt

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The release build carries the WasmEdge runtime patches as heredocs inside
// scripts/build-static-wasmedge.sh, because the CI prefix cache key and the
// Dockerfile's static layer are keyed on that file's bytes. The copies in
// testdata are what the regression tests and the stack's isolated development
// build apply. The two must be the same bytes, or the binary that ships is not
// the runtime the tests validated.
func TestStaticBuildCarriesTheRuntimePatches(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "build-static-wasmedge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ fn, file string }{
		{"write_sdn_patch_01_atomic_wait", "wasmedge-0.16.4-atomic-wait.patch"},
		{"write_sdn_patch_02_stop_token", "wasmedge-0.16.4-stop-token.patch"},
		{"write_sdn_patch_03_fault_jmp", "wasmedge-0.16.4-fault-jmp.patch"},
		{"write_sdn_patch_04_atomic_memarg_offset", "wasmedge-0.16.4-atomic-memarg-offset.patch"},
	} {
		re := regexp.MustCompile(`(?s)` + tc.fn + `\(\) \{\n  cat <<'SDN_WASMEDGE_PATCH_EOF'\n(.*?)SDN_WASMEDGE_PATCH_EOF\n\}`)
		m := re.FindSubmatch(script)
		if m == nil {
			t.Fatalf("%s: no heredoc in scripts/build-static-wasmedge.sh", tc.fn)
		}
		want, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if string(m[1]) != string(want) {
			t.Fatalf("%s differs from testdata/%s", tc.fn, tc.file)
		}
	}
}
