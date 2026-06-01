# Self-Contained SDN CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a portable, self-contained `spacedatanetwork` CLI archive lane with `sdn` alias, bundled Kubo/UI/module runtime assets, and the first signed-update integration surface.

**Architecture:** Keep the existing Go `sdn-server/cmd/spacedatanetwork` command as the product CLI. Add a small runtime bundle resolver in Go, a release archive assembler in Node, and focused release tests that verify layout, aliases, checksums, and manifest coverage. Defer automatic update application until the archive lane and bundle discovery are stable, but add concrete `update check/apply` command scaffolding that reads the bundled updater manifest.

**Tech Stack:** Go 1.24/Cobra, Node.js release tooling, shell release scripts, existing Kubo builder, existing SDN UI/WebUI build outputs, `space-data-module-sdk` updater wasm, SHA-256 checksums, GitHub release artifacts.

---

## File Structure

- Create `sdn-server/internal/bundle/bundle.go`: resolves the current extracted bundle root, runtime paths, alias path, and default runtime assets.
- Create `sdn-server/internal/bundle/bundle_test.go`: unit tests for bundle root discovery and path resolution.
- Modify `sdn-server/cmd/spacedatanetwork/main.go`: add `version`, `status`, `open`, and `update` command registration; use bundle paths as defaults for Kubo/UI/updater assets.
- Create `sdn-server/cmd/spacedatanetwork/update_cli.go`: `update check` and `update apply` command scaffolding that reads `manifest.json`, reports channel/version and `updates_available=false`, and refuses unsigned or incomplete manifests.
- Create `sdn-server/cmd/spacedatanetwork/update_cli_test.go`: CLI tests for manifest parsing and rejection.
- Create `deployment/release/build-self-contained-cli.mjs`: assembles target archives from built binaries and runtime assets.
- Create `deployment/release/build-self-contained-cli.test.mjs`: Node tests for archive staging layout, aliases, checksums, and manifest coverage.
- Modify `deployment/release/assemble-beta-release-artifacts.sh`: copy self-contained CLI archives into `dist/release`.
- Modify `scripts/install.sh`: point at `DigitalArsenal/space-data-network`, download self-contained archives, verify `spacedatanetwork-checksums.txt`, extract, and link both `spacedatanetwork` and `sdn`.
- Modify `README.md`: document the portable CLI archive as the primary beta path.

## Task 1: Bundle Path Resolver

**Files:**
- Create: `sdn-server/internal/bundle/bundle.go`
- Create: `sdn-server/internal/bundle/bundle_test.go`

- [ ] **Step 1: Write the failing bundle resolver tests**

Create `sdn-server/internal/bundle/bundle_test.go`:

```go
package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFromExecutableInsideBundle(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"schema":"org.spacedatanetwork.bundle.v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	layout := ResolveFromExecutable(exe)

	if layout.Root != root {
		t.Fatalf("Root = %q, want %q", layout.Root, root)
	}
	if layout.KuboBinary != filepath.Join(root, "runtime", "kubo", "ipfs") {
		t.Fatalf("KuboBinary = %q", layout.KuboBinary)
	}
	if layout.SDNUIPath != filepath.Join(root, "runtime", "ui", "sdn") {
		t.Fatalf("SDNUIPath = %q", layout.SDNUIPath)
	}
	if layout.WebUIPath != filepath.Join(root, "runtime", "ui", "webui") {
		t.Fatalf("WebUIPath = %q", layout.WebUIPath)
	}
	if layout.UpdaterWASM != filepath.Join(root, "runtime", "modules", "org.spacedatanetwork.updater.wasm") {
		t.Fatalf("UpdaterWASM = %q", layout.UpdaterWASM)
	}
	if layout.ManifestPath != filepath.Join(root, "manifest.json") {
		t.Fatalf("ManifestPath = %q", layout.ManifestPath)
	}
}

func TestResolveFromExecutableOutsideBundleReturnsEmptyOptionalPaths(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "spacedatanetwork")

	layout := ResolveFromExecutable(exe)

	if layout.Root != "" {
		t.Fatalf("Root = %q, want empty", layout.Root)
	}
	if layout.KuboBinary != "" || layout.SDNUIPath != "" || layout.WebUIPath != "" || layout.UpdaterWASM != "" {
		t.Fatalf("runtime paths should be empty outside a bundle: %#v", layout)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:

```bash
cd sdn-server
go test ./internal/bundle
```

Expected: FAIL because `ResolveFromExecutable` is undefined.

- [ ] **Step 3: Implement the resolver**

Create `sdn-server/internal/bundle/bundle.go`:

```go
package bundle

