package update

// A repeated object key must never reach a signed-document parser.
//
// SECURITY FINDING (review of PR 174, 2026-11-15; reproduces on main).
// CanonicalManifestBytes decodes into map[string]any, where a repeated key keeps
// only its last value, so the bytes the signature covers ignore every earlier
// copy. ParseManifest decodes into the Manifest struct, and encoding/json
// REUSES an existing map or struct when a key repeats, so the struct merges the
// copies. Whoever serves a manifest (the feed host, or anyone who controls it)
// could put `"trust_roots":{"<their id>":"<their key>"},` in front of a signed
// rotation manifest: the signature still verified, Apply installed their root
// beside the signed one, and from then on their key installed code.
//
// Every signed document (manifest, signal) and the trust file are now refused
// when any object, at any depth, names a key twice, or names two keys that
// encoding/json would match to the same struct field (case-folded).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

// jsonEscape is one backslash, so the documents below can spell a key with a
// JSON escape sequence (the decoder unescapes it before keys are compared).
const jsonEscape = "\x5c"

// The reviewer's probe, as a regression test: a feed host prepends a second
// trust_roots to a signed rotation manifest.
func TestARepeatedTrustRootsKeyCannotAddARootToASignedRotation(t *testing.T) {
	current := newTestSigner(t)
	paths, _ := setupBundleRoot(t, current)
	distribution := newTestSigner(t)
	distribution.keyID = RootKeyID(distribution.publicKey)
	attacker := newTestSigner(t)
	attacker.keyID = RootKeyID(attacker.publicKey)

	build := func(signer *testSigner, updateID, version string, sequence int64, mutate func(map[string]any)) ([]byte, []byte) {
		t.Helper()
		bundleBytes := makeBundleTarGz(t, version, map[string]string{"bin/spacedatanetwork": "binary-" + version})
		wasmBytes := BuildCarrier(bundleBytes)
		manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
			doc["update_id"], doc["version"], doc["sequence"] = updateID, version, sequence
			if mutate != nil {
				mutate(doc)
			}
		}, bundleBytes, wasmBytes)
		return manifestBytes, wasmBytes
	}

	signed, wasm := build(current, "move-roots", "3.0.0", 100, func(doc map[string]any) {
		doc["trust_roots"] = map[string]any{distribution.keyID: distribution.publicKeyBase64SPKI(t)}
	})
	if signed[0] != '{' {
		t.Fatalf("signed manifest does not start with an object: %q", signed[:1])
	}
	injected := fmt.Sprintf(`{"trust_roots":{%q:%q},`, attacker.keyID, attacker.publicKeyBase64SPKI(t))
	tampered := append([]byte(injected), signed[1:]...)
	trustBefore, err := os.ReadFile(paths.Trust)
	if err != nil {
		t.Fatal(err)
	}

	// The install path refuses the tampered bytes before anything is staged.
	if _, err := Stage(paths, tampered, wasm, HostVerifyOptions(current.roots(t), 0, time.Now()), nil); err == nil {
		if _, applyErr := Apply(paths, ApplyOptions{UpdateID: "move-roots"}); applyErr == nil {
			after, _ := os.ReadFile(paths.Trust)
			t.Fatalf("the tampered rotation staged and applied; the trust file is now %s (attacker root %s)",
				strings.TrimSpace(string(after)), attacker.keyID)
		}
		t.Fatal("the tampered rotation staged")
	} else if !errors.Is(err, ErrRepeatedKey) {
		t.Fatalf("Stage(tampered) = %v, want ErrRepeatedKey", err)
	}
	if trustAfter, _ := os.ReadFile(paths.Trust); !bytes.Equal(trustAfter, trustBefore) {
		t.Fatalf("a refused stage changed the trust file: %s", trustAfter)
	}
	// And so does every parser of a signed manifest, naming the key.
	if _, err := CanonicalManifestBytes(tampered); !errors.Is(err, ErrRepeatedKey) || !strings.Contains(err.Error(), `"trust_roots"`) {
		t.Fatalf("CanonicalManifestBytes(tampered) = %v, want ErrRepeatedKey naming trust_roots", err)
	}
	if _, err := ParseManifest(tampered); !errors.Is(err, ErrRepeatedKey) {
		t.Fatalf("ParseManifest(tampered) = %v, want ErrRepeatedKey", err)
	}

	// The release as signed still rotates the roots to exactly what it signed.
	if _, err := Stage(paths, signed, wasm, HostVerifyOptions(current.roots(t), 0, time.Now()), nil); err != nil {
		t.Fatalf("stage the signed rotation: %v", err)
	}
	if _, err := Apply(paths, ApplyOptions{UpdateID: "move-roots"}); err != nil {
		t.Fatalf("apply the signed rotation: %v", err)
	}
	installed, err := LoadTrustRoots(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[distribution.keyID] == "" {
		t.Fatalf("installed roots = %v, want exactly the signed distribution root %s", installed, distribution.keyID)
	}

	// So the attacker's key installs nothing.
	evil, evilWasm := build(attacker, "attacker-build", "6.6.6", 101, nil)
	if _, err := Stage(paths, evil, evilWasm, HostVerifyOptions(installed, 100, time.Now()), nil); err == nil || !strings.Contains(err.Error(), "untrusted update signing key") {
		t.Fatalf("stage an attacker-signed build after the rotation = %v, want untrusted update signing key", err)
	}
}

