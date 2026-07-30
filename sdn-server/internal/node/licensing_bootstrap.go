package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	lcf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCF"
	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"golang.org/x/crypto/ed25519"
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

func (n *Node) moduleRuntimeKeySlots() ([]byte, []byte, error) {
	envSigningSeed := readDevRuntimeKeySlotEnv("SDN_DEV_PROVIDER_SIGNING_SEED_HEX")
	envWrappingKey := readDevRuntimeKeySlotEnv("SDN_DEV_NODE_ENCRYPTION_KEY_HEX")

	if n != nil && n.identity != nil {
		rawSigningKey, err := n.identity.RawSigningKey()
		if err != nil {
			return nil, nil, fmt.Errorf("derive provider signing seed: %w", err)
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

	var signingSeed []byte
	if identity.SigningKey != nil && len(identity.SigningKey.PrivateKey) >= 32 {
		signingSeed = append([]byte(nil), identity.SigningKey.PrivateKey[:32]...)
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

func bootstrapLicensingModule(mod *modulert.Module, reg *license.PluginRegistry) error {
	if mod == nil {
		return fmt.Errorf("licensing module is required")
	}
	if reg == nil {
		return nil
	}

	configFrame, expectedSigningPublicKey, err := buildLicensingRuntimeConfigFrame(mod.NodeContext())
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
	// The guest echoes the verifier key it actually retained in its $LCF status.
	// Check it: that key — not the one we sent — is what gets stamped into every
	// grant as GRANT_VERIFIER_PUBKEY and what clients verify against. A guest
	// that silently kept zeroes (or a different key) must fail the bootstrap
	// here, loudly, instead of coming up healthy and issuing grants no client
	// can verify (task sdn-module-delivery-grant-sig-broken).
	if err := verifyLicensingStatusVerifierKey(configResponse, expectedSigningPublicKey); err != nil {
		return fmt.Errorf("configure licensing runtime: %w", err)
	}

	var publishErrs []error
	for _, asset := range catalogPublicationAssets(reg) {
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

// buildLicensingRuntimeConfigFrame returns the $LCF configure frame and the
// provider signing PUBLIC key it carries, so the caller can check the guest
// echoed that exact key back in its status.
func buildLicensingRuntimeConfigFrame(nodeCtx *modulert.NodeContext) ([]byte, ed25519.PublicKey, error) {
	if nodeCtx == nil {
		return nil, nil, fmt.Errorf("licensing node context is required")
	}
	providerPeerID := strings.TrimSpace(nodeCtx.PeerID)
	if providerPeerID == "" {
		return nil, nil, fmt.Errorf("licensing node context peer id is required")
	}
	keySlots := nodeCtx.KeySlots
	if len(keySlots) == 0 {
		return nil, nil, fmt.Errorf("licensing node context key slots are required")
	}
	if raw := keySlots[providerSigningSlotID]; len(raw) != 32 {
		return nil, nil, fmt.Errorf("provider signing slot %q must contain a 32-byte Ed25519 seed", providerSigningSlotID)
	}
	if raw := keySlots[providerWrappingSlotID]; len(raw) != 32 {
		return nil, nil, fmt.Errorf("provider wrapping slot %q must contain a 32-byte X25519 private key", providerWrappingSlotID)
	}

	// The guest CANNOT derive this itself. Grant signing goes through the host
	// keyslot.sign oracle (internal/modulert/caps/keyslot.go:138), so the
	// provider's Ed25519 seed never enters guest memory — and since
	// space-data-network-modules 54bd4be (2026-07-09) removed the raw-key
	// keyslot.get op, the ONLY way the licensing module can learn the public
	// half is this field. It stamps whatever it is given here into every
	// issued grant as LGR.GRANT_VERIFIER_PUBKEY, which is exactly the key
	// browser clients ed25519-verify the grant's PROVIDER_SIGNATURE against
	// (space-data-module-sdk src/licensing/records.js verifyLicensing-
	// GrantProviderSignature). Omitting it left the guest holding 32 zero
	// bytes (key_server.cpp: "leave it zeroed when absent"), so every grant
	// issued since 2026-07-09 was unverifiable and the live sandcastle failed
	// with "licensing grant provider signature verification failed"
	// (task sdn-module-delivery-grant-sig-broken). The Go delivery client does
	// not verify provider signatures, which is why only browsers saw it.
	//
	// Fail CLOSED: a licensing runtime that cannot state its own verifier key
	// would come up healthy, list every module and issue grants no client can
	// verify. Refusing to configure is the honest outcome.
	signingPublicKey, err := caps.KeyslotEd25519PublicKey(nodeCtx, providerSigningSlotID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve provider signing public key: %w", err)
	}
	if len(signingPublicKey) != ed25519.PublicKeySize {
		return nil, nil, fmt.Errorf(
			"provider signing public key must be %d bytes, got %d",
			ed25519.PublicKeySize, len(signingPublicKey),
		)
	}

	builder := flatbuffers.NewBuilder(256)

	providerPeerIDOffset := builder.CreateString(providerPeerID)
	signingKeyRefOffset := buildKeyReferenceFrame(
		builder,
		providerSigningKeyID,
		providerSigningSlotID,
		keyReferenceRoleProviderSigning,
		keyReferenceAlgorithmEd25519Seed,
		1,
		signingPublicKey,
	)
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
	lcf.LCFAddMAX_CLOCK_SKEW_MS(builder, 5_000)
	lcf.LCFAddCHALLENGE_TTL_MS(builder, 30_000)
	root := lcf.LCFEnd(builder)
	lcf.FinishLCFBuffer(builder, root)
	return builder.FinishedBytes(), signingPublicKey, nil
}

// verifyLicensingStatusVerifierKey checks that the licensing guest retained the
// provider signing PUBLIC key the host sent it, by reading the key back out of
// the $LCF status frame the guest returns from server_configure_runtime.
//
// This is the only host-observable proof that grants will be verifiable. The
// guest stamps whatever it retained into every grant as GRANT_VERIFIER_PUBKEY;
// if it kept zeroes (its documented behaviour when the host omits the field)
// the node still boots, still lists every module and still issues grants —
// which then fail in every browser with "licensing grant provider signature
// verification failed". Checking the echo turns that into a boot failure.
func verifyLicensingStatusVerifierKey(statusFrame []byte, expected ed25519.PublicKey) error {
	if len(expected) != ed25519.PublicKeySize {
		return fmt.Errorf(
			"provider signing public key must be %d bytes, got %d",
			ed25519.PublicKeySize, len(expected),
		)
	}
	status := lcf.GetRootAsLCF(statusFrame, 0)
	if status == nil {
		return fmt.Errorf("licensing status frame is not a readable $LCF record")
	}
	signingKey := status.PROVIDER_SIGNING_KEY(nil)
	if signingKey == nil {
		return fmt.Errorf("licensing status omits PROVIDER_SIGNING_KEY")
	}
	reported := signingKey.PUBLIC_KEYBytes()
	if len(reported) == 0 {
		return fmt.Errorf(
			"licensing runtime reported no provider signing public key — every grant it issues would be unverifiable",
		)
	}
	if !bytes.Equal(reported, expected) {
		return fmt.Errorf(
			"licensing runtime retained provider signing public key %s, host sent %s — grants would be signed by one key and verified against another",
			hex.EncodeToString(reported), hex.EncodeToString(expected),
		)
	}
	return nil
}

// buildKeyReferenceFrame writes one KRF. publicKey is the PUBLIC half of a
// host-managed slot and may be nil: it is emitted for the signing key, because
// the guest cannot derive it and must publish it in every grant, and omitted
// for the wrapping key, whose public half the guest already learns from the
// requester's ECDH envelope.
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
	var publicKeyOffset flatbuffers.UOffsetT
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
