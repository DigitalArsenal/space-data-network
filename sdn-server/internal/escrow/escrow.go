// Package escrow provides recoverable custody of node identity key material.
//
// # Why this exists
//
// vm-orbit-det-01 was rebuilt and its identity was gone. At-rest encryption
// does not help with that: a wiped disk takes the ciphertext with it. The only
// protection that survives a destroy+recreate is a copy held somewhere else —
// and a copy of a private key is exactly the thing that must never exist in
// plaintext. Escrow is that copy, sealed so only the owner's root wallet can
// open it, which makes the blob safe to replicate to peers or hand to an
// operator to keep off-box.
//
// # Derivation first, escrow only when forced
//
// Escrow is the FALLBACK, not the default. The owner's paradigm is HD/xpub
// everywhere: an identity derived from the root mnemonic at a known path is
// re-creatable from the seed alone and needs no escrow blob at all. Prefer that.
//
//	IDENTITY                              RE-DERIVABLE   PROTECTION
//	sdn-server node identity (secp256k1)  yes            derivation path; the
//	                                                     mnemonic is the backup
//	kubo peer key (Ed25519, random at     NO             escrow the actual key
//	`ipfs init`, PeerID must not change)
//	newly initialized nodes               yes            derivation path
//
// A Subject carrying a DerivationPath and no sealed payload is a legitimate,
// complete escrow record: it says "this identity is re-creatable from the root
// seed at this path", which is all a recovery needs.
//
// # Sealing — composed, not invented
//
// Nothing here is new cryptography. Two in-tree primitives are composed:
//
//  1. internal/ecies.Wrap/Unwrap — the SDS $ENC + $KMF envelope
//     (docs/UNIFIED_ECIES.md) that seals a 32-byte content key to a recipient
//     public key over X25519 or secp256k1.
//
//  2. internal/keys.EncryptSecret/DecryptSecret — the Argon2id +
//     XChaCha20-Poly1305 envelope already used for node.key and the mnemonic,
//     here keyed by the content key rather than a password.
//
//     contentKey        = 32 random bytes
//     payload           = EncryptSecret(privateKeyMaterial, contentKey)
//     ($ENC, $KMF)      = ecies.Wrap(recipientPub, contentKey)
//
// The recipient is the node's ADVERTISED ENCRYPTION KEY — the xpub-derivable
// secp256k1 key at m/44'/0'/account'/1/0 (internal/epm.AdvertisedEncryptionKey).
// Recovery therefore requires the private key at that path, which only the root
// mnemonic can produce. Anyone else holding the blob has ciphertext.
//
// # Fail closed
//
// Open verifies that the recovered key material reproduces the PeerID recorded
// in the blob before returning it. A blob that opens but yields a different
// identity is treated as an error, never as a successful recovery — silently
// restoring the wrong identity is worse than restoring none.
package escrow

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// Context is the escrow domain separator, distinct from the module-delivery
// grant context so an escrow envelope can never be opened by a grant reader or
// vice versa.
const Context = "space-data-network/identity-escrow/v1"

// Magic prefixes every escrow blob. The blob is a text manifest so an operator
// can eyeball what an escrow file claims to hold without decrypting anything.
const Magic = "SDN-IDENTITY-ESCROW/1"

// Key type identifiers recorded in a Subject.
const (
	KeyTypeLibp2pEd25519   = "libp2p-ed25519"
	KeyTypeLibp2pSecp256k1 = "libp2p-secp256k1"
)

// Subject is the PLAINTEXT description of what an escrow blob covers. It
// deliberately contains no secret: a PeerID is public, a derivation path is
// public, and the machine name is already advertised. This is what makes the
// blob safe to replicate.
type Subject struct {
	// PeerID the sealed material reproduces. Verified on Open.
	PeerID string `json:"PeerID"`
	// KeyType is one of the KeyType* constants.
	KeyType string `json:"KeyType"`
	// DerivationPath, when non-empty, means this identity is RE-DERIVABLE from
	// the root seed and needs no sealed payload.
	DerivationPath string `json:"DerivationPath,omitempty"`
	// MachineName records where the identity lived, for operator triage.
	MachineName string `json:"MachineName,omitempty"`
	// Role is a free-form label ("kubo-od-producer", "sidecar") so a recovering
	// operator knows which host a blob belongs to.
	Role string `json:"Role,omitempty"`
	// SealedAt is an RFC3339 UTC timestamp.
	SealedAt string `json:"SealedAt"`
}

