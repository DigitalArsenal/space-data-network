package flowcc

// nodedata.go turns the flowcc toolchain from a scratchpad-only fixture (P4)
// into NODE DATA: a stable on-disk layout under the node's own data dir that
// holds the ~94 MB toolchain (llvm-box.wasm + the extracted sysroot), the fixed
// flow-runtime C++ template, and every staged module guest-link object keyed by
// pluginId. `flowcc` resolves llvm-box.wasm and the sysroot from this layout by
// default, so a booted node bakes flows without any SDN_LLVM_* env plumbing;
// the env vars still override (explicit arg > env > node-data default).
//
// Layout (rooted at Home):
//
//	{home}/                       ($SDN_FLOWCC_HOME | $IPFS_PATH/sdn/flowcc | ~/.ipfs/sdn/flowcc)
//	  llvm-box.wasm              — emception clang+wasm-ld single artifact (~58 MB)
//	  sysroot/                   — extracted clang-16 sysroot tree (~36 MB)
//	    lib/wasm32-emscripten/…
//	    include/…
//	  template/                  — the FIXED flow-runtime C++ template (flow-independent)
//	    flow_runtime.cpp
//	    space_data_module_invoke.h
//	  modules/                   — staged module guest-link objects, keyed by pluginId
//	    {sanitized-pluginId}/
//	      module-link.o          — relocatable guest-link object (byte-identical to dist)
//	      metadata.json          — { symbolPrefix, methodSymbols{method:symbol} }
//	  cache/
//	    flow_runtime/{sha}.o     — compiled flow_runtime.o, keyed by (template+inc+flags) hash
//
// How a deployed (prod) node receives this: the toolchain (llvm-box.wasm +
// sysroot + template) rides the same module dist push that ships the modules'
// dist/guest-link objects — StageToolchain + StageModulesFromDist populate the
// layout from the built artifacts once, and the node reads them read-only
// forever after. See StageToolchain / StageModulesFromDist below.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EnvFlowccHome overrides the resolved node-data home directory.
const EnvFlowccHome = "SDN_FLOWCC_HOME"

// Home is a resolved flowcc node-data directory. All accessor methods are pure
// path joins; nothing is created until a Stage* helper runs.
type Home struct{ root string }

// ResolveHome resolves the flowcc node-data home:
//
//	$SDN_FLOWCC_HOME              (explicit override), else
//	$IPFS_PATH/sdn/flowcc         (co-located with the kubo repo), else
//	~/.ipfs/sdn/flowcc            (kubo default repo location).
func ResolveHome() Home {
	if h := os.Getenv(EnvFlowccHome); h != "" {
		return Home{root: h}
	}
	if p := os.Getenv("IPFS_PATH"); p != "" {
		return Home{root: filepath.Join(p, "sdn", "flowcc")}
	}
	if hd, err := os.UserHomeDir(); err == nil && hd != "" {
		return Home{root: filepath.Join(hd, ".ipfs", "sdn", "flowcc")}
	}
	return Home{root: filepath.Join(".ipfs", "sdn", "flowcc")}
}

// HomeAt returns a Home rooted at an explicit directory (used by tests and by
// the module-dist staging path).
func HomeAt(root string) Home { return Home{root: root} }

// Root returns the home's root directory.
func (h Home) Root() string { return h.root }

// BoxPath is the resolved llvm-box.wasm location.
func (h Home) BoxPath() string { return filepath.Join(h.root, "llvm-box.wasm") }

// SysrootDir is the resolved sysroot tree location.
func (h Home) SysrootDir() string { return filepath.Join(h.root, "sysroot") }

// TemplateDir holds the fixed flow-runtime C++ template files.
func (h Home) TemplateDir() string { return filepath.Join(h.root, "template") }

// FlowRuntimeCppPath / InvokeHeaderPath are the two fixed template files.
func (h Home) FlowRuntimeCppPath() string {
	return filepath.Join(h.TemplateDir(), "flow_runtime.cpp")
}
func (h Home) InvokeHeaderPath() string {
	return filepath.Join(h.TemplateDir(), "space_data_module_invoke.h")
}

// ModulesDir is the root of the staged module guest-link objects.
func (h Home) ModulesDir() string { return filepath.Join(h.root, "modules") }

// ModuleDir is the staged directory for one module, keyed by pluginId.
func (h Home) ModuleDir(pluginID string) string {
	return filepath.Join(h.ModulesDir(), sanitizePluginID(pluginID))
}

// ModuleLinkObjectPath / ModuleMetadataPath are the two staged files per module.
func (h Home) ModuleLinkObjectPath(pluginID string) string {
	return filepath.Join(h.ModuleDir(pluginID), "module-link.o")
}
func (h Home) ModuleMetadataPath(pluginID string) string {
	return filepath.Join(h.ModuleDir(pluginID), "metadata.json")
}

// CacheDir / FlowRuntimeCacheDir hold compiled-object caches.
func (h Home) CacheDir() string { return filepath.Join(h.root, "cache") }
func (h Home) FlowRuntimeCacheDir() string {
	return filepath.Join(h.CacheDir(), "flow_runtime")
}

// Staged reports whether the core toolchain (box + sysroot + template) is
// present so a caller can decide between baking and the prebuilt-only path.
func (h Home) Staged() bool {
	if fi, err := os.Stat(h.BoxPath()); err != nil || fi.IsDir() {
		return false
	}
	if fi, err := os.Stat(h.SysrootDir()); err != nil || !fi.IsDir() {
		return false
	}
	if fi, err := os.Stat(h.FlowRuntimeCppPath()); err != nil || fi.IsDir() {
		return false
	}
	return true
}

