package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// registerNodeFirstReadLanes mounts every baseline node-first frame lane.
// Trust edges and claims live here rather than behind the optional rules
// engine, so a node without that service still has the complete dashboard API.
func registerNodeFirstReadLanes(mux *http.ServeMux, n *node.Node, requireAuth bool, resolveAuth func() *auth.Handler) *api.TrustHandler {
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

	selfPeerID := n.PeerID().String()
	graph := trust.NewGraph()
	var edgeStore *trust.Store
	if flat := n.Store(); flat != nil {
		var err error
		edgeStore, err = trust.NewStoreWithFlatSQL(flat)
		if err != nil {
			log.Warnf("Trust edge store unavailable: %v", err)
		} else if heldGraph, loadErr := edgeStore.LoadGraph(); loadErr != nil {
			log.Warnf("Stored trust graph unavailable: %v", loadErr)
		} else {
			graph = heldGraph
		}
	}
	service := trust.NewService(graph, nil)
	service.TrackEvaluator(selfPeerID)
	signingKey, signingErr := storefrontSigningKeyFromRaw(n.SigningKey())
	if signingErr != nil {
		log.Warnf("Node-first trust and claim signing unavailable: %v", signingErr)
	}
	protect := nodeFirstOperatorProtect(requireAuth, resolveAuth)
	trustHandler := api.NewTrustHandler(service)
	trustHandler.Store = edgeStore
	trustHandler.ResolveEPM = func(peerID string) []byte { return heldNodeEPM(n, peerID) }
	trustHandler.SelfPeerID = selfPeerID
	trustHandler.SigningKey = signingKey
	trustHandler.PeerRegistry = n.PeerRegistry()
	trustHandler.Protect = protect

	claims := api.NewClaimsHandler(api.ClaimsHandlerOptions{
		SelfPeerID: selfPeerID,
		SigningKey: signingKey,
		Store:      api.NewFlatSQLClaimFrameStore(n.Store()),
		ResolveEPM: func(claimant string) []byte { return heldNodeEPM(n, claimant) },
		Trusted: func(claimant string) bool {
			if trustHandler.Engine != nil {
				status, ok := trustHandler.Service.Status(selfPeerID, claimant)
				return ok && status.Trusted
			}
			return peerRegistryTrusts(n.PeerRegistry(), claimant)
		},
		Protect: protect,
	})
	registerNodeFirstTrustAndClaimLanes(mux, trustHandler, claims)
	return trustHandler
}

func registerNodeFirstTrustAndClaimLanes(mux *http.ServeMux, trustHandler *api.TrustHandler, claims *api.ClaimsHandler) {
	trustHandler.RegisterEdgeRoutes(mux)
	claims.RegisterRoutes(mux)
}

func nodeFirstOperatorProtect(requireAuth bool, resolveAuth func() *auth.Handler) func(http.HandlerFunc) http.HandlerFunc {
	return func(inner http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if requireAuth {
				var handler *auth.Handler
				if resolveAuth != nil {
					handler = resolveAuth()
				}
				if handler == nil {
					http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
					return
				}
				if !handler.RequireTrust(w, r, peers.Admin) {
					return
				}
			}
			inner(w, r)
		}
	}
}

func peerRegistryTrusts(registry api.TrustPeerRegistry, peerID string) bool {
	if registry == nil {
		return false
	}
	id, err := peer.Decode(strings.TrimSpace(peerID))
	return err == nil && registry.IsTrusted(id)
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