import (
	"os"
	"path/filepath"
	"runtime"
)

type Layout struct {
	Root         string
	BinDir       string
	KuboBinary   string
	SDNUIPath    string
	WebUIPath    string
	UpdaterWASM  string
	ManifestPath string
}

func ResolveCurrent() Layout {
	exe, err := os.Executable()
	if err != nil {
		return Layout{}
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	return ResolveFromExecutable(resolved)
}

func ResolveFromExecutable(executablePath string) Layout {
	if executablePath == "" {
		return Layout{}
	}
	binDir := filepath.Dir(executablePath)
	root := filepath.Dir(binDir)
	if filepath.Base(binDir) != "bin" {
		return Layout{}
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return Layout{}
	}
	kuboName := "ipfs"
	if runtime.GOOS == "windows" {
		kuboName = "ipfs.exe"
	}
	return Layout{
		Root:         root,
		BinDir:       binDir,
		KuboBinary:   filepath.Join(root, "runtime", "kubo", kuboName),
		SDNUIPath:    filepath.Join(root, "runtime", "ui", "sdn"),
		WebUIPath:    filepath.Join(root, "runtime", "ui", "webui"),
		UpdaterWASM:  filepath.Join(root, "runtime", "modules", "org.spacedatanetwork.updater.wasm"),
		ManifestPath: manifestPath,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:

```bash
cd sdn-server
go test ./internal/bundle
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add sdn-server/internal/bundle
git commit -m "Add SDN bundle path resolver"
```

## Task 2: CLI Command Surface

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Create: `sdn-server/cmd/spacedatanetwork/update_cli.go`
- Create: `sdn-server/cmd/spacedatanetwork/update_cli_test.go`

- [ ] **Step 1: Write failing command registration tests**

Append to `sdn-server/cmd/spacedatanetwork/main_test.go`:

```go
func TestUserFacingCLICommandsAreRegistered(t *testing.T) {
	want := []string{"daemon", "init", "status", "open", "update", "version", "config"}
	for _, name := range want {
		if _, _, err := rootCmd.Find([]string{name}); err != nil {
			t.Fatalf("root command %q is not registered: %v", name, err)
		}
	}
	if _, _, err := rootCmd.Find([]string{"update", "check"}); err != nil {
		t.Fatalf("update check is not registered: %v", err)
	}
	if _, _, err := rootCmd.Find([]string{"update", "apply"}); err != nil {
		t.Fatalf("update apply is not registered: %v", err)
	}
}
```

Create `sdn-server/cmd/spacedatanetwork/update_cli_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBundleManifestAcceptsSignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	manifest, err := loadBundleManifest(path)
	if err != nil {
		t.Fatalf("loadBundleManifest returned error: %v", err)
	}
	if manifest.Version != "1.2.3" {
		t.Fatalf("Version = %q, want 1.2.3", manifest.Version)
	}
	if manifest.Channel != "beta" {
		t.Fatalf("Channel = %q, want beta", manifest.Channel)
	}
}

func TestLoadBundleManifestRejectsUnsignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted an unsigned manifest")
	}
}

func TestLoadBundleManifestRejectsMissingVersion(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without version")
	}
}

func writeBundleManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```bash
cd sdn-server
go test ./cmd/spacedatanetwork -run 'TestUserFacingCLICommandsAreRegistered|TestLoadBundleManifest' -count=1
```

Expected: FAIL because `status`, `open`, `update`, `version`, and `config` are not all registered, and `loadBundleManifest` is undefined.

- [ ] **Step 3: Add minimal command registrations**

In `sdn-server/cmd/spacedatanetwork/main.go`, add command variables near the existing `initCmd`:

```go
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print SDN version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(cmd.OutOrStdout(), "version=%s\n", versioninfo.SuiteVersion)
		fmt.Fprintf(cmd.OutOrStdout(), "agent=%s\n", versioninfo.AgentVersion)
		return nil
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the SDN configuration path",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := strings.TrimSpace(configPath)
		if path == "" {
			path = config.DefaultPath()
		}
		fmt.Fprintln(cmd.OutOrStdout(), path)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print local SDN daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd)
	},
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Print the local SDN UI URL",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		addr := cfg.Admin.ListenAddr
		if addr == "" {
			addr = "127.0.0.1:5001"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "http://%s/\n", addr)
		return nil
	},
}
```

Add these helper functions in `main.go`:

```go
func runStatus(cmd *cobra.Command) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	addr := cfg.Admin.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:5001"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "admin_url=http://%s/\n", addr)
	fmt.Fprintln(cmd.OutOrStdout(), "daemon_status=unknown")
	return nil
}
```

Register the commands in `init()`:

```go
rootCmd.AddCommand(statusCmd)
rootCmd.AddCommand(openCmd)
rootCmd.AddCommand(versionCmd)
rootCmd.AddCommand(configCmd)
```

Create `sdn-server/cmd/spacedatanetwork/update_cli.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check and apply signed SDN bundle updates",
}

