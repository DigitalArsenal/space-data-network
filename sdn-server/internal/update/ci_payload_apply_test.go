package update

// A CI-SHAPED PAYLOAD APPLIES.
//
// The release workflow's cli-update-feed job builds every update payload from
// the downloadable CLI archive: build-self-contained-cli.mjs stages and
// archives the bundle (trust/update-roots.json included, for a fresh
// install), then build-cli-update-payload.mjs wraps it in a carrier under a
// signed manifest. Until the payload builder dropped trust/, every such
// payload was refused by every node ("update bundle must not contain
// protected entry \"trust\""), so a signed release feed could never have
// installed anywhere. This test runs those two release scripts as CI does and
// applies what they produce through Apply.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCIShapedUpdatePayloadAppliesThroughApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the release scripts stage unix bundles here; the Windows zip payload is covered by deployment/release/cli-update-payload-trust.test.mjs")
	}
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH: this test runs the release scripts that build the payload")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(repo, "deployment", "release")
	work := t.TempDir()

	// The release key, a throwaway, and the roots the installed bundle trusts.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyID := RootKeyID(pub)
	installedRoots := TrustedRoots{keyID: base64.StdEncoding.EncodeToString(spki)}
	rootsFile := filepath.Join(work, "roots.json")
	writeJSONFile(t, rootsFile, installedRoots)

	inputs := filepath.Join(work, "inputs")
	for _, dir := range []string{"ui-sdn", "ui-webui"} {
		if err := os.MkdirAll(filepath.Join(inputs, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inputs, dir, "index.html"), []byte("<!doctype html>"+dir), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"updater.wasm": "updater", "hd-wallet.wasm": "wallet", "LICENSE": "license", "README.md": "readme"} {
		if err := os.WriteFile(filepath.Join(inputs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// stage runs build-self-contained-cli.mjs as the cli job does and returns
	// the archive it printed.
	stage := func(version, daemon string, extra ...string) string {
		t.Helper()
		binary := filepath.Join(inputs, "spacedatanetwork-"+version)
		if err := os.WriteFile(binary, []byte(daemon), 0o755); err != nil {
			t.Fatal(err)
		}
		args := append([]string{filepath.Join(release, "build-self-contained-cli.mjs"),
			"--version", version, "--os", runtime.GOOS, "--arch", runtime.GOARCH,
			"--output-dir", filepath.Join(work, "dist-"+version),
			"--binary-path", binary,
			"--sdn-ui-path", filepath.Join(inputs, "ui-sdn"),
			"--web-ui-path", filepath.Join(inputs, "ui-webui"),
			"--updater-wasm-path", filepath.Join(inputs, "updater.wasm"),
			"--hd-wallet-wasm-path", filepath.Join(inputs, "hd-wallet.wasm"),
			"--license-path", filepath.Join(inputs, "LICENSE"),
			"--readme-path", filepath.Join(inputs, "README.md"),
			"--manifest-signature", "github-actions:v" + version + ":e2e",
		}, extra...)
		out, err := exec.Command(nodeBin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("build-self-contained-cli.mjs %s: %v\n%s", version, err, out)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}

	// The installed bundle: release 1.1.1, unpacked from its own archive, with
	// the roots that trust the throwaway release key.
	installedArchive := stage("1.1.1", "daemon 1.1.1", "--trust-roots-path", rootsFile)
	unpacked := filepath.Join(work, "installed")
	if err := extractBundleArchive(installedArchive, "tar.gz", unpacked); err != nil {
		t.Fatalf("unpack the installed bundle: %v", err)
	}
	root, err := locateBundleRoot(unpacked)
	if err != nil {
		t.Fatal(err)
	}
	paths := PathsFor(root)
	t.Setenv(TrustRootsEnv, "")

	// Release 1.1.2, archived exactly as CI archives it — fleet roots and all —
	// then wrapped by the payload builder the cli-update-feed job runs.
	releaseArchive := stage("1.1.2", "daemon 1.1.2")
	if listing, err := exec.Command("tar", "-tzf", releaseArchive).Output(); err != nil || !strings.Contains(string(listing), "/trust/update-roots.json") {
		t.Fatalf("the release archive should carry trust/ for a fresh install (%v):\n%s", err, listing)
	}
	payloadDir := filepath.Join(work, "payload")
	build := exec.Command(nodeBin, filepath.Join(release, "build-cli-update-payload.mjs"),
		"--bundle-archive", releaseArchive,
		"--version", "1.1.2",
		"--sequence", "7",
		"--channel", "release",
		"--platform", runtime.GOOS,
		"--arch", runtime.GOARCH,
		"--key-id", keyID,
		"--created-at", time.Now().UTC().Format(time.RFC3339),
		"--out-dir", payloadDir)
	build.Env = append(os.Environ(), "SDN_UPDATE_SIGNING_KEY_PEM="+keyPEM)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build-cli-update-payload.mjs: %v\n%s", err, out)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(payloadDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	carrier, err := os.ReadFile(filepath.Join(payloadDir, "update.wasm"))
	if err != nil {
		t.Fatal(err)
	}

	roots, err := LoadTrustRoots(paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(paths, manifestBytes, carrier, HostVerifyOptions(roots, 0, time.Now()), nil); err != nil {
		t.Fatalf("stage the CI payload: %v", err)
	}
	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("apply the CI payload: %v", err)
	}
	if result.Version != "1.1.2" || result.Sequence != 7 {
		t.Fatalf("applied %+v, want 1.1.2 at sequence 7", result)
	}

	daemon, err := os.ReadFile(filepath.Join(root, "runtime", "sdn", "spacedatanetwork"))
	if err != nil || string(daemon) != "daemon 1.1.2" {
		t.Fatalf("the bundle runs %q (%v), want the 1.1.2 daemon", daemon, err)
	}
	var bundleManifest struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil || json.Unmarshal(raw, &bundleManifest) != nil || bundleManifest.Version != "1.1.2" {
		t.Fatalf("the bundle manifest is %s (%v), want version 1.1.2", raw, err)
	}
	// The swap never touched the install's own roots.
	kept, err := LoadTrustRoots(paths)
	if err != nil || len(kept) != 1 || kept[keyID] != installedRoots[keyID] {
		t.Fatalf("the installed roots are %v (%v) after the update, want them unchanged: %v", kept, err, installedRoots)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