// Recipient records which key the blob was sealed to, so a recovering operator
// knows which wallet/path opens it.
type Recipient struct {
	XPub      string `json:"XPub,omitempty"`
	KeyPath   string `json:"KeyPath"`
	PublicKey string `json:"PublicKey"`
}

// Blob is the on-disk escrow manifest.
//
// JSON keys here are SDN-internal (not an SDS record projection), but they
// follow the same capitalization discipline as the rest of the tree so a future
// API projection needs no renaming.
type Blob struct {
	Magic     string    `json:"Magic"`
	Version   int       `json:"Version"`
	Subject   Subject   `json:"Subject"`
	Recipient Recipient `json:"Recipient"`
	// ENC and KMF are the base64 SDS envelope bytes from ecies.Wrap. Empty for
	// a derivation-only record.
	ENC string `json:"ENC,omitempty"`
	KMF string `json:"KMF,omitempty"`
	// Payload is the base64 EncryptSecret envelope over the private key
	// material, keyed by the content key sealed in ENC/KMF. Empty for a
	// derivation-only record.
	Payload string `json:"Payload,omitempty"`
}

// Derivable reports whether this record describes a re-derivable identity that
// carries no sealed key material.
func (b *Blob) Derivable() bool {
	return b != nil && b.Payload == "" && strings.TrimSpace(b.Subject.DerivationPath) != ""
}

// NewDerivable builds a record for an identity that is re-creatable from the
// root seed. No key material is sealed because none needs to be: the mnemonic
// reproduces it.
func NewDerivable(subject Subject) (*Blob, error) {
	if strings.TrimSpace(subject.DerivationPath) == "" {
		return nil, errors.New("escrow: a derivable record requires a derivation path")
	}
	if strings.TrimSpace(subject.PeerID) == "" {
		return nil, errors.New("escrow: a derivable record requires the PeerID it reproduces")
	}
	subject.SealedAt = nowStamp(subject.SealedAt)
	return &Blob{Magic: Magic, Version: 1, Subject: subject}, nil
}

// Seal escrows raw libp2p-marshaled private key material to recipientPub.
//
// keyMaterial must be the output of crypto.MarshalPrivateKey; it is verified to
// reproduce subject.PeerID BEFORE sealing, so a mislabeled blob cannot be
// created in the first place.
func Seal(keyMaterial []byte, subject Subject, recipient Recipient, recipientPub []byte, kx ecies.KeyExchange) (*Blob, error) {
	if len(keyMaterial) == 0 {
		return nil, errors.New("escrow: no key material to seal")
	}
	priv, err := crypto.UnmarshalPrivateKey(keyMaterial)
	if err != nil {
		return nil, fmt.Errorf("escrow: key material is not a libp2p private key: %w", err)
	}
	derivedID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("escrow: derive PeerID: %w", err)
	}
	if want := strings.TrimSpace(subject.PeerID); want != "" && want != derivedID.String() {
		return nil, fmt.Errorf("escrow: key material is for PeerID %s, not %s — refusing to seal a mislabeled blob",
			derivedID, want)
	}
	subject.PeerID = derivedID.String()
	if subject.KeyType == "" {
		subject.KeyType = keyTypeOf(priv)
	}
	subject.SealedAt = nowStamp(subject.SealedAt)

	contentKey := make([]byte, 32)
	if _, err := rand.Read(contentKey); err != nil {
		return nil, fmt.Errorf("escrow: generate content key: %w", err)
	}
	payload, err := keys.EncryptSecret(keyMaterial, string(contentKey))
	if err != nil {
		return nil, fmt.Errorf("escrow: seal key material: %w", err)
	}
	encBytes, kmfBytes, err := ecies.Wrap(recipientPub, contentKey, ecies.WrapOptions{
		KeyExchange: kx,
		Context:     Context,
	})
	if err != nil {
		return nil, fmt.Errorf("escrow: wrap content key for recipient: %w", err)
	}

	return &Blob{
		Magic:     Magic,
		Version:   1,
		Subject:   subject,
		Recipient: recipient,
		ENC:       base64.StdEncoding.EncodeToString(encBytes),
		KMF:       base64.StdEncoding.EncodeToString(kmfBytes),
		Payload:   base64.StdEncoding.EncodeToString(payload),
	}, nil
}