var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check the bundled update manifest",
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest, err := loadCurrentBundleManifest()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "version=%s\n", manifest.Version)
		fmt.Fprintf(cmd.OutOrStdout(), "channel=%s\n", manifest.Channel)
		fmt.Fprintln(cmd.OutOrStdout(), "updates_available=false")
		return nil
	},
}

var updateApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a staged signed SDN bundle update",
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("no staged update is available")
	},
}

type bundleManifest struct {
	Schema    string `json:"schema"`
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	Signature string `json:"signature"`
}

func init() {
	updateCmd.AddCommand(updateCheckCmd)
	updateCmd.AddCommand(updateApplyCmd)
	rootCmd.AddCommand(updateCmd)
}

func loadCurrentBundleManifest() (*bundleManifest, error) {
	layout := bundle.ResolveCurrent()
	if layout.ManifestPath == "" {
		return nil, errors.New("current executable is not running from a self-contained SDN bundle")
	}
	return loadBundleManifest(layout.ManifestPath)
}

func loadBundleManifest(path string) (*bundleManifest, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest bundleManifest
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "org.spacedatanetwork.bundle.v1" {
		return nil, fmt.Errorf("unsupported bundle manifest schema: %s", manifest.Schema)
	}
	if manifest.Version == "" {
		return nil, errors.New("bundle manifest missing version")
	}
	if manifest.Channel == "" {
		return nil, errors.New("bundle manifest missing channel")
	}
	if manifest.Signature == "" {
		return nil, errors.New("bundle manifest missing signature")
	}
	return &manifest, nil
}
```

- [ ] **Step 4: Run the command registration test**

Run:

```bash
cd sdn-server
go test ./cmd/spacedatanetwork -run 'TestUserFacingCLICommandsAreRegistered|TestLoadBundleManifest' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add sdn-server/cmd/spacedatanetwork/main.go sdn-server/cmd/spacedatanetwork/main_test.go sdn-server/cmd/spacedatanetwork/update_cli.go sdn-server/cmd/spacedatanetwork/update_cli_test.go
git commit -m "Add SDN CLI command surface"
```

## Task 3: Bundle Defaults In Daemon

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Test: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] **Step 1: Write failing tests for bundled runtime defaulting**

Append to `sdn-server/cmd/spacedatanetwork/main_test.go`:

```go
func TestApplyBundleDefaultsUsesBundledAssetsWhenConfigIsEmpty(t *testing.T) {
	root := t.TempDir()
	layout := bundle.Layout{
		Root:        root,
		KuboBinary:  filepath.Join(root, "runtime", "kubo", "ipfs"),
		SDNUIPath:   filepath.Join(root, "runtime", "ui", "sdn"),
		WebUIPath:   filepath.Join(root, "runtime", "ui", "webui"),
		UpdaterWASM: filepath.Join(root, "runtime", "modules", "org.spacedatanetwork.updater.wasm"),
	}
	if err := os.MkdirAll(layout.SDNUIPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.WebUIPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Admin.FrontendPath = ""
	cfg.Admin.WebuiPath = ""
	cfg.Admin.IPFSAPIURL = ""
	cfg.Admin.IPFSGatewayURL = ""

	applyBundleDefaults(cfg, layout)

	if cfg.Admin.FrontendPath != layout.SDNUIPath {
		t.Fatalf("FrontendPath = %q, want %q", cfg.Admin.FrontendPath, layout.SDNUIPath)
	}
	if cfg.Admin.WebuiPath != layout.WebUIPath {
		t.Fatalf("WebuiPath = %q, want %q", cfg.Admin.WebuiPath, layout.WebUIPath)
	}
}

func TestApplyBundleDefaultsPreservesExplicitConfig(t *testing.T) {
	layout := bundle.Layout{
		SDNUIPath: filepath.Join(t.TempDir(), "runtime", "ui", "sdn"),
		WebUIPath: filepath.Join(t.TempDir(), "runtime", "ui", "webui"),
	}
	cfg := config.Default()
	cfg.Admin.FrontendPath = "/custom/sdn"
	cfg.Admin.WebuiPath = "/custom/webui"

	applyBundleDefaults(cfg, layout)

	if cfg.Admin.FrontendPath != "/custom/sdn" {
		t.Fatalf("FrontendPath changed to %q", cfg.Admin.FrontendPath)
	}
	if cfg.Admin.WebuiPath != "/custom/webui" {
		t.Fatalf("WebuiPath changed to %q", cfg.Admin.WebuiPath)
	}
}
```

Also add imports for `os`, `path/filepath`, `github.com/spacedatanetwork/sdn-server/internal/bundle`, and `github.com/spacedatanetwork/sdn-server/internal/config` if missing.

- [ ] **Step 2: Run the tests to verify they fail**

Run:

```bash
cd sdn-server
go test ./cmd/spacedatanetwork -run 'TestApplyBundleDefaults' -count=1
```

Expected: FAIL because `applyBundleDefaults` is undefined.

- [ ] **Step 3: Implement `applyBundleDefaults`**

In `sdn-server/cmd/spacedatanetwork/main.go`, add:

```go
func applyBundleDefaults(cfg *config.Config, layout bundle.Layout) {
	if cfg == nil || layout.Root == "" {
		return
	}
	if strings.TrimSpace(cfg.Admin.FrontendPath) == "" && pathExists(layout.SDNUIPath) {
		cfg.Admin.FrontendPath = layout.SDNUIPath
	}
	if strings.TrimSpace(cfg.Admin.WebuiPath) == "" && pathExists(layout.WebUIPath) {
		cfg.Admin.WebuiPath = layout.WebUIPath
	}
}

func pathExists(pathValue string) bool {
	if strings.TrimSpace(pathValue) == "" {
		return false
	}
	_, err := os.Stat(pathValue)
	return err == nil
}
```

Import `github.com/spacedatanetwork/sdn-server/internal/bundle`.

Call it in `runDaemon` immediately after `cfg, err := config.Load(configPath)`:

```go
	applyBundleDefaults(cfg, bundle.ResolveCurrent())
```

- [ ] **Step 4: Run focused tests**

Run:

```bash
cd sdn-server
go test ./cmd/spacedatanetwork -run 'TestApplyBundleDefaults' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add sdn-server/cmd/spacedatanetwork/main.go sdn-server/cmd/spacedatanetwork/main_test.go
git commit -m "Default daemon assets from SDN bundle"
```

## Task 4: Self-Contained Archive Builder

**Files:**
- Create: `deployment/release/build-self-contained-cli.mjs`
- Create: `deployment/release/build-self-contained-cli.test.mjs`

- [ ] **Step 1: Write failing archive builder tests**

Create `deployment/release/build-self-contained-cli.test.mjs`:

```js
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { createArchive, stageBundle } from './build-self-contained-cli.mjs';

test('stageBundle creates expected portable archive layout', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'ipfs'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'linux',
    arch: 'amd64',
    channel: 'beta',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    kuboPath: join(inputs, 'ipfs'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
  });

  assert.equal(staged.bundleName, 'spacedatanetwork-1.2.3-linux-amd64');
  await stat(join(staged.root, 'bin', 'spacedatanetwork'));
  await stat(join(staged.root, 'bin', 'sdn'));
  await stat(join(staged.root, 'runtime', 'kubo', 'ipfs'));
  await stat(join(staged.root, 'runtime', 'ui', 'sdn', 'index.html'));
  await stat(join(staged.root, 'runtime', 'ui', 'webui', 'index.html'));
  await stat(join(staged.root, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'));
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.schema, 'org.spacedatanetwork.bundle.v1');
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.channel, 'beta');
  assert.equal(manifest.signature, 'test-signature');
  assert.ok(manifest.artifacts.some((artifact) => artifact.path === 'bin/spacedatanetwork'));
  assert.ok(manifest.artifacts.some((artifact) => artifact.path === 'runtime/kubo/ipfs'));
  assert.ok(manifest.artifacts.some((artifact) => artifact.path === 'runtime/ui/sdn/index.html'));
  assert.ok(!manifest.artifacts.some((artifact) => artifact.path === 'manifest.json'));
  const checksums = await readFile(join(staged.root, 'checksums.txt'), 'utf8');
  assert.match(checksums, /bin\/spacedatanetwork/);
  assert.match(checksums, /runtime\/kubo\/ipfs/);

  const archive = await createArchive(staged);
  assert.equal(archive.archiveName, 'spacedatanetwork-1.2.3-linux-amd64.tar.gz');
  await stat(archive.path);
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```bash
node --test deployment/release/build-self-contained-cli.test.mjs
```

Expected: FAIL because `build-self-contained-cli.mjs` does not exist.

- [ ] **Step 3: Implement `stageBundle` and CLI argument parsing**

Create `deployment/release/build-self-contained-cli.mjs` with:

```js
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cp, chmod, mkdir, readdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const executableMode = 0o755;

export async function stageBundle(options) {
  const version = required(options.version, 'version');
  const osName = required(options.os, 'os');
  const arch = required(options.arch, 'arch');
  const channel = options.channel || 'beta';
  const signature = required(options.manifestSignature, 'manifestSignature');
  const bundleName = `spacedatanetwork-${version}-${osName}-${arch}`;
  const root = join(required(options.outputDir, 'outputDir'), bundleName);
  await rm(root, { recursive: true, force: true });
  await mkdir(root, { recursive: true });
  await mkdir(join(root, 'bin'), { recursive: true });
  await mkdir(join(root, 'runtime', 'kubo'), { recursive: true });
  await mkdir(join(root, 'runtime', 'modules'), { recursive: true });
  await mkdir(join(root, 'runtime', 'ui'), { recursive: true });

  const exeName = osName === 'windows' ? 'spacedatanetwork.exe' : 'spacedatanetwork';
  const aliasName = osName === 'windows' ? 'sdn.exe' : 'sdn';
  const kuboName = osName === 'windows' ? 'ipfs.exe' : 'ipfs';
  await cp(required(options.binaryPath, 'binaryPath'), join(root, 'bin', exeName));
  await cp(required(options.kuboPath, 'kuboPath'), join(root, 'runtime', 'kubo', kuboName));
  await cp(required(options.sdnUIPath, 'sdnUIPath'), join(root, 'runtime', 'ui', 'sdn'), { recursive: true });
  await cp(required(options.webUIPath, 'webUIPath'), join(root, 'runtime', 'ui', 'webui'), { recursive: true });
  await cp(required(options.updaterWasmPath, 'updaterWasmPath'), join(root, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'));
  await cp(required(options.licensePath, 'licensePath'), join(root, 'LICENSE'));
  await cp(required(options.readmePath, 'readmePath'), join(root, 'README.md'));

  if (osName === 'windows') {
    await cp(join(root, 'bin', exeName), join(root, 'bin', aliasName));
  } else {
    await chmod(join(root, 'bin', exeName), executableMode);
    await chmod(join(root, 'runtime', 'kubo', kuboName), executableMode);
    await symlink(exeName, join(root, 'bin', aliasName));
  }

  const artifacts = await collectArtifacts(root);
  const manifest = {
    schema: 'org.spacedatanetwork.bundle.v1',
    version,
    channel,
    signature,
    os: osName,
    arch,
    artifacts,
  };
  await writeFile(join(root, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  const checksums = artifacts
    .map((artifact) => `${artifact.sha256}  ${artifact.path}`)
    .join('\n') + '\n';
  await writeFile(join(root, 'checksums.txt'), checksums);
  return { bundleName, root, os: osName };
}

export async function createArchive(staged) {
  const outputDir = dirname(staged.root);
  const archiveName = staged.os === 'windows' ? `${staged.bundleName}.zip` : `${staged.bundleName}.tar.gz`;
  const archivePath = join(outputDir, archiveName);
  await rm(archivePath, { force: true });
  if (staged.os === 'windows') {
    await run('zip', ['-qr', archivePath, staged.bundleName], { cwd: outputDir });
  } else {
    await run('tar', ['-czf', archivePath, staged.bundleName], { cwd: outputDir });
  }
  return { archiveName, path: archivePath };
}

async function collectArtifacts(root) {
  const paths = (await listRelativeFiles(root, '')).filter((path) => path !== 'manifest.json' && path !== 'checksums.txt').sort();
  const artifacts = [];
  for (const path of paths) {
    const bytes = await readFile(join(root, path));
    artifacts.push({
      path,
      sha256: createHash('sha256').update(bytes).digest('hex'),
      size: bytes.length,
    });
  }
  return artifacts;
}

async function listRelativeFiles(root, prefix) {
  const entries = await readdir(join(root, prefix), { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      files.push(...(await listRelativeFiles(root, relativePath)));
      continue;
    }
    if (entry.isFile() || entry.isSymbolicLink()) {
      files.push(relativePath);
    }
  }
  return files;
}

function required(value, name) {
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key.startsWith('--') || !value) {
      throw new Error(`Invalid argument near ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const staged = await stageBundle(parseArgs(process.argv.slice(2)));
  const archive = await createArchive(staged);
  console.log(archive.path);
}

