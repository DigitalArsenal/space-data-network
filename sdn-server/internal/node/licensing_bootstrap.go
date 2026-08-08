package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	lcf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCF"
	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

const (
	providerSigningSlotID  = "provider-signing"
	providerSigningKeyID   = "licensing.provider.signing"
	providerWrappingSlotID = "provider-wrapping"
	providerWrappingKeyID  = "licensing.provider.wrapping"

	licensingConfigMessageTypeConfigure = 0
	licensingConfigRoleProvider         = 0

	keyReferenceRoleProviderSigning  = 1
	keyReferenceRoleProviderWrapping = 2
	keyReferenceAlgorithmEd25519Seed = 1
	keyReferenceAlgorithmX25519      = 3

	keyMaterialRolePublicationContent = 1
	keyMaterialRoleDecryptKey         = 4
	keyMaterialAlgorithmX25519Private = 3
	keyMaterialAlgorithmAes256Gcm     = 5
	keyMaterialEncodingRawBytes       = 1

	publicationTrailerFooterLength = 8
	publicationTrailerMagicText    = "$REC"

	pluginTypeAnalysis = 3
)

// The licensing challenge's two time bounds, and which one carries the
// security property.
//
// MAX_CLOCK_SKEW_MS is a CLOCK-DISAGREEMENT tolerance. The key server compares
// the client's REQUESTED_AT against the provider's own arrival clock
// (key_server.cpp:2372-2374). But a browser stamps REQUESTED_AT before it
// encodes the request and before it dials (sdn-js module-delivery.ts:217,235,256),
// and it re-sends the SAME stamped bytes to every candidate multiaddr in turn
// (module-delivery.ts:671-687). So every millisecond of dial, handshake and
// failed-candidate retry is charged against this window. At 5 s that made it an
// end-to-end TRANSPORT budget wearing the name of a clock check, and it refused
// live grants under ordinary concurrency: measured on host-01 over one boot,
// 160 challenge requests -> 132 granted, 24 refused reason=invalid_timestamp,
// every refusal on the requester's FIRST hop, the same module flipping
// granted -> refused -> granted inside 18 s, ten refusals inside 37 ms.
//
// 300 s restores the key server's own kDefaultMaxSkewMs (key_server.cpp:69),
// which the 5 s override was suppressing (key_server.cpp:1135-1137). This is
// not widening a security window, because REQUESTED_AT was never the freshness
// control:
//
//   - the provider mints a random 32-byte CHALLENGE_NONCE and computes
//     EXPIRES_AT on its OWN clock (key_server.cpp:2400-2406) under
//     CHALLENGE_TTL_MS below, and the proof leg must sign over that nonce;
//   - that leg does not fail — 132 of 132 in the measured boot;
//   - replaying a stale Request frame therefore buys an attacker a fresh
//     challenge and nothing else.
//
// CHALLENGE_TTL_MS is the bound that DOES carry freshness, it is anchored to
// server-issued state rather than to the client's clock, and it is deliberately
// unchanged.
const (
	licensingMaxClockSkewMS = 300_000
	licensingChallengeTTLMS = 30_000
)

// ModuleDeliveryListing is a canonical $PLG listing generated from a published
// module catalog asset.
type ModuleDeliveryListing struct {
	PluginID  string
	Version   string
	Payload   []byte
	Timestamp string
}

func catalogPublicationAssets(reg *license.PluginRegistry) []*license.PluginAsset {
	if reg == nil {
		return nil
	}

	descriptors := reg.ListPublic()
	assets := make([]*license.PluginAsset, 0, len(descriptors))
	for _, descriptor := range descriptors {
		pluginID := strings.TrimSpace(descriptor.ID)
		if pluginID == "" {
			continue
		}
		asset, ok := reg.Get(pluginID)
		if !ok || asset == nil {
			continue
		}
		assets = append(assets, asset)
	}
	return assets
}

// BuildModuleDeliveryListings materializes canonical $PLG listings for the
// current plugin catalog so browser clients can browse signed module metadata
// without depending on the legacy storefront API.
func BuildModuleDeliveryListings(reg *license.PluginRegistry) ([]ModuleDeliveryListing, error) {
	assets := catalogPublicationAssets(reg)
	listings := make([]ModuleDeliveryListing, 0, len(assets))
	for _, asset := range assets {
		payload, err := buildPublicationDescriptorFrame(asset)
		if err != nil {
			return nil, fmt.Errorf("build publication descriptor for %q: %w", asset.ID, err)
		}
		listings = append(listings, ModuleDeliveryListing{
			PluginID:  strings.TrimSpace(asset.ID),
			Version:   strings.TrimSpace(asset.Version),
			Payload:   payload,
			Timestamp: strings.TrimSpace(asset.UploadedAt),
		})
	}
	return listings, nil
}

