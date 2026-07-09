package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// encryptedIdentityKeyMagic prefixes an Identity.PrivKey value in the managed
// Kubo repo config to mark it as an SDN-encrypted envelope (Argon2id +
// XChaCha20-Poly1305, see internal/keys.EncryptSecret/DecryptSecret) rather
// than a legacy plaintext base64-encoded libp2p private key. Both forms are
// opaque base64-ish strings, so an explicit marker is used instead of
// sniffing content (unlike the mnemonic file, whose plaintext form is
// distinguishable ASCII words).
const encryptedIdentityKeyMagic = "sdnenc1:"

// isEncryptedIdentityKeyField reports whether a Kubo config Identity.PrivKey
// value is an SDN-encrypted envelope (post-fix) rather than a legacy
// plaintext key.
func isEncryptedIdentityKeyField(field string) bool {
	return strings.HasPrefix(field, encryptedIdentityKeyMagic)
}

// decryptIdentityKeyField decrypts a PrivKey field written by
// EnsureManagedIPFSRepoIdentity. It fails closed: any error decoding or
// decrypting the envelope is returned rather than treated as "no key".
func decryptIdentityKeyField(field string, password string) ([]byte, error) {
	if !isEncryptedIdentityKeyField(field) {
		return nil, errors.New("not an SDN-encrypted identity key envelope")
	}
	enc, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(field, encryptedIdentityKeyMagic))
	if err != nil {
		return nil, fmt.Errorf("decode identity key envelope: %w", err)
	}
	raw, err := keys.DecryptSecret(enc, password)
	if err != nil {
		return nil, fmt.Errorf("decrypt identity key envelope: %w", err)
	}
	return raw, nil
}

// EnsureManagedIPFSRepoIdentity synchronizes the managed Kubo repo identity
// with the node mnemonic-backed identity bundle. The private key is written
// encrypted at rest (Argon2id + XChaCha20-Poly1305, the same scheme used for
// the mnemonic file) — never plaintext.
//
// If an existing config on disk still holds a legacy plaintext key, it is
// transparently migrated to the encrypted form in place, provided its PeerID
// matches the identity we are about to write (a mismatch means the file
// belongs to a different identity and must not be silently clobbered).
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
	migratingPlaintext := false
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("decode kubo config %s: %w", cfgPath, err)
		}
		if existingIdentity, ok := cfg["Identity"].(map[string]any); ok {
			existingPeerID, _ := existingIdentity["PeerID"].(string)
			existingPrivKey, _ := existingIdentity["PrivKey"].(string)
			if existingPrivKey != "" && !isEncryptedIdentityKeyField(existingPrivKey) {
				// A legacy plaintext key is present. Refuse to clobber it
				// with a different identity's key — a silently changed
				// PeerID would break the trust map. Only proceed (as a
				// one-way encrypt-in-place migration) when the PeerID
				// already on disk matches the identity we're about to
				// write, or is absent.
				if existingPeerID != "" && existingPeerID != bundle.PeerID.String() {
					return fmt.Errorf("refusing to overwrite kubo config %s: existing PeerID %s does not match derived identity PeerID %s",
						cfgPath, existingPeerID, bundle.PeerID.String())
				}
				migratingPlaintext = true
			}
		}
	}

	encPrivKey, err := bundle.encryptedPrivateKeyForConfig()
	if err != nil {
		return fmt.Errorf("encrypt node identity private key: %w", err)
	}

	cfg["Identity"] = map[string]any{
		"PeerID":  bundle.PeerID.String(),
		"PrivKey": encPrivKey,
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

	if migratingPlaintext {
		log.Warnf("Migrated plaintext libp2p identity key in managed Kubo config %s to encrypted storage (PeerID unchanged: %s)",
			cfgPath, bundle.PeerID.String())
	}
	return nil
}

// LoadManagedIPFSRepoIdentity reads and decrypts the libp2p identity stored
// in a managed Kubo-style repo config previously written by
// EnsureManagedIPFSRepoIdentity (or a legacy plaintext config predating the
// encryption fix). It fails closed: if the stored key cannot be decrypted or
// decoded, it returns an error rather than falling back to generating a new
// identity. A silently regenerated PeerID would break the network's trust
// map, so callers must surface this error rather than paper over it.
func LoadManagedIPFSRepoIdentity(repoPath string, password string) (crypto.PrivKey, peer.ID, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil, "", errors.New("repo path is required")
	}

	cfgPath := filepath.Join(repoPath, "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, "", fmt.Errorf("read kubo config %s: %w", cfgPath, err)
	}

	var cfg struct {
		Identity struct {
			PeerID  string `json:"PeerID"`
			PrivKey string `json:"PrivKey"`
		} `json:"Identity"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, "", fmt.Errorf("decode kubo config %s: %w", cfgPath, err)
	}
	if cfg.Identity.PrivKey == "" {
		return nil, "", fmt.Errorf("kubo config %s has no identity private key", cfgPath)
	}

	var rawKey []byte
	if isEncryptedIdentityKeyField(cfg.Identity.PrivKey) {
		rawKey, err = decryptIdentityKeyField(cfg.Identity.PrivKey, password)
		if err != nil {
			return nil, "", fmt.Errorf("decrypt identity private key from %s: %w", cfgPath, err)
		}
	} else {
		// Legacy plaintext (pre-migration) config.
		rawKey, err = base64.StdEncoding.DecodeString(cfg.Identity.PrivKey)
		if err != nil {
			return nil, "", fmt.Errorf("decode legacy plaintext identity private key from %s: %w", cfgPath, err)
		}
	}
	defer zeroBytes(rawKey)

	privKey, err := crypto.UnmarshalPrivateKey(rawKey)
	if err != nil {
		return nil, "", fmt.Errorf("unmarshal identity private key from %s: %w", cfgPath, err)
	}
	pid, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return nil, "", fmt.Errorf("derive peer id from identity private key: %w", err)
	}
	if cfg.Identity.PeerID != "" && cfg.Identity.PeerID != pid.String() {
		return nil, "", fmt.Errorf("kubo config %s PeerID %s does not match decrypted key PeerID %s",
			cfgPath, cfg.Identity.PeerID, pid.String())
	}
	return privKey, pid, nil
}
