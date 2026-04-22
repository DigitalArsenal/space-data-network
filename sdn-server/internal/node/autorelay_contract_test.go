package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeSourceIncludesAutoRelayPeerSource(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join(".", "node.go")
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", sourcePath, err)
	}

	source := string(data)
	if !strings.Contains(source, "libp2p.EnableAutoRelayWithPeerSource(") {
		t.Fatalf("node host config no longer enables auto relay with peer source")
	}
}