func (n *Node) buildModuleNodeContext() (*modulert.NodeContext, error) {
	nodeCtx := &modulert.NodeContext{}
	if n != nil && n.host != nil {
		nodeCtx.PeerID = n.host.ID().String()
	}
	if n != nil && n.config != nil {
		// Operator override for the SCHEDULED (cron / run-now) module invoke
		// budget on slow hosts. Zero (unset) leaves the modulert built-in
		// default (10m) in force; interactive invokes are never affected.
		nodeCtx.ScheduledInvokeTimeout = n.config.Modules.ScheduledInvokeTimeout
	}
	signingSeed, wrappingKey, err := n.moduleRuntimeKeySlots()
	if err != nil {
		return nil, err
	}

	// DOMAIN SEPARATION GUARD (owner ruling 2026-08-07). The seed above signs
	// licensing grants; n.updateRootSigningSeed() signs SDN-UPDATE-MANIFEST-V1 and
	// module publication statements — fleet code authority. They must be DIFFERENT
	// keys. If they are not, the grant slot is not provisioned at all: the node
	// keeps serving everything else, and the licensing lane fails closed with the
	// recorded reason (see buildLicensingRuntimeConfigFrame).
	//
	// This is checked at every context build rather than once at boot because a
	// dev seed override (SDN_DEV_PROVIDER_SIGNING_SEED_HEX) can reintroduce the
	// collision after boot, and because a guard that runs where the key is
	// provisioned cannot be bypassed by a caller that skips boot.
	if len(signingSeed) == 32 {
		if reason := grantSigningKeyDomainConflict(signingSeed, n.updateRootSigningSeed()); reason != "" {
			log.Errorf(
				"REFUSING to provision licensing key slot %q: %s. The licensing grant signer and the fleet update/publisher root MUST be different keys (owner ruling 2026-08-07, graph/tasks/sdn-grant-verifier-key-domain-separation.md). Module-delivery grants are DISABLED on this node until the grant key is derived at %s. Nothing else is affected.",
				providerSigningSlotID, reason, licensingGrantKeyPathLabel,
			)
			if nodeCtx.KeySlotRefusals == nil {
				nodeCtx.KeySlotRefusals = make(map[string]string, 1)
			}
			nodeCtx.KeySlotRefusals[providerSigningSlotID] = reason
			signingSeed = nil
		}
	}

	if len(wrappingKey) > 0 {
		nodeCtx.EncryptionKey = append([]byte(nil), wrappingKey...)
	}
	if len(wrappingKey) == 32 {
		if publicKeyHex, err := deriveP256PublicKeyHex(wrappingKey); err == nil {
			nodeCtx.PublicKeyHex = publicKeyHex
		}
	}
	if len(signingSeed) == 32 || len(wrappingKey) == 32 {
		nodeCtx.KeySlots = make(map[string][]byte, 2)
		// Loop B9.5: each slot is declared for exactly one algorithm so the
		// keyslot oracle rejects cross-protocol use (e.g. the Ed25519 signing
		// seed driven through keyslot.unwrap as an X25519 scalar).
		nodeCtx.KeySlotAlgorithms = make(map[string]string, 2)
	}
	if len(signingSeed) == 32 {
		nodeCtx.KeySlots[providerSigningSlotID] = append([]byte(nil), signingSeed...)
		nodeCtx.KeySlotAlgorithms[providerSigningSlotID] = modulert.KeySlotAlgorithmEd25519
	}
	if len(wrappingKey) == 32 {
		nodeCtx.KeySlots[providerWrappingSlotID] = append([]byte(nil), wrappingKey...)
		nodeCtx.KeySlotAlgorithms[providerWrappingSlotID] = modulert.KeySlotAlgorithmX25519
	}

	return nodeCtx, nil
}

// moduleRuntimeKeySlots resolves the two secrets the licensing runtime is given:
// the GRANT signing seed (Ed25519, "provider-signing" slot) and the wrapping key
// (X25519, "provider-wrapping" slot).
//
// The grant signing seed is the HD child at LicensingGrantKeyPath —
// m/44'/0'/<account>'/2'/0' — NOT the node identity signing key at
// m/44'/0'/<account>'/0'/0'. Owner ruling 2026-08-07, verbatim: "derive a
// grant-signing child from the node identity, keep the update root isolated".
// Before that ruling this function returned RawSigningKey(), which is why one
// ed25519 key held both fleet code authority and every anonymous browser grant.
func (n *Node) moduleRuntimeKeySlots() ([]byte, []byte, error) {
	envSigningSeed := readDevRuntimeKeySlotEnv("SDN_DEV_PROVIDER_SIGNING_SEED_HEX")
	envWrappingKey := readDevRuntimeKeySlotEnv("SDN_DEV_NODE_ENCRYPTION_KEY_HEX")

	if n != nil && n.identity != nil {
		rawSigningKey, err := n.identity.RawGrantSigningKey()
		if err != nil {
			return nil, nil, fmt.Errorf("derive licensing grant signing seed: %w", err)
		}

		var wrappingKey []byte
		if len(n.identity.EncryptionKey) > 0 {
			wrappingKey = append([]byte(nil), n.identity.EncryptionKey...)
		}
		if len(rawSigningKey) != 32 && len(envSigningSeed) == 32 {
			rawSigningKey = append([]byte(nil), envSigningSeed...)
		}
		if len(wrappingKey) != 32 && len(envWrappingKey) == 32 {
			wrappingKey = append([]byte(nil), envWrappingKey...)
		}
		return append([]byte(nil), rawSigningKey...), wrappingKey, nil
	}

	identity, err := n.loadLegacyServerIdentity()
	if err != nil {
		return nil, nil, err
	}
	if identity == nil {
		return envSigningSeed, envWrappingKey, nil
	}

	// LEGACY on-disk identity (no HD seed, so no BIP-32 path to derive at). The
	// ruling still binds: this lane gets a CHILD, produced by a domain-separated
	// KDF over the identity signing key rather than the identity signing key
	// itself. Deterministic (same identity → same child), one-way, and never
	// written to disk. See legacyGrantSigningSeed.
	var signingSeed []byte
	if identity.SigningKey != nil && len(identity.SigningKey.PrivateKey) >= 32 {
		signingSeed = legacyGrantSigningSeed(identity.SigningKey.PrivateKey[:32])
	}

	var wrappingKey []byte
	if identity.EncryptionKey != nil && len(identity.EncryptionKey.PrivateKey) > 0 {
		wrappingKey = append([]byte(nil), identity.EncryptionKey.PrivateKey...)
	}
	if len(signingSeed) != 32 && len(envSigningSeed) == 32 {
		signingSeed = append([]byte(nil), envSigningSeed...)
	}
	if len(wrappingKey) != 32 && len(envWrappingKey) == 32 {
		wrappingKey = append([]byte(nil), envWrappingKey...)
	}

	return signingSeed, wrappingKey, nil
}

// licensingGrantKeyPathLabel is the account-independent shape of the grant key's
// derivation path, for log lines and errors. The concrete path an identity used
// is on DerivedIdentity.GrantSigningKeyPath.
const licensingGrantKeyPathLabel = "m/44'/0'/<account>'/2'/0'"

