package flowrt

// netmodule.go is the Phase-2 Part-B network-module path: it lets the flow editor
// reference a module by CONTENT HASH and have the node FETCH that module's
// guest-link bundle from the content-addressed blockstore on demand, VERIFY its
// signature (signed-only), and STAGE it so it becomes bakeable — without the
// module having been pre-staged from a local dist tree.
//
// Discovery status (see the loop's Part-B report): full DHT module-ANNOUNCE does
// NOT exist in the node yet — the SDN DHT flag (plugin/plugins/sdnflag) is peer
// rendezvous only, and the gossipsub channels carry SDS DATA records, not code
// artifacts. What DOES exist and is reused here is the content-addressed
// blockstore put/get with verify-by-hash (sdn/appmanifest.StoreModuleBytes /
// ResolveModuleByContentHash). So this file implements the REACHABLE MVP:
// fetch-a-module-bundle-by-hash + signed-only gate + stage-to-bake, plus a small
// node-local catalog of network-advertised signed modules the palette lists.
// A real DHT announce/discover of module bundle hashes is future work.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flowcc"
)

// ModuleBundle is the content-addressed envelope a network module ships in: the
// guest-link object plus the metadata + manifest the node needs to stage + bake
// it. It is stored as a single raw block; the lowercase-hex sha256 of its
// canonical bytes (the "bundle hash") is the content hash the editor references
// and the digest a publisher signs.
type ModuleBundle struct {
	PluginID string `json:"pluginId"`
	Object   []byte `json:"object"`             // module-link.o bytes
	Metadata []byte `json:"metadata"`           // dist metadata.json bytes (methodSymbols)
	Manifest []byte `json:"manifest,omitempty"` // plugin-manifest.json bytes (typed ports)
}

// MarshalBundle serializes a bundle to its canonical content-addressed bytes.
// json.Marshal emits struct fields in declaration order, so the encoding is
// deterministic and its sha256 is a stable content hash.
func MarshalBundle(b *ModuleBundle) ([]byte, error) { return json.Marshal(b) }

// HashBundleBytes returns the lowercase-hex sha256 of canonical bundle bytes.
func HashBundleBytes(bundleBytes []byte) string {
	sum := sha256.Sum256(bundleBytes)
	return hex.EncodeToString(sum[:])
}

// NetModuleFetcher fetches a signed module bundle by its bundle hash from a
// content-addressed blockstore and stages it so it becomes bakeable. It is
// SIGNED-ONLY and FAIL-CLOSED: a bundle stages only if it carries a valid Ed25519
// signature over its bundle-hash digest by a key in TrustedSigners. This mirrors
// the modulert publication-signature primitive (crypto/ed25519 over the
// artifact's content-hash digest, verified against a trusted signer set) applied
// to the guest-link bundle; on a live node feed TrustedSigners from the same
// publisher keys modulert.ModuleSignaturePolicy pins.
type NetModuleFetcher struct {
	bs      appmanifest.ModuleBlockstore
	trusted []ed25519.PublicKey
}

// NewNetModuleFetcher builds a fetcher over a blockstore and a trusted signer
// set. An empty trusted set trusts nobody (every fetch is refused) — signed-only.
func NewNetModuleFetcher(bs appmanifest.ModuleBlockstore, trusted []ed25519.PublicKey) *NetModuleFetcher {
	return &NetModuleFetcher{bs: bs, trusted: trusted}
}

// StoreBundle content-addresses a module bundle in the fetcher's blockstore and
// returns its bundle hash — the content hash the editor references and a
// publisher signs. It is the publish-side counterpart of fetchAndVerify: the
// flow-module publish path (Baker.PublishFlowAsModule) persists a freshly
// emitted flow-module here so a later fetch-to-bake resolves it by hash.
func (f *NetModuleFetcher) StoreBundle(ctx context.Context, bundleBytes []byte) (string, error) {
	if f == nil || f.bs == nil {
		return "", fmt.Errorf("netmodule: no blockstore configured")
	}
	ch, _, err := appmanifest.StoreModuleBytes(ctx, f.bs, bundleBytes)
	if err != nil {
		return "", fmt.Errorf("netmodule: store bundle: %w", err)
	}
	return ch, nil
}

// FetchAndStage fetches + verifies + stages a network module into `home` so it
// becomes bakeable. Signed-only.
func (f *NetModuleFetcher) FetchAndStage(ctx context.Context, home flowcc.Home, ref BakeModuleRef) error {
	b, err := f.fetchAndVerify(ctx, ref)
	if err != nil {
		return err
	}
	return flowcc.StageModuleBytes(home, b.PluginID, b.Object, b.Metadata, b.Manifest, "network")
}

// PublishNetworkModule verifies a signed module bundle (fetch-by-hash + signed-
// only gate) and returns the catalog entry the editor lists it by — WITHOUT
// staging it. The guest-link object is staged lazily at bake time (fetch-to-bake).
func (f *NetModuleFetcher) PublishNetworkModule(ctx context.Context, ref BakeModuleRef) (NetModuleEntry, error) {
	b, err := f.fetchAndVerify(ctx, ref)
	if err != nil {
		return NetModuleEntry{}, err
	}
	return NetModuleEntry{
		PluginID:     b.PluginID,
		BundleHash:   strings.ToLower(strings.TrimSpace(ref.BundleHash)),
		Signature:    ref.Signature,
		SignerPubKey: strings.TrimSpace(ref.SignerPubKey),
		Methods:      bundleMethods(b),
	}, nil
}

