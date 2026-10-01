package flatsqlrt

import (
	"encoding/binary"
	"strings"
	"testing"
)

// The embedded format-4 engine is the published release's file: its sha256
// equals the one the release ships in wasm/integrity.json. Before the
// release the file is empty, and only then.
func TestEmbeddedP4ThreadsArtifact(t *testing.T) {
	if got := P4ThreadsDigest(); got != P4ThreadsSHA256 {
		t.Fatalf("embedded flatsql-p4-threads.wasm sha256 %s, want %s (%s)", got, P4ThreadsSHA256, P4ThreadsPackage)
	}
	if strings.HasPrefix(P4ThreadsAOTPrefix, "flatsql-") || strings.HasPrefix(P4ThreadsAOTPrefix, DefaultPSAOTPrefix) {
		t.Fatalf("AOT prefix %q collides with the legacy or partition-store engine's", P4ThreadsAOTPrefix)
	}
	b := P4ThreadsWasm()
	if P4ThreadsPackage == "flatsql@unreleased" {
		if len(b) != 0 || P4ThreadsGitHead != "" {
			t.Fatal("an artifact is embedded but P4ThreadsPackage says unreleased")
		}
		if _, _, err := PrewarmP4ThreadsAOT(t.TempDir()); err != ErrNoP4Artifact {
			t.Fatalf("prewarm without an artifact: %v", err)
		}
		t.Skip("no released format-4 engine is embedded yet")
	}
	if len(b) < 8 || string(b[:4]) != "\x00asm" || binary.LittleEndian.Uint32(b[4:8]) != 1 {
		t.Fatalf("embedded artifact is not a wasm module (%d bytes)", len(b))
	}
	if !strings.HasPrefix(P4ThreadsPackage, "flatsql@") || len(P4ThreadsGitHead) != 40 {
		t.Fatalf("provenance %s @ %q", P4ThreadsPackage, P4ThreadsGitHead)
	}
}
