package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureManagedIPFSRepoIdentity synchronizes the managed Kubo repo identity
// with the node mnemonic-backed identity bundle.
func EnsureManagedIPFSRepoIdentity(repoPath string, bundle *IdentityBundle) error {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return errors.New("repo path is required")
	}
	if bundle == nil || bundle.Identity == nil {
		return errors.New("identity bundle is required")
	}

	cfgPath := filepath.Join(repoPath, "config")
	cfg := map[string]any{}
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("decode kubo config %s: %w", cfgPath, err)
		}
	}

	privKey, err := bundle.base64PrivateKey()
	if err != nil {
		return fmt.Errorf("marshal node identity private key: %w", err)
	}

	cfg["Identity"] = map[string]any{
		"PeerID":  bundle.PeerID.String(),
		"PrivKey": privKey,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode kubo config %s: %w", cfgPath, err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return fmt.Errorf("create kubo repo path %s: %w", repoPath, err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return fmt.Errorf("write kubo config %s: %w", cfgPath, err)
	}
	return nil
}
