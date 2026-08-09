package update

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// The installer's half of the silent-revert guard (2026-08-09). The ancestry
// TEST runs at publish time, where a git repository exists; what a fleet host
// enforces is the publisher's signed verdict. These tests pin that enforcement:
// a declared rollback is refused by default, accepted when the operator says
// so, and every other manifest — including every one published before the field
// existed — is unaffected.

func lineageVerifyOptions(s *testSigner, t *testing.T) VerifyOptions {
	return VerifyOptions{
		Platform:     runtime.GOOS,
		Arch:         runtime.GOARCH,
		TrustedRoots: s.roots(t),
		Now:          time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	}
}

func TestDeclaredRollbackIsRefusedUnlessTheOperatorSaysSo(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle payload")
	wasmBytes := []byte("carrier payload")

	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["provenance"] = map[string]any{
			"source_commit":     "aaaaaaaa1111",
			"supersedes_commit": "bbbbbbbb2222",
			"lineage":           LineageRollback,
			"rollback_reason":   "per-call change regressed host-01 boot",
		}
	}, bundleBytes, wasmBytes)

	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}

	// Default: refused, and the message has to be usable — it must name the
	// reason and the remedy, or an operator cannot act on it.
	opts := lineageVerifyOptions(signer, t)
	_, err = manifest.VerifyPayload(wasmBytes, bundleBytes, opts)
	if err == nil {
		t.Fatal("a declared rollback was accepted with AllowRollback=false")
	}
	for _, want := range []string{"ROLLBACK", "per-call change regressed host-01 boot", "--allow-rollback", "aaaaaaaa1111", "bbbbbbbb2222"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rollback refusal does not mention %q: %v", want, err)
		}
	}

	// Explicit acceptance: installs.
	opts.AllowRollback = true
	result, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts)
	if err != nil {
		t.Fatalf("a declared rollback was refused even with AllowRollback=true: %v", err)
	}
	if result.UpdateID != "cli-beta-0001" {
		t.Fatalf("unexpected update id %q", result.UpdateID)
	}
}

func TestDescendantAndAbsentProvenancePassUntouched(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle payload")
	wasmBytes := []byte("carrier payload")

	cases := map[string]func(map[string]any){
		// Every manifest on the live feed today: no provenance at all. The gate
		// must not strand the fleet on artifacts that predate it.
		"no provenance": nil,
		"descendant": func(doc map[string]any) {
			doc["provenance"] = map[string]any{
				"source_commit":     "cccccccc3333",
				"supersedes_commit": "bbbbbbbb2222",
				"lineage":           LineageDescendant,
			}
		},
		"initial": func(doc map[string]any) {
			doc["provenance"] = map[string]any{"source_commit": "dddddddd4444", "lineage": LineageInitial}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := ParseManifest(signer.signedManifest(t, mutate, bundleBytes, wasmBytes))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, lineageVerifyOptions(signer, t)); err != nil {
				t.Fatalf("%s manifest was refused: %v", name, err)
			}
		})
	}
}

func TestProvenanceIsCoveredBySignature(t *testing.T) {
	// The whole design rests on this: the installer trusts the lineage verdict
	// because the same signature that authorizes the bytes authorizes the
	// claim. If provenance could be edited in flight, a rollback could be
	// relabelled a descendant by anyone on the path.
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle payload")
	wasmBytes := []byte("carrier payload")

	signed := signer.signedManifest(t, func(doc map[string]any) {
		doc["provenance"] = map[string]any{"source_commit": "aaaaaaaa1111", "lineage": LineageRollback}
	}, bundleBytes, wasmBytes)

	tampered := strings.Replace(string(signed), `"lineage":"rollback"`, `"lineage":"descenda"`, 1)
	if tampered == string(signed) {
		t.Fatalf("test did not find the lineage field to tamper with in %s", signed)
	}

	manifest, err := ParseManifest([]byte(tampered))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, lineageVerifyOptions(signer, t)); err == nil {
		t.Fatal("a manifest with a rewritten lineage verified — provenance is not covered by the signature")
	} else if !strings.Contains(err.Error(), "invalid update signature") {
		t.Fatalf("expected a signature failure, got: %v", err)
	}
}

