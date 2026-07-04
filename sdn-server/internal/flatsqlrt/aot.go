package flatsqlrt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	return EnsureAOTArtifact(cacheDir, "flatsql", wasm)
}

// EnsureAOTArtifact AOT-compiles arbitrary portable wasm bytes through the
// same sha256-keyed disk cache the engine uses (one
// <prefix>-<hash>-we<runtime-version>.aot.wasm per artifact; stale entries
// under the SAME prefix are pruned, other prefixes in the directory are left
// alone). The cache key includes the libwasmedge RUNTIME version (loop C.9):
// native code compiled by one runtime must never load into another — a
// machine-wide cache shared by daemons built against 0.14.0 and 0.16.4
// would otherwise serve stale artifacts across the upgrade. Callers hosting
// non-engine modules (e.g. flow HTTP mounts) share the cache directory
// safely by picking a distinct prefix.
func EnsureAOTArtifact(cacheDir, prefix string, wasm []byte) ([]byte, error) {
	sum := sha256.Sum256(wasm)
	ver := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '.' {
			return r
		}
		return '_'
	}, RuntimeVersion())
	path := filepath.Join(cacheDir, fmt.Sprintf("%s-%s-we%s.aot.wasm", prefix, hex.EncodeToString(sum[:8]), ver))

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

	// Unique temp name per process: concurrent compilers (parallel test
	// binaries, multiple daemons sharing the machine-wide cache) must never
	// write the same temp file — the final rename is atomic, so readers only
	// ever observe complete artifacts.
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := compiler.CompileBuffer(wasm, tmp); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("flatsqlrt: AOT compile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("flatsqlrt: AOT cache rename: %w", err)
	}

	// Prune artifacts compiled from older bytes of the SAME module (prefix) —
	// stale entries would otherwise accumulate across upgrades. Other
	// prefixes sharing the directory are untouched.
	if entries, err := os.ReadDir(cacheDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if name != filepath.Base(path) &&
				strings.HasPrefix(name, prefix+"-") && strings.HasSuffix(name, ".aot.wasm") {
				os.Remove(filepath.Join(cacheDir, name))
			}
		}
	}
	return os.ReadFile(path)
}