// A signal is a signed document too: the same rule, before anything else.
func TestARepeatedKeyInASignedSignalIsRefused(t *testing.T) {
	_, priv, roots, keyID := signalTestKey(t)
	signed := signTestSignal(t, priv, keyID, nil, sigdomain.DomainUpdateSignalV1)
	signal, err := ParseSignal(signed)
	if err != nil {
		t.Fatalf("ParseSignal(signed) = %v", err)
	}
	if err := signal.Verify(hostOpts(roots)); err != nil {
		t.Fatalf("the untampered signal does not verify: %v", err)
	}
	for name, prefix := range map[string]string{
		"signing":      `{"signing":{"public_key":"AAAA"},`,
		"target":       `{"target":{"kind":"ui-bundle"},`,
		"manifest_url": `{"manifest_url":"https://elsewhere.example/m.json",`,
		"escaped name": `{"manifest` + jsonEscape + `u005furl":"https://elsewhere.example/m.json",`,
		"case-folded":  `{"Manifest_URL":"https://elsewhere.example/m.json",`,
	} {
		tampered := append([]byte(prefix), signed[1:]...)
		if _, err := ParseSignal(tampered); !errors.Is(err, ErrRepeatedKey) {
			t.Errorf("%s: ParseSignal(tampered) = %v, want ErrRepeatedKey", name, err)
		}
		if _, err := SignalStatement(tampered); !errors.Is(err, ErrRepeatedKey) {
			t.Errorf("%s: SignalStatement(tampered) = %v, want ErrRepeatedKey", name, err)
		}
	}
}

