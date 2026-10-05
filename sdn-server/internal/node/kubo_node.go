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

// kuboNewRepoAddrs says where a Kubo repository this node creates serves its
// RPC API and gateway. An existing repository, one the supervised child left
// or an operator's ipfs.service ran on, keeps the addresses in its own config,
// so taking it over moves no port.
//
// The default API URL (Kubo's 5001) maps to 5002, as the supervised child did,
// because the admin listener uses 5001. An empty API URL leaves the RPC
// unserved and the pinning paths off, as before. A gateway URL off loopback is
// the operator's to use, and a new repository then serves no gateway.
func kuboNewRepoAddrs(admin config.AdminConfig) (api, gateway string, err error) {
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

// kuboClientURLs points the node's own Kubo clients (pinning and publishing
// through the RPC API, the /ipfs/ proxy through the gateway) at the
// in-process Kubo, where it actually listens. An explicit gateway URL stays:
// it can be a cache in front of Kubo's gateway (host-01's terrain cache on
// 8081 serves tiles and falls through to Kubo's 8091).
func kuboClientURLs(admin *config.AdminConfig, k *kubo.Node) {
	if strings.TrimSpace(admin.IPFSAPIURL) != "" {
		if k.APIURL() == "" {
			log.Warnf("admin.ipfs_api_url is set, but the Kubo repository serves no RPC API (its Addresses.API is empty): pinning and publishing through Kubo are off")
		}
		admin.IPFSAPIURL = k.APIURL()
	}
	raw := strings.TrimSpace(admin.IPFSGatewayURL)
	if addr, err := kubo.LoopbackAddr(raw); raw == "" || (err == nil && strings.HasSuffix(addr, ":0")) {
		admin.IPFSGatewayURL = k.GatewayURL()
	} else if raw != k.GatewayURL() {
		gateway := k.GatewayURL()
		if gateway == "" {
			gateway = "off"
		}
		log.Infof("admin.ipfs_gateway_url %s fronts the in-process Kubo gateway (%s)", raw, gateway)
	}
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
