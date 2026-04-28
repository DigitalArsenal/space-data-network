package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	lgr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LGR"
	lpf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const sdkModuleDeliveryProtocolID = "/space-data-network/module-delivery/1.0.0"

type sdkModuleQueryOptions struct {
	NodeURL            string
	ModuleID           string
	ModuleVersion      string
	RequesterDomain    string
	RequestedTimeoutMs int64
	Wallet             *sdkLoadedWallet
}

type sdkProviderDescriptor struct {
	PublicKey      string   `json:"publicKey"`
	PeerID         string   `json:"peerId"`
	RelayAddresses []string `json:"relayAddresses"`
}

type sdkChallengeResponse struct {
	ReqID          string
	ModuleID       string
	ModuleVersion  string
	Nonce          []byte
	ExpiresAtMs    uint64
	ProviderPeerID string
	RawBytes       []byte
}

type sdkGrantResponse struct {
	ReqID                         string
	ModuleID                      string
	ModuleVersion                 string
	GrantedDomain                 string
	GrantedTimeoutMs              uint64
	CID                           string
	ContentHash                   []byte
	EncryptedSizeBytes            uint64
	WrappedContentKeyPayload      []byte
	WrappedContentKeyRootType     string
	WrappedContentKeyContext      string
	WrappedContentKeyEphemeralPub []byte
}

func sdkQueryModuleDelivery(ctx context.Context, options sdkModuleQueryOptions) (map[string]any, error) {
	nodeOrigin := sdkNormalizeNodeOrigin(options.NodeURL)
	provider, err := sdkFetchProviderDescriptor(ctx, nodeOrigin)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(provider.PeerID) == "" {
		return nil, fmt.Errorf("provider descriptor missing peerId")
	}
	reqID := sdkCreateQueryRequestID()
	requestedAt := uint64(time.Now().UnixMilli())
	challengeRequest, err := sdkBuildChallengeRequest(provider, options, reqID, requestedAt)
	if err != nil {
		return nil, err
	}
	challengeBytes, err := sdkDialModuleDelivery(ctx, provider, challengeRequest)
	if err != nil {
		return nil, fmt.Errorf("request module challenge: %w", err)
	}
	challenge, err := sdkDecodeChallengeResponse(challengeBytes)
	if err != nil {
		return nil, err
	}
	if challenge.ReqID != reqID || challenge.ModuleID != options.ModuleID {
		return nil, fmt.Errorf("challenge response did not match request")
	}
	if options.ModuleVersion != "" && challenge.ModuleVersion != "" && challenge.ModuleVersion != options.ModuleVersion {
		return nil, fmt.Errorf("challenge response module version mismatch")
	}
	signature, err := options.Wallet.Identity.Sign(challenge.RawBytes)
	if err != nil {
		return nil, fmt.Errorf("sign challenge response: %w", err)
	}
	proofRequest, err := sdkBuildProofRequest(provider, options, challenge, signature)
	if err != nil {
		return nil, err
	}
	grantBytes, err := sdkDialModuleDelivery(ctx, provider, proofRequest)
	if err != nil {
		return nil, fmt.Errorf("request module grant: %w", err)
	}
	grant, err := sdkDecodeGrantResponse(grantBytes)
	if err != nil {
		return nil, err
	}
	if grant.ReqID != reqID || grant.ModuleID != options.ModuleID {
		return nil, fmt.Errorf("grant response did not match request")
	}
	contentKey, err := sdkUnwrapGrantContentKey(grant, options.Wallet)
	if err != nil {
		return nil, err
	}
	defer sdkZeroBytes(contentKey)
	encryptedBundleBytes, err := sdkFetchCIDBytes(ctx, nodeOrigin, grant.CID)
	if err != nil {
		return nil, err
	}
	if len(grant.ContentHash) > 0 {
		digest := sha256.Sum256(encryptedBundleBytes)
		if !bytes.Equal(digest[:], grant.ContentHash) {
			return nil, fmt.Errorf("encrypted bundle hash mismatch")
		}
	}
	if grant.EncryptedSizeBytes > 0 && uint64(len(encryptedBundleBytes)) != grant.EncryptedSizeBytes {
		return nil, fmt.Errorf("encrypted bundle size mismatch")
	}
	decryptedBundleBytes, err := sdkDecryptAESGCM(contentKey, encryptedBundleBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"protocol_id":          sdkModuleDeliveryProtocolID,
		"provider_peer_id":     provider.PeerID,
		"module_id":            grant.ModuleID,
		"module_version":       grant.ModuleVersion,
		"granted_domain":       grant.GrantedDomain,
		"granted_timeout_ms":   grant.GrantedTimeoutMs,
		"cid":                  grant.CID,
		"encrypted_size_bytes": len(encryptedBundleBytes),
		"decrypted_size_bytes": len(decryptedBundleBytes),
	}, nil
}