function run(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] });
    let stderr = '';
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('error', reject);
    child.on('close', (code) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`${command} exited ${code}: ${stderr.trim()}`));
    });
  });
}
```

- [ ] **Step 4: Run archive builder tests**

Run:

```bash
node --test deployment/release/build-self-contained-cli.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add deployment/release/build-self-contained-cli.mjs deployment/release/build-self-contained-cli.test.mjs
git commit -m "Add self-contained CLI bundle staging"
```

## Task 5: Release Artifact Integration

**Files:**
- Modify: `deployment/release/assemble-beta-release-artifacts.sh`
- Modify: `deployment/release/assemble-beta-release-artifacts.test.mjs`
- Modify: `deployment/release/beta-download-links.test.mjs`
- Modify: `README.md`

- [ ] **Step 1: Write failing release integration expectations**

Update `deployment/release/assemble-beta-release-artifacts.test.mjs` to create a fake `dist/cli` directory with:

```js
await writeFile(join(distDir, 'cli', 'spacedatanetwork-1.0.3-beta.1-linux-amd64.tar.gz'), 'cli');
```

Add an assertion:

```js
assert.ok(files.includes('spacedatanetwork-1.0.3-beta.1-linux-amd64.tar.gz'));
```

Update `deployment/release/beta-download-links.test.mjs` to expect the README to mention:

```js
spacedatanetwork-<beta-version>-darwin-arm64.tar.gz
spacedatanetwork-<beta-version>-windows-amd64.zip
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
node --test deployment/release/assemble-beta-release-artifacts.test.mjs deployment/release/beta-download-links.test.mjs
```

Expected: FAIL because `dist/cli` is not copied and README does not document the new artifacts yet.

- [ ] **Step 3: Copy CLI archives into release output**

In `deployment/release/assemble-beta-release-artifacts.sh`, add this after existing `copy_matches` calls:

```bash
copy_matches "${dist_dir}/cli/*.tar.gz"
copy_matches "${dist_dir}/cli/*.zip"
```

Update `README.md` Downloads and Native releases sections to list:

```markdown
| `spacedatanetwork-<beta-version>-<os>-<arch>.tar.gz` / `.zip` | Self-contained native CLI bundle with SDN, Kubo, UI assets, updater module |
```

And direct artifact examples for darwin/linux/windows.

- [ ] **Step 4: Run release tests**

Run:

```bash
node --test deployment/release/assemble-beta-release-artifacts.test.mjs deployment/release/beta-download-links.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add deployment/release/assemble-beta-release-artifacts.sh deployment/release/assemble-beta-release-artifacts.test.mjs deployment/release/beta-download-links.test.mjs README.md
git commit -m "Publish self-contained CLI archives in beta releases"
```

## Task 6: Install Script For Archive Lane

**Files:**
- Modify: `scripts/install.sh`

- [ ] **Step 1: Write a focused shellcheck-style test script**

Create temporary test command:

```bash
bash -n scripts/install.sh
```

Expected now: PASS syntax only, but the script still references the wrong repo and direct binary assets. This establishes syntax before edits.

- [ ] **Step 2: Update install script constants and artifact names**

In `scripts/install.sh`:

```bash
REPO="DigitalArsenal/space-data-network"
PRIMARY_BINARY_NAME="spacedatanetwork"
ALIAS_BINARY_NAME="sdn"
```

Change platform artifact naming:

```bash
if [ "$OS" = "windows" ]; then
  ARCHIVE_NAME="spacedatanetwork-${VERSION}-${OS}-${ARCH}.zip"
