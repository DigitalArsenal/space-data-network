package update

// The update roots every install trusts, and the one key none may trust.
//
// SECURITY FINDING (2026-11-15). deployment/release/fleet-trust-roots.json
// shipped one root, d4a971a7e534: the raw key 0d80e1fd…fc8c, which is the node
// identity leaked in SEC-001 (internal/sec001) and "was derived from a
// published seed". Every bundle up to v1.1.1 trusted an update root anyone can
// sign for. The roots are now the demo key 12cb25de07c0, compiled in as a
// fallback, and a SEC-001 key is refused wherever roots are loaded.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	demoRootKeyID = "12cb25de07c0"
	demoRootSPKI  = "MCowBQYDK2VwAyEAi0cVzY4ICSRzddR7HYHKgEPXmXXFh1uvFizfMb/V5ZM="
	// The SEC-001 leaked node key as it was shipped: public values only.
	leakedRootKeyID = "d4a971a7e534"
	leakedRootSPKI  = "MCowBQYDK2VwAyEADYDh/V+aTjTf3zag4VK9maZc//i8xsqydXtISuRC/Iw="
)

func readRootsFile(t *testing.T, path string) (TrustedRoots, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var roots TrustedRoots
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&roots); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return roots, data
}

// The compiled-in roots are the fleet roots, byte for byte, and both are
// exactly the demo root.
func TestDefaultUpdateRootsMatchTheFleetRoots(t *testing.T) {
	fleet, fleetBytes := readRootsFile(t, filepath.Join("..", "..", "..", "deployment", "release", "fleet-trust-roots.json"))
	compiled, compiledBytes := readRootsFile(t, "default_update_roots.json")
	if !bytes.Equal(fleetBytes, compiledBytes) {
		t.Fatalf("sdn-server/internal/update/default_update_roots.json differs from deployment/release/fleet-trust-roots.json:\n%s\nvs\n%s", compiledBytes, fleetBytes)
	}
	want := TrustedRoots{demoRootKeyID: demoRootSPKI}
	for name, roots := range map[string]TrustedRoots{"fleet-trust-roots.json": fleet, "default_update_roots.json": compiled} {
		if len(roots) != 1 || roots[demoRootKeyID] != demoRootSPKI {
			t.Fatalf("%s = %v, want exactly %v", name, roots, want)
		}
		if _, burned := roots[leakedRootKeyID]; burned {
			t.Fatalf("%s still trusts the SEC-001 leaked key %s", name, leakedRootKeyID)
		}
	}
	key, err := decodeTrustedPublicKey(demoRootSPKI)
	if err != nil {
		t.Fatal(err)
	}
	if got := RootKeyID(key); got != demoRootKeyID {
		t.Fatalf("the demo root is listed as %s but its key id is %s", demoRootKeyID, got)
	}
}

