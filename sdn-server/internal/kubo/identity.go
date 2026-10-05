package kubo

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/ipfs/kubo/config"
	// Kubo declares this package as fsrepo.
	serialize "github.com/ipfs/kubo/config/serialize"
	"github.com/ipfs/kubo/repo"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// identityRepo is Kubo's repository with the node's identity handed over in
// memory. Kubo builds its peer from Identity.PrivKey in the config it reads,
// so the key goes in there on every read and never reaches the file: the
// config on disk keeps the PeerID only. That also ends the one plaintext key
// a box used to carry, the separate Kubo's random identity.
type identityRepo struct {
	repo.Repo
	key      crypto.PrivKey
	id       peer.ID
	settings func(*config.Config)
}

func (r *identityRepo) Config() (*config.Config, error) {
	c, err := r.Repo.Config()
	if err != nil {
		return nil, err
	}
	out, err := c.Clone()
	if err != nil {
		return nil, err
	}
	raw, err := crypto.MarshalPrivateKey(r.key)
	if err != nil {
		return nil, err
	}
	out.Identity.PeerID = r.id.String()
	out.Identity.PrivKey = base64.StdEncoding.EncodeToString(raw)
	if r.settings != nil {
		r.settings(out)
	}
	return out, nil
}

// SetConfig persists a config with the key taken back out.
func (r *identityRepo) SetConfig(c *config.Config) error {
	out, err := c.Clone()
	if err != nil {
		return err
	}
	out.Identity = config.Identity{PeerID: r.id.String()}
	return r.Repo.SetConfig(out)
}

// SetConfigKey refuses the identity: it belongs to the node's key subsystem,
// and rotating it in Kubo would split the node into two peers again.
func (r *identityRepo) SetConfigKey(key string, value any) error {
	if strings.HasPrefix(strings.ToLower(key), "identity") {
		return fmt.Errorf("the node identity belongs to the SDN key subsystem; %s cannot be set through Kubo", key)
	}
	return r.Repo.SetConfigKey(key, value)
}

// openRepo opens the repository at path, creating it when missing, and makes
// its on-disk identity the node's: PeerID set, no private key. An existing
// repository from the supervised child or an operator's ipfs.service carries
// its own random identity; that config, key included, is first copied beside
// it, never deleted, so a rollback to the two-process layout is one file copy.
func openRepo(path string, id peer.ID, logf func(string, ...any)) (r repo.Repo, tookOver bool, err error) {
	if !fsrepo.IsInitialized(path) {
		cfg, err := config.InitWithIdentity(config.Identity{PeerID: id.String()})
		if err != nil {
			return nil, false, fmt.Errorf("kubo: new repository config: %w", err)
		}
		if err := fsrepo.Init(path, cfg); err != nil {
			return nil, false, fmt.Errorf("kubo: create repository %s: %w", path, err)
		}
		logf("Kubo repository created at %s for %s", path, id)
	}
	// Open first: the repository lock is what keeps an operator's Kubo that
	// still runs on this repository from being rewritten underneath it.
	if r, err = fsrepo.Open(path); err != nil {
		return nil, false, fmt.Errorf("kubo: open repository %s: %w", path, err)
	}
	c, err := r.Config()
	if err != nil {
		r.Close()
		return nil, false, fmt.Errorf("kubo: read repository config: %w", err)
	}
	if c.Identity.PeerID == id.String() && c.Identity.PrivKey == "" {
		return r, false, nil
	}
	previous := c.Identity.PeerID
	backup, err := takeOverIdentity(path, id, c.Identity.PrivKey != "")
	if err != nil {
		r.Close()
		return nil, false, err
	}
	// Reopen so the repository's in-memory config is the file's.
	if err := r.Close(); err != nil {
		return nil, false, fmt.Errorf("kubo: close repository %s: %w", path, err)
	}
	if r, err = fsrepo.Open(path); err != nil {
		return nil, false, fmt.Errorf("kubo: reopen repository %s: %w", path, err)
	}
	if backup != "" {
		logf("Kubo repository %s now runs as the node identity %s (was %s). The previous config, key included, is kept at %s; restoring it is the rollback to a separate Kubo.", path, id, previous, backup)
	} else {
		logf("Kubo repository %s now runs as the node identity %s (was %q)", path, id, previous)
	}
	return r, previous != id.String(), nil
}

// takeOverIdentity rewrites the Identity in the repository's config file: the
// node's PeerID and no key. Kubo's own writers cannot remove a key: SetConfig
// merges into the file and PrivKey is omitempty, and SetConfigKey restores
// PrivKey on purpose. When the config carries a key it is first copied beside
// it (0600). The copy's path is returned.
func takeOverIdentity(repoPath string, id peer.ID, keep bool) (string, error) {
	filename, err := config.Filename(repoPath, "")
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := serialize.ReadConfigFile(filename, &m); err != nil {
		return "", fmt.Errorf("kubo: read repository config: %w", err)
	}
	backup := ""
	if keep {
		raw, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("kubo: read repository config: %w", err)
		}
		f, err := os.CreateTemp(repoPath, "config-pre-sdn-identity-")
		if err != nil {
			return "", fmt.Errorf("kubo: keep the repository's previous identity: %w", err)
		}
		if _, err := f.Write(raw); err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", fmt.Errorf("kubo: keep the repository's previous identity: %w", err)
		}
		backup = f.Name()
	}
	ident, _ := m["Identity"].(map[string]any)
	if ident == nil {
		ident = map[string]any{}
	}
	ident["PeerID"] = id.String()
	delete(ident, "PrivKey")
	m["Identity"] = ident
	if err := serialize.WriteConfigFile(filename, m); err != nil {
		return "", fmt.Errorf("kubo: write the node identity into the repository: %w", err)
	}
	return backup, nil
}
