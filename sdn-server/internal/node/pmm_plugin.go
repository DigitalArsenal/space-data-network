package node

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/cct"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/pmm"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// PMMPluginID is the plugin identity.
const PMMPluginID = "pmm"

// pmmManifestTTL bounds anonymous trust. An unexpiring signed manifest cannot be
// withdrawn, so the record always carries an EXPIRES_AT.
const pmmManifestTTL = 30 * 24 * time.Hour

// pmmRefreshInterval re-signs so EXPIRES_AT stays in the future and a re-staged
// artifact is picked up without a restart.
const pmmRefreshInterval = 6 * time.Hour

// pmmCatalogFile is the catalog, relative to <storage>/modules/. It is deployed
// DATA: it declares what this node offers and the tier/access policy of each
// entry. No tier table is compiled into the daemon.
const pmmCatalogFile = "modules-catalog.json"

// pmmPlugin serves the $PMM provider module manifest and the artifact bytes it
// names.
//
// It is a CONNECTOR. It reads a catalog, hashes the bytes the node actually
// serves, signs the canonical statement with the node key, and serves the
// result. Every policy decision arrives as data.
//
// Registering it as a plugin rather than wiring it into the daemon's main mux is
// deliberate: plugins.Manager.RegisterRoutes is already mounted on the admin mux,
// so this surface costs the daemon's wiring exactly one Register call.
type pmmPlugin struct {
	node *Node

	mu        sync.RWMutex
	source    *pmm.StaticSource
	artifacts map[string]string // ARTIFACT_PATH -> on-disk file
	root      string            // artifact root
	catalog   string            // catalog path
	store     *pmm.SubmissionStore
	stop      chan struct{}
	mounted   bool

	// submitMu latches the self-serve submission lane against the manifest
	// rebuild: a submission is Save+publish as one unit, and the refresh loop
	// cannot interleave a rebuild between the two. The manifest GET/artifact
	// GET stay lock-free (they read the StaticSource and the artifacts map
	// under mu), so an idle lane costs the hot path nothing.
	submitMu sync.Mutex
}

func newPMMPlugin(n *Node) *pmmPlugin {
	return &pmmPlugin{node: n, source: &pmm.StaticSource{}, artifacts: map[string]string{}, stop: make(chan struct{})}
}

// ID implements plugins.Plugin.
func (p *pmmPlugin) ID() string { return PMMPluginID }

// nodeSigner adapts the node's Ed25519 signing key to pmm.Signer.
type nodeSigner struct{ priv ed25519.PrivateKey }

func (s nodeSigner) Sign(data []byte) ([]byte, error) {
	if len(s.priv) != ed25519.PrivateKeySize {
		return nil, errors.New("pmm: node signing key unavailable")
	}
	return ed25519.Sign(s.priv, data), nil
}