func TestProviderFeedEntryMustAgreeWithTheManifestItResolvesTo(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle payload")
	wasmBytes := []byte("carrier payload")
	manifest, err := ParseManifest(signer.signedManifest(t, nil, bundleBytes, wasmBytes))
	if err != nil {
		t.Fatal(err)
	}

	good := ProviderFeedUpdate{
		UpdateID:   "cli-beta-0001",
		Version:    "9.9.9",
		Sequence:   42,
		BundleHash: sha256Hex(bundleBytes),
		BundleSize: int64(len(bundleBytes)),
		WasmHash:   sha256Hex(wasmBytes),
		WasmSize:   int64(len(wasmBytes)),
	}
	if err := good.AssertMatchesPayload(manifest, len(wasmBytes)); err != nil {
		t.Fatalf("a consistent index entry was rejected: %v", err)
	}

	// An index that omits the advisory fields is still valid: feeds published
	// before they existed must keep working.
	sparse := ProviderFeedUpdate{UpdateID: "cli-beta-0001"}
	if err := sparse.AssertMatchesPayload(manifest, len(wasmBytes)); err != nil {
		t.Fatalf("a pre-existing index entry without hashes was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*ProviderFeedUpdate){
		"update id":   func(u *ProviderFeedUpdate) { u.UpdateID = "cli-beta-0002" },
		"version":     func(u *ProviderFeedUpdate) { u.Version = "9.9.8" },
		"sequence":    func(u *ProviderFeedUpdate) { u.Sequence = 43 },
		"bundle hash": func(u *ProviderFeedUpdate) { u.BundleHash = strings.Repeat("0", 64) },
		"bundle size": func(u *ProviderFeedUpdate) { u.BundleSize = 1 },
		"wasm hash":   func(u *ProviderFeedUpdate) { u.WasmHash = strings.Repeat("0", 64) },
		"wasm size":   func(u *ProviderFeedUpdate) { u.WasmSize = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			entry := good
			mutate(&entry)
			if err := entry.AssertMatchesPayload(manifest, len(wasmBytes)); err == nil {
				t.Fatalf("index/manifest %s divergence was accepted", name)
			}
		})
	}
}

// TestCanonicalizationPreservesUnmodelledFields pins the single assumption the
// whole lineage design rests on.
//
// The verdict is trustworthy only because it is INSIDE the signed document. The
// chain that keeps it there is: the publisher writes `provenance`; `update
// sign-manifest` decodes the manifest into a generic map and re-marshals THAT
// map (so fields the Go structs do not model survive the round trip to the
// signing key); the node canonicalizes the RAW submitted bytes; and this
// function canonicalizes the raw bytes again on the host.
//
// Every link is generic-over-JSON on purpose. The one that looks most like
// tidy-up-able code is the CLI's `map[string]any` decode — re-typing it into
// the Manifest struct would silently drop provenance, and a rollback would
// arrive looking like an ordinary update. This test fails the moment
// canonicalization stops carrying unmodelled fields, which is the same
// property from the other end.
func TestCanonicalizationPreservesUnmodelledFields(t *testing.T) {
	raw := []byte(`{
		"schema": "org.spacedatanetwork.update.v1",
		"version": "1.0.0",
		"provenance": {"lineage": "rollback", "source_commit": "aaaa1111", "binary_sha256": "deadbeef"},
		"not_a_field_any_struct_models": {"z": 1, "a": 2},
		"signing": {"signature": "dropped", "key_id": "k1", "algorithm": "Ed25519"}
	}`)
	canonical, err := CanonicalManifestBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := string(canonical)

	// Present, key-sorted, and with signing.signature removed.
	want := `{"not_a_field_any_struct_models":{"a":2,"z":1},` +
		`"provenance":{"binary_sha256":"deadbeef","lineage":"rollback","source_commit":"aaaa1111"},` +
		`"schema":"org.spacedatanetwork.update.v1","signing":{"algorithm":"Ed25519","key_id":"k1"},"version":"1.0.0"}`
	if got != want {
		t.Fatalf("canonical bytes dropped or reordered unmodelled fields:\n got  %s\n want %s", got, want)
	}
}
