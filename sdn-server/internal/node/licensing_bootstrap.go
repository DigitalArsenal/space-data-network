package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	keyMaterialAlgorithmAes256Gcm     = 5
	keyMaterialEncodingRawBytes       = 1

	pluginTypeAnalysis = 3
)

func catalogPublicationAssets(reg *license.PluginRegistry) []*license.PluginAsset {
	if reg == nil {
		return nil
	}

	descriptors := reg.ListPublic()
	assets := make([]*license.PluginAsset, 0, len(descriptors))
	for _, descriptor := range descriptors {
		pluginID := strings.TrimSpace(descriptor.ID)
		if pluginID == "" || pluginID == licensingModuleID {
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

func (n *Node) buildModuleNodeContext() (*modulert.NodeContext, error) {
	nodeCtx := &modulert.NodeContext{}
	if n != nil && n.host != nil {
		nodeCtx.PeerID = n.host.ID().String()
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
	}
	if len(signingSeed) == 32 {
		nodeCtx.KeySlots[providerSigningSlotID] = append([]byte(nil), signingSeed...)
	}
	if len(wrappingKey) == 32 {
		nodeCtx.KeySlots[providerWrappingSlotID] = append([]byte(nil), wrappingKey...)
	}

	return nodeCtx, nil
}

func (n *Node) moduleRuntimeKeySlots() ([]byte, []byte, error) {
	if n != nil && n.identity != nil {
		rawSigningKey, err := n.identity.RawSigningKey()
		if err != nil {
			return nil, nil, fmt.Errorf("derive provider signing seed: %w", err)
		}

		var wrappingKey []byte
		if len(n.identity.EncryptionKey) > 0 {
			wrappingKey = append([]byte(nil), n.identity.EncryptionKey...)
		}
		return append([]byte(nil), rawSigningKey...), wrappingKey, nil
	}

	identity, err := n.loadLegacyServerIdentity()
	if err != nil {
		return nil, nil, err
	}
	if identity == nil {
		return nil, nil, nil
	}

	var signingSeed []byte
	if identity.SigningKey != nil && len(identity.SigningKey.PrivateKey) >= 32 {
		signingSeed = append([]byte(nil), identity.SigningKey.PrivateKey[:32]...)
	}

	var wrappingKey []byte
	if identity.EncryptionKey != nil && len(identity.EncryptionKey.PrivateKey) > 0 {
		wrappingKey = append([]byte(nil), identity.EncryptionKey.PrivateKey...)
	}

	return signingSeed, wrappingKey, nil
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
		keyFrame, err := buildPublicationContentKeyFrame(asset, contentKey)
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

func buildLicensingRuntimeConfigFrame(nodeCtx *modulert.NodeContext) ([]byte, error) {
	if nodeCtx == nil {
		return nil, fmt.Errorf("licensing node context is required")
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

	providerPeerIDOffset := builder.CreateString(providerPeerID)
	signingKeyRefOffset := buildKeyReferenceFrame(
		builder,
		providerSigningKeyID,
		providerSigningSlotID,
		keyReferenceRoleProviderSigning,
		keyReferenceAlgorithmEd25519Seed,
		1,
	)
	wrappingKeyRefOffset := buildKeyReferenceFrame(
		builder,
		providerWrappingKeyID,
		providerWrappingSlotID,
		keyReferenceRoleProviderWrapping,
		keyReferenceAlgorithmX25519,
		1,
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
	return builder.FinishedBytes(), nil
}

func buildKeyReferenceFrame(
	builder *flatbuffers.Builder,
	keyID string,
	slotID string,
	role byte,
	algorithm byte,
	version uint32,
) flatbuffers.UOffsetT {
	keyIDOffset := builder.CreateString(keyID)
	slotIDOffset := builder.CreateString(slotID)

	lcf.KRFStart(builder)
	lcf.KRFAddKEY_ID(builder, keyIDOffset)
	lcf.KRFAddSLOT_ID(builder, slotIDOffset)
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

func buildPublicationContentKeyFrame(asset *license.PluginAsset, keyBytes []byte) ([]byte, error) {
	if asset == nil {
		return nil, fmt.Errorf("publication asset is required")
	}
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("publication content key is required")
	}

	builder := flatbuffers.NewBuilder(128 + len(keyBytes))
	keyIDOffset := builder.CreateString(publicationKeyID(asset))
	keyBytesOffset := builder.CreateByteVector(keyBytes)

	kmf.KMFStart(builder)
	kmf.KMFAddKEY_ID(builder, keyIDOffset)
	kmf.KMFAddROLE(builder, keyMaterialRolePublicationContent)
	kmf.KMFAddALGORITHM(builder, keyMaterialAlgorithmAes256Gcm)
	kmf.KMFAddENCODING(builder, keyMaterialEncodingRawBytes)
	kmf.KMFAddKEY_BYTES(builder, keyBytesOffset)
	kmf.KMFAddVERSION(builder, 0)
	kmf.KMFAddEXPIRES_AT(builder, 0)
	root := kmf.KMFEnd(builder)
	kmf.FinishKMFBuffer(builder, root)
	return builder.FinishedBytes(), nil
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

	allowedDomainsOffset := flatbuffers.UOffsetT(0)
	if len(asset.AllowedDomains) > 0 {
		domainOffsets := make([]flatbuffers.UOffsetT, 0, len(asset.AllowedDomains))
		for _, domain := range asset.AllowedDomains {
			normalized := strings.TrimSpace(domain)
			if normalized == "" {
				continue
			}
			domainOffsets = append(domainOffsets, builder.CreateString(normalized))
		}
		allowedDomainsOffset = createOffsetVector(builder, domainOffsets)
	}

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
	if allowedDomainsOffset != 0 {
		plg.PLGAddALLOWED_DOMAINS(builder, allowedDomainsOffset)
	}
	plg.PLGAddMAX_GRANT_TIMEOUT_MS(builder, asset.GrantTimeoutLimitMs())
	plg.PLGAddCREATED_AT(builder, nowMs)
	plg.PLGAddUPDATED_AT(builder, nowMs)
	root := plg.PLGEnd(builder)
	plg.FinishPLGBuffer(builder, root)
	return builder.FinishedBytes(), nil
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
