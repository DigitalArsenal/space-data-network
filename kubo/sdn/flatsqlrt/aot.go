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

// WithAOTCache enables ahead-of-time native compilation of the engine. This
// option is for tests and explicit prewarm/maintenance paths: on cache miss
// it invokes WasmEdge's AOT compiler. Daemon startup should use
// WithPrecompiledAOTCache so service start never compiles wasm artifacts.
// If compilation fails, New falls back to interpreting the portable bytes
// and Runtime.AOT() reports false.
func WithAOTCache(dir string) Option {
	return func(c *config) {
		c.aotCacheDir = dir
		c.aotCompileOnMiss = true
	}
}

// WithPrecompiledAOTCache loads an existing AOT artifact from dir when it is
// present, but never invokes the compiler. On cache miss New falls back to
// interpreting the portable bytes and Runtime.AOT reports false.
func WithPrecompiledAOTCache(dir string) Option {
	return func(c *config) {
		c.aotCacheDir = dir
		c.aotCompileOnMiss = false
	}
}

// AOT reports whether this runtime is executing an AOT-compiled artifact.
func (r *Runtime) AOT() bool { return r.aot }

// engineAOTPrefix names the engine's AOT artifacts in the shared cache
// (<prefix>-<hash>-we<ver>.aot.wasm). The runtime's on-open load and
// PrewarmEngineAOT MUST agree on it or a prewarmed artifact won't be found.
const engineAOTPrefix = "flatsql"

// ensureAOT returns AOT-compiled bytes for wasm, compiling into cacheDir on
// first use. The cache key is the sha256 of the portable module, so engine
// upgrades recompile automatically.
func ensureAOT(cacheDir string, wasm []byte) ([]byte, error) {
	return EnsureAOTArtifact(cacheDir, engineAOTPrefix, wasm)
}

// PrewarmAOTArtifact AOT-compiles wasm into cacheDir under prefix unless it is
// already cached, returning the artifact path and whether it was already
// present. It is the maintenance/prewarm entry point behind the
// `prewarm-aot` CLI command: production daemons open the engine with
// WithPrecompiledAOTCache and never compile on the service path, so an
// operator runs the prewarm once per host to populate the cache. Idempotent:
// a second call finds the artifact and reports alreadyPresent=true without
// recompiling. A non-nil error (compiler unavailable, compile failure) is
// meant to be surfaced with a non-zero exit.
func PrewarmAOTArtifact(cacheDir, prefix string, wasm []byte) (path string, alreadyPresent bool, err error) {
	path = aotArtifactPath(cacheDir, prefix, wasm)
	if cached, readErr := os.ReadFile(path); readErr == nil && len(cached) > 0 {
		return path, true, nil
	}
	if _, err = EnsureAOTArtifact(cacheDir, prefix, wasm); err != nil {
		return path, false, err
	}
	return path, false, nil
}

// PrewarmEngineAOT AOT-compiles the embedded FlatSQL engine into cacheDir,
// producing the exact "flatsql"-prefixed artifact the daemon loads via
// WithPrecompiledAOTCache (keyed on the embedded engine bytes AND the
// libwasmedge runtime version). See PrewarmAOTArtifact.
func PrewarmEngineAOT(cacheDir string) (path string, alreadyPresent bool, err error) {
	return PrewarmAOTArtifact(cacheDir, engineAOTPrefix, flatsqlWasm)
}

// LoadAOTArtifact returns a precompiled AOT artifact from cacheDir for the
// supplied portable wasm bytes. It never creates directories or invokes the
// WasmEdge compiler.
func LoadAOTArtifact(cacheDir, prefix string, wasm []byte) ([]byte, error) {
	path := aotArtifactPath(cacheDir, prefix, wasm)
	cached, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: precompiled AOT artifact not found at %s: %w", path, err)
	}
	if len(cached) == 0 {
		return nil, fmt.Errorf("flatsqlrt: precompiled AOT artifact %s is empty", path)
	}
	return cached, nil
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
	path := aotArtifactPath(cacheDir, prefix, wasm)
	if cached, err := os.ReadFile(path); err == nil && len(cached) > 0 {
		return cached, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("flatsqlrt: create AOT cache dir: %w", err)
	}

	// Enable the threads proposal so a shared-memory/atomics (wasi-threads)
	// composed artifact AOT-compiles — WasmEdge's default AOT compiler rejects
	// atomics ("requires enabling Threads proposal"). Harmless for non-threaded
	// modules (flatsql uses neither atomics nor shared memory), so this stays a
	// single reusable AOT path (no fork).
	conf := wasmedge.NewConfigure()
	if conf == nil {
		return nil, fmt.Errorf("flatsqlrt: WasmEdge AOT configure unavailable")
	}
	defer conf.Release()
	conf.AddConfig(wasmedge.THREADS)
	compiler := wasmedge.NewCompilerWithConfig(conf)
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

func aotArtifactPath(cacheDir, prefix string, wasm []byte) string {
	sum := sha256.Sum256(wasm)
	ver := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '.' {
			return r
		}
		return '_'
	}, RuntimeVersion())
	return filepath.Join(cacheDir, fmt.Sprintf("%s-%s-we%s.aot.wasm", prefix, hex.EncodeToString(sum[:8]), ver))
}
