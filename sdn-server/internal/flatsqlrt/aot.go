package flatsqlrt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// WithAOTCache enables ahead-of-time native compilation of the engine.
// On first use the embedded portable wasm is compiled with WasmEdge's AOT
// compiler into <dir>/flatsql-<sha256[:16]>.aot.wasm (a universal wasm
// binary, platform-specific native code); subsequent starts load the cached
// artifact. Interpreted execution is ~100x slower for query workloads
// (measured in loop A.3), so production hosts should always set this.
// If compilation fails, New falls back to interpreting the portable bytes
// and Runtime.AOT() reports false.
func WithAOTCache(dir string) Option {
	return func(c *config) { c.aotCacheDir = dir }
}

// AOT reports whether this runtime is executing an AOT-compiled artifact.
func (r *Runtime) AOT() bool { return r.aot }

// ensureAOT returns AOT-compiled bytes for wasm, compiling into cacheDir on
// first use. The cache key is the sha256 of the portable module, so engine
// upgrades recompile automatically.
func ensureAOT(cacheDir string, wasm []byte) ([]byte, error) {
	sum := sha256.Sum256(wasm)
	path := filepath.Join(cacheDir, fmt.Sprintf("flatsql-%s.aot.wasm", hex.EncodeToString(sum[:8])))

	if cached, err := os.ReadFile(path); err == nil && len(cached) > 0 {
		return cached, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("flatsqlrt: create AOT cache dir: %w", err)
	}

	compiler := wasmedge.NewCompiler()
	if compiler == nil {
		return nil, fmt.Errorf("flatsqlrt: WasmEdge AOT compiler unavailable")
	}
	defer compiler.Release()

	tmp := path + ".tmp"
	if err := compiler.CompileBuffer(wasm, tmp); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("flatsqlrt: AOT compile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("flatsqlrt: AOT cache rename: %w", err)
	}
	return os.ReadFile(path)
}
