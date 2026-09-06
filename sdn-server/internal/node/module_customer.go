package node

// Customer-sealed test delivery uses the SDK REC/ENC envelope and IPFS. No
// payment is taken and no production license entitlement is asserted.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

const customerModuleLimit = 64 << 20

type customerProvider struct {
	Name   string `json:"name"`
	PeerID string `json:"peerId"`
}
type customerModule struct {
	PluginID          string `json:"pluginId"`
	Version           string `json:"version"`
	Name              string `json:"name"`
	Owner             string `json:"owner"`
	Protected         bool   `json:"protected"`
	Status            string `json:"status"`
	Published         bool   `json:"published"`
	CustomerCID       string `json:"customerCid"`
	CustomerSHA256    string `json:"customerSha256"`
	CustomerPublicKey string `json:"customerPublicKey"`
	Artifact          struct {
		CanonicalSHA256 string `json:"canonicalSha256"`
		Size            int64  `json:"size"`
	} `json:"artifact"`
}
type customerCatalog struct {
	CustomerPeerID    string                      `json:"customerPeerId"`
	CustomerPublicKey string                      `json:"customerPublicKey"`
	Nodes             map[string]customerProvider `json:"nodes"`
	Modules           []customerModule            `json:"modules"`
	TestMode          bool                        `json:"testMode"`
}

func (n *Node) customerCatalog() (*customerCatalog, error) {
	if os.Getenv("SDN_STOREFRONT_DEV_PAYMENTS") != "1" {
		return nil, errors.New("test purchases are disabled")
	}
	name := os.Getenv("SDN_MODULE_CUSTOMER_CATALOG")
	if name == "" {
		return nil, errors.New("module placement inventory is not configured")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var c customerCatalog
	if err := json.NewDecoder(io.LimitReader(file, 4<<20)).Decode(&c); err != nil {
		return nil, err
	}
	if n.host == nil || c.CustomerPeerID != n.host.ID().String() {
		return nil, errors.New("module inventory belongs to a different customer node")
	}
	if n.identity == nil || len(n.identity.EncryptionPub) != 32 || len(n.identity.EncryptionKey) != 32 {
		return nil, errors.New("customer encryption identity is unavailable")
	}
	c.TestMode = true
	c.CustomerPublicKey = hex.EncodeToString(n.identity.EncryptionPub)
	for i := range c.Modules {
		m := &c.Modules[i]
		m.Published = m.Published && m.CustomerCID != "" && m.CustomerPublicKey == c.CustomerPublicKey && m.Status == "artifact-verified"
	}
	return &c, nil
}
func (n *Node) ModuleCustomerCatalog() (json.RawMessage, error) {
	c, err := n.customerCatalog()
	if err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

// The request selects a node-approved publication; it cannot supply a customer
// identity, decryption key, CID, provider key, file path, price or expected hash.
func (n *Node) TestPurchaseModule(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	var req struct {
		PluginID string `json:"pluginId"`
		Version  string `json:"version"`
		Owner    string `json:"owner"`
	}
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	c, err := n.customerCatalog()
	if err != nil {
		return nil, err
	}
	var selected *customerModule
	for i := range c.Modules {
		m := &c.Modules[i]
		if m.PluginID == req.PluginID && m.Version == req.Version && m.Owner == req.Owner {
			selected = m
			break
		}
	}
	if selected == nil || !selected.Published {
		return nil, errors.New("this module has not been published encrypted for this customer")
	}
	provider, ok := c.Nodes[selected.Owner]
	if !ok || provider.PeerID == "" {
		return nil, errors.New("unknown module provider")
	}
	if _, err := decodeModuleHash(selected.CustomerSHA256); err != nil {
		return nil, err
	}
	root := filepath.Join(n.config.Storage.Path, "customer-modules")
	target := filepath.Join(root, selected.CustomerSHA256)
	encrypted, readErr := os.ReadFile(filepath.Join(target, "module.wasm.enc"))
	cached := readErr == nil
	if !cached {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		encrypted, err = newIPFSTipFetcher(n.resolveRuntimeIPFSAPIURL(), customerModuleLimit).Fetch(ctx, selected.CustomerCID)
		if err != nil {
			return nil, fmt.Errorf("module download failed: %w", err)
		}
	}
	plain, err := verifyCustomerModule(encrypted, n.identity.EncryptionKey, selected.CustomerSHA256, selected.Artifact.CanonicalSHA256)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	receipt := map[string]any{"pluginId": req.PluginID, "version": req.Version, "owner": req.Owner, "customerPeerId": c.CustomerPeerID, "providerPeerId": provider.PeerID, "testMode": true, "charged": false, "status": "downloaded", "encrypted": true, "delivery": "customer-sealed-test", "sha256": selected.Artifact.CanonicalSHA256, "bytes": len(plain), "cid": selected.CustomerCID, "cached": cached, "downloadedAt": time.Now().UTC().Format(time.RFC3339)}
	result, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if !cached {
		if err := os.MkdirAll(root, 0700); err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp(root, ".download-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		for name, data := range map[string][]byte{"module.wasm.enc": encrypted, "receipt.json": result} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				return nil, err
			}
		}
		if err := os.Rename(dir, target); err != nil {
			// Another request may have completed the same immutable download.
			existing, readErr := os.ReadFile(filepath.Join(target, "module.wasm.enc"))
			if readErr != nil || !bytes.Equal(existing, encrypted) {
				return nil, err
			}
		}
	}
	return result, nil
}
func decodeModuleHash(value string) ([]byte, error) {
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New("module hash is not pinned")
	}
	return b, nil
}
func verifyCustomerModule(encrypted, key []byte, encryptedHash, plainHash string) ([]byte, error) {
	expected, err := decodeModuleHash(encryptedHash)
	if err != nil {
		return nil, err
	}
	if len(encrypted) > customerModuleLimit {
		return nil, errors.New("module exceeds download limit")
	}
	actual := sha256.Sum256(encrypted)
	if !bytes.Equal(actual[:], expected) {
		return nil, errors.New("encrypted module differs from its pinned publication")
	}
	expected, err = decodeModuleHash(plainHash)
	if err != nil {
		return nil, err
	}
	plain, err := license.DecryptProtectedPublication(encrypted, key)
	if err != nil {
		return nil, fmt.Errorf("this node cannot decrypt the customer module: %w", err)
	}
	actual = sha256.Sum256(plain)
	if !bytes.Equal(actual[:], expected) || len(plain) < 8 || !bytes.Equal(plain[:8], []byte{0, 97, 115, 109, 1, 0, 0, 0}) {
		clear(plain)
		return nil, errors.New("decrypted module differs from the verified artifact")
	}
	return plain, nil
}
