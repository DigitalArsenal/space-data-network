package flatsqlrt

// p4artifact.go: the embedded format-4 engine (stack design
// docs/architecture/flatsql-sqlite-partitions.md §5.1, gates G4/G5;
// build-out contract §1, §5.3). flatsql-p4-threads.wasm is the file of the
// PUBLISHED flatsql npm release below, byte for byte (published-deps law): the
// release workflow (flatsql .github/workflows/npm-publish.yml) rebuilds it and
// refuses to publish unless the build equals the committed file, and its
// sha256 is in the package's wasm/integrity.json. TestEmbeddedP4ThreadsArtifact
// holds the embedded bytes to P4ThreadsSHA256, and versioninfo.P4EngineSHA256
// names the same engine for the build stamp.
//
// UNTIL THE RELEASE: the file is empty and P4ThreadsPackage says so. Open
// refuses an empty artifact, and nothing selects format 4 by default.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
)

//go:embed flatsql-p4-threads.wasm
var p4ThreadsWasm []byte

const (
	// P4ThreadsPackage is the npm release the artifact comes from.
	P4ThreadsPackage = "flatsql@unreleased"
	// P4ThreadsGitHead is that release's gitHead (flatsql main).
	P4ThreadsGitHead = ""
	// P4ThreadsSHA256 is the artifact's sha256 (the package's integrity.json).
	P4ThreadsSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// P4ThreadsAOTPrefix names its threaded AOT artifacts in the engine cache
	// (it must not start with "flatsql-", which the legacy engine prunes, nor
	// with the partition store's "fsqlps").
	P4ThreadsAOTPrefix = "fsqlp4"
)

// ErrNoP4Artifact: this build embeds no format-4 engine.
var ErrNoP4Artifact = errors.New("flatsqlrt: this build embeds no format-4 engine (flatsql-p4-threads.wasm)")

// P4ThreadsWasm returns the embedded format-4 engine (portable bytes); empty
// in a build made before the engine's release.
func P4ThreadsWasm() []byte { return p4ThreadsWasm }

// P4ThreadsDigest returns the sha256 of the embedded bytes, hex.
func P4ThreadsDigest() string {
	sum := sha256.Sum256(p4ThreadsWasm)
	return hex.EncodeToString(sum[:])
}

// P4ThreadsAOTPath is where the threaded AOT artifact of the embedded engine
// lives in cacheDir for the linked runtime.
func P4ThreadsAOTPath(cacheDir string) string {
	return ThreadedAOTPath(cacheDir, P4ThreadsAOTPrefix, p4ThreadsWasm)
}

// PrewarmP4ThreadsAOT compiles the embedded format-4 engine into cacheDir as
// the THREADS + Interruptible artifact a format-4 instance loads (it never
// runs interpreted, and a daemon never compiles on the service path).
// Returns the path and whether it was already present.
func PrewarmP4ThreadsAOT(cacheDir string) (path string, alreadyPresent bool, err error) {
	if len(p4ThreadsWasm) == 0 {
		return "", false, ErrNoP4Artifact
	}
	path = P4ThreadsAOTPath(cacheDir)
	if _, _, err := EnsureThreadedAOTArtifact(cacheDir, P4ThreadsAOTPrefix, p4ThreadsWasm, false); err == nil {
		return path, true, nil
	}
	_, path, err = EnsureThreadedAOTArtifact(cacheDir, P4ThreadsAOTPrefix, p4ThreadsWasm, true)
	return path, false, err
}