// Which derivation produced the grant key. TWO schemes exist in the fleet — the
// HD path for mnemonic identities and an HKDF child for legacy on-disk identities
// — and a host that does not say which one it used is undiagnosable from a log
// (HEPHAESTUS, SEAL COUNCIL condition on Q1, 2026-08-07).
const (
	grantKeySchemeHD     = "SLIP-0010 HD child at " + licensingGrantKeyPathLabel
	grantKeySchemeLegacy = "HKDF-SHA512 child under " + legacyGrantSigningDomain + " (legacy on-disk identity, no HD seed)"
	grantKeySchemeEnv    = "SDN_DEV_PROVIDER_SIGNING_SEED_HEX override (development only)"
	grantKeySchemeNone   = "none"
)

// legacyGrantSigningDomain is the KDF label for the legacy (non-HD) identity path.
// It is versioned so the derivation can never be silently changed under a fleet
// that has already published the corresponding verifier key.
const legacyGrantSigningDomain = "SDN-LICENSING-GRANT-SIGNING-V1"

// legacyGrantSigningSeed derives the licensing grant signing seed for a LEGACY
// on-disk identity, which has no HD seed and therefore no BIP-32 path to derive
// at. HKDF-SHA512 with a versioned info label gives the same three properties the
// HD path gives — deterministic, one-way, and distinct from the parent — without
// inventing a second HD scheme.
//
// The HD identity path does NOT use this: it uses LicensingGrantKeyPath, which is
// the contract of record. This exists so a legacy node is domain-separated too
// rather than tripping the guard and losing grants entirely.
func legacyGrantSigningSeed(parent []byte) []byte {
	if len(parent) < 32 {
		return nil
	}
	reader := hkdf.New(sha512.New, parent[:32], nil, []byte(legacyGrantSigningDomain))
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(reader, seed); err != nil {
		return nil
	}
	return seed
}

// logGrantKeyDomainSeparation states, once at boot, which key signs grants, which
// key is the fleet update/publisher root, and how the grant key was derived.
//
// It asserts the separation POSITIVELY, not only on failure. The whole reason the
// two duties shared a key for as long as they did is that nothing ever stated
// which key was which — the collision was finally found by decoding a
// fleet-trust-roots key_id by hand. Everything logged here is public: the update
// root's public key is already printed by the update-signing endpoint
// registration, and the grant verifier's is published to every client in
// KRF.PUBLIC_KEY and in the node EPM.
//
// It reports the seed that is ACTUALLY provisioned (via moduleRuntimeKeySlots),
// not the one the HD identity would imply, so a dev override cannot make the log
// disagree with the running node.
func (n *Node) logGrantKeyDomainSeparation(updateRootPath string) {
	if n == nil {
		return
	}
	grantSeed, _, err := n.moduleRuntimeKeySlots()
	if err != nil {
		log.Warnf("Licensing grant signing key unavailable: %v. This node will issue NO module-delivery grants.", err)
		return
	}
	if len(grantSeed) != ed25519.SeedSize {
		// No error, just no key material — a node with neither an HD identity nor
		// a legacy on-disk one. Say that plainly rather than reporting a nil error.
		log.Warnf("No licensing grant signing key is provisioned (this node has no HD identity and no legacy signing identity). It will issue NO module-delivery grants.")
		return
	}
	scheme := n.grantSigningKeyScheme(grantSeed)

	if reason := grantSigningKeyDomainConflict(grantSeed, n.updateRootSigningSeed()); reason != "" {
		log.Errorf(
			"KEY DOMAIN COLLISION: %s. Grant key derivation: %s. Module-delivery grants will be REFUSED on this node. See graph/tasks/sdn-grant-verifier-key-domain-separation.md.",
			reason, scheme,
		)
		return
	}

	grantPub, err := ed25519PublicFromSeed(grantSeed)
	if err != nil {
		log.Warnf("Licensing grant signing key does not yield a usable verifier key: %v", err)
		return
	}
	grantPath := licensingGrantKeyPathLabel
	if n.identity != nil && strings.TrimSpace(n.identity.GrantSigningKeyPath) != "" {
		grantPath = n.identity.GrantSigningKeyPath
	}

	updateRoot := n.updateRootSigningSeed()
	if len(updateRoot) != ed25519.SeedSize {
		log.Infof(
			"Key domain separation OK: licensing grant verifier %x (%s, %s). This node holds no fleet update/publisher root, so it signs no update manifests.",
			grantPub, grantPath, scheme,
		)
		return
	}
	updateRootPub, err := ed25519PublicFromSeed(updateRoot)
	if err != nil {
		return
	}
	log.Infof(
		"Key domain separation OK: licensing grant verifier %x (%s, %s) is distinct from the fleet update/publisher root %x (%s). Grants and fleet code authority do not share a key.",
		grantPub, grantPath, scheme, updateRootPub, updateRootPath,
	)
}

// grantSigningKeyScheme names WHICH derivation produced the grant seed that was
// actually provisioned. It identifies the source by comparing the resolved seed
// against each possible origin rather than by re-walking moduleRuntimeKeySlots'
// branches, so it cannot drift out of agreement with the resolution it describes.
//
// Two schemes coexist in the fleet by design (HD for mnemonic identities, HKDF for
// legacy on-disk ones). A host that does not say which one it used is
// undiagnosable from a log — SEAL COUNCIL condition, HEPHAESTUS 2026-08-07.
func (n *Node) grantSigningKeyScheme(seed []byte) string {
	if len(seed) != ed25519.SeedSize {
		return grantKeySchemeNone
	}
	if n != nil && n.identity != nil {
		if raw, err := n.identity.RawGrantSigningKey(); err == nil && bytes.Equal(raw, seed) {
			return grantKeySchemeHD
		}
	}
	if bytes.Equal(readDevRuntimeKeySlotEnv("SDN_DEV_PROVIDER_SIGNING_SEED_HEX"), seed) {
		return grantKeySchemeEnv
	}
	return grantKeySchemeLegacy
}

