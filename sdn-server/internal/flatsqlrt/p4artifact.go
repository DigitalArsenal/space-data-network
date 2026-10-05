package flatsqlrt

// p4artifact.go: the embedded format-4 engine (stack design
// docs/architecture/flatsql-sqlite-partitions.md §5.1, gates G4/G5;
// build-out contract §1, §5.3). flatsql-p4-threads.wasm is the file of the
// PUBLISHED flatsql npm release below, byte for byte (published-deps law): the
// release workflow (flatsql .github/workflows/npm-publish.yml) rebuilds it and
// refuses to publish unless the build equals the committed file, and its
// sha256 is in the package's wasm/integrity.json. versioninfo.P4EngineSHA256
// is that sha256, the one pin: it decides the build's store-format stamp, and
// init refuses to start a binary whose embedded bytes are not the pinned
// engine, so a build stamped format 4 always carries its engine.
//
// Format 4 is the default store format (SDN_STORE_FORMAT unset; "1" opts
// out). A build with no pin embeds no bytes, Open refuses that empty
// artifact, and its stamp stays below 4.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

//go:embed flatsql-p4-threads.wasm
var p4ThreadsWasm []byte

const (
	// P4ThreadsPackage is the npm release the artifact comes from.
	P4ThreadsPackage = "flatsql@3.7.1"
	// P4ThreadsGitHead is that release's gitHead (flatsql main).
	P4ThreadsGitHead = "4845fd0176dafa96774b1ad37872128914f7ac43"
	// P4ThreadsSHA256 is the artifact's sha256 (the package's integrity.json):
	// the release pin.
	P4ThreadsSHA256 = versioninfo.P4EngineSHA256
	// P4ThreadsAOTPrefix names its threaded AOT artifacts in the engine cache
	// (it must not start with "flatsql-", which the legacy engine prunes, nor
	// with the partition store's "fsqlps").
	P4ThreadsAOTPrefix = "fsqlp4"
)

// ErrNoP4Artifact: this build embeds no format-4 engine.
var ErrNoP4Artifact = errors.New("flatsqlrt: this build embeds no format-4 engine (flatsql-p4-threads.wasm)")

func init() {
	if err := checkP4Pin(p4ThreadsWasm, P4ThreadsSHA256); err != nil {
		panic(err)
	}
}

// checkP4Pin holds the embedded engine to the pin: no pin, no bytes; a pin,
// non-empty bytes with exactly that sha256.
func checkP4Pin(wasm []byte, pin string) error {
	if pin == "" {
		if len(wasm) != 0 {
			return fmt.Errorf("flatsqlrt: flatsql-p4-threads.wasm is embedded (%d bytes) but versioninfo.P4EngineSHA256 pins no format-4 engine", len(wasm))
		}
		return nil
	}
	sum := sha256.Sum256(wasm)
	if got := hex.EncodeToString(sum[:]); len(wasm) == 0 || got != pin {
		return fmt.Errorf("flatsqlrt: the embedded flatsql-p4-threads.wasm (%d bytes, sha256 %s) is not the pinned format-4 engine %s", len(wasm), got, pin)
	}
	return nil
}

// P4ThreadsWasm returns the embedded format-4 engine (portable bytes); empty
// in a build that pins none.
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
