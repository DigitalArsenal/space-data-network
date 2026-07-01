package deliveryclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SignFunc signs message with the requester's signing private key. It abstracts
// the signing curve (ed25519 default, secp256k1) — the requester holds the key.
type SignFunc func(message []byte) ([]byte, error)

// Transport carries the consumer's protocol frames to a provider and fetches
// content-addressed bytes. A libp2p node implements Dial over the
// module-delivery stream protocol and FetchCID over its IPFS/bitswap client;
// tests implement it in-process.
type Transport interface {
	// Dial sends one request frame to the provider on the module-delivery
	// protocol and returns the single response frame.
	Dial(ctx context.Context, providerPeerID string, request []byte) ([]byte, error)
	// FetchCID returns the bytes addressed by cid (the encrypted bundle).
	FetchCID(ctx context.Context, cid string) ([]byte, error)
}

// ContentKeyUnwrapper recovers the module content key from a validated grant
// using the requester's ephemeral private key. The concrete ENC/KMF
// implementation is WS4.3d (cross-validated against a JS/SDK vector); the
// interface lets the orchestration be built and tested independently.
type ContentKeyUnwrapper interface {
	Unwrap(grant *Grant) (contentKey []byte, err error)
}

// BundleDecryptor decrypts an encrypted module bundle with the content key,
// returning the plaintext WASM bytes.
type BundleDecryptor interface {
	Decrypt(encryptedBundle, contentKey []byte) (plaintext []byte, err error)
}

// RequesterIdentity is the consumer's identity and keys used across the flow.
// EphemeralPublicKey is the X25519 public key the provider wraps the content key
// to; Sign proves control of the signing key the provider gates on.
type RequesterIdentity struct {
	PeerID             string
	XPub               string
	SigningPublicKey   []byte
	EphemeralPublicKey []byte
	Sign               SignFunc
}

// Provider identifies the module provider to dial.
type Provider struct {
	PeerID string
}

// PullParams are the inputs to a single module pull.
type PullParams struct {
	RequestID          string
	ModuleID           string
	ModuleVersion      string
	RequestedDomain    string
	RequestedTimeoutMs uint64
	RequestedAtMs      uint64
	// NowMs, when >0, is used to reject expired grants.
	NowMs uint64
}

// Consumer runs the SDN module-delivery grant flow — the Go peer of the sdn-js
// requestModuleGrant + fetch + decrypt client.
type Consumer struct {
	transport Transport
	identity  RequesterIdentity
}

// NewConsumer builds a Consumer. transport and identity.Sign are required.
func NewConsumer(transport Transport, identity RequesterIdentity) (*Consumer, error) {
	if transport == nil {
		return nil, errors.New("deliveryclient: transport is required")
	}
	if identity.Sign == nil {
		return nil, errors.New("deliveryclient: requester Sign func is required")
	}
	return &Consumer{transport: transport, identity: identity}, nil
}