func sdkFetchProviderDescriptor(ctx context.Context, nodeOrigin string) (sdkProviderDescriptor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeOrigin+"/api/module-delivery/provider", nil)
	if err != nil {
		return sdkProviderDescriptor{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return sdkProviderDescriptor{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return sdkProviderDescriptor{}, fmt.Errorf("provider descriptor fetch failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var descriptor sdkProviderDescriptor
	if err := json.NewDecoder(resp.Body).Decode(&descriptor); err != nil {
		return sdkProviderDescriptor{}, err
	}
	return descriptor, nil
}

func sdkBuildChallengeRequest(
	provider sdkProviderDescriptor,
	options sdkModuleQueryOptions,
	reqID string,
	requestedAt uint64,
) ([]byte, error) {
	builder := flatbuffers.NewBuilder(512)
	requesterSigningPubKey, err := hex.DecodeString(options.Wallet.SigningPublicKeyHex)
	if err != nil {
		return nil, err
	}
	reqIDOffset := builder.CreateString(reqID)
	moduleIDOffset := builder.CreateString(options.ModuleID)
	moduleVersionOffset := sdkCreateOptionalString(builder, options.ModuleVersion)
	requesterPeerIDOffset := builder.CreateString(options.Wallet.PeerID)
	requesterXPubOffset := builder.CreateString(options.Wallet.XPub)
	requesterSigningPubKeyOffset := builder.CreateByteVector(requesterSigningPubKey)
	requesterEphemeralPubKeyOffset := builder.CreateByteVector(options.Wallet.Identity.EncryptionPub)
	requestedDomainOffset := builder.CreateString(options.RequesterDomain)
	providerPeerIDOffset := builder.CreateString(provider.PeerID)

	lch.LCHStart(builder)
	lch.LCHAddMESSAGE_TYPE(builder, 0)
	lch.LCHAddROLE(builder, 0)
	lch.LCHAddREQUEST_ID(builder, reqIDOffset)
	lch.LCHAddMODULE_ID(builder, moduleIDOffset)
	if moduleVersionOffset != 0 {
		lch.LCHAddMODULE_VERSION(builder, moduleVersionOffset)
	}
	lch.LCHAddREQUESTER_PEER_ID(builder, requesterPeerIDOffset)
	lch.LCHAddREQUESTER_XPUB(builder, requesterXPubOffset)
	lch.LCHAddREQUESTER_SIGNING_PUBKEY(builder, requesterSigningPubKeyOffset)
	lch.LCHAddREQUESTER_EPHEMERAL_PUBKEY(builder, requesterEphemeralPubKeyOffset)
	lch.LCHAddREQUESTED_DOMAIN(builder, requestedDomainOffset)
	lch.LCHAddREQUESTED_TIMEOUT_MS(builder, uint64(options.RequestedTimeoutMs))
	lch.LCHAddREQUESTED_AT(builder, requestedAt)
	lch.LCHAddPROVIDER_PEER_ID(builder, providerPeerIDOffset)
	root := lch.LCHEnd(builder)
	lch.FinishLCHBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func sdkBuildProofRequest(
	provider sdkProviderDescriptor,
	options sdkModuleQueryOptions,
	challenge sdkChallengeResponse,
	signature []byte,
) ([]byte, error) {
	builder := flatbuffers.NewBuilder(512)
	requesterSigningPubKey, err := hex.DecodeString(options.Wallet.SigningPublicKeyHex)
	if err != nil {
		return nil, err
	}
	reqIDOffset := builder.CreateString(challenge.ReqID)
	moduleIDOffset := builder.CreateString(options.ModuleID)
	moduleVersionOffset := sdkCreateOptionalString(builder, options.ModuleVersion)
	requesterPeerIDOffset := builder.CreateString(options.Wallet.PeerID)
	requesterXPubOffset := builder.CreateString(options.Wallet.XPub)
	requestedDomainOffset := builder.CreateString(options.RequesterDomain)
	requesterEphemeralPubKeyOffset := builder.CreateByteVector(options.Wallet.Identity.EncryptionPub)
	challengeNonceOffset := builder.CreateByteVector(challenge.Nonce)
	providerPeerID := strings.TrimSpace(challenge.ProviderPeerID)
	if providerPeerID == "" {
		providerPeerID = provider.PeerID
	}
	providerPeerIDOffset := builder.CreateString(providerPeerID)
	signatureOffset := builder.CreateByteVector(signature)
	signingPubKeyOffset := builder.CreateByteVector(requesterSigningPubKey)

	lpf.LPFStart(builder)
	lpf.LPFAddMESSAGE_TYPE(builder, 0)
	lpf.LPFAddREQUEST_ID(builder, reqIDOffset)
	lpf.LPFAddMODULE_ID(builder, moduleIDOffset)
	if moduleVersionOffset != 0 {
		lpf.LPFAddMODULE_VERSION(builder, moduleVersionOffset)
	}
	lpf.LPFAddREQUESTER_PEER_ID(builder, requesterPeerIDOffset)
	lpf.LPFAddREQUESTER_XPUB(builder, requesterXPubOffset)
	lpf.LPFAddREQUESTED_DOMAIN(builder, requestedDomainOffset)
	lpf.LPFAddREQUESTED_TIMEOUT_MS(builder, uint64(options.RequestedTimeoutMs))
	lpf.LPFAddREQUESTER_EPHEMERAL_PUBKEY(builder, requesterEphemeralPubKeyOffset)
	lpf.LPFAddCHALLENGE_NONCE(builder, challengeNonceOffset)
	lpf.LPFAddCHALLENGE_EXPIRES_AT(builder, challenge.ExpiresAtMs)
	lpf.LPFAddPROVIDER_PEER_ID(builder, providerPeerIDOffset)
	lpf.LPFAddSIGNATURE(builder, signatureOffset)
	lpf.LPFAddSIGNING_PUBKEY(builder, signingPubKeyOffset)
	lpf.LPFAddTIMESTAMP_MS(builder, uint64(time.Now().UnixMilli()))
	root := lpf.LPFEnd(builder)
	lpf.FinishLPFBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func sdkDecodeChallengeResponse(data []byte) (sdkChallengeResponse, error) {
	if !lch.LCHBufferHasIdentifier(data) {
		return sdkChallengeResponse{}, fmt.Errorf("challenge response missing $LCH identifier")
	}
	challenge := lch.GetRootAsLCH(data, 0)
	if int8(challenge.MESSAGE_TYPE()) != 1 || int8(challenge.ROLE()) != 1 {
		return sdkChallengeResponse{}, fmt.Errorf("unexpected licensing challenge response")
	}
	return sdkChallengeResponse{
		ReqID:          string(challenge.REQUEST_ID()),
		ModuleID:       string(challenge.MODULE_ID()),
		ModuleVersion:  string(challenge.MODULE_VERSION()),
		Nonce:          append([]byte(nil), challenge.CHALLENGE_NONCEBytes()...),
		ExpiresAtMs:    challenge.EXPIRES_AT(),
		ProviderPeerID: string(challenge.PROVIDER_PEER_ID()),
		RawBytes:       append([]byte(nil), data...),
	}, nil
}

func sdkDecodeGrantResponse(data []byte) (sdkGrantResponse, error) {
	if !lgr.LGRBufferHasIdentifier(data) {
		return sdkGrantResponse{}, fmt.Errorf("grant response missing $LGR identifier")
	}
	grant := lgr.GetRootAsLGR(data, 0)
	if int8(grant.MESSAGE_TYPE()) != 1 {
		return sdkGrantResponse{}, fmt.Errorf("licensing grant was not granted")
	}
	descriptor := grant.MODULE_DESCRIPTOR(nil)
	if descriptor == nil {
		return sdkGrantResponse{}, fmt.Errorf("grant missing module descriptor")
	}
	response := sdkGrantResponse{
		ReqID:                    string(grant.REQUEST_ID()),
		ModuleID:                 string(grant.MODULE_ID()),
		ModuleVersion:            string(grant.MODULE_VERSION()),
		GrantedDomain:            string(grant.GRANTED_DOMAIN()),
		GrantedTimeoutMs:         grant.GRANTED_TIMEOUT_MS(),
		CID:                      string(descriptor.WASM_CID()),
		ContentHash:              append([]byte(nil), descriptor.ENCRYPTED_WASM_HASHBytes()...),
		EncryptedSizeBytes:       descriptor.ENCRYPTED_WASM_SIZE(),
		WrappedContentKeyPayload: append([]byte(nil), grant.WRAPPED_CONTENT_KEY_PAYLOADBytes()...),
	}
	if header := grant.WRAPPED_CONTENT_KEY_HEADER(nil); header != nil {
		response.WrappedContentKeyRootType = string(header.ROOT_TYPE())
		response.WrappedContentKeyContext = string(header.CONTEXT())
		response.WrappedContentKeyEphemeralPub = append([]byte(nil), header.EPHEMERAL_PUBLIC_KEYBytes()...)
	}
	return response, nil
}

func sdkUnwrapGrantContentKey(grant sdkGrantResponse, wallet *sdkLoadedWallet) ([]byte, error) {
	rootType := sdkNormalizeRootType(grant.WrappedContentKeyRootType)
	if rootType == "" || rootType == "KMF" {
		return sdkDecodeDirectKMFContentKey(grant.WrappedContentKeyPayload)
	}
	if rootType != "REC" {
		return nil, fmt.Errorf("unsupported wrapped content key root type: %s", grant.WrappedContentKeyRootType)
	}
	if len(grant.WrappedContentKeyEphemeralPub) != 32 {
		return nil, fmt.Errorf("wrapped content key REC missing provider ephemeral public key")
	}
	sharedSecret, err := curve25519.X25519(wallet.Identity.EncryptionKey, grant.WrappedContentKeyEphemeralPub)
	if err != nil {
		return nil, fmt.Errorf("derive wrapped content key shared secret: %w", err)
	}
	context := strings.TrimSpace(grant.WrappedContentKeyContext)
	if context == "" {
		context = "space-data-network/module-delivery/grant/v1"
	}
	payloadKey, err := sdkHKDFSHA256(sharedSecret, []byte(context), 32)
	if err != nil {
		return nil, err
	}
	return sdkDecodeRECWrappedKMFContentKey(grant.WrappedContentKeyPayload, payloadKey)
}

func sdkDecodeDirectKMFContentKey(payload []byte) ([]byte, error) {
	if !kmf.KMFBufferHasIdentifier(payload) {
		return nil, fmt.Errorf("wrapped content key payload missing $KMF identifier")
	}
	keyMaterial := kmf.GetRootAsKMF(payload, 0)
	keyBytes := append([]byte(nil), keyMaterial.KEY_BYTESBytes()...)
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("content key must be 32 bytes, got %d", len(keyBytes))
	}
	return keyBytes, nil
}

func sdkDecodeRECWrappedKMFContentKey(payload, payloadKey []byte) ([]byte, error) {
	if !flatbuffers.BufferHasIdentifier(payload, "$REC") {
		return nil, fmt.Errorf("wrapped content key REC missing $REC identifier")
	}
	root := flatbuffers.GetUOffsetT(payload)
	collection := flatbuffers.Table{Bytes: payload, Pos: root}
	recordsOffset := flatbuffers.UOffsetT(collection.Offset(6))
	if recordsOffset == 0 {
		return nil, fmt.Errorf("wrapped content key REC record missing")
	}
	recordsVector := collection.Vector(recordsOffset)
	if collection.VectorLen(recordsOffset) < 1 {
		return nil, fmt.Errorf("wrapped content key REC record missing")
	}
	recordPos := collection.Indirect(recordsVector)
	record := flatbuffers.Table{Bytes: payload, Pos: recordPos}
	valueOffset := flatbuffers.UOffsetT(record.Offset(6))
	if valueOffset == 0 {
		return nil, fmt.Errorf("wrapped content key REC did not contain a KMF payload")
	}
	var table flatbuffers.Table
	record.Union(&table, valueOffset)
	keyBytesOffset := flatbuffers.UOffsetT(table.Offset(12))
	if keyBytesOffset == 0 {
		return nil, fmt.Errorf("wrapped content key KMF key bytes missing")
	}
	keyBytes := append([]byte(nil), table.ByteVector(keyBytesOffset+table.Pos)...)
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("wrapped content key KMF key bytes missing")
	}
	if err := sdkDecryptFlatbufferFieldInPlace(keyBytes, payloadKey, 4, 0); err != nil {
		return nil, err
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("content key must be 32 bytes, got %d", len(keyBytes))
	}
	return keyBytes, nil
}

func sdkDecryptFlatbufferFieldInPlace(bytes []byte, payloadKey []byte, fieldID uint16, recordIndex uint32) error {
	fieldKey, err := sdkDeriveFlatbufferFieldBytes(payloadKey, "flatbuffers-field", fieldID, recordIndex, 32)
	if err != nil {
		return err
	}
	fieldIV, err := sdkDeriveFlatbufferFieldBytes(payloadKey, "flatbuffers-iv", fieldID, recordIndex, 16)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(fieldKey)
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, fieldIV)
	stream.XORKeyStream(bytes, bytes)
	return nil
}

func sdkDeriveFlatbufferFieldBytes(payloadKey []byte, label string, fieldID uint16, recordIndex uint32, outputLength int) ([]byte, error) {
	info := make([]byte, len(label)+2+4)
	copy(info, []byte(label))
	info[len(label)] = byte(fieldID >> 8)
	info[len(label)+1] = byte(fieldID)
	info[len(label)+2] = byte(recordIndex >> 24)
	info[len(label)+3] = byte(recordIndex >> 16)
	info[len(label)+4] = byte(recordIndex >> 8)
	info[len(label)+5] = byte(recordIndex)
	return sdkHKDFSHA256(payloadKey, info, outputLength)
}

func sdkHKDFSHA256(inputKeyMaterial, info []byte, outputLength int) ([]byte, error) {
	reader := hkdf.New(sha256.New, inputKeyMaterial, nil, info)
	output := make([]byte, outputLength)
	if _, err := io.ReadFull(reader, output); err != nil {
		return nil, fmt.Errorf("derive HKDF-SHA256 bytes: %w", err)
	}
	return output, nil
}

func sdkNormalizeRootType(value string) string {
	return strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(value), "$"))
}