// ModuleMetadata mirrors the metadata.json that ships in each module's
// dist/guest-link directory: the guest-link object's exported entry symbols,
// keyed by method id. It is the authoritative (pluginId, method) -> symbol map
// the bake descriptor generator resolves against.
type ModuleMetadata struct {
	Version       int               `json:"version"`
	Format        string            `json:"format"`
	Language      string            `json:"language"`
	ThreadModel   string            `json:"threadModel"`
	SymbolPrefix  string            `json:"symbolPrefix"`
	MethodSymbols map[string]string `json:"methodSymbols"`
}

// LoadModuleMetadata reads a staged module's metadata.json.
func (h Home) LoadModuleMetadata(pluginID string) (*ModuleMetadata, error) {
	b, err := os.ReadFile(h.ModuleMetadataPath(pluginID))
	if err != nil {
		return nil, fmt.Errorf("flowcc: module %q not staged: %w", pluginID, err)
	}
	var m ModuleMetadata
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("flowcc: module %q metadata: %w", pluginID, err)
	}
	return &m, nil
}

// ---------------------------------------------------------------------------
// Staging helpers — populate the node-data layout from built artifacts.
// ---------------------------------------------------------------------------

// StageToolchain populates the core toolchain into the home: it copies
// llvm-box.wasm, the sysroot tree, and the two fixed template files. Any
// argument left empty is skipped (so a caller can stage just the template, or
// re-point at an already-staged box). It is idempotent — re-running overwrites.
func StageToolchain(home Home, srcBox, srcSysroot, srcTemplateDir string) error {
	if err := os.MkdirAll(home.root, 0o755); err != nil {
		return fmt.Errorf("flowcc: mkdir home: %w", err)
	}
	if srcBox != "" {
		if err := copyFile(srcBox, home.BoxPath()); err != nil {
			return fmt.Errorf("flowcc: stage llvm-box.wasm: %w", err)
		}
	}
	if srcSysroot != "" {
		if err := copyTree(srcSysroot, home.SysrootDir()); err != nil {
			return fmt.Errorf("flowcc: stage sysroot: %w", err)
		}
	}
	if srcTemplateDir != "" {
		if err := os.MkdirAll(home.TemplateDir(), 0o755); err != nil {
			return err
		}
		for _, f := range []string{"flow_runtime.cpp", "space_data_module_invoke.h"} {
			src := filepath.Join(srcTemplateDir, f)
			if _, err := os.Stat(src); err != nil {
				return fmt.Errorf("flowcc: stage template %s: %w", f, err)
			}
			if err := copyFile(src, filepath.Join(home.TemplateDir(), f)); err != nil {
				return fmt.Errorf("flowcc: stage template %s: %w", f, err)
			}
		}
	}
	return nil
}

// StageModule stages one module's guest-link object + metadata under the home,
// keyed by pluginId. linkObjPath is the module's dist/guest-link/module-link.o
// and metadataPath its dist/guest-link/metadata.json.
func StageModule(home Home, pluginID, linkObjPath, metadataPath string) error {
	if pluginID == "" {
		return fmt.Errorf("flowcc: StageModule: empty pluginId")
	}
	dir := home.ModuleDir(pluginID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := copyFile(linkObjPath, filepath.Join(dir, "module-link.o")); err != nil {
		return fmt.Errorf("flowcc: stage module %q object: %w", pluginID, err)
	}
	if err := copyFile(metadataPath, filepath.Join(dir, "metadata.json")); err != nil {
		return fmt.Errorf("flowcc: stage module %q metadata: %w", pluginID, err)
	}
	return nil
}

// StageModulesFromDist walks a modules monorepo dist tree and stages every
// module that ships a dist/guest-link/module-link.o + metadata.json, keying it
// by the pluginId in the sibling dist/plugin-manifest.json (falling back to the
// metadata's own pluginId field if present). It returns the pluginIds staged.
// This is the "part of the module dist push" a prod node runs once.
func StageModulesFromDist(home Home, distRoot string) ([]string, error) {
	var staged []string
	err := filepath.Walk(distRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees rather than abort the whole push
		}
		if info.IsDir() || info.Name() != "module-link.o" {
			return nil
		}
		gdir := filepath.Dir(p) // .../dist/guest-link
		metaPath := filepath.Join(gdir, "metadata.json")
		if _, err := os.Stat(metaPath); err != nil {
			return nil
		}
		pluginID := pluginIDFromDist(gdir)
		if pluginID == "" {
			return nil
		}
		if err := StageModule(home, pluginID, p, metaPath); err != nil {
			return err
		}
		staged = append(staged, pluginID)
		return nil
	})
	return staged, err
}

// pluginIDFromDist reads the pluginId for a module given its dist/guest-link
// directory, checking the sibling dist/plugin-manifest.json first.
func pluginIDFromDist(guestLinkDir string) string {
	manifest := filepath.Join(filepath.Dir(guestLinkDir), "plugin-manifest.json")
	if b, err := os.ReadFile(manifest); err == nil {
		var m struct {
			PluginID string `json:"pluginId"`
		}
		if json.Unmarshal(b, &m) == nil && m.PluginID != "" {
			return m.PluginID
		}
	}
	return ""
}

// fileExists / dirExists are the resolution-order predicates New uses.
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// sanitizePluginID makes a pluginId safe as a single path segment.
func sanitizePluginID(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "..", "_", string(os.PathSeparator), "_")
	return r.Replace(id)
}

// copyFile copies src to dst (0644), creating dst's parent.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree recursively copies the src directory tree to dst.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks/devices; the sysroot is plain files
		}
		return copyFile(p, target)
	})
}
