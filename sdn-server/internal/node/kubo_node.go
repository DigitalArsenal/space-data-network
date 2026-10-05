package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

// kuboListenAddrs says where the node's in-process Kubo serves.
//
// admin.ipfs_api_url and admin.ipfs_gateway_url used to name a Kubo to talk
// to: the supervised child, or an operator's ipfs.service. Kubo is this
// process now, so they name where it listens, on loopback only. The default
// API URL (Kubo's 5001) maps to 5002, as the supervised child did, because
// the admin listener uses 5001. An empty API URL keeps the RPC unserved and
// the pinning paths off, as before. A gateway URL off loopback stays the
// operator's to use, and the node serves no gateway of its own.
func kuboListenAddrs(admin config.AdminConfig) (api, gateway string, err error) {
	switch raw := strings.TrimSpace(admin.IPFSAPIURL); raw {
	case "":
	case config.DefaultIPFSAPIURL:
		api = kubo.DefaultAPIAddr
	default:
		if api, err = kubo.LoopbackAddr(raw); err != nil {
			return "", "", fmt.Errorf("admin.ipfs_api_url: %w. Kubo runs inside this node now; a Kubo anywhere else would be a second peer with its own identity", err)
		}
	}
	switch raw := strings.TrimSpace(admin.IPFSGatewayURL); raw {
	case "":
		gateway = kubo.DefaultGatewayAddr
	default:
		if addr, gwErr := kubo.LoopbackAddr(raw); gwErr == nil {
			gateway = addr
		}
	}
	return api, gateway, nil
}

// kuboRepoPath is the repository the node's Kubo runs on:
// asset_pins.kubo_repo_path when it names an existing repository (an
// operator's ipfs.service volume on the fleet), otherwise <data>/kubo, where
// the supervised child kept it.
func kuboRepoPath(cfg *config.Config) string {
	if repo := strings.TrimSpace(cfg.AssetPins.KuboRepoPath); repo != "" {
		if info, err := os.Stat(filepath.Join(repo, "config")); err == nil && !info.IsDir() {
			return repo
		}
	}
	data := strings.TrimSpace(cfg.Setup.DataPath)
	if data == "" {
		data = "."
		if storagePath := strings.TrimSpace(cfg.Storage.Path); storagePath != "" {
			data = filepath.Dir(storagePath)
		}
	}
	return filepath.Join(data, "kubo")
}

// kuboDHTMode maps the node's DHT participation onto Kubo's routing.
func kuboDHTMode(p dhtParticipation) kubo.DHTMode {
	switch p {
	case dhtParticipationOff:
		return kubo.DHTOff
	case dhtParticipationClient:
		return kubo.DHTClient
	case dhtParticipationServer:
		return kubo.DHTServer
	default:
		return kubo.DHTAuto
	}
}

// kuboBootstrap is the node's resolved bootstrap list (the configured peers,
// or the built-in defaults) as Kubo's Bootstrap multiaddrs, so Kubo's DHT
// and the node agree on whom to bootstrap from.
func kuboBootstrap(configured []string) []string {
	peers, _, err := bootstrap.ResolveBootstrapPeers(configured)
	if err != nil {
		log.Warnf("Kubo bootstrap list: %v", err)
	}
	var out []string
	for _, p := range peers {
		for _, addr := range p.AddrInfo.Addrs {
			out = append(out, addr.String()+"/p2p/"+p.AddrInfo.ID.String())
		}
	}
	return out
}