// signer returns the node key, or an error when the node cannot sign.
//
// Fail-closed: with no key there is no manifest. An unsigned $PMM is worthless —
// the record exists to be verified — so this never degrades to serving one.
func (p *pmmPlugin) signer() (pmm.Signer, error) {
	raw := p.node.SigningKey()
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return nodeSigner{ed25519.PrivateKey(raw)}, nil
	case ed25519.SeedSize:
		return nodeSigner{ed25519.NewKeyFromSeed(raw)}, nil
	default:
		return nil, fmt.Errorf("pmm: node signing key is %d bytes, expected %d or %d",
			len(raw), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

// Start builds and signs the first manifest.
//
// A missing catalog is NOT an error: it means this node publishes no offering,
// and the endpoint correctly stays absent. A catalog that is present but broken
// IS an error, because it means the operator meant to publish something and the
// node would otherwise silently serve nothing.
func (p *pmmPlugin) Start(_ context.Context, _ plugins.RuntimeContext) error {
	base := strings.TrimSpace(p.node.config.Storage.Path)
	if base == "" {
		return nil
	}
	root := filepath.Join(base, "modules")
	catalog := filepath.Join(root, pmmCatalogFile)
	if _, err := os.Stat(catalog); err != nil {
		log.Infof("pmm: no module catalog at %s; this node publishes no manifest", catalog)
		return nil
	}

	p.mu.Lock()
	p.root, p.catalog = root, catalog
	p.store = pmm.NewSubmissionStore(root)
	p.mu.Unlock()

	if err := p.rebuild(); err != nil {
		return fmt.Errorf("pmm: %w", err)
	}
	p.mu.Lock()
	p.mounted = true
	p.mu.Unlock()

	go p.refreshLoop()
	return nil
}

func (p *pmmPlugin) refreshLoop() {
	ticker := time.NewTicker(pmmRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			if err := p.rebuild(); err != nil {
				// Keep serving the previous, still-valid manifest.
				log.Warnf("pmm: manifest refresh failed, serving previous: %v", err)
			}
		}
	}
}

// rebuild re-reads the catalog, merges the self-serve submission store, re-
// hashes every served artifact from disk, and re-signs.
//
// Hashing from disk rather than trusting the catalog's declared hash is the
// point: a catalog that disagrees with the artifact it names is exactly the
// failure this record exists to prevent, and a stale hash would be signed into a
// manifest every client then rejects.
func (p *pmmPlugin) rebuild() error {
	p.submitMu.Lock()
	defer p.submitMu.Unlock()
	return p.rebuildLocked()
}

// rebuildLocked is rebuild with the submission latch already held. It exists so
// a submission that just landed can publish itself as one unit (submitMu held
// by the HTTP handler) without re-entering the latch.
func (p *pmmPlugin) rebuildLocked() error {
	p.mu.RLock()
	root, catalog := p.root, p.catalog
	p.mu.RUnlock()

	signer, err := p.signer()
	if err != nil {
		return err
	}
	trust, err := p.trustAnchor()
	if err != nil {
		return err
	}
	cf, err := pmm.LoadCatalog(catalog, root)
	if err != nil {
		return err
	}
	if p.store != nil {
		// Self-serve submissions merge AFTER the operator catalog so the
		// operator ALWAYS wins on a MODULE_ID collision. A submission that
		// cannot be re-hashed from disk is skipped with a logged reason — a
		// deleted record is a withdrawal, a corrupt one is stopped at the
		// door — and can never take the signed manifest down.
		stored, skips := p.store.Load()
		for _, skipErr := range skips {
			log.Warnf("pmm: skipping submission: %v", skipErr)
		}
		if added, suppressed := cf.MergeSubmissions(stored); added > 0 || len(suppressed) > 0 {
			log.Infof("pmm: merged %d self-serve submission(s), %d suppressed by operator catalog", added, len(suppressed))
		}
	}
	m, err := pmm.BuildManifest(cf, *trust, uint64(time.Now().Unix()),
		"https://"+trust.ProviderDomain+pmm.Path, time.Now(), pmmManifestTTL, signer)
	if err != nil {
		return err
	}

	next := make(map[string]string, len(cf.Entries))
	for i := range cf.Entries {
		e := &cf.Entries[i]
		if e.AccessPolicy == "ANONYMOUS" && e.ArtifactPath != "" && e.SourceArtifact != "" {
			next[e.ArtifactPath] = e.SourceArtifact
		}
	}

	p.mu.Lock()
	p.artifacts = next
	p.mu.Unlock()
	p.source.Set(m, cf.Browse)

	// Re-declare module families to the plugin registry from the catalog we
	// just read. A re-staged catalog therefore re-categorizes the $PLG listing
	// lane on the same refresh that re-signs the $PMM manifest, instead of the
	// two surfaces drifting apart until the next restart.
	types := make(map[string]string, len(cf.Entries))
	for i := range cf.Entries {
		e := &cf.Entries[i]
		for _, key := range []string{e.PluginID, e.ModuleID} {
			if k := strings.TrimSpace(key); k != "" {
				types[k] = strings.TrimSpace(e.PluginType)
			}
		}
	}
	p.node.PluginRegistry().SetPluginTypes(types)

	log.Infof("pmm: manifest signed — %d module(s), %d anonymously fetchable", len(m.Modules), len(next))
	return nil
}

// applyModuleCatalogPluginTypes joins the deployed $PMM module catalog's
// declared families onto the plugin registry.
//
// A missing catalog is not an error — it means this node declares no families,
// and every listing then says Unspecified, which is the truth. A catalog that
// is present but unreadable IS worth a log: the operator meant to declare
// something and the shelves will silently read Unspecified instead.
func (n *Node) applyModuleCatalogPluginTypes(reg *license.PluginRegistry) {
	if n == nil || reg == nil || n.config == nil {
		return
	}
	base := strings.TrimSpace(n.config.Storage.Path)
	if base == "" {
		return
	}
	catalog := filepath.Join(base, "modules", pmmCatalogFile)
	if _, err := os.Stat(catalog); err != nil {
		return
	}
	types, err := pmm.CatalogPluginTypes(catalog)
	if err != nil {
		log.Warnf("pmm: module families unavailable from %s; listings will read Unspecified: %v", catalog, err)
		return
	}
	reg.SetPluginTypes(types)
}

// ModuleCapabilityClass resolves a module ID to the $CCT capabilityClass member
// it shelves under, reading the SAME registry join that $PLG and $PMM encode
// from.
//
// This is the storefront's only door to a category. It is a live lookup rather
// than a snapshot, so a re-staged catalog re-shelves storefront listings on the
// same refresh that re-categorizes the $PLG lane — there is no second copy to
// forget to update.
//
// An unknown module, an uncategorized one, and a node with no registry at all
// are the same answer: UNSPECIFIED. $CCT defines that to render ungrouped, and
// this function never guesses a class from an ID, a name or a tag.
func (n *Node) ModuleCapabilityClass(moduleID string) string {
	if n == nil {
		return cct.Unspecified
	}
	reg := n.PluginRegistry()
	if reg == nil {
		return cct.Unspecified
	}
	asset, ok := reg.Get(strings.TrimSpace(moduleID))
	if !ok {
		return cct.Unspecified
	}
	return cct.FromPluginType(asset.PluginType)
}

// trustAnchor derives the anchor from the node's own identity.
//
// Fields the node cannot PROVE stay empty. The DNS proof in particular is left
// blank until the TXT record exists: a fabricated proof is worse than a missing
// one, because a client that trusts it has been lied to by the very record whose
// purpose is to be verifiable.
func (p *pmmPlugin) trustAnchor() (*pmm.TrustAnchor, error) {
	domain := p.providerDomain()
	if domain == "" {
		return nil, errors.New("pmm: no provider domain; the manifest must name the origin it is served from")
	}
	a := &pmm.TrustAnchor{
		ProviderDomain:     domain,
		NodePeerID:         p.node.PeerID().String(),
		SignatureAlgorithm: "ed25519",
		DNSProofRecordName: "_sdnkey." + domain,
	}
	if p.node.identityBundle != nil {
		a.NodeXpub = p.node.identityBundle.XPub
		a.SigningKeyPath = p.node.identityBundle.SigningKeyPath
	}
	if p.node.identity != nil && p.node.identity.SigningPubKey != nil {
		if raw, err := p.node.identity.SigningPubKey.Raw(); err == nil {
			a.SigningPublicKey = hexLower(raw)
		}
	}
	if a.SigningPublicKey == "" {
		return nil, errors.New("pmm: node has no signing public key to anchor the manifest")
	}
	return a, nil
}

// providerDomain is declared by the catalog, so adding this surface needs no
// config-schema change and the domain always matches the catalog it describes.
func (p *pmmPlugin) providerDomain() string {
	p.mu.RLock()
	catalog := p.catalog
	p.mu.RUnlock()
	if catalog == "" {
		return ""
	}
	d, err := pmm.CatalogProviderDomain(catalog)
	if err != nil {
		return ""
	}
	return d
}

func hexLower(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}

// RegisterRoutes mounts the manifest and the artifact bytes it names.
//
// Both live OUTSIDE the /api/ prefix, which is what makes them anonymous: the
// daemon's auth wall gates /api/ and /orbpro-key-broker/ only, so no allowlist
// entry is needed and none is added.
func (p *pmmPlugin) RegisterRoutes(mux *http.ServeMux) {
	p.mu.RLock()
	mounted := p.mounted
	p.mu.RUnlock()
	if !mounted {
		// No catalog: publish nothing. A 404 correctly says "this provider
		// serves no manifest"; an empty signed one would be a claim we cannot
		// support.
		return
	}
	mux.Handle(pmm.Path, pmm.Handler(p.source))
	mux.Handle(pmm.ArtifactPrefix, p.artifactHandler())
	if p.store != nil {
		// Self-serve submission lane: ANONYMOUS, no admin wallet, mounted next
		// to the manifest it feeds. The route is outside /api/ like the rest
		// of this surface, so it is anonymous by construction.
		mux.Handle(pmm.SubmissionPath, p.submissionHandler())
	}
}

// submissionHandler wraps the package-level lane handler with the plugin's
// own latch so Save+publish is one unit and a concurrent refresh cannot
// interleave (see pmmPlugin.submitMu).
func (p *pmmPlugin) submissionHandler() http.Handler {
	inner := pmm.NewSubmissionHandler(p.store, p.catalogHasModule, func() error { return p.rebuildLocked() })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.submitMu.Lock()
		defer p.submitMu.Unlock()
		inner.ServeHTTP(w, r)
	})
}

