package flowrt

// prewarm.go: ahead-of-time compilation of FLOW artifacts into the same cache
// the daemon reads at mount time.
//
// The daemon deliberately never compiles wasm on the service path: LoadFlowService
// and LoadMountedFlow call LoadAOTArtifact (precompiled-only) and fall back to
// the INTERPRETER on a miss. That is the right default — a production node must
// not spend minutes of a 2-vCPU box inside the WasmEdge AOT compiler while a
// timer is firing — but it means an unprimed host runs every flow interpreted,
// forever, silently except for one "aot false" line at boot.
//
// So priming is an operator step, and this is the connector for it: resolve the
// artifact exactly as the daemon resolves it, strip the publication trailer
// exactly as the daemon strips it, and write the artifact under the exact cache
// key the daemon will look for. Anything less exact produces a cache that looks
// populated and is never read.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// flowAOTPrefix scopes a flow's AOT artifact to that flow.
//
// The cache prunes stale artifacts by PREFIX: writing one artifact deletes
// every other file sharing its prefix, which is how a module's older builds get
// cleaned up across upgrades. That is correct for the engine and the link shim,
// which are one module each — and destructive for flows, which are many modules
// behind one name. With a shared "flowmount" prefix, priming three ingest flows
// left exactly one artifact on disk: each compile deleted its predecessors, and
// the daemon then interpreted two of the three while a populated-looking cache
// sat next to it.
//
// Scoping by flow reference restores the intent: an upgrade of THIS flow still
// evicts THIS flow's older artifact, and a sibling is never touched. Hyphens in
// the reference are folded to underscores so the "-" between prefix and hash
// stays an unambiguous boundary, and a truncated-name digest keeps two long
// references from colliding.
func flowAOTPrefix(flowRef string) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.':
			return r
		default:
			return '_'
		}
	}, flowRef)
	if len(sanitized) > 48 {
		sanitized = sanitized[:48]
	}
	sum := sha256.Sum256([]byte(flowRef))
	return flowAOTCachePrefix + "-" + sanitized + "_" + hex.EncodeToString(sum[:4])
}

// PrewarmFlowAOT compiles one flow artifact into cacheDir and reports where it
// landed and whether it was already there.
//
// flowRef accepts what the daemon accepts: an installed flow program ID (when
// store is non-nil), a bundle directory, or a path to a .wasm file.
//
// NOT an admission point. The publication trailer is stripped with a nil policy
// — the same call the daemon makes, minus the gate — because this tool only
// decides what to COMPILE, never what to run. The daemon still enforces its own
// signature and capability policy when it mounts the flow; a prewarmed artifact
// for a module the node later refuses is simply a file nobody opens. Stripping
// with the identical code path is what makes the cache key match: the key is
// the hash of the portable bytes, so a tool that hashed the signed bytes would
// write an artifact the daemon can never find.
func PrewarmFlowAOT(flowRef string, store *FlowStore, cacheDir string) (path string, alreadyPresent bool, err error) {
	wasmPath, _, err := resolveFlowArtifact(flowRef, store)
	if err != nil {
		return "", false, err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return "", false, fmt.Errorf("read flow artifact: %w", err)
	}
	portable, _, err := modulert.EnforceModuleSignaturePolicy(nil, wasmBytes)
	if err != nil {
		return "", false, fmt.Errorf("strip publication trailer: %w", err)
	}
	return flatsqlrt.PrewarmAOTArtifact(cacheDir, flowAOTPrefix(flowRef), portable)
}

// FlowAOTArtifactPath reports where the daemon will look for flowRef's
// precompiled artifact, without compiling anything. An operator uses this to
// confirm a cache is primed for the daemon that will read it — the failure this
// program actually hit was a cache primed under the wrong HOME.
func FlowAOTArtifactPath(flowRef string, store *FlowStore, cacheDir string) (string, error) {
	wasmPath, _, err := resolveFlowArtifact(flowRef, store)
	if err != nil {
		return "", err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return "", fmt.Errorf("read flow artifact: %w", err)
	}
	portable, _, err := modulert.EnforceModuleSignaturePolicy(nil, wasmBytes)
	if err != nil {
		return "", fmt.Errorf("strip publication trailer: %w", err)
	}
	return flatsqlrt.AOTArtifactPath(cacheDir, flowAOTPrefix(flowRef), portable), nil
}
