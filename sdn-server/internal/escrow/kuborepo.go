// Kubo repo identity escrow.
//
// The kubo peer key is the one identity in this system that CANNOT be
// re-derived: `ipfs init` generates it randomly, and its PeerID is what the
// network knows the producer by. It is also the one identity that must stay
// PLAINTEXT on disk — kubo decodes Identity.PrivKey with a plain base64 decode
// (kubo/config/identity.go:22 DecodePrivateKey), so an encrypted value there
// bricks the node. Escrow is therefore the ONLY protection available for it:
// the local copy stays plaintext at 0600, and a sealed copy lives elsewhere.

package escrow

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// encryptedIdentityPrefix marks an Identity.PrivKey that sdn-server encrypted.
// kubo cannot read it back; see the package comment.
const encryptedIdentityPrefix = "sdnenc1:"

// KuboIdentity is the slice of a kubo repo config this package touches. The
// config is decoded into a generic map elsewhere so unrelated settings survive
// a rewrite untouched.
type KuboIdentity struct {
	PeerID  string `json:"PeerID"`
	PrivKey string `json:"PrivKey,omitempty"`
}

// ReadKuboIdentity loads the Identity section of a kubo repo config.
func ReadKuboIdentity(repoPath string) (KuboIdentity, error) {
	var ident KuboIdentity
	raw, err := os.ReadFile(kuboConfigPath(repoPath))
	if err != nil {
		return ident, err
	}
	var cfg struct {
		Identity KuboIdentity `json:"Identity"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ident, fmt.Errorf("parse kubo config: %w", err)
	}
	return cfg.Identity, nil
}

// SealKuboRepo escrows the peer identity of the kubo repo at repoPath.
//
// It refuses an already-encrypted PrivKey: that value cannot be read by kubo,
// so escrowing it would preserve an unusable identity and hide the real defect.
func SealKuboRepo(repoPath string, recipient Recipient, recipientPub []byte, kx ecies.KeyExchange, role string) (*Blob, error) {
	ident, err := ReadKuboIdentity(repoPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(ident.PrivKey) == "" {
		return nil, fmt.Errorf("kubo repo %s has no Identity.PrivKey to escrow", repoPath)
	}
	if strings.HasPrefix(ident.PrivKey, encryptedIdentityPrefix) {
		return nil, fmt.Errorf("kubo repo %s has an %s-encrypted Identity.PrivKey, which kubo itself cannot read; "+
			"restore a plaintext key before escrowing it", repoPath, encryptedIdentityPrefix)
	}
	keyMaterial, err := base64.StdEncoding.DecodeString(ident.PrivKey)
	if err != nil {
		return nil, fmt.Errorf("decode kubo Identity.PrivKey: %w", err)
	}
	hostname, _ := os.Hostname()

	return Seal(keyMaterial, Subject{
		PeerID:      strings.TrimSpace(ident.PeerID),
		MachineName: hostname,
		Role:        role,
	}, recipient, recipientPub, kx)
}

// RecoverKuboRepo restores an escrowed peer identity into the kubo repo at
// repoPath, writing Identity.PrivKey back as PLAINTEXT base64 (the only form
// kubo can read) and leaving every other config setting untouched.
//
// It fails closed rather than clobbering: if the repo already holds a DIFFERENT
// identity, recovery is refused unless force is set. Overwriting a live peer
// identity by accident is the exact loss this package exists to prevent.
func RecoverKuboRepo(blob *Blob, recipientPriv []byte, repoPath string, force bool) (peer.ID, error) {
	keyMaterial, err := blob.Open(recipientPriv)
	if err != nil {
		return "", err
	}
	priv, err := crypto.UnmarshalPrivateKey(keyMaterial)
	if err != nil {
		return "", fmt.Errorf("recovered material is not a libp2p private key: %w", err)
	}
	recoveredID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("derive recovered PeerID: %w", err)
	}

	cfgPath := kuboConfigPath(repoPath)
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("read kubo config: %w", err)
	}
	// Decode generically so unrelated settings round-trip untouched.
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("parse kubo config: %w", err)
	}

	if existing, ok := cfg["Identity"].(map[string]any); ok {
		if current, _ := existing["PeerID"].(string); strings.TrimSpace(current) != "" &&
			current != recoveredID.String() && !force {
			return "", fmt.Errorf("kubo repo %s already holds identity %s, but the escrow recovers %s; "+
				"refusing to overwrite a different identity (pass force to override)",
				repoPath, current, recoveredID)
		}
	}

	cfg["Identity"] = map[string]any{
		"PeerID": recoveredID.String(),
		// PLAINTEXT base64 — kubo's DecodePrivateKey does a bare base64 decode.
		"PrivKey": base64.StdEncoding.EncodeToString(keyMaterial),
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode kubo config: %w", err)
	}
	if err := writeFileAtomic(cfgPath, append(out, '\n'), 0o600); err != nil {
		return "", err
	}
	return recoveredID, nil
}

// writeFileAtomic replaces path via a temp file + rename, so an interrupted
// write cannot leave a repo with a truncated config and no identity at all.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".escrow-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	return os.Rename(tmpName, path)
}

func kuboConfigPath(repoPath string) string {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return ""
	}
	if strings.HasSuffix(repoPath, string(os.PathSeparator)+"config") {
		return repoPath
	}
	return filepath.Join(repoPath, "config")
}
