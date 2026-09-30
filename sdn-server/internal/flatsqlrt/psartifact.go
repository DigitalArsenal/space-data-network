package flatsqlrt

// psartifact.go: the embedded partition-store engine (design §1 "Published
// deps", A34, T6). flatsql-ps-threads.wasm is the file of the PUBLISHED
// flatsql npm release below, byte for byte: the release workflow
// (flatsql .github/workflows/npm-publish.yml) rebuilds it and refuses to
// publish unless the build equals the committed file, and its sha256 is in
// the package's wasm/integrity.json. TestEmbeddedPSThreadsArtifact holds the
// embedded bytes to that hash, so a hand-built artifact cannot slip in.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed flatsql-ps-threads.wasm
var psThreadsWasm []byte

const (
	// PSThreadsPackage is the npm release the artifact comes from.
	PSThreadsPackage = "flatsql@3.6.0"
	// PSThreadsGitHead is that release's gitHead (flatsql main).
	PSThreadsGitHead = "7df7140c88b0bfd23330a608bdb906d6c8cc16e6"
	// PSThreadsSHA256 is the artifact's sha256 (the package's integrity.json).
	PSThreadsSHA256 = "87ea0c727eb3c0b889a2d3fb41f8dac631ec2af07f521fe9526556d094d2e119"
	// PSThreadsAOTPrefix names its threaded AOT artifacts in the engine cache
	// (it must not start with "flatsql-": the legacy engine prunes that prefix).
	PSThreadsAOTPrefix = DefaultPSAOTPrefix
)

// PSThreadsWasm returns the embedded partition-store engine (portable bytes).
func PSThreadsWasm() []byte { return psThreadsWasm }

// PSThreadsDigest returns the sha256 of the embedded bytes, hex.
func PSThreadsDigest() string {
	sum := sha256.Sum256(psThreadsWasm)
	return hex.EncodeToString(sum[:])
}

// PSThreadsAOTPath is where the threaded AOT artifact of the embedded engine
// lives in cacheDir for the linked runtime.
func PSThreadsAOTPath(cacheDir string) string {
	return ThreadedAOTPath(cacheDir, PSThreadsAOTPrefix, psThreadsWasm)
}

// PrewarmPSThreadsAOT compiles the embedded partition-store engine into
// cacheDir as the THREADS + Interruptible artifact a format-2 instance loads
// (A30: format 2 never runs interpreted, and a daemon never compiles on the
// service path). Returns the path and whether it was already present.
func PrewarmPSThreadsAOT(cacheDir string) (path string, alreadyPresent bool, err error) {
	path = PSThreadsAOTPath(cacheDir)
	if _, _, err := EnsureThreadedAOTArtifact(cacheDir, PSThreadsAOTPrefix, psThreadsWasm, false); err == nil {
		return path, true, nil
	}
	_, path, err = EnsureThreadedAOTArtifact(cacheDir, PSThreadsAOTPrefix, psThreadsWasm, true)
	return path, false, err
}