// fetchAndVerify resolves the bundle by hash (verify-by-hash) and enforces the
// signed-only gate, returning the decoded bundle.
func (f *NetModuleFetcher) fetchAndVerify(ctx context.Context, ref BakeModuleRef) (*ModuleBundle, error) {
	if f == nil || f.bs == nil {
		return nil, fmt.Errorf("netmodule: no blockstore configured")
	}
	hash := strings.ToLower(strings.TrimSpace(ref.BundleHash))
	if hash == "" {
		return nil, fmt.Errorf("netmodule: module %q has no bundleHash to fetch", ref.PluginID)
	}
	bundleBytes, err := appmanifest.ResolveModuleByContentHash(ctx, f.bs, hash) // verify-by-hash
	if err != nil {
		return nil, fmt.Errorf("netmodule: fetch bundle %s: %w", hash, err)
	}
	if err := f.verifySignature(hash, ref.Signature, ref.SignerPubKey); err != nil {
		return nil, err
	}
	var b ModuleBundle
	if err := json.Unmarshal(bundleBytes, &b); err != nil {
		return nil, fmt.Errorf("netmodule: decode bundle %s: %w", hash, err)
	}
	if b.PluginID == "" {
		return nil, fmt.Errorf("netmodule: bundle %s carries no pluginId", hash)
	}
	if ref.PluginID != "" && b.PluginID != ref.PluginID {
		return nil, fmt.Errorf("netmodule: bundle %s pluginId %q does not match ref %q", hash, b.PluginID, ref.PluginID)
	}
	return &b, nil
}

// verifySignature enforces the signed-only gate: a valid Ed25519 signature over
// the bundle-hash digest by a TRUSTED signer. Fail-closed — an empty trusted set
// trusts nobody and any decode/verify failure rejects.
func (f *NetModuleFetcher) verifySignature(bundleHash, sigB64, signerHex string) error {
	if len(f.trusted) == 0 {
		return fmt.Errorf("netmodule: signed-only: no trusted signers configured (refusing module %s)", bundleHash)
	}
	digest, err := hex.DecodeString(bundleHash)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("netmodule: signed-only: invalid bundle hash %q", bundleHash)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(signerHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("netmodule: signed-only: missing/invalid signer public key for %s", bundleHash)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("netmodule: signed-only: missing/invalid signature for %s", bundleHash)
	}
	trusted := false
	for _, k := range f.trusted {
		if len(k) == ed25519.PublicKeySize && string(k) == string(pub) {
			trusted = true
			break
		}
	}
	if !trusted {
		return fmt.Errorf("netmodule: signed-only: signer %s… is not in the trusted set", short(signerHex))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), digest, sig) {
		return fmt.Errorf("netmodule: signature verification failed for %s", bundleHash)
	}
	return nil
}

// bundleMethods reconstructs the bakeable methods (methodSymbols) + their typed
// ports (from the bundle manifest) for the palette — the same shape a locally
// staged module exposes.
func bundleMethods(b *ModuleBundle) []StagedMethod {
	var meta flowcc.ModuleMetadata
	_ = json.Unmarshal(b.Metadata, &meta)
	portsByMethod := map[string]flowcc.MethodPortSchema{}
	for _, mp := range flowcc.MethodPortsFromManifestBytes(b.Manifest) {
		portsByMethod[mp.MethodID] = mp
	}
	methods := make([]StagedMethod, 0, len(meta.MethodSymbols))
	for m := range meta.MethodSymbols {
		sm := StagedMethod{MethodID: m}
		if mp, ok := portsByMethod[m]; ok {
			sm.InputPorts = mp.InputPorts
			sm.OutputPorts = mp.OutputPorts
		}
		methods = append(methods, sm)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].MethodID < methods[j].MethodID })
	return methods
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ---------------------------------------------------------------------------
// Network-module catalog — the node's set of network-advertised SIGNED modules
// the palette lists (source "network"). Full DHT announce/discover does not
// exist yet; entries are added by a verified publish, not discovered.
// ---------------------------------------------------------------------------

// NetModuleEntry is one network-advertised module: enough for the editor to list
// + wire it (typed methods) and to bake it (the signed content-hash ref). The
// guest-link object is fetched + staged lazily at bake time.
type NetModuleEntry struct {
	PluginID     string
	BundleHash   string
	Signature    string
	SignerPubKey string
	Methods      []StagedMethod
}

// ref rebuilds the bake module ref that fetches + verifies this entry.
func (e NetModuleEntry) ref() BakeModuleRef {
	return BakeModuleRef{
		PluginID:     e.PluginID,
		BundleHash:   e.BundleHash,
		Signature:    e.Signature,
		SignerPubKey: e.SignerPubKey,
	}
}

// NetModuleCatalog is the node's set of network-advertised signed modules, keyed
// by pluginId, safe for concurrent use.
type NetModuleCatalog struct {
	mu      sync.RWMutex
	entries map[string]NetModuleEntry
}

// NewNetModuleCatalog returns an empty catalog.
func NewNetModuleCatalog() *NetModuleCatalog {
	return &NetModuleCatalog{entries: map[string]NetModuleEntry{}}
}

// Put registers (or replaces) a network module entry.
func (c *NetModuleCatalog) Put(e NetModuleEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[e.PluginID] = e
}

// Get returns the entry for a pluginId.
func (c *NetModuleCatalog) Get(pluginID string) (NetModuleEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[pluginID]
	return e, ok
}

// List returns all catalog entries, sorted by pluginId.
func (c *NetModuleCatalog) List() []NetModuleEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]NetModuleEntry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out
}