// Open recovers the libp2p-marshaled private key material from a sealed blob
// using the recipient's private key.
//
// It FAILS CLOSED on identity mismatch: the recovered material must reproduce
// the PeerID the blob claims, or nothing is returned.
func (b *Blob) Open(recipientPriv []byte) ([]byte, error) {
	if b == nil {
		return nil, errors.New("escrow: nil blob")
	}
	if b.Derivable() {
		return nil, fmt.Errorf("escrow: this record is derivation-only (path %s); recover it from the root mnemonic, not from a sealed payload",
			b.Subject.DerivationPath)
	}
	if b.ENC == "" || b.KMF == "" || b.Payload == "" {
		return nil, errors.New("escrow: blob carries no sealed key material")
	}
	encBytes, err := base64.StdEncoding.DecodeString(b.ENC)
	if err != nil {
		return nil, fmt.Errorf("escrow: decode $ENC: %w", err)
	}
	kmfBytes, err := base64.StdEncoding.DecodeString(b.KMF)
	if err != nil {
		return nil, fmt.Errorf("escrow: decode $KMF: %w", err)
	}
	payload, err := base64.StdEncoding.DecodeString(b.Payload)
	if err != nil {
		return nil, fmt.Errorf("escrow: decode payload: %w", err)
	}

	contentKey, err := ecies.Unwrap(recipientPriv, encBytes, kmfBytes, Context)
	if err != nil {
		return nil, fmt.Errorf("escrow: unwrap content key (wrong recovery key for path %s?): %w", b.Recipient.KeyPath, err)
	}
	keyMaterial, err := keys.DecryptSecret(payload, string(contentKey))
	if err != nil {
		return nil, fmt.Errorf("escrow: open sealed key material: %w", err)
	}

	priv, err := crypto.UnmarshalPrivateKey(keyMaterial)
	if err != nil {
		return nil, fmt.Errorf("escrow: recovered material is not a libp2p private key: %w", err)
	}
	got, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("escrow: derive recovered PeerID: %w", err)
	}
	if want := strings.TrimSpace(b.Subject.PeerID); want != "" && got.String() != want {
		return nil, fmt.Errorf("escrow: RECOVERED THE WRONG IDENTITY — blob claims %s but material yields %s", want, got)
	}
	return keyMaterial, nil
}

// Marshal renders the blob as the on-disk manifest.
func (b *Blob) Marshal() ([]byte, error) {
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Parse reads an escrow manifest, rejecting anything that is not one.
func Parse(data []byte) (*Blob, error) {
	var b Blob
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("escrow: not an escrow manifest: %w", err)
	}
	if b.Magic != Magic {
		return nil, fmt.Errorf("escrow: unrecognized manifest (magic %q)", b.Magic)
	}
	if b.Version != 1 {
		return nil, fmt.Errorf("escrow: unsupported escrow version %d", b.Version)
	}
	return &b, nil
}

// WriteFile persists a manifest at 0600. The blob is safe to replicate, but a
// conservative mode costs nothing and keeps casual readers out.
func (b *Blob) WriteFile(path string) error {
	data, err := b.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadFile loads a manifest from disk.
func ReadFile(path string) (*Blob, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func keyTypeOf(priv crypto.PrivKey) string {
	switch priv.Type() {
	case crypto.Ed25519:
		return KeyTypeLibp2pEd25519
	case crypto.Secp256k1:
		return KeyTypeLibp2pSecp256k1
	default:
		return priv.Type().String()
	}
}

func nowStamp(existing string) string {
	if strings.TrimSpace(existing) != "" {
		return existing
	}
	return time.Now().UTC().Format(time.RFC3339)
}