func sdkFetchCIDBytes(ctx context.Context, nodeOrigin, cid string) ([]byte, error) {
	var errors []string
	if data, err := sdkFetchCIDBytesFromIPFSAPI(ctx, nodeOrigin, cid); err == nil {
		return data, nil
	} else {
		errors = append(errors, err.Error())
	}
	if data, err := sdkFetchCIDBytesFromGateway(ctx, nodeOrigin, cid); err == nil {
		return data, nil
	} else {
		errors = append(errors, err.Error())
	}
	return nil, fmt.Errorf("failed to fetch CID %s: %s", cid, strings.Join(errors, "; "))
}

func sdkFetchCIDBytesFromIPFSAPI(ctx context.Context, nodeOrigin, cid string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodeOrigin+"/api/v0/cat?arg="+url.QueryEscape(cid), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	return sdkDoBytes(req)
}

func sdkFetchCIDBytesFromGateway(ctx context.Context, nodeOrigin, cid string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeOrigin+"/ipfs/"+url.PathEscape(cid), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	return sdkDoBytes(req)
}

func sdkDoBytes(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s failed: %d %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

func sdkDecryptAESGCM(key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 12 {
		return nil, fmt.Errorf("encrypted bundle too short")
	}
	iv := ciphertext[:12]
	payload := ciphertext[12:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, iv, payload, nil)
}

func sdkDialModuleDelivery(ctx context.Context, provider sdkProviderDescriptor, payload []byte) ([]byte, error) {
	targetPeerID, err := peer.Decode(provider.PeerID)
	if err != nil {
		return nil, fmt.Errorf("decode provider peer id: %w", err)
	}
	host, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
	)
	if err != nil {
		return nil, err
	}
	defer host.Close()

	candidates := provider.RelayAddresses
	if len(candidates) == 0 {
		return nil, fmt.Errorf("provider descriptor did not advertise relay addresses")
	}
	var lastErr error
	for _, candidate := range candidates {
		normalized := sdkNormalizeDialTarget(candidate, provider.PeerID)
		addr, err := multiaddr.NewMultiaddr(normalized)
		if err != nil {
			lastErr = err
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			lastErr = err
			continue
		}
		if info.ID == "" {
			info.ID = targetPeerID
		}
		host.Peerstore().AddAddrs(info.ID, info.Addrs, time.Minute)
		dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = host.Connect(dialCtx, *info)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		stream, err := host.NewStream(streamCtx, info.ID, protocol.ID(sdkModuleDeliveryProtocolID))
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(30 * time.Second))
		if _, err := stream.Write(payload); err != nil {
			cancel()
			_ = stream.Reset()
			lastErr = err
			continue
		}
		if closeWriter, ok := stream.(interface{ CloseWrite() error }); ok {
			_ = closeWriter.CloseWrite()
		}
		response, err := io.ReadAll(io.LimitReader(stream, 16<<20))
		cancel()
		_ = stream.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if len(response) == 0 {
			lastErr = fmt.Errorf("empty module-delivery response")
			continue
		}
		return response, nil
	}
	return nil, fmt.Errorf("failed to dial module-delivery provider: %w", lastErr)
}

func sdkNormalizeDialTarget(addr, targetPeerID string) string {
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

func sdkCreateQueryRequestID() string {
	random := make([]byte, 4)
	if _, err := cryptorand.Read(random); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(random)
}

func sdkCreateOptionalString(builder *flatbuffers.Builder, value string) flatbuffers.UOffsetT {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return builder.CreateString(value)
}