// grantSigningKeyDomainConflict is the structural guard the owner ruling requires:
// the key that signs licensing grants and the key that is the fleet update /
// publisher-of-record root must not be the same key. It returns "" when they are
// properly separated, and an operator-readable reason when they are not.
//
// It compares the SEEDS and, independently, the derived PUBLIC keys. Seed equality
// is the direct question; public-key equality is the one that actually matters to a
// verifier and is what would still catch a future path where the two lanes reach the
// same key by different routes. Constant-time comparison throughout — these are
// secrets, and a guard that leaks timing about them is a worse trade than the guard
// is worth.
//
// An ABSENT update root (no HD identity loaded, so no update signing endpoint is
// registered — see api.registerUpdateSigningRoutes) is not a conflict: there is no
// second authority to collide with.
func grantSigningKeyDomainConflict(grantSeed, updateRootSeed []byte) string {
	if len(grantSeed) != ed25519.SeedSize {
		return ""
	}
	if len(updateRootSeed) < ed25519.SeedSize {
		return ""
	}
	updateRootSeed = updateRootSeed[:ed25519.SeedSize]

	if subtle.ConstantTimeCompare(grantSeed, updateRootSeed) == 1 {
		return "the licensing grant signing seed is BYTE-IDENTICAL to the fleet update/publisher root seed (the node identity Ed25519 key at m/44'/0'/<account>'/0'/0'); one key would hold both fleet code authority and every anonymous browser grant"
	}

	grantPub, err := ed25519PublicFromSeed(grantSeed)
	if err != nil {
		return fmt.Sprintf("the licensing grant signing seed does not yield a usable ed25519 verification key: %v", err)
	}
	updateRootPub, err := ed25519PublicFromSeed(updateRootSeed)
	if err != nil {
		// The update root is not this lane's to validate. If it is unusable the
		// update lane refuses itself; there is no grant-side conflict.
		return ""
	}
	if subtle.ConstantTimeCompare(grantPub, updateRootPub) == 1 {
		return fmt.Sprintf(
			"the licensing grant verifier key %x is the SAME ed25519 public key as the fleet update/publisher root; publishing it on the grant surface would publish the cluster's code-authority key",
			grantPub,
		)
	}
	return ""
}

// updateRootSigningSeed returns the 32-byte Ed25519 seed of the key that signs
// update manifests and module publication statements — i.e. the OTHER side of the
// domain-separation guard. nil when this node has no such key, which is also the
// condition under which the update signing endpoint is not registered.
//
// Deliberately reads through the same accessor the update lane uses
// (Node.SigningKey, via api.nodeSigningKeyProvider) so the guard can never be
// comparing against a key the update lane does not actually use.
func (n *Node) updateRootSigningSeed() []byte {
	if n == nil {
		return nil
	}
	raw := n.SigningKey()
	if len(raw) < ed25519.SeedSize {
		return nil
	}
	return raw[:ed25519.SeedSize]
}

func readDevRuntimeKeySlotEnv(name string) []byte {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	raw = strings.TrimPrefix(strings.ToLower(raw), "0x")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return nil
	}
	return decoded
}

func (n *Node) loadLegacyServerIdentity() (*keys.Identity, error) {
	basePath := n.serverKeyBasePath()
	if basePath == "" {
		return nil, nil
	}

	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		return nil, fmt.Errorf("create server key manager: %w", err)
	}
	if !keyMgr.HasIdentity() {
		return nil, nil
	}

	identity, err := keyMgr.LoadIdentity()
	if errors.Is(err, keys.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load server identity: %w", err)
	}
	return identity, nil
}

func (n *Node) serverKeyBasePath() string {
	if n == nil || n.config == nil {
		return ""
	}

	if basePath := strings.TrimSpace(n.config.Setup.DataPath); basePath != "" {
		return basePath
	}

	storagePath := strings.TrimSpace(n.config.Storage.Path)
	if storagePath == "" {
		return ""
	}
	return filepath.Dir(storagePath)
}

func (n *Node) resolveRuntimeIPFSAPIURL() string {
	if n == nil {
		return ""
	}
	if configured := strings.TrimSpace(n.config.Admin.IPFSAPIURL); configured != "" {
		return configured
	}
	const localKuboAPIURL = "http://127.0.0.1:5001"
	if isRuntimeIPFSAPIReachable(localKuboAPIURL) {
		return localKuboAPIURL
	}
	return ""
}

func isRuntimeIPFSAPIReachable(rawURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(rawURL, "/")+"/api/v0/version", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode > 0 && resp.StatusCode < 500
}

// bootstrapLicensingModule configures the licensing runtime from scratch and
// then provisions the WHOLE catalog. It is a BOOT path, and only a boot path.
//
// `server_configure_runtime` is a session reset, not a refresh: the key server
// answers it by dropping every pending challenge, every issued grant and every
// module publication, and by discarding its ephemeral keypair
// (key_server.cpp key_server_configure_runtime -> clear_pending_challenges /
// clear_pending_grants / clear_publications). That is correct for "configure".
// It is catastrophic as a reaction to "a module was published", which is what
// this function used to be wired to — see publishCatalogAssets and
// graph/tasks/sdn-delivery-first-attempt-bar-remaining-modes.md.
func bootstrapLicensingModule(mod *modulert.Module, reg *license.PluginRegistry) error {
	if mod == nil {
		return fmt.Errorf("licensing module is required")
	}
	if reg == nil {
		return nil
	}

	configFrame, err := buildLicensingRuntimeConfigFrame(mod.NodeContext())
	if err != nil {
		return err
	}
	configResponse, err := mod.InvokeMethodFrames(context.Background(), "server_configure_runtime", []modulert.InvokeInputFrame{
		{
			PortID:         "config",
			FileIdentifier: "$LCF",
			Payload:        configFrame,
		},
	})
	if err != nil {
		return fmt.Errorf("configure licensing runtime: %w", err)
	}
	if !flatbuffers.BufferHasIdentifier(configResponse, "$LCF") {
		return fmt.Errorf("configure licensing runtime returned %d bytes without $LCF identifier", len(configResponse))
	}

	return publishCatalogAssets(mod, reg, nil)
}

