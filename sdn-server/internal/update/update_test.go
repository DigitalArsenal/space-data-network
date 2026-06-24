package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type testSigner struct {
	keyID      string
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{keyID: "release-test", publicKey: pub, privateKey: priv}
}

func (s *testSigner) publicKeyBase64SPKI(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(s.publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func (s *testSigner) roots(t *testing.T) TrustedRoots {
	return TrustedRoots{s.keyID: s.publicKeyBase64SPKI(t)}
}

func sortedJSON(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

func (s *testSigner) signedManifest(t *testing.T, mutate func(map[string]any), bundleBytes, wasmBytes []byte) []byte {
	t.Helper()
	doc := map[string]any{
		"schema":     ManifestSchema,
		"update_id":  "cli-beta-0001",
		"version":    "9.9.9",
		"sequence":   int64(42),
		"channel":    "beta",
		"created_at": "2026-06-01T00:00:00Z",
		"expires_at": "2030-01-01T00:00:00Z",
		"target": map[string]any{
			"platform": runtime.GOOS,
			"arch":     runtime.GOARCH,
			"kind":     "cli-bundle",
		},
		"bundle": map[string]any{
			"hash":   sha256Hex(bundleBytes),
			"size":   int64(len(bundleBytes)),
			"format": "tar.gz",
		},
		"wasm": map[string]any{
			"hash": sha256Hex(wasmBytes),
		},
		"signing": map[string]any{
			"key_id":    s.keyID,
			"algorithm": "Ed25519",
		},
	}
	if mutate != nil {
		mutate(doc)
	}
	canonical, err := CanonicalManifestBytes(sortedJSON(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(s.privateKey, canonical)
	doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(signature)
	return sortedJSON(t, doc)
}

func TestCanonicalManifestBytesMatchesDesktopFixture(t *testing.T) {
	// Generated with desktop/src/sdn-updater/manifest.js canonicalManifestBytes:
	// canonicalize sorts keys recursively, drops signing.signature, and
	// stringifies compactly.
	raw := []byte(`{
		"schema": "org.spacedatanetwork.update.v1",
		"version": "1.0.0",
		"sequence": 7,
		"signing": {"signature": "abc", "key_id": "k1", "algorithm": "Ed25519"},
		"target": {"platform": "darwin", "arch": "arm64", "kind": "desktop-app"}
	}`)
	want := `{"schema":"org.spacedatanetwork.update.v1","sequence":7,"signing":{"algorithm":"Ed25519","key_id":"k1"},"target":{"arch":"arm64","kind":"desktop-app","platform":"darwin"},"version":"1.0.0"}`
	got, err := CanonicalManifestBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("canonical bytes = %s, want %s", got, want)
	}
}

func TestVerifyPayloadAcceptsSignedManifest(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, nil, bundleBytes, wasmBytes)

	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		CurrentSequence: 41,
		TrustedRoots:    signer.roots(t),
	})
	if err != nil {
		t.Fatalf("VerifyPayload returned error: %v", err)
	}
	if result.Sequence != 42 || result.Version != "9.9.9" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyPayloadRejectsTamperedManifest(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, nil, bundleBytes, wasmBytes)
	tampered := bytes.Replace(manifestBytes, []byte(`"version":"9.9.9"`), []byte(`"version":"6.6.6"`), 1)
	if bytes.Equal(tampered, manifestBytes) {
		t.Fatal("tamper failed to change manifest")
	}

	manifest, err := ParseManifest(tampered)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		CurrentSequence: 41,
		TrustedRoots:    signer.roots(t),
	})
	if err == nil || err.Error() != "invalid update signature" {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}

