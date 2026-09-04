package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// registerNodeFirstReadLanes mounts the identity and installed-module frame
// lanes. Trust edges are mounted with the trust engine because they share its
// graph, persistence store, event publisher, and operator gate.
func registerNodeFirstReadLanes(mux *http.ServeMux, n *node.Node) {
	nodes := api.NewNodesHandler(api.NodesHandlerOptions{
		SelfPeerID: n.PeerID().String(),
		Self:       n.EPMService(),
		Profiles: func() []api.NodeProfile {
			return heldNodeProfiles(n)
		},
	})
	nodes.RegisterRoutes(mux)

	modules := api.NewModulesHandler(func() plugins.RuntimeSnapshot {
		snapshot := plugins.RuntimeSnapshot{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Modules:     []plugins.RuntimeModuleEntry{},
		}
		if manager := n.PluginManager(); manager != nil {
			snapshot = manager.RuntimeSnapshot()
		}
		mergeModuleRuntimeCatalog(&snapshot, n.PluginRegistry())
		snapshot.Count = len(snapshot.Modules)
		return snapshot
	})
	mux.Handle(api.ModulesPath, modules)
}

// heldNodeProfiles joins the registry's live EPM copies with the directory's
// durable copies. Registry bytes win when both sources know the same peer.
func heldNodeProfiles(n *node.Node) []api.NodeProfile {
	if n == nil {
		return nil
	}
	byPeer := make(map[string][]byte)
	if registry := n.PeerRegistry(); registry != nil {
		for _, known := range registry.ListPeers() {
			if known == nil || len(known.EPMData) == 0 {
				continue
			}
			peerID := strings.TrimSpace(known.ID.String())
			if peerID != "" {
				byPeer[peerID] = append([]byte(nil), known.EPMData...)
			}
		}
	}
	if directoryService := n.DirectoryService(); directoryService != nil {
		if profiles, err := directoryService.HeldNodeProfiles(); err == nil {
			for _, profile := range profiles {
				peerID := strings.TrimSpace(profile.PeerID)
				if peerID == "" || len(profile.EPMBytes) == 0 {
					continue
				}
				if _, exists := byPeer[peerID]; !exists {
					byPeer[peerID] = append([]byte(nil), profile.EPMBytes...)
				}
			}
		}
	}
	out := make([]api.NodeProfile, 0, len(byPeer))
	for peerID, frame := range byPeer {
		out = append(out, api.NodeProfile{PeerID: peerID, Frame: frame})
	}
	return out
}

// heldNodeEPM resolves one profile with the same precedence as the node lane.
func heldNodeEPM(n *node.Node, peerID string) []byte {
	peerID = strings.TrimSpace(peerID)
	if n == nil || peerID == "" {
		return nil
	}
	if peerID == n.PeerID().String() {
		if service := n.EPMService(); service != nil {
			return service.GetNodeEPM()
		}
		return nil
	}
	for _, profile := range heldNodeProfiles(n) {
		if profile.PeerID == peerID {
			return append([]byte(nil), profile.Frame...)
		}
	}
	return nil
}