// publishCatalogAssets provisions content keys into the ALREADY-CONFIGURED
// licensing runtime for the named assets, or for the whole catalog when ids is
// empty. It never reconfigures the runtime, so publishing a module does not
// interrupt a delivery already in flight for a different one.
//
// This is the incremental publish path. It exists because re-running the boot
// bootstrap on every publish is what made three unrelated-looking errors appear
// in fresh gallery loads: a browser mid-handshake would meet
// "invalid licensing grant identifier" (its challenge/grant was cleared), or
// "requested module publication was not found" (its module had been cleared and
// not yet re-added), or a dial that never answered (42 serial guest invocations
// holding the module mutex). One publish, one module.
func publishCatalogAssets(mod *modulert.Module, reg *license.PluginRegistry, ids []string) error {
	if mod == nil {
		return fmt.Errorf("licensing module is required")
	}
	if reg == nil {
		return nil
	}

	var scope map[string]struct{}
	if len(ids) > 0 {
		scope = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				scope[trimmed] = struct{}{}
			}
		}
		if len(scope) == 0 {
			return nil
		}
	}

	// ADMIT POINT (graph/tasks/sdn-allowed-xpubs-not-enforced.md, P1).
	//
	// Everything below this line hands the licensing runtime a module's
	// CONTENT KEY. Once a key is inside the guest the key server decides who
	// gets a grant for it, and its rule for an empty ALLOWED_XPUBS is
	// "unrestricted" — which is how a throwaway identity was granted
	// com.orbpro.hpop. The host cannot and should not make grant decisions,
	// but it does decide which keys it provisions at all, and provisioning a
	// key for a module that declares a restriction it cannot express is the
	// thing that fails open. So the gate is here: refuse the key, and no
	// grant for that module is reachable by any path. The admit point is the
	// SAME ruling on the incremental path as on the boot path — scoping which
	// assets are considered never widens what may be published.
	plan := planCatalogPublication(reg, scope)

	var publishErrs []error
	for _, asset := range plan.Admitted {
		protectedContent, _, err := reg.ReadEncryptedBundle(asset.ID)
		if err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("read encrypted bundle for %q: %w", asset.ID, err))
			_ = reg.SetRuntimeStatus(asset.ID, "error", err.Error())
			continue
		}
		contentKey, err := reg.ReadBundleKey(asset.ID)
		if err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("read bundle key for %q: %w", asset.ID, err))
			_ = reg.SetRuntimeStatus(asset.ID, "error", err.Error())
			continue
		}

		descriptorFrame, err := buildPublicationDescriptorFrame(asset)
		if err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("build publication descriptor for %q: %w", asset.ID, err))
			_ = reg.SetRuntimeStatus(asset.ID, "error", err.Error())
			continue
		}
		keyFrame, err := buildPublicationContentKeyFrame(asset, protectedContent, contentKey)
		if err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("build publication key frame for %q: %w", asset.ID, err))
			_ = reg.SetRuntimeStatus(asset.ID, "error", err.Error())
			continue
		}

		publishResponse, err := mod.InvokeMethodFrames(context.Background(), "server_publish_module", []modulert.InvokeInputFrame{
			{
				PortID:         "module_descriptor",
				FileIdentifier: "$PLG",
				Payload:        descriptorFrame,
			},
			{
				PortID:  "protected_content",
				Payload: protectedContent,
			},
			{
				PortID:         "content_key",
				FileIdentifier: "$KMF",
				Payload:        keyFrame,
			},
		})
		if err != nil {
			publishErrs = append(publishErrs, fmt.Errorf("publish module %q: %w", asset.ID, err))
			_ = reg.SetRuntimeStatus(asset.ID, "error", err.Error())
			continue
		}
		if !flatbuffers.BufferHasIdentifier(publishResponse, "$PLG") {
			err := fmt.Errorf("publish module %q returned %d bytes without $PLG identifier", asset.ID, len(publishResponse))
			publishErrs = append(publishErrs, err)
			_ = reg.SetRuntimeStatus(asset.ID, "error", err.Error())
			continue
		}

		if err := reg.SetRuntimeStatus(asset.ID, "stopped", "published via licensing runtime"); err != nil {
			log.Warnf("Unable to update runtime status for plugin %q: %v", asset.ID, err)
		}
	}

	return errors.Join(publishErrs...)
}

// catalogPublicationPlan is the host's admit-point ruling over the whole
// catalog: which modules get a content key provisioned into the licensing
// runtime, and which are refused before any key leaves the host.
type catalogPublicationPlan struct {
	Admitted  []*license.PluginAsset
	Refused   []string
	Open      []string
	Decisions []license.PublicationDecision
}