else
  ARCHIVE_NAME="spacedatanetwork-${VERSION}-${OS}-${ARCH}.tar.gz"
fi
```

Download `$ARCHIVE_NAME`, verify against `spacedatanetwork-checksums.txt`, extract to `${SDN_BUNDLE_DIR:-$HOME/.spacedatanetwork/bundles}`, and link both:

```bash
ln -sf "${BUNDLE_ROOT}/bin/spacedatanetwork" "${INSTALL_DIR}/spacedatanetwork"
ln -sf "${BUNDLE_ROOT}/bin/sdn" "${INSTALL_DIR}/sdn"
```

On Windows, print extraction instructions instead of attempting Unix symlinks.

- [ ] **Step 3: Run syntax verification**

Run:

```bash
bash -n scripts/install.sh
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add scripts/install.sh
git commit -m "Install self-contained SDN CLI archives"
```

## Task 7: Full Focused Verification

**Files:**
- No code changes unless verification exposes a bug.

- [ ] **Step 1: Run Go focused tests**

Run:

```bash
cd sdn-server
go test ./internal/bundle ./cmd/spacedatanetwork -run 'TestResolve|TestUserFacingCLICommandsAreRegistered|TestApplyBundleDefaults|TestUpdate' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run release focused tests**

Run:

```bash
node --test deployment/release/build-self-contained-cli.test.mjs deployment/release/assemble-beta-release-artifacts.test.mjs deployment/release/beta-download-links.test.mjs
```

