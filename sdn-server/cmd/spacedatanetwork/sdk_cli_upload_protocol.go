package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/libp2p/go-libp2p"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

const sdkModuleUploadProtocolID = "/space-data-network/plugin-module-upload/1.0.0"

type sdkModuleUploadProviderDescriptor struct {
	PeerID       string `json:"peerId"`
	ModuleUpload struct {
		ProtocolID           string   `json:"protocolId"`
		ProviderX25519PubKey string   `json:"providerX25519PubKey"`
		RelayAddresses       []string `json:"relayAddresses"`
	} `json:"moduleUpload"`
}

func sdkUploadModuleOverProtocol(
	ctx context.Context,
	nodeOrigin string,
	packaged *sdkPackagedModule,
	contentKey []byte,
	wallet *sdkLoadedWallet,
) (map[string]any, error) {
	if packaged == nil {
		return nil, fmt.Errorf("module package is required")
	}
	if wallet == nil || wallet.Identity == nil {
		return nil, fmt.Errorf("wallet identity is required")
	}
	provider, err := sdkFetchModuleUploadProviderDescriptor(ctx, nodeOrigin)
	if err != nil {
		return nil, err
	}
	if provider.ModuleUpload.ProtocolID != sdkModuleUploadProtocolID {
		return nil, fmt.Errorf("provider descriptor does not advertise plugin module upload protocol")
	}
	providerPublicKey, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(provider.ModuleUpload.ProviderX25519PubKey))
	if err != nil || len(providerPublicKey) != 32 {
		return nil, fmt.Errorf("provider module upload key must be base64url 32 bytes")
	}
	envelope, err := license.WrapProviderContentKey(contentKey, providerPublicKey, license.ProviderContentKeyAAD{
		ModuleID:           packaged.PackageFile.Metadata.ID,
		Version:            packaged.PackageFile.Metadata.Version,
		BundleSHA256:       packaged.PackageFile.BundleSHA256,
		SignerPublicKeyHex: packaged.PackageFile.SignerPublicKeyHex,
		ProviderPeerID:     provider.PeerID,
	})
	if err != nil {
		return nil, fmt.Errorf("wrap provider content key: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"version":               1,
		"metadata":              packaged.PackageFile.Metadata,
		"uploader_xpub":         wallet.XPub,
		"signer_public_key_hex": packaged.PackageFile.SignerPublicKeyHex,
		"signature_hex":         packaged.PackageFile.SignatureHex,
		"content_key_envelope":  envelope,
		"encrypted_bundle_b64":  base64.RawURLEncoding.EncodeToString(packaged.EncryptedBundleBytes),
	})
	if err != nil {
		return nil, err
	}

	responseBytes, err := sdkDialModuleUploadProtocol(
		ctx,
		wallet,
		provider.PeerID,
		provider.ModuleUpload.ProtocolID,
		payload,
		provider.ModuleUpload.RelayAddresses,
	)
	if err != nil {
		return nil, err
	}
	var response map[string]any
	if err := json.Unmarshal(responseBytes, &response); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}
	if ok, _ := response["ok"].(bool); !ok {
		if message, _ := response["error"].(string); strings.TrimSpace(message) != "" {
			return nil, fmt.Errorf("module upload failed: %s", message)
		}
		return nil, fmt.Errorf("module upload failed")
	}
	return response, nil
}

func sdkFetchModuleUploadProviderDescriptor(ctx context.Context, nodeOrigin string) (sdkModuleUploadProviderDescriptor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(nodeOrigin, "/")+"/api/module-delivery/provider", nil)
	if err != nil {
		return sdkModuleUploadProviderDescriptor{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return sdkModuleUploadProviderDescriptor{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return sdkModuleUploadProviderDescriptor{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return sdkModuleUploadProviderDescriptor{}, fmt.Errorf("provider descriptor fetch failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var descriptor sdkModuleUploadProviderDescriptor
	if err := json.Unmarshal(body, &descriptor); err != nil {
		return sdkModuleUploadProviderDescriptor{}, err
	}
	if strings.TrimSpace(descriptor.PeerID) == "" {
		return sdkModuleUploadProviderDescriptor{}, fmt.Errorf("provider descriptor missing peerId")
	}
	return descriptor, nil
}

func sdkDialModuleUploadProtocol(
	ctx context.Context,
	wallet *sdkLoadedWallet,
	providerPeerID string,
	protocolID string,
	payload []byte,
	relayAddresses []string,
) ([]byte, error) {
	if wallet == nil || wallet.Identity == nil || wallet.Identity.IdentityPrivKey == nil {
		return nil, fmt.Errorf("wallet libp2p identity key is required")
	}
	targetPeerID, err := peer.Decode(providerPeerID)
	if err != nil {
		return nil, fmt.Errorf("decode provider peer id: %w", err)
	}
	host, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.Identity(wallet.Identity.IdentityPrivKey),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		return nil, fmt.Errorf("create upload libp2p host: %w", err)
	}
	defer host.Close()

	candidates := relayAddresses
	if len(candidates) == 0 {
		candidates = []string{"/p2p/" + providerPeerID}
	}
	var lastErr error
	for _, candidate := range candidates {
		response, err := sdkDialModuleUploadCandidate(ctx, host, targetPeerID, protocolID, payload, candidate)
		if err == nil {
			return response, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("failed to dial module upload provider: %w", lastErr)
}

func sdkDialModuleUploadCandidate(
	ctx context.Context,
	host libp2phost.Host,
	targetPeerID peer.ID,
	protocolID string,
	payload []byte,
	candidate string,
) ([]byte, error) {
	dialTarget := sdkNormalizeUploadDialTarget(candidate, targetPeerID.String())
	ma, err := multiaddr.NewMultiaddr(dialTarget)
	if err != nil {
		return nil, fmt.Errorf("parse relay address %q: %w", dialTarget, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		info = &peer.AddrInfo{ID: targetPeerID, Addrs: []multiaddr.Multiaddr{ma}}
	}
	if info.ID != targetPeerID {
		info.ID = targetPeerID
	}
	if err := host.Connect(ctx, *info); err != nil {
		return nil, fmt.Errorf("connect %s: %w", dialTarget, err)
	}
	stream, err := host.NewStream(ctx, targetPeerID, libp2pprotocol.ID(protocolID))
	if err != nil {
		return nil, fmt.Errorf("open upload stream: %w", err)
	}
	defer stream.Close()
	if _, err := stream.Write(payload); err != nil {
		return nil, fmt.Errorf("write upload payload: %w", err)
	}
	if closeWriter, ok := stream.(interface{ CloseWrite() error }); ok {
		_ = closeWriter.CloseWrite()
	}
	response, err := io.ReadAll(io.LimitReader(stream, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read upload response: %w", err)
	}
	if len(response) == 0 {
		return nil, fmt.Errorf("empty upload response")
	}
	return response, nil
}

func sdkNormalizeUploadDialTarget(addr string, targetPeerID string) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return trimmed
	}
	if strings.Contains(trimmed, "/p2p-circuit") {
		if strings.Contains(trimmed, "/p2p/"+targetPeerID) {
			return trimmed
		}
		return trimmed + "/p2p/" + targetPeerID
	}
	if strings.Contains(trimmed, "/p2p/"+targetPeerID) {
		return trimmed
	}
	if strings.Contains(trimmed, "/p2p/") {
		return trimmed + "/p2p-circuit/p2p/" + targetPeerID
	}
	return trimmed + "/p2p/" + targetPeerID
}