// planCatalogPublication rules on every catalog asset and writes the boot
// ledger. It provisions nothing itself, so the ruling can be asserted in a test
// without a live WASM runtime — which is the whole reason the P1 went
// unnoticed: nothing tested the decision, only the plumbing around it.
// planCatalogPublication rules on the catalog. A nil scope means the whole
// catalog (a boot ruling, which also writes the standing open/refused ledger);
// a non-nil scope restricts the ruling to those asset IDs, which is what an
// incremental publish needs — the same decision per asset, without re-stating a
// 42-line ledger for a one-module change.
func planCatalogPublication(reg *license.PluginRegistry, scope map[string]struct{}) catalogPublicationPlan {
	plan := catalogPublicationPlan{}
	if reg == nil {
		return plan
	}
	policyCfg := reg.GrantPolicyConfig()

	for _, asset := range catalogPublicationAssets(reg) {
		if scope != nil {
			if _, wanted := scope[asset.ID]; !wanted {
				continue
			}
		}
		decision := license.EvaluatePublication(asset, policyCfg)
		plan.Decisions = append(plan.Decisions, decision)
		if !decision.Publish {
			plan.Refused = append(plan.Refused, asset.ID)
			log.Errorf("%s", license.FormatPublicationAudit(decision))
			_ = reg.SetRuntimeStatus(asset.ID, "refused", decision.Reason)
			continue
		}
		if license.IsOpenGrantPolicy(decision.Policy) {
			plan.Open = append(plan.Open, asset.ID)
		}
		log.Infof("%s", license.FormatPublicationAudit(decision))
		plan.Admitted = append(plan.Admitted, asset)
	}

	// The standing ledger. Every boot names the full open set, so an "open
	// for now" can never quietly become an open forever: the operator sees
	// the list, and closing it is one edit to grant-policy.json. A scoped
	// ruling is not a census, so it does not restate the census.
	if scope != nil {
		return plan
	}
	if len(plan.Open) > 0 {
		sort.Strings(plan.Open)
		log.Warnf(
			"grant-audit summary: %d module(s) grant to ANY identity that passes the licensing challenge — %s. "+
				"This is a declared policy, not a default. To close it, set enforce_allowlist_only or a per-module \"allowlist\" rule in <plugin-root>/%s.",
			len(plan.Open), strings.Join(plan.Open, ", "), license.GrantPolicyFileName,
		)
	}
	if len(plan.Refused) > 0 {
		sort.Strings(plan.Refused)
		log.Errorf(
			"grant-audit summary: %d module(s) REFUSED publication and can issue NO grants — %s. Entitle them (allowed_xpubs) or declare them open in <plugin-root>/%s.",
			len(plan.Refused), strings.Join(plan.Refused, ", "), license.GrantPolicyFileName,
		)
	}
	return plan
}