Expected: PASS.

- [ ] **Step 3: Run install script syntax check**

Run:

```bash
bash -n scripts/install.sh
```

Expected: PASS.

- [ ] **Step 4: Commit verification fixes if needed**

If any verification command required code changes:

```bash
git add sdn-server/internal/bundle sdn-server/cmd/spacedatanetwork/main.go sdn-server/cmd/spacedatanetwork/main_test.go sdn-server/cmd/spacedatanetwork/update_cli.go sdn-server/cmd/spacedatanetwork/update_cli_test.go deployment/release/build-self-contained-cli.mjs deployment/release/build-self-contained-cli.test.mjs deployment/release/assemble-beta-release-artifacts.sh deployment/release/assemble-beta-release-artifacts.test.mjs deployment/release/beta-download-links.test.mjs scripts/install.sh README.md
git commit -m "Fix self-contained CLI verification gaps"
```

Expected: no commit if all commands already pass.

## Plan Self-Review

Spec coverage:

- Native downloadable archives: Tasks 4 and 5.
- `spacedatanetwork` primary and `sdn` alias: Tasks 2, 4, and 6.
- Bundled Kubo/UI/updater assets: Tasks 1, 3, and 4.
- Signed update channel surface: Tasks 2 and 4 require a manifest signature field and reject missing signatures; cryptographic signature verification and automatic update application remain follow-ups after updater module implementation.
- Install behavior: Task 6.
- Testing coverage: Tasks 1 through 7.

Known follow-up after this plan:

- Implement actual updater wasm host integration and signed artifact application.
- Add real multi-OS CI matrix builds and platform smoke extraction.
- Add macOS/Windows code signing once Developer ID and Windows signing credentials exist.