// RequestGrant runs challenge -> proof -> grant against the provider and returns
// the validated grant. It signs the exact challenge response bytes, mirroring
// the sdn-js requestModuleGrant flow.
func (c *Consumer) RequestGrant(ctx context.Context, provider Provider, p PullParams) (*Grant, error) {
	if strings.TrimSpace(p.ModuleID) == "" {
		return nil, errors.New("deliveryclient: module id is required")
	}

	req := ChallengeRequest{
		RequestID:                   p.RequestID,
		ModuleID:                    p.ModuleID,
		ModuleVersion:               p.ModuleVersion,
		RequesterPeerID:             c.identity.PeerID,
		RequesterXPub:               c.identity.XPub,
		RequesterSigningPublicKey:   c.identity.SigningPublicKey,
		RequesterEphemeralPublicKey: c.identity.EphemeralPublicKey,
		RequestedDomain:             p.RequestedDomain,
		RequestedTimeoutMs:          p.RequestedTimeoutMs,
		RequestedAtMs:               p.RequestedAtMs,
		ProviderPeerID:              provider.PeerID,
	}
	challengeRequestFrame, err := EncodeChallengeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: encode challenge request: %w", err)
	}

	challengeResponseFrame, err := c.transport.Dial(ctx, provider.PeerID, challengeRequestFrame)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: dial challenge: %w", err)
	}
	challenge, err := DecodeChallengeResponse(challengeResponseFrame)
	if err != nil {
		return nil, err
	}
	if req.RequestID != "" && challenge.RequestID != "" && challenge.RequestID != req.RequestID {
		return nil, errors.New("deliveryclient: challenge request id mismatch")
	}
	if challenge.ModuleID != "" && challenge.ModuleID != req.ModuleID {
		return nil, errors.New("deliveryclient: challenge module id mismatch")
	}

	signature, err := c.identity.Sign(challenge.RawBytes)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: sign challenge: %w", err)
	}

	proof := GrantProofFromChallenge(req, challenge, signature, c.identity.SigningPublicKey, p.NowMs)
	proofFrame, err := EncodeGrantProof(proof)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: encode proof: %w", err)
	}

	grantFrame, err := c.transport.Dial(ctx, provider.PeerID, proofFrame)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: dial proof: %w", err)
	}
	grant, err := DecodeGrant(grantFrame)
	if err != nil {
		return nil, err
	}
	if err := grant.Validate(GrantExpectations{
		RequestID:          p.RequestID,
		ModuleID:           p.ModuleID,
		ModuleVersion:      p.ModuleVersion,
		ExpectedDomain:     p.RequestedDomain,
		RequestedTimeoutMs: p.RequestedTimeoutMs,
		NowMs:              p.NowMs,
	}); err != nil {
		return nil, err
	}
	return grant, nil
}

// FetchAndDecrypt fetches the granted module's encrypted bundle by CID, unwraps
// the content key, and returns the decrypted WASM bytes.
func (c *Consumer) FetchAndDecrypt(ctx context.Context, grant *Grant, unwrapper ContentKeyUnwrapper, decryptor BundleDecryptor) ([]byte, error) {
	if grant == nil {
		return nil, errors.New("deliveryclient: nil grant")
	}
	if unwrapper == nil || decryptor == nil {
		return nil, errors.New("deliveryclient: unwrapper and decryptor are required")
	}
	cid := strings.TrimSpace(grant.ModuleDescriptorCID)
	if cid == "" {
		return nil, errors.New("deliveryclient: grant has no module descriptor CID")
	}

	encryptedBundle, err := c.transport.FetchCID(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: fetch %s: %w", cid, err)
	}
	if len(encryptedBundle) == 0 {
		return nil, fmt.Errorf("deliveryclient: fetched empty bundle for %s", cid)
	}

	contentKey, err := unwrapper.Unwrap(grant)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: unwrap content key: %w", err)
	}
	plaintext, err := decryptor.Decrypt(encryptedBundle, contentKey)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: decrypt bundle: %w", err)
	}
	if len(plaintext) == 0 {
		return nil, errors.New("deliveryclient: decrypted bundle is empty")
	}
	return plaintext, nil
}

// PullModule runs the full flow: RequestGrant then FetchAndDecrypt, returning
// the decrypted module WASM bytes. This is the Go equivalent of the sdn-js
// requestModuleGrant + fetchEncryptedModuleBundle + decrypt sequence and the
// unit the dependency resolver invokes per planned module.
func (c *Consumer) PullModule(ctx context.Context, provider Provider, p PullParams, unwrapper ContentKeyUnwrapper, decryptor BundleDecryptor) ([]byte, error) {
	grant, err := c.RequestGrant(ctx, provider, p)
	if err != nil {
		return nil, err
	}
	return c.FetchAndDecrypt(ctx, grant, unwrapper, decryptor)
}
