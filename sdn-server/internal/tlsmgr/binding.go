package tlsmgr

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
)

var (
	BootstrapBindingOID              = asn1.ObjectIdentifier{1, 3, 112, 4, 57, 10, 1}
	ed25519SignatureAlgorithmOID     = asn1.ObjectIdentifier{1, 3, 101, 112}
	bootstrapBindingDomainSeparation = []byte("SDN TLS BOOTSTRAP BINDING V1")
)

type bootstrapBindingASN1 struct {
	Version                         int
	PeerID                          string `asn1:"optional,utf8"`
	EncryptionPath                  string `asn1:"utf8"`
	EncryptionX25519PublicKey       []byte
	EncryptionProofEd25519PublicKey []byte
	TLSSPKISHA256                   []byte
	SignatureAlgorithm              asn1.ObjectIdentifier
	Signature                       []byte
}

type BootstrapBindingInput struct {
	PeerID                    string
	EncryptionPath            string
	EncryptionX25519PublicKey []byte
	ProofEd25519Seed          []byte
	TLSSPKISHA256             []byte
}

type Binding struct {
	Version                         int
	PeerID                          string
	EncryptionPath                  string
	EncryptionX25519PublicKey       []byte
	EncryptionProofEd25519PublicKey []byte
	TLSSPKISHA256                   []byte
	SignatureAlgorithm              asn1.ObjectIdentifier
}

func EncodeBootstrapBinding(input BootstrapBindingInput) (pkix.Extension, error) {
	if len(input.ProofEd25519Seed) != ed25519.SeedSize {
		return pkix.Extension{}, fmt.Errorf("proof seed length = %d, want %d", len(input.ProofEd25519Seed), ed25519.SeedSize)
	}
	if len(input.EncryptionX25519PublicKey) == 0 {
		return pkix.Extension{}, fmt.Errorf("encryption x25519 public key is required")
	}
	if len(input.TLSSPKISHA256) == 0 {
		return pkix.Extension{}, fmt.Errorf("tls spki hash is required")
	}

	proofPriv := ed25519.NewKeyFromSeed(input.ProofEd25519Seed)
	proofPub := proofPriv.Public().(ed25519.PublicKey)
	message := bootstrapBindingMessage(
		input.PeerID,
		input.EncryptionPath,
		input.EncryptionX25519PublicKey,
		proofPub,
		input.TLSSPKISHA256,
	)
	signature := ed25519.Sign(proofPriv, message)

	payload, err := asn1.Marshal(bootstrapBindingASN1{
		Version:                         1,
		PeerID:                          input.PeerID,
		EncryptionPath:                  input.EncryptionPath,
		EncryptionX25519PublicKey:       append([]byte(nil), input.EncryptionX25519PublicKey...),
		EncryptionProofEd25519PublicKey: append([]byte(nil), proofPub...),
		TLSSPKISHA256:                   append([]byte(nil), input.TLSSPKISHA256...),
		SignatureAlgorithm:              ed25519SignatureAlgorithmOID,
		Signature:                       append([]byte(nil), signature...),
	})
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("marshal bootstrap binding: %w", err)
	}

	return pkix.Extension{
		Id:       BootstrapBindingOID,
		Critical: false,
		Value:    payload,
	}, nil
}

func VerifyBootstrapBinding(raw []byte, wantSPKIHash []byte) (*Binding, error) {
	var decoded bootstrapBindingASN1
	if _, err := asn1.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("unmarshal bootstrap binding: %w", err)
	}
	if decoded.Version != 1 {
		return nil, fmt.Errorf("unsupported bootstrap binding version %d", decoded.Version)
	}
	if !decoded.SignatureAlgorithm.Equal(ed25519SignatureAlgorithmOID) {
		return nil, fmt.Errorf("unsupported bootstrap signature algorithm %v", decoded.SignatureAlgorithm)
	}
	if len(wantSPKIHash) > 0 && !bytes.Equal(decoded.TLSSPKISHA256, wantSPKIHash) {
		return nil, fmt.Errorf("tls spki hash mismatch")
	}

	message := bootstrapBindingMessage(
		decoded.PeerID,
		decoded.EncryptionPath,
		decoded.EncryptionX25519PublicKey,
		decoded.EncryptionProofEd25519PublicKey,
		decoded.TLSSPKISHA256,
	)
	if !ed25519.Verify(
		ed25519.PublicKey(decoded.EncryptionProofEd25519PublicKey),
		message,
		decoded.Signature,
	) {
		return nil, fmt.Errorf("invalid bootstrap binding signature")
	}

	return &Binding{
		Version:                         decoded.Version,
		PeerID:                          decoded.PeerID,
		EncryptionPath:                  decoded.EncryptionPath,
		EncryptionX25519PublicKey:       append([]byte(nil), decoded.EncryptionX25519PublicKey...),
		EncryptionProofEd25519PublicKey: append([]byte(nil), decoded.EncryptionProofEd25519PublicKey...),
		TLSSPKISHA256:                   append([]byte(nil), decoded.TLSSPKISHA256...),
		SignatureAlgorithm:              decoded.SignatureAlgorithm,
	}, nil
}

func bootstrapBindingMessage(peerID, path string, x25519Pub, proofEd25519Pub, spkiHash []byte) []byte {
	h := sha256.New()
	h.Write(bootstrapBindingDomainSeparation)
	h.Write([]byte(peerID))
	h.Write([]byte(path))
	h.Write(x25519Pub)
	h.Write(proofEd25519Pub)
	h.Write(spkiHash)
	return h.Sum(nil)
}
