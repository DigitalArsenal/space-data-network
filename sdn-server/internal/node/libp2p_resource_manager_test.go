package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

func TestNodeSourceInstallsFlatSQLSyncResourceManager(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(filepath.Join(".", "node.go"))
	if err != nil {
		t.Fatalf("os.ReadFile(node.go) failed: %v", err)
	}
	if !strings.Contains(string(source), "libp2p.ResourceManager(resourceManager)") {
		t.Fatalf("node host must install the FlatSQL sync resource manager")
	}
}

func TestFlatSQLSyncResourceManagerAllowsBulkInboundRangeStreams(t *testing.T) {
	t.Parallel()

	manager, err := newFlatSQLSyncResourceManager()
	if err != nil {
		t.Fatalf("newFlatSQLSyncResourceManager failed: %v", err)
	}
	defer manager.Close()

	remotePeer, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	scopes := make([]network.StreamManagementScope, 0, flatSQLSyncBulkStreamLimit)
	defer func() {
		for _, scope := range scopes {
			scope.Done()
		}
	}()

	for i := 0; i < flatSQLSyncBulkStreamLimit; i++ {
		scope, err := manager.OpenStream(remotePeer, network.DirInbound)
		if err != nil {
			t.Fatalf("OpenStream #%d failed: %v", i+1, err)
		}
		if err := scope.SetProtocol(protocol.FlatSQLSyncProtocolID); err != nil {
			scope.Done()
			t.Fatalf("SetProtocol #%d failed: %v", i+1, err)
		}
		scopes = append(scopes, scope)
	}
}