func writeRoots(t *testing.T, path string, roots any) {
	t.Helper()
	data, err := json.Marshal(roots)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The compiled-in roots are used ONLY when the bundle's trust file is absent.
// A present file wins — even one that names other roots, so a rotation can
// drop the demo root — and a broken present file is an error, not a reason
// to fall back.
func TestLoadTrustRootsFallsBackToTheCompiledInRootsOnlyWhenTheFileIsAbsent(t *testing.T) {
	t.Setenv(TrustRootsEnv, "")

	paths := PathsFor(t.TempDir())
	roots, err := LoadTrustRoots(paths)
	if err != nil {
		t.Fatalf("a bundle with no trust file: %v, want the compiled-in roots", err)
	}
	if len(roots) != 1 || roots[demoRootKeyID] != demoRootSPKI {
		t.Fatalf("a bundle with no trust file loaded %v, want the demo root", roots)
	}

	other := newTestSigner(t)
	other.keyID = RootKeyID(other.publicKey)
	writeRoots(t, paths.Trust, other.roots(t))
	roots, err = LoadTrustRoots(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[other.keyID] == "" {
		t.Fatalf("a present trust file loaded %v, want only its own root %s", roots, other.keyID)
	}

	writeRoots(t, paths.Trust, map[string]string{})
	if roots, err := LoadTrustRoots(paths); err == nil {
		t.Fatalf("an empty present trust file loaded %v, want an error (no fallback)", roots)
	}
	if err := os.WriteFile(paths.Trust, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if roots, err := LoadTrustRoots(paths); err == nil {
		t.Fatalf("a corrupt present trust file loaded %v, want an error (no fallback)", roots)
	}

	t.Setenv(TrustRootsEnv, filepath.Join(t.TempDir(), "named-but-missing.json"))
	if roots, err := LoadTrustRoots(PathsFor(t.TempDir())); err == nil {
		t.Fatalf("SDN_UPDATE_TRUST_ROOTS naming a missing file loaded %v, want an error", roots)
	}
}

// The compiled-in roots are for a bundle that never had a trust file, and for
// nothing else: not for a path where something exists but cannot be read (a
// dangling symlink), and not, after a release replaced the roots, for a file
// or directory that has gone missing. Otherwise losing the file would bring
// back a compiled-in root the rotation dropped (for example a retired demo
// key), with no signed release saying so.
func TestCompiledInRootsDoNotComeBackForAPresentPathOrAfterARotation(t *testing.T) {
	t.Setenv(TrustRootsEnv, "")

	t.Run("dangling symlink", func(t *testing.T) {
		paths := PathsFor(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(paths.Trust), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "nowhere.json"), paths.Trust); err != nil {
			t.Fatal(err)
		}
		if roots, err := LoadTrustRoots(paths); err == nil {
			t.Fatalf("a dangling symlink at the trust file loaded %v, want an error", roots)
		}
	})

	t.Run("after a rotation", func(t *testing.T) {
		current := newTestSigner(t)
		paths, _ := setupBundleRoot(t, current)
		distribution := newTestSigner(t)
		distribution.keyID = RootKeyID(distribution.publicKey)
		bundleBytes := makeBundleTarGz(t, "3.0.0", map[string]string{"bin/spacedatanetwork": "binary-3.0.0"})
		wasmBytes := BuildCarrier(bundleBytes)
		manifestBytes := current.signedManifest(t, func(doc map[string]any) {
			doc["update_id"], doc["version"], doc["sequence"] = "move-roots", "3.0.0", int64(100)
			doc["trust_roots"] = map[string]any{distribution.keyID: distribution.publicKeyBase64SPKI(t)}
		}, bundleBytes, wasmBytes)
		if _, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(current.roots(t), 0, time.Now()), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(paths, ApplyOptions{UpdateID: "move-roots"}); err != nil {
			t.Fatal(err)
		}
		state, err := LoadState(paths)
		if err != nil || state.TrustRootsFrom != "move-roots" {
			t.Fatalf("state after the rotation: trust_roots_from = %q, %v; want move-roots", state.TrustRootsFrom, err)
		}

		if err := os.Remove(paths.Trust); err != nil {
			t.Fatal(err)
		}
		if roots, err := LoadTrustRoots(paths); err == nil || !strings.Contains(err.Error(), "move-roots") {
			t.Fatalf("a rotated bundle with its trust file deleted: %v, %v; want an error naming the rotation", roots, err)
		}
		if err := os.RemoveAll(filepath.Dir(paths.Trust)); err != nil {
			t.Fatal(err)
		}
		if roots, err := LoadTrustRoots(paths); err == nil {
			t.Fatalf("a rotated bundle with its trust directory deleted loaded %v, want an error", roots)
		}

		// Rolling the binary back keeps the record, as it keeps the roots.
		writeRoots(t, paths.Trust, distribution.roots(t))
		if _, err := RollbackLast(paths, RollbackOptions{Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		if state, err := LoadState(paths); err != nil || state.TrustRootsFrom != "move-roots" {
			t.Fatalf("state after a rollback: trust_roots_from = %q, %v; want move-roots kept", state.TrustRootsFrom, err)
		}
		if err := os.Remove(paths.Trust); err != nil {
			t.Fatal(err)
		}
		if roots, err := LoadTrustRoots(paths); err == nil {
			t.Fatalf("after a rollback, a missing trust file loaded %v, want an error", roots)
		}
	})

	t.Run("an unreadable state", func(t *testing.T) {
		paths := PathsFor(t.TempDir())
		if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.State, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if roots, err := LoadTrustRoots(paths); err == nil {
			t.Fatalf("no trust file and an unreadable state loaded %v, want an error", roots)
		}
	})
}

func requireSEC001Refusal(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "SEC-001") {
		t.Fatalf("%s: err = %v, want a refusal naming SEC-001", what, err)
	}
}

// A SEC-001 key is refused as an update root wherever roots are loaded: the
// trust file, the override file, under its own key id or another, and as a
// root a signed release carries in trust_roots.
func TestSEC001KeyIsRefusedWhereverRootsAreLoaded(t *testing.T) {
	t.Setenv(TrustRootsEnv, "")

	t.Run("trust file", func(t *testing.T) {
		paths := PathsFor(t.TempDir())
		writeRoots(t, paths.Trust, TrustedRoots{leakedRootKeyID: leakedRootSPKI})
		_, err := LoadTrustRoots(paths)
		requireSEC001Refusal(t, "a trust file holding the leaked root", err)
	})
	t.Run("trust file, beside the demo root", func(t *testing.T) {
		paths := PathsFor(t.TempDir())
		writeRoots(t, paths.Trust, TrustedRoots{leakedRootKeyID: leakedRootSPKI, demoRootKeyID: demoRootSPKI})
		_, err := LoadTrustRoots(paths)
		requireSEC001Refusal(t, "a trust file holding the leaked root beside the demo root", err)
	})
	t.Run("leaked key under another id", func(t *testing.T) {
		paths := PathsFor(t.TempDir())
		writeRoots(t, paths.Trust, TrustedRoots{"fleet-root": leakedRootSPKI})
		_, err := LoadTrustRoots(paths)
		requireSEC001Refusal(t, "the leaked key listed under another id", err)
	})
	t.Run("leaked key as raw hex", func(t *testing.T) {
		paths := PathsFor(t.TempDir())
		writeRoots(t, paths.Trust, TrustedRoots{"hex-root": "0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c"})
		_, err := LoadTrustRoots(paths)
		requireSEC001Refusal(t, "the leaked key as raw hex", err)
	})
	t.Run("override file", func(t *testing.T) {
		override := filepath.Join(t.TempDir(), "roots.json")
		writeRoots(t, override, TrustedRoots{leakedRootKeyID: leakedRootSPKI})
		t.Setenv(TrustRootsEnv, override)
		_, err := LoadTrustRoots(PathsFor(t.TempDir()))
		requireSEC001Refusal(t, "SDN_UPDATE_TRUST_ROOTS holding the leaked root", err)
	})
	t.Run("roots handed in by a caller", func(t *testing.T) {
		// A manifest that claims the leaked key, checked against roots that
		// list it: refused for what the key is, before any signature check.
		doc := map[string]any{
			"schema": ManifestSchema, "update_id": "u", "version": "1", "sequence": int64(2), "channel": "release",
			"created_at": "2026-11-01T00:00:00Z", "expires_at": "2030-01-01T00:00:00Z",
			"target":  map[string]any{"platform": "linux", "arch": "amd64", "kind": "cli-bundle"},
			"bundle":  map[string]any{"hash": strings.Repeat("a", 64), "size": 1, "format": "tar.gz"},
			"wasm":    map[string]any{"hash": strings.Repeat("b", 64)},
			"signing": map[string]any{"key_id": leakedRootKeyID, "algorithm": "Ed25519", "signature": "AAAA"},
		}
		manifest, err := ParseManifest(sortedJSON(t, doc))
		if err != nil {
			t.Fatal(err)
		}
		requireSEC001Refusal(t, "a manifest under the leaked key", manifest.VerifySignature(TrustedRoots{leakedRootKeyID: leakedRootSPKI}))
	})
	t.Run("trust_roots in a signed release", func(t *testing.T) {
		signer := newTestSigner(t)
		paths, _ := setupBundleRoot(t, signer)
		bundleBytes := makeBundleTarGz(t, "2.0.0", map[string]string{"bin/spacedatanetwork": "binary-2.0.0"})
		wasmBytes := BuildCarrier(bundleBytes)
		for name, roots := range map[string]map[string]any{
			"leaked root alone":          {leakedRootKeyID: leakedRootSPKI},
			"leaked root beside demo":    {leakedRootKeyID: leakedRootSPKI, demoRootKeyID: demoRootSPKI},
			"leaked key under demo's id": {demoRootKeyID: leakedRootSPKI},
		} {
			manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
				doc["version"] = "2.0.0"
				doc["trust_roots"] = roots
			}, bundleBytes, wasmBytes)
			_, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()), nil)
			requireSEC001Refusal(t, "a release whose trust_roots are "+name, err)
		}
		// The control: the same release carrying only the demo root stages.
		manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
			doc["version"] = "2.0.0"
			doc["trust_roots"] = map[string]any{demoRootKeyID: demoRootSPKI}
		}, bundleBytes, wasmBytes)
		if _, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()), nil); err != nil {
			t.Fatalf("a release carrying only the demo root: %v", err)
		}
	})
}