// catalogHasModule reports whether the OPERATOR catalog manages a MODULE_ID.
// The submission lane refuses such IDs up front (the operator wins collisions
// by construction at merge time; this is the prompt 409 rather than the slow
// silent suppression). The decode-only read re-hashes nothing — the hash cost
// belongs to the rebuild, not a request path.
func (p *pmmPlugin) catalogHasModule(moduleID string) bool {
	p.mu.RLock()
	catalog := p.catalog
	p.mu.RUnlock()
	if catalog == "" {
		return false
	}
	cf, err := pmm.DecodeCatalog(catalog)
	if err != nil {
		// A broken operator catalog will fail the rebuild anyway; answer
		// "no collision" and let the merge's suppression rule be the belt.
		return false
	}
	for i := range cf.Entries {
		if cf.Entries[i].ModuleID == moduleID {
			return true
		}
	}
	return false
}

// artifactHandler serves the portable WASM bytes the manifest names.
//
// The manifest IS the access control: only a path the current manifest lists as
// ANONYMOUS is served, by exact match. An ENTITLED module never carries a path,
// so closed bytes are unreachable by construction, and nothing is derived from
// the URL except a map lookup, so there is no traversal.
func (p *pmmPlugin) artifactHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m, _, err := p.source.Manifest()
		if err != nil {
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		var contentHash string
		for i := range m.Modules {
			e := &m.Modules[i]
			if e.AccessPolicy == "ANONYMOUS" && e.ArtifactPath == r.URL.Path {
				contentHash = e.ContentHash
				break
			}
		}
		p.mu.RLock()
		srcFile, ok := p.artifacts[r.URL.Path]
		root := p.root
		p.mu.RUnlock()
		if contentHash == "" || !ok {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Clean(filepath.Join(root, srcFile)))
		if err != nil {
			http.Error(w, "artifact unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// Content-addressed: bytes for a given hash never change, and the client
		// verifies this value against the manifest anyway.
		w.Header().Set("ETag", `"`+contentHash+`"`)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	})
}

// Close implements plugins.Plugin.
func (p *pmmPlugin) Close() error {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	return nil
}
