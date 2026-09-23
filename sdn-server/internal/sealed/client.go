package sealed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// NodeKeys are a node's sealed-transport keys, as its /api/node/info
// advertises them.
type NodeKeys struct {
	EncryptionKey []byte
	SigningKey    ed25519.PublicKey
	Fingerprint   string
}

// ReadNodeKeys fetches a node's advertised sealed-transport keys and refuses
// them unless their fingerprint is the one the caller expects. A CLI has no
// place to show a first-use prompt, so the expected fingerprint is required.
func ReadNodeKeys(ctx context.Context, client *http.Client, baseURL, wantFingerprint string) (NodeKeys, error) {
	if strings.TrimSpace(wantFingerprint) == "" {
		return NodeKeys{}, errors.New("sealed: the node's fingerprint is required (run `spacedatanetwork show-identity` on the node)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/node/info", nil)
	if err != nil {
		return NodeKeys{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return NodeKeys{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return NodeKeys{}, fmt.Errorf("sealed: node info: %s", resp.Status)
	}
	var info struct {
		Sealed *struct {
			EncryptionKey string `json:"encryption_key"`
			SigningKey    string `json:"signing_key"`
			KeyExchange   string `json:"key_exchange"`
			Fingerprint   string `json:"fingerprint"`
		} `json:"sealed_transport"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return NodeKeys{}, fmt.Errorf("sealed: node info: %w", err)
	}
	if info.Sealed == nil || info.Sealed.KeyExchange != "Secp256k1" {
		return NodeKeys{}, errors.New("sealed: this node does not offer the sealed admin transport")
	}
	enc, err1 := hex.DecodeString(info.Sealed.EncryptionKey)
	sig, err2 := hex.DecodeString(info.Sealed.SigningKey)
	if err1 != nil || err2 != nil || len(enc) != 33 || len(sig) != ed25519.PublicKeySize {
		return NodeKeys{}, errors.New("sealed: node info carries malformed keys")
	}
	// The fingerprint is recomputed from the key itself, never taken on the
	// node's word.
	if got := fingerprint(enc); got != strings.TrimSpace(wantFingerprint) {
		return NodeKeys{}, fmt.Errorf("sealed: node fingerprint is %s, expected %s — refusing to talk to it", got, strings.TrimSpace(wantFingerprint))
	}
	return NodeKeys{EncryptionKey: enc, SigningKey: sig, Fingerprint: info.Sealed.Fingerprint}, nil
}

// Result is the inner response of a sealed call.
type Result struct {
	Status      int
	ContentType string
	Body        []byte
}

// Call sends one admin request sealed to node and signed by signer, and opens
// the reply. The reply must be signed by the node's advertised key and bound to
// this exact request.
func Call(ctx context.Context, client *http.Client, baseURL string, node NodeKeys, signer ed25519.PrivateKey, method, route string, body []byte, contentType string) (Result, error) {
	replyPriv := make([]byte, 32)
	if _, err := rand.Read(replyPriv); err != nil {
		return Result{}, err
	}
	defer zeroBytes(replyPriv)
	replyPub, err := curve25519.X25519(replyPriv, curve25519.Basepoint)
	if err != nil {
		return Result{}, err
	}
	envelope, err := Seal(Body{Method: method, Route: route, Body: body, BodyFileID: contentType, ReplyKey: replyPub}, SealOptions{
		RecipientPub: node.EncryptionKey, KeyExchange: ecies.Secp256k1, Signer: signer,
	})
	if err != nil {
		return Result{}, err
	}
	sent, err := Open(envelope)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+Route, bytes.NewReader(envelope))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", ContentType)
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxEnvelope+1))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// The node refused before opening anything (bad signature, not an
		// admin, replay, clock): its plain answer is the result.
		return Result{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: payload}, nil
	}
	reply, err := Open(payload)
	if err != nil {
		return Result{}, fmt.Errorf("sealed: reply: %w", err)
	}
	if !reply.Response || !bytes.Equal(reply.Signer, node.SigningKey) {
		return Result{}, errors.New("sealed: the reply is not signed by this node")
	}
	inner, err := reply.Decrypt(replyPriv)
	if err != nil {
		return Result{}, fmt.Errorf("sealed: reply: %w", err)
	}
	if !bytes.Equal(inner.RequestNonce, sent.Nonce) || !bytes.Equal(inner.RequestDigest, Digest(envelope)) {
		return Result{}, errors.New("sealed: the reply answers a different request")
	}
	return Result{Status: int(inner.Status), ContentType: inner.BodyFileID, Body: inner.Body}, nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// fingerprint is walletderive.Fingerprint (the form `admin init` prints),
// repeated here so this package stays free of the wallet module.
func fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + ":" + h[4:8] + ":" + h[8:12] + ":" + h[12:16]
}