func TestVerifyPayloadRejectsUntrustedKeyExpiryTargetAndSequence(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)

	cases := []struct {
		name    string
		mutate  func(map[string]any)
		opts    func(VerifyOptions) VerifyOptions
		wantErr string
	}{
		{
			name: "untrusted key",
			opts: func(o VerifyOptions) VerifyOptions {
				o.TrustedRoots = TrustedRoots{"other": o.TrustedRoots[signer.keyID]}
				return o
			},
			wantErr: "untrusted update signing key",
		},
		{
			name:    "expired",
			mutate:  func(doc map[string]any) { doc["expires_at"] = "2020-01-01T00:00:00Z" },
			wantErr: "update manifest expired",
		},
		{
			name:    "platform mismatch",
			mutate:  func(doc map[string]any) { doc["target"].(map[string]any)["platform"] = "plan9" },
			wantErr: "update target platform mismatch",
		},
		{
			name:    "stale sequence without rollback",
			opts:    func(o VerifyOptions) VerifyOptions { o.CurrentSequence = 42; return o },
			wantErr: "update sequence rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifestBytes := signer.signedManifest(t, tc.mutate, bundleBytes, wasmBytes)
			manifest, err := ParseManifest(manifestBytes)
			if err != nil {
				t.Fatal(err)
			}
			opts := VerifyOptions{
				Platform:        runtime.GOOS,
				Arch:            runtime.GOARCH,
				CurrentSequence: 41,
				TrustedRoots:    signer.roots(t),
			}
			if tc.opts != nil {
				opts = tc.opts(opts)
			}
			_, err = manifest.VerifyPayload(wasmBytes, bundleBytes, opts)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("error = %v, want %s", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyPayloadAllowsSignedRollback(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["sequence"] = int64(40)
		doc["rollback"] = map[string]any{
			"previous_sequence": int64(40),
			"reason":            "bad release",
		}
	}, bundleBytes, wasmBytes)

	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		CurrentSequence: 42,
		TrustedRoots:    signer.roots(t),
	}); err != nil {
		t.Fatalf("signed rollback rejected: %v", err)
	}
}

func TestCarrierRoundTrip(t *testing.T) {
	payload := []byte("compressed-bundle-bytes")
	carrier := BuildCarrier(payload)
	got, err := ExtractBundleFromCarrier(carrier)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("carrier round trip = %q, want %q", got, payload)
	}
	if _, err := ExtractBundleFromCarrier([]byte("not wasm")); err == nil {
		t.Fatal("accepted non-wasm carrier")
	}
}