func buildLicensingRuntimeConfigFrame(nodeCtx *modulert.NodeContext) ([]byte, error) {
	if nodeCtx == nil {
		return nil, fmt.Errorf("licensing node context is required")
	}
	// A REFUSED slot is not a missing slot. The host has the key and declined to
	// provision it; say so, because "slot missing" would send an operator hunting
	// for a key that is present and fine. This is the fail-closed end of the
	// grant/update-root domain separation guard (owner ruling 2026-08-07): with the
	// slot refused, no grant can be signed at all — the licensing runtime never
	// gets configured.
	if reason := nodeCtx.KeySlotRefusals[providerSigningSlotID]; reason != "" {
		return nil, fmt.Errorf(
			"licensing runtime REFUSED: the host declined to provision key slot %q — %s. Module-delivery grants are disabled until the grant signing key is a distinct child of the node identity (%s)",
			providerSigningSlotID, reason, licensingGrantKeyPathLabel,
		)
	}

	providerPeerID := strings.TrimSpace(nodeCtx.PeerID)
	if providerPeerID == "" {
		return nil, fmt.Errorf("licensing node context peer id is required")
	}
	keySlots := nodeCtx.KeySlots
	if len(keySlots) == 0 {
		return nil, fmt.Errorf("licensing node context key slots are required")
	}
	if raw := keySlots[providerSigningSlotID]; len(raw) != 32 {
		return nil, fmt.Errorf("provider signing slot %q must contain a 32-byte Ed25519 seed", providerSigningSlotID)
	}
	if raw := keySlots[providerWrappingSlotID]; len(raw) != 32 {
		return nil, fmt.Errorf("provider wrapping slot %q must contain a 32-byte X25519 private key", providerWrappingSlotID)
	}

	builder := flatbuffers.NewBuilder(256)

	// The guest cannot derive this itself any more (keyslot.get was removed; only
	// keyslot.sign / keyslot.unwrap remain), so the host must publish the ed25519
	// verification key that corresponds to the seed held in the provider signing slot.
	// This is a PUBLIC key by construction — it is what every client verifies grants
	// against — and it never leaves the slot boundary as secret material.
	providerSigningPublic, err := ed25519PublicFromSeed(keySlots[providerSigningSlotID])
	if err != nil {
		return nil, fmt.Errorf("derive provider signing public key: %w", err)
	}

	providerPeerIDOffset := builder.CreateString(providerPeerID)
	signingKeyRefOffset := buildKeyReferenceFrame(
		builder,
		providerSigningKeyID,
		providerSigningSlotID,
		keyReferenceRoleProviderSigning,
		keyReferenceAlgorithmEd25519Seed,
		1,
		providerSigningPublic,
	)
	// The X25519 wrapping key deliberately carries no PUBLIC_KEY: the key server reads
	// PUBLIC_KEY only off PROVIDER_SIGNING_KEY (key_server.cpp:1112-1116) and never off
	// the wrapping reference. Adding it here would publish a field nothing consumes.
	wrappingKeyRefOffset := buildKeyReferenceFrame(
		builder,
		providerWrappingKeyID,
		providerWrappingSlotID,
		keyReferenceRoleProviderWrapping,
		keyReferenceAlgorithmX25519,
		1,
		nil,
	)

	lcf.LCFStart(builder)
	lcf.LCFAddMESSAGE_TYPE(builder, licensingConfigMessageTypeConfigure)
	lcf.LCFAddROLE(builder, licensingConfigRoleProvider)
	lcf.LCFAddPROVIDER_PEER_ID(builder, providerPeerIDOffset)
	lcf.LCFAddPROVIDER_SIGNING_KEY(builder, signingKeyRefOffset)
	lcf.LCFAddPROVIDER_WRAPPING_KEY(builder, wrappingKeyRefOffset)
	lcf.LCFAddACTIVE_KEY_VERSION(builder, 1)
	lcf.LCFAddEXPIRES_AT(builder, 0)
	lcf.LCFAddMAX_CLOCK_SKEW_MS(builder, licensingMaxClockSkewMS)
	lcf.LCFAddCHALLENGE_TTL_MS(builder, licensingChallengeTTLMS)
	root := lcf.LCFEnd(builder)
	lcf.FinishLCFBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

// ed25519PublicFromSeed derives the ed25519 verification key for a provider signing
// seed. It MUST stay byte-identical to the derivation used by the signer, which is
// ed25519.NewKeyFromSeed(slot) in internal/modulert/caps/keyslot.go:135-142 — if these
// two ever diverge, every grant verifies against the wrong key and the failure is
// indistinguishable from a corrupt signature.
func ed25519PublicFromSeed(seed []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("provider signing seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	pub, ok := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("provider signing seed did not yield an ed25519 public key")
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("derived ed25519 public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	// Fail closed rather than publish a zero verifier key: an all-zero PUBLIC_KEY is
	// exactly the defect this function exists to prevent (upstream-sdn-3), and it is
	// silently accepted by both the key server and the client length check.
	if isAllZero(pub) {
		return nil, fmt.Errorf("derived ed25519 public key is all zero bytes")
	}
	return pub, nil
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// buildKeyReferenceFrame emits one KRF for the licensing runtime config frame.
//
// publicKey may be nil. When it is non-empty it is written to KRF.PUBLIC_KEY, which is
// the ONLY channel by which the licensing WASM key server can learn the provider's
// ed25519 verification key: it holds only the SEED in a host key slot and, since
// space-data-network-modules 54bd4be removed the in-guest derivation, it copies
// PUBLIC_KEY out of this frame and otherwise "leaves it zeroed when absent"
// (licensing/core/src/cpp/src/key_server.cpp:1107-1118). That zero array is then stamped
// into every issued grant as GRANT_VERIFIER_PUBKEY (:1158-1160), so omitting this field
// makes ed25519 verification fail for EVERY browser client. Do not drop it again.
//
// FlatBuffers requires vectors to be created BEFORE the table is started, hence the
// CreateByteVector call above KRFStart.
func buildKeyReferenceFrame(
	builder *flatbuffers.Builder,
	keyID string,
	slotID string,
	role byte,
	algorithm byte,
	version uint32,
	publicKey []byte,
) flatbuffers.UOffsetT {
	keyIDOffset := builder.CreateString(keyID)
	slotIDOffset := builder.CreateString(slotID)
	publicKeyOffset := flatbuffers.UOffsetT(0)
	if len(publicKey) > 0 {
		publicKeyOffset = builder.CreateByteVector(publicKey)
	}

	lcf.KRFStart(builder)
	lcf.KRFAddKEY_ID(builder, keyIDOffset)
	lcf.KRFAddSLOT_ID(builder, slotIDOffset)
	if publicKeyOffset != 0 {
		lcf.KRFAddPUBLIC_KEY(builder, publicKeyOffset)
	}
	switch role {
	case keyReferenceRoleProviderSigning:
		lcf.KRFAddROLE(builder, 1)
	case keyReferenceRoleProviderWrapping:
		lcf.KRFAddROLE(builder, 2)
	default:
		lcf.KRFAddROLE(builder, 0)
	}
	switch algorithm {
	case keyReferenceAlgorithmEd25519Seed:
		lcf.KRFAddALGORITHM(builder, 1)
	case keyReferenceAlgorithmX25519:
		lcf.KRFAddALGORITHM(builder, 3)
	default:
		lcf.KRFAddALGORITHM(builder, 0)
	}
	lcf.KRFAddVERSION(builder, version)
	lcf.KRFAddEXPIRES_AT(builder, 0)
	lcf.KRFAddHOST_MANAGED(builder, true)
	return lcf.KRFEnd(builder)
}

func buildPublicationContentKeyFrame(asset *license.PluginAsset, protectedContent []byte, keyBytes []byte) ([]byte, error) {
	if asset == nil {
		return nil, fmt.Errorf("publication asset is required")
	}
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("publication content key is required")
	}
	useDecryptKey := hasEncryptedPublicationRecordCollection(protectedContent)

	builder := flatbuffers.NewBuilder(128 + len(keyBytes))
	keyIDOffset := builder.CreateString(publicationKeyID(asset))
	keyBytesOffset := builder.CreateByteVector(keyBytes)

	kmf.KMFStart(builder)
	kmf.KMFAddKEY_ID(builder, keyIDOffset)
	if useDecryptKey {
		kmf.KMFAddROLE(builder, keyMaterialRoleDecryptKey)
		kmf.KMFAddALGORITHM(builder, keyMaterialAlgorithmX25519Private)
	} else {
		kmf.KMFAddROLE(builder, keyMaterialRolePublicationContent)
		kmf.KMFAddALGORITHM(builder, keyMaterialAlgorithmAes256Gcm)
	}
	kmf.KMFAddENCODING(builder, keyMaterialEncodingRawBytes)
	kmf.KMFAddKEY_BYTES(builder, keyBytesOffset)
	kmf.KMFAddVERSION(builder, 0)
	kmf.KMFAddEXPIRES_AT(builder, 0)
	root := kmf.KMFEnd(builder)
	kmf.FinishKMFBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func hasEncryptedPublicationRecordCollection(protectedContent []byte) bool {
	recordCollectionBytes, ok := extractPublicationRecordCollectionBytes(protectedContent)
	if !ok {
		return false
	}
	if !flatbuffers.BufferHasIdentifier(recordCollectionBytes, publicationTrailerMagicText) {
		return false
	}

	recordCollectionRoot := flatbuffers.GetUOffsetT(recordCollectionBytes)
	recordCollectionTable := flatbuffers.Table{
		Bytes: recordCollectionBytes,
		Pos:   recordCollectionRoot,
	}
	recordsOffset := flatbuffers.UOffsetT(recordCollectionTable.Offset(6))
	if recordsOffset == 0 {
		return false
	}

	recordsVector := recordCollectionTable.Vector(recordsOffset)
	for recordIndex := 0; recordIndex < recordCollectionTable.VectorLen(recordsOffset); recordIndex++ {
		recordOffset := recordsVector + flatbuffers.UOffsetT(recordIndex*4)
		recordTable := flatbuffers.Table{
			Bytes: recordCollectionBytes,
			Pos:   recordCollectionTable.Indirect(recordOffset),
		}
		standardOffset := flatbuffers.UOffsetT(recordTable.Offset(8))
		if standardOffset == 0 {
			continue
		}
		if bytes.Equal(recordTable.ByteVector(standardOffset+recordTable.Pos), []byte("ENC")) {
			return true
		}
	}
	return false
}

func extractPublicationRecordCollectionBytes(protectedContent []byte) ([]byte, bool) {
	if len(protectedContent) < publicationTrailerFooterLength {
		return nil, false
	}

	footerOffset := len(protectedContent) - publicationTrailerFooterLength
	if string(protectedContent[footerOffset+4:]) != publicationTrailerMagicText {
		return nil, false
	}

	recordCollectionLength := int(binary.LittleEndian.Uint32(protectedContent[footerOffset : footerOffset+4]))
	recordCollectionOffset := footerOffset - recordCollectionLength
	if recordCollectionLength <= 0 || recordCollectionOffset < 0 {
		return nil, false
	}

	return protectedContent[recordCollectionOffset:footerOffset], true
}

func buildPublicationDescriptorFrame(asset *license.PluginAsset) ([]byte, error) {
	if asset == nil {
		return nil, fmt.Errorf("publication asset is required")
	}
	pluginID := strings.TrimSpace(asset.ID)
	version := strings.TrimSpace(asset.Version)
	if pluginID == "" || version == "" {
		return nil, fmt.Errorf("publication asset id and version are required")
	}

	builder := flatbuffers.NewBuilder(512)
	pluginIDOffset := builder.CreateString(pluginID)
	nameOffset := builder.CreateString(pluginID)
	versionOffset := builder.CreateString(version)
	descriptionOffset := builder.CreateString("Protected module publication")

	requiredScopeOffset := flatbuffers.UOffsetT(0)
	if requiredScope := strings.TrimSpace(asset.RequiredScopeOrDefault()); requiredScope != "" {
		requiredScopeOffset = builder.CreateString(requiredScope)
	}

	keyIDOffset := builder.CreateString(publicationKeyID(asset))

	allowedXpubsOffset := flatbuffers.UOffsetT(0)
	if len(asset.AllowedXpubs) > 0 {
		xpubOffsets := make([]flatbuffers.UOffsetT, 0, len(asset.AllowedXpubs))
		for _, xpub := range asset.AllowedXpubs {
			normalized := strings.TrimSpace(xpub)
			if normalized == "" {
				continue
			}
			xpubOffsets = append(xpubOffsets, builder.CreateString(normalized))
		}
		allowedXpubsOffset = createOffsetVector(builder, xpubOffsets)
	}

	dependenciesOffset := buildDependenciesVector(builder, asset.Dependencies)

	nowMs := uint64(time.Now().UnixMilli())

	plg.PLGStart(builder)
	plg.PLGAddPLUGIN_ID(builder, pluginIDOffset)
	plg.PLGAddNAME(builder, nameOffset)
	plg.PLGAddVERSION(builder, versionOffset)
	plg.PLGAddDESCRIPTION(builder, descriptionOffset)
	plg.PLGAddPLUGIN_TYPE(builder, pluginTypeAnalysis)
	plg.PLGAddABI_VERSION(builder, 1)
	plg.PLGAddENCRYPTED(builder, true)
	if requiredScopeOffset != 0 {
		plg.PLGAddREQUIRED_SCOPE(builder, requiredScopeOffset)
	}
	plg.PLGAddKEY_ID(builder, keyIDOffset)
	if allowedXpubsOffset != 0 {
		plg.PLGAddALLOWED_XPUBS(builder, allowedXpubsOffset)
	}
	if dependenciesOffset != 0 {
		plg.PLGAddDEPENDENCIES(builder, dependenciesOffset)
	}
	plg.PLGAddMAX_GRANT_TIMEOUT_MS(builder, asset.GrantTimeoutLimitMs())
	plg.PLGAddCREATED_AT(builder, nowMs)
	plg.PLGAddUPDATED_AT(builder, nowMs)
	root := plg.PLGEnd(builder)
	plg.FinishPLGBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

// buildDependenciesVector encodes an asset's declared plugin dependencies as a
// PLG.PluginDependency vector. Every child string and table is created before
// the vector — and before the enclosing PLG table is opened by the caller — per
// flatbuffers' rule that nested objects must be finished before their parent.
func buildDependenciesVector(builder *flatbuffers.Builder, deps []license.PluginDependencyRef) flatbuffers.UOffsetT {
	if len(deps) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, 0, len(deps))
	for _, dep := range deps {
		pluginID := strings.TrimSpace(dep.PluginID)
		if pluginID == "" {
			continue
		}
		idOffset := builder.CreateString(pluginID)
		minOffset := flatbuffers.UOffsetT(0)
		if v := strings.TrimSpace(dep.MinVersion); v != "" {
			minOffset = builder.CreateString(v)
		}
		maxOffset := flatbuffers.UOffsetT(0)
		if v := strings.TrimSpace(dep.MaxVersion); v != "" {
			maxOffset = builder.CreateString(v)
		}
		plg.PluginDependencyStart(builder)
		plg.PluginDependencyAddPLUGIN_ID(builder, idOffset)
		if minOffset != 0 {
			plg.PluginDependencyAddMIN_VERSION(builder, minOffset)
		}
		if maxOffset != 0 {
			plg.PluginDependencyAddMAX_VERSION(builder, maxOffset)
		}
		offsets = append(offsets, plg.PluginDependencyEnd(builder))
	}
	return createOffsetVector(builder, offsets)
}

func createOffsetVector(builder *flatbuffers.Builder, offsets []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(offsets) == 0 {
		return 0
	}
	builder.StartVector(4, len(offsets), 4)
	for index := len(offsets) - 1; index >= 0; index -= 1 {
		builder.PrependUOffsetT(offsets[index])
	}
	return builder.EndVector(len(offsets))
}

func publicationKeyID(asset *license.PluginAsset) string {
	return strings.TrimSpace(asset.ID) + ":" + strings.TrimSpace(asset.Version)
}
