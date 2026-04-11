package moduledelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// Registry loads the SDN plugin catalog and publishes encrypted bundles to IPFS on demand.
type Registry struct {
	catalog    *license.PluginRegistry
	ipfsAPIURL string

	mu          sync.Mutex
	publication map[string]string
}

// NewRegistry creates a registry rooted at baseDir/license/plugins and optionally backed by an IPFS API.
func NewRegistry(baseDir, ipfsAPIURL string) (*Registry, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, errors.New("base directory is required")
	}

	pluginRoot := license.DefaultPluginRoot(baseDir)
	catalog, err := license.LoadPluginRegistry(pluginRoot)
	if err != nil {
		return nil, err
	}

	return &Registry{
		catalog:     catalog,
		ipfsAPIURL:  strings.TrimSpace(ipfsAPIURL),
		publication: make(map[string]string),
	}, nil
}

// PluginRegistry exposes the underlying license catalog.
func (r *Registry) PluginRegistry() *license.PluginRegistry {
	if r == nil {
		return nil
	}
	return r.catalog
}

// Count returns the number of catalog entries.
func (r *Registry) Count() int {
	if r == nil || r.catalog == nil {
		return 0
	}
	return r.catalog.Count()
}

// ListPublic returns the safe public catalog entries.
func (r *Registry) ListPublic() []license.PluginDescriptor {
	if r == nil || r.catalog == nil {
		return nil
	}
	return r.catalog.ListPublic()
}

// Get returns a catalog asset by ID.
func (r *Registry) Get(id string) (*license.PluginAsset, bool) {
	if r == nil || r.catalog == nil {
		return nil, false
	}
	return r.catalog.Get(id)
}

// ReadEncryptedBundle reads the plugin bundle bytes and asset metadata.
func (r *Registry) ReadEncryptedBundle(id string) ([]byte, *license.PluginAsset, error) {
	if r == nil || r.catalog == nil {
		return nil, nil, os.ErrNotExist
	}
	return r.catalog.ReadEncryptedBundle(id)
}

// ReadBundleKey reads the plugin's symmetric content key.
func (r *Registry) ReadBundleKey(id string) ([]byte, error) {
	if r == nil || r.catalog == nil {
		return nil, os.ErrNotExist
	}
	return r.catalog.ReadBundleKey(id)
}

// EnsurePublicationCID returns the published CID for a module, publishing the encrypted bundle once on cache miss.
func (r *Registry) EnsurePublicationCID(ctx context.Context, moduleID string) (string, *license.PluginAsset, error) {
	if r == nil {
		return "", nil, errors.New("registry is nil")
	}
	moduleID = strings.TrimSpace(moduleID)
	if moduleID == "" {
		return "", nil, errors.New("module id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	asset, ok := r.catalog.Get(moduleID)
	if !ok {
		return "", nil, os.ErrNotExist
	}

	r.mu.Lock()
	if cid, ok := r.publication[moduleID]; ok {
		r.mu.Unlock()
		return cid, asset, nil
	}
	r.mu.Unlock()

	if r.ipfsAPIURL == "" {
		return "", nil, errors.New("IPFS API URL is required")
	}

	bundle, _, err := r.catalog.ReadEncryptedBundle(moduleID)
	if err != nil {
		return "", nil, err
	}

	cid, err := r.publishBundle(ctx, bundle)
	if err != nil {
		return "", nil, err
	}

	r.mu.Lock()
	r.publication[moduleID] = cid
	r.mu.Unlock()

	return cid, asset, nil
}

func (r *Registry) publishBundle(ctx context.Context, bundle []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "bundle.wasm.enc")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, bytes.NewReader(bundle)); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.ipfsAPIURL, "/")+"/api/v0/add", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("ipfs add failed: %s: %s", resp.Status, strings.TrimSpace(string(slurp)))
	}

	var payload struct {
		Hash string `json:"Hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode ipfs add response: %w", err)
	}
	cid := strings.TrimSpace(payload.Hash)
	if cid == "" {
		return "", errors.New("ipfs add response missing hash")
	}
	return cid, nil
}