// makeBundleTarGz builds a tar.gz of a new bundle payload wrapped in a
// versioned directory, mirroring deployment/release/build-self-contained-cli.mjs.
func makeBundleTarGz(t *testing.T, version string, files map[string]string) []byte {
	t.Helper()
	wrapper := fmt.Sprintf("spacedatanetwork-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)

	type artifact struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	var artifacts []artifact
	for path, contents := range files {
		artifacts = append(artifacts, artifact{Path: path, SHA256: sha256Hex([]byte(contents)), Size: len(contents)})
	}
	manifest := map[string]any{
		"schema":    "org.spacedatanetwork.bundle.v1",
		"version":   version,
		"channel":   "beta",
		"signature": "test-signature",
		"artifacts": artifacts,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name, contents string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range files {
		write(wrapper+"/"+path, contents)
	}
	write(wrapper+"/manifest.json", string(manifestBytes))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeBundleZip(t *testing.T, version string, files map[string]string) []byte {
	t.Helper()
	wrapper := fmt.Sprintf("spacedatanetwork-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)

	type artifact struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	var artifacts []artifact
	for path, contents := range files {
		artifacts = append(artifacts, artifact{Path: path, SHA256: sha256Hex([]byte(contents)), Size: len(contents)})
	}
	manifest := map[string]any{
		"schema":    "org.spacedatanetwork.bundle.v1",
		"version":   version,
		"channel":   "beta",
		"signature": "test-signature",
		"artifacts": artifacts,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, contents string) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o755)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range files {
		write(wrapper+"/"+path, contents)
	}
	write(wrapper+"/manifest.json", string(manifestBytes))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func setupBundleRoot(t *testing.T, signer *testSigner) (Paths, string) {
	t.Helper()
	root := t.TempDir()
	paths := PathsFor(root)
	for _, dir := range []string{"bin", "runtime"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "spacedatanetwork"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"schema":"org.spacedatanetwork.bundle.v1","version":"1.0.0","channel":"beta","signature":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Trust), 0o755); err != nil {
		t.Fatal(err)
	}
	rootsJSON, err := json.Marshal(signer.roots(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Trust, rootsJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	return paths, root
}

func stageSignedUpdate(t *testing.T, paths Paths, signer *testSigner, version string, files map[string]string) *StagedUpdate {
	t.Helper()
	bundleBytes := makeBundleTarGz(t, version, files)
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["version"] = version
		doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
		doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
		doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
	}, bundleBytes, wasmBytes)
	staged, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestStageAndApplySwapsBundleAndKeepsRollback(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	// Local user data outside the payload must survive the swap.
	if err := os.WriteFile(filepath.Join(root, "local-notes.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
		"runtime/asset.txt":    "new-asset",
	})

	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Version != "9.9.9" || result.Sequence != 42 {
		t.Fatalf("unexpected apply result: %+v", result)
	}

	newBinary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(newBinary) != "new-binary" {
		t.Fatalf("binary = %q, want new-binary", newBinary)
	}
	oldBinary, err := os.ReadFile(filepath.Join(result.RollbackPath, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(oldBinary) != "old-binary" {
		t.Fatalf("rollback binary = %q, want old-binary", oldBinary)
	}
	if _, err := os.Stat(filepath.Join(root, "local-notes.txt")); err != nil {
		t.Fatalf("local data was not preserved: %v", err)
	}

	state, err := LoadState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != 42 || state.Version != "9.9.9" {
		t.Fatalf("state = %+v", state)
	}
	if entries, _ := os.ReadDir(paths.Staged); len(entries) != 0 {
		t.Fatalf("staged dir not cleaned: %v", entries)
	}

	// A second apply of the same sequence must be rejected by state policy.
	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "newer-binary",
	})
	if _, err := Apply(paths, ApplyOptions{}); err == nil {
		t.Fatal("re-applying same sequence succeeded, want sequence rejection")
	}
}

func TestStageAndApplySupportsZipBundleFormat(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	bundleBytes := makeBundleZip(t, "9.9.10", map[string]string{
		"bin/spacedatanetwork": "new-zip-binary",
		"runtime/asset.txt":    "zip-asset",
	})
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["version"] = "9.9.10"
		doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
		doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
		doc["bundle"].(map[string]any)["format"] = "zip"
		doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
	}, bundleBytes, wasmBytes)

	staged, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(staged.BundleFile) != "bundle.zip" {
		t.Fatalf("BundleFile = %s, want bundle.zip", staged.BundleFile)
	}
	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Version != "9.9.10" {
		t.Fatalf("Version = %s, want 9.9.10", result.Version)
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "new-zip-binary" {
		t.Fatalf("binary = %q, want new-zip-binary", binary)
	}
}

func TestApplyDryRunDoesNotTouchBundle(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})

	result, err := Apply(paths, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun {
		t.Fatal("expected dry run result")
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "old-binary" {
		t.Fatalf("dry run modified bundle: %q", binary)
	}
}

func TestApplyRejectsBundleWithProtectedEntries(t *testing.T) {
	signer := newTestSigner(t)
	paths, _ := setupBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork":    "new-binary",
		"trust/update-roots.json": `{"evil":"key"}`,
	})

	_, err := Apply(paths, ApplyOptions{})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("protected entry")) {
		t.Fatalf("expected protected entry rejection, got %v", err)
	}
}

func TestApplyWithoutStagedUpdateFails(t *testing.T) {
	signer := newTestSigner(t)
	paths, _ := setupBundleRoot(t, signer)
	_, err := Apply(paths, ApplyOptions{})
	if err == nil || err.Error() != "no staged update is available" {
		t.Fatalf("error = %v, want no staged update is available", err)
	}
}

func TestExtractTarRejectsPathEscape(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("oops")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	archive := filepath.Join(dir, "bundle.tar.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractBundleArchive(archive, "tar.gz", filepath.Join(dir, "out")); err == nil {
		t.Fatal("accepted path escape in bundle archive")
	}
}
