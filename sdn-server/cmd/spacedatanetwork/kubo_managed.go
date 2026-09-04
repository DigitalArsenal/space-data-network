package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

// managedKuboPlan decides whether this node runs Kubo itself (INST-04):
// it does when a Kubo binary is at hand (the bundle's, or kubo.binary_path)
// and the operator has not pointed admin.ipfs_api_url at a Kubo of their
// own. The managed instance lives under the data directory, answers on
// loopback ports that leave the admin listener's 5001 alone, and its API and
// gateway URLs replace the config's so every CID, pin and archive path uses
// it without further configuration.
type managedKuboPlan struct {
	Binary      string
	RepoPath    string
	APIAddr     string
	GatewayAddr string
	Reason      string
}

// planManagedKubo returns nil (with the reason) when Kubo is not managed.
func planManagedKubo(cfg *config.Config, layout bundle.Layout, dataPath string) (*managedKuboPlan, string) {
	if cfg == nil {
		return nil, "no config"
	}
	if url := strings.TrimSpace(cfg.Admin.IPFSAPIURL); url != "" && url != config.DefaultIPFSAPIURL {
		return nil, "admin.ipfs_api_url names an operator-run Kubo (" + url + ")"
	}
	// SDN_KUBO_BINARY names a Kubo outside the bundle (a bare-binary install
	// with a distribution Kubo); the bundle's own copy is the default.
	binary := strings.TrimSpace(os.Getenv("SDN_KUBO_BINARY"))
	if binary == "" {
		binary = layout.KuboBinary
	}
	if binary == "" {
		return nil, "no Kubo binary: not running from a bundle and SDN_KUBO_BINARY is unset"
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		return nil, "Kubo binary not found at " + binary
	}
	// asset_pins.kubo_repo_path names an existing repository when the
	// operator already has one; a path that does not exist (the config
	// default is a production host's volume) is not created — the managed
	// repository lives under the data directory.
	repo := strings.TrimSpace(cfg.AssetPins.KuboRepoPath)
	if repo != "" {
		if info, err := os.Stat(filepath.Join(repo, "config")); err != nil || info.IsDir() {
			repo = ""
		}
	}
	if repo == "" {
		repo = filepath.Join(dataPath, "kubo")
	}
	return &managedKuboPlan{
		Binary:      binary,
		RepoPath:    repo,
		APIAddr:     kubo.DefaultAPIAddr,
		GatewayAddr: kubo.DefaultGatewayAddr,
		Reason:      "bundle Kubo " + binary,
	}, ""
}

// startManagedKubo runs the plan and points the config at the managed
// instance. The returned supervisor is nil when Kubo is not managed.
func startManagedKubo(ctx context.Context, cfg *config.Config, layout bundle.Layout, dataPath string, logf func(string, ...any)) (*kubo.Supervisor, error) {
	plan, reason := planManagedKubo(cfg, layout, dataPath)
	if plan == nil {
		logf("Kubo not managed by this node: %s", reason)
		return nil, nil
	}
	sup, err := kubo.New(kubo.Config{
		Binary:       plan.Binary,
		RepoPath:     plan.RepoPath,
		APIAddr:      plan.APIAddr,
		GatewayAddr:  plan.GatewayAddr,
		StartTimeout: 90 * time.Second,
		Logf:         logf,
	})
	if err != nil {
		return nil, err
	}
	if err := sup.Start(ctx); err != nil {
		return nil, fmt.Errorf("managed Kubo: %w", err)
	}
	cfg.Admin.IPFSAPIURL = sup.APIURL()
	if strings.TrimSpace(cfg.Admin.IPFSGatewayURL) == "" {
		cfg.Admin.IPFSGatewayURL = sup.GatewayURL()
	}
	logf("Kubo managed by this node (%s): API %s, gateway %s, repo %s", plan.Reason, sup.APIURL(), sup.GatewayURL(), plan.RepoPath)
	return sup, nil
}

// managedKuboDataPath is where the managed repository lives when the config
// names no existing one: the node's data directory, or the parent of the
// storage path (the layout `init` writes).
func managedKuboDataPath(cfg *config.Config) string {
	if cfg == nil {
		return "."
	}
	if base := strings.TrimSpace(cfg.Setup.DataPath); base != "" {
		return base
	}
	if storagePath := strings.TrimSpace(cfg.Storage.Path); storagePath != "" {
		return filepath.Dir(storagePath)
	}
	return "."
}
