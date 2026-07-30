package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/pmm"
)

// pmmManifestTTL bounds anonymous trust in the manifest. An unexpiring signed
// manifest cannot be withdrawn, so the record always carries an EXPIRES_AT and
// the node re-signs well before it lapses.
const pmmManifestTTL = 30 * 24 * time.Hour

// pmmRefreshInterval re-signs the manifest so EXPIRES_AT stays in the future and
// a re-staged artifact is picked up without a restart.
const pmmRefreshInterval = 6 * time.Hour

// registerPMMRoutes mounts the $PMM provider module manifest and the artifact
// bytes it points at.
//
// Deliberately ONE call, so wiring it costs the daemon's main wiring a single
// line. Both routes live outside the /api/ prefix, which is what makes them
// anonymous: the auth wall gates /api/ and /orbpro-key-broker/ only, so no
// allowlist entry is needed and none is added.
//
// Returns nil (mounting nothing) when the node cannot produce a SIGNED manifest.
// That is the fail-closed choice: a 404 correctly says "this provider publishes
// no manifest", whereas an unsigned or unverifiable one would be worse than
// absent — clients are told to trust what they can verify.
func registerPMMRoutes(
	mux *http.ServeMux,
	src providerDescriptorSource,
	signer pmm.Signer,
	catalogPath string,
	artifactRoot string,
	providerDomain string,
) error {
	if mux == nil {
		return fmt.Errorf("pmm: nil mux")
	}
	if strings.TrimSpace(catalogPath) == "" {
		// No catalog declared: this node offers nothing. Not an error.
		return nil
	}
	if signer == nil {
		return fmt.Errorf("pmm: refusing to mount an unsigned manifest")
	}
	if _, err := os.Stat(catalogPath); err != nil {
		return fmt.Errorf("pmm: catalog %s: %w", catalogPath, err)
	}

	source := &pmm.StaticSource{}
	// artifactPath -> on-disk source file, rebuilt with the manifest. Held
	// separately because the record itself must never carry local paths.
	var artifactMu sync.RWMutex
	artifacts := map[string]string{}

	rebuild := func() error {
		trust, err := pmmTrustAnchor(src, providerDomain)
		if err != nil {
			return err
		}
		cf, err := pmm.LoadCatalog(catalogPath, artifactRoot)
		if err != nil {
			return err
		}
		m, err := pmm.BuildManifest(cf, *trust, uint64(time.Now().Unix()),
			"https://"+providerDomain+pmm.Path, time.Now(), pmmManifestTTL, signer)
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
		artifactMu.Lock()
		artifacts = next
		artifactMu.Unlock()
		source.Set(m, cf.Browse)
		return nil
	}
	if err := rebuild(); err != nil {
		return err
	}

	mux.Handle(pmm.Path, pmm.Handler(source))
	mux.Handle(pmm.ArtifactPrefix, pmmArtifactHandler(source, artifactRoot, func(p string) (string, bool) {
		artifactMu.RLock()
		defer artifactMu.RUnlock()
		src, ok := artifacts[p]
		return src, ok
	}))

	go func() {
		ticker := time.NewTicker(pmmRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := rebuild(); err != nil {
				// Keep serving the previous, still-valid manifest.
				log.Warnf("pmm: manifest refresh failed, serving previous manifest: %v", err)
			}
		}
	}()
	return nil
}

// pmmTrustAnchor derives the manifest's trust anchor from the node's own
// identity, reusing the same provider descriptor the module-delivery lane
// publishes so the two can never disagree about who this node is.
//
// Fields the node cannot PROVE stay empty. The DNS proof in particular is left
// blank until the TXT record actually exists — a fabricated proof is worse than
// a missing one, because a client that trusts it has been lied to by the record
// whose entire purpose is to be verifiable.
func pmmTrustAnchor(src providerDescriptorSource, providerDomain string) (*pmm.TrustAnchor, error) {
	desc, err := buildProviderDescriptor(src)
	if err != nil {
		return nil, fmt.Errorf("pmm: provider descriptor unavailable: %w", err)
	}
	anchor := &pmm.TrustAnchor{
		ProviderDomain:     providerDomain,
		NodePeerID:         desc.PeerID,
		SigningKeyPath:     pmmSigningKeyPath,
		SignatureAlgorithm: "ed25519",
		DNSProofRecordName: "_sdnkey." + providerDomain,
	}
	if desc.Identity != nil {
		anchor.NodeXpub = desc.Identity.XPub
		anchor.SigningPublicKey = desc.Identity.SigningPublicKey
		for _, addr := range desc.Identity.Addresses {
			anchor.BondAddresses = append(anchor.BondAddresses, pmm.ChainProof{
				Chain:     addr.Chain,
				Address:   addr.Address,
				KeyPath:   addr.KeyPath,
				PublicKey: addr.PublicKey,
			})
		}
	}
	if anchor.SigningPublicKey == "" {
		anchor.SigningPublicKey = desc.PublicKey
	}
	if anchor.SigningPublicKey == "" {
		return nil, fmt.Errorf("pmm: node has no signing public key to anchor the manifest")
	}
	return anchor, nil
}

// pmmSigningKeyPath is the HD path of the node signing key (SLIP-10 hardened),
// matching IdentityBundle.SigningKeyPath.
const pmmSigningKeyPath = "m/44'/0'/0'/0'/0'"

// pmmArtifactHandler serves the portable WASM bytes the manifest names.
//
// It serves ONLY what the current manifest lists with an ANONYMOUS access
// policy, looked up by exact ARTIFACT_PATH. That is the whole access control:
// an ENTITLED module never carries a path, so it can never be reached here, and
// a path that is not in the manifest is not served at all. No directory
// traversal is possible because nothing is derived from the URL except an exact
// map lookup.
func pmmArtifactHandler(source pmm.Source, artifactRoot string, resolve func(string) (string, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m, _, err := source.Manifest()
		if err != nil {
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		// Serve ONLY a path the current manifest lists as ANONYMOUS. An
		// ENTITLED module never carries a path, so it can never be reached.
		var contentHash string
		for i := range m.Modules {
			e := &m.Modules[i]
			if e.AccessPolicy == "ANONYMOUS" && e.ArtifactPath == r.URL.Path {
				contentHash = e.ContentHash
				break
			}
		}
		srcFile, ok := resolve(r.URL.Path)
		if contentHash == "" || !ok {
			http.NotFound(w, r)
			return
		}
		full := filepath.Join(artifactRoot, srcFile)
		data, err := os.ReadFile(filepath.Clean(full))
		if err != nil {
			http.Error(w, "artifact unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// Content-addressed: the bytes for a given hash never change, and the
		// client is expected to verify this value against the manifest anyway.
		w.Header().Set("ETag", `"`+contentHash+`"`)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	})
}
