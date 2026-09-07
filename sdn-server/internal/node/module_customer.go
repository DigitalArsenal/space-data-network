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
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
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
	Installed         bool   `json:"installed"`
	Downloaded        bool   `json:"downloaded"`
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
	for i := range c.Modules {
		m := &c.Modules[i]
		if !m.Published {
			continue
		}
		receipt := n.customerModuleReceipt(m)
		m.Downloaded = receipt != nil
		m.Installed = receipt != nil && receipt["status"] == "installed" && n.customerModuleRegistered(m)
	}
	return json.Marshal(c)
}

func (n *Node) customerModuleRegistered(m *customerModule) bool {
	if n.plugins == nil {
		return false
	}
	mod, ok := n.plugins.Get(m.PluginID).(*modulert.Module)
	return ok && mod.Manifest() != nil && mod.Manifest().Version == m.Version && mod.ContentHash() == m.Artifact.CanonicalSHA256
}

func (n *Node) customerModuleReceipt(m *customerModule) map[string]any {
	if _, err := decodeModuleHash(m.CustomerSHA256); err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(n.config.Storage.Path, "customer-modules", m.CustomerSHA256, "receipt.json"))
	if err != nil {
		return nil
	}
	var receipt map[string]any
	if json.Unmarshal(raw, &receipt) != nil || receipt["pluginId"] != m.PluginID || receipt["version"] != m.Version || receipt["sha256"] != m.Artifact.CanonicalSHA256 {
		return nil
	}
	return receipt
}

// Loading uses the existing runtime's manifest, signature and capability gates.
// The decrypted bytes are held only in memory; the persistent install is sealed.
func (n *Node) installCustomerModule(m *customerModule, plain []byte) error {
	if n.plugins == nil {
		return errors.New("module runtime is unavailable")
	}
	if n.customerModuleRegistered(m) {
		for _, entry := range n.plugins.RuntimeSnapshot().Modules {
			if entry.ID == m.PluginID && entry.Status == "error" {
				return n.plugins.RunRuntimeModuleAction(context.Background(), m.PluginID, "start")
			}
		}
		return nil
	}
	if n.plugins.Get(m.PluginID) != nil {
		return errors.New("a different version of this module is already installed")
	}
	nodeCtx, err := n.buildModuleNodeContextWithPolicy()
	if err != nil {
		return err
	}
	mod, err := modulert.NewModule(plain, n.buildCapRegistry(), nodeCtx)
	if err != nil {
		return fmt.Errorf("module installation failed: %w", err)
	}
	if mod.ID() != m.PluginID || mod.Manifest().Version != m.Version || mod.ContentHash() != m.Artifact.CanonicalSHA256 {
		mod.Close()
		return errors.New("module manifest does not match the selected publication")
	}
	if err := n.plugins.Register(mod); err != nil {
		mod.Close()
		return err
	}
	_, err = n.plugins.StartLateRegistered(mod)
	return err
}

// Restore only explicit installations. A delivery cache is not an installation.
func (n *Node) restoreCustomerModules() {
	c, err := n.customerCatalog()
	if err != nil {
		return
	}
	for i := range c.Modules {
		m := &c.Modules[i]
		if !m.Published {
			continue
		}
		receipt := n.customerModuleReceipt(m)
		if receipt == nil || receipt["status"] != "installed" {
			continue
		}
		encrypted, err := os.ReadFile(filepath.Join(n.config.Storage.Path, "customer-modules", m.CustomerSHA256, "module.wasm.enc"))
		if err == nil {
			var plain []byte
			plain, err = verifyCustomerModule(encrypted, n.identity.EncryptionKey, m.CustomerSHA256, m.Artifact.CanonicalSHA256)
			if err == nil {
				err = n.installCustomerModule(m, plain)
				clear(plain)
			}
		}
		if err != nil {
			n.recordModuleLoadFailure("customer-install", m.PluginID, err)
		}
	}
}

// The request selects a node-approved publication; it cannot supply a customer
// identity, decryption key, CID, provider key, file path, price or expected hash.
func (n *Node) TestPurchaseModule(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	n.customerModuleMu.Lock()
	defer n.customerModuleMu.Unlock()
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
	if err := n.installCustomerModule(selected, plain); err != nil {
		return nil, err
	}
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
	receipt["status"] = "installed"
	receipt["installedAt"] = time.Now().UTC().Format(time.RFC3339)
	result, err = json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	// Atomic replacement also upgrades an earlier download-only receipt.
	tmp, err := os.CreateTemp(target, ".receipt-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(result); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(target, "receipt.json")); err != nil {
		return nil, err
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