// The rule at every depth, and what it leaves alone.
func TestStrictDocumentRefusesRepeatedKeysAtAnyDepth(t *testing.T) {
	refused := map[string]string{
		"top level":                   `{"a":1,"b":2,"a":3}`,
		"nested object":               `{"a":{"x":1,"y":2,"x":1}}`,
		"object inside an array":      `{"a":[{"x":1},{"y":1,"y":2}]}`,
		"deep":                        `{"a":[[{"b":{"c":{"d":1,"d":1}}}]]}`,
		"repeated object values":      `{"m":{"k":"v"},"m":{"k":"v"}}`,
		"escaped spelling":            `{"key_id":"a","key` + jsonEscape + `u005fid":"b"}`,
		"case-folded":                 `{"version":"1","Version":"2"}`,
		"long s folds to s":           `{"schema":"a","` + jsonEscape + `u017fchema":"b"}`,
		"kelvin sign folds to k":      `{"kind":"a","` + jsonEscape + `u212aind":"b"}`,
		"top-level array, inner dup":  `[{"a":1,"a":2}]`,
		"trailing value":              `{"a":1} {"b":2}`,
		"trailing garbage":            `{"a":1} x`,
		"second document after space": "{\"a\":1}\n\n{}",
	}
	for name, doc := range refused {
		err := assertStrictJSONDocument([]byte(doc))
		if err == nil {
			t.Errorf("%s: %s accepted", name, doc)
			continue
		}
		if strings.HasPrefix(name, "trailing") || strings.HasPrefix(name, "second") {
			if !errors.Is(err, ErrTrailingData) {
				t.Errorf("%s: %v, want ErrTrailingData", name, err)
			}
		} else if !errors.Is(err, ErrRepeatedKey) {
			t.Errorf("%s: %v, want ErrRepeatedKey", name, err)
		}
	}
	accepted := map[string]string{
		"same key in sibling objects": `{"a":{"x":1},"b":{"x":2}}`,
		"same key in array elements":  `{"r":[{"key_id":"a"},{"key_id":"b"}]}`,
		"same key at two depths":      `{"x":{"x":{"x":1}}}`,
		"key equal to a string value": `{"a":"a","b":"a"}`,
		"empty object and array":      `{"a":{},"b":[]}`,
		"trailing whitespace":         "{\"a\":1}\n \t\r\n",
		"leading whitespace":          " \n{\"a\":1}",
		"scalars":                     `{"n":1.5e3,"t":true,"f":false,"z":null,"s":"` + jsonEscape + `u00e9"}`,
		"key that only looks similar": `{"key_id":"a","key-id":"b","keyid":"c"}`,
	}
	for name, doc := range accepted {
		if err := assertStrictJSONDocument([]byte(doc)); err != nil {
			t.Errorf("%s: %s refused: %v", name, doc, err)
		}
	}
	for name, doc := range map[string]string{"empty": "", "whitespace": "  ", "truncated": `{"a":`, "unclosed": `{"a":1`} {
		if err := assertStrictJSONDocument([]byte(doc)); err == nil {
			t.Errorf("%s: %q accepted", name, doc)
		}
	}
}

// The trust file: a repeated key used to keep only its last value (so a file
// could list the SEC-001 key and have it silently dropped, or the reverse), and
// anything after the first JSON value was ignored.
func TestTrustRootsFileRefusesARepeatedKeyAndTrailingData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bundle")
	paths := PathsFor(root)
	if err := os.MkdirAll(filepath.Dir(paths.Trust), 0o755); err != nil {
		t.Fatal(err)
	}
	demo := fmt.Sprintf("%q:%q", demoRootKeyID, demoRootSPKI)
	for name, content := range map[string]string{
		"repeated id":         fmt.Sprintf(`{%q:%q,%s}`, demoRootKeyID, "AAAA", demo),
		"trailing value":      "{" + demo + `} {"x":1}`,
		"trailing garbage":    "{" + demo + "} garbage",
		"case-folded id pair": fmt.Sprintf(`{%s,%q:%q}`, demo, strings.ToUpper(demoRootKeyID), demoRootSPKI),
	} {
		if err := os.WriteFile(paths.Trust, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if roots, err := LoadTrustRoots(paths); err == nil {
			t.Errorf("%s: trust file %s loaded as %v", name, content, roots)
		}
	}
	if err := os.WriteFile(paths.Trust, []byte("{"+demo+"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := LoadTrustRoots(paths)
	if err != nil || len(roots) != 1 || roots[demoRootKeyID] != demoRootSPKI {
		t.Fatalf("a plain demo trust file = %v, %v", roots, err)
	}
}
