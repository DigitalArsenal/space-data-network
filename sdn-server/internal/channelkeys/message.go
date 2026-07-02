package channelkeys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	enc "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENC"
	flatbuffers "github.com/google/flatbuffers/go"
)

// Channel chat message envelope (WS9.2) — the encrypted pub/sub wire format,
// byte-identical across Go and sdn-js:
//
//	u32LE(len(encBytes)) || encBytes || signature(64) || ciphertext||tag
//
// where encBytes is a standalone SDS $ENC header describing the symmetric
// message crypto:
//
//	SYMMETRIC            = 1 (AES-256-GCM — the deployed SDK convention; the
//	                          published SymmetricAlgo enum only names CTR=0)
//	EPHEMERAL_PUBLIC_KEY = the SENDER's ed25519 public key (32 bytes; the
//	                          message's public-key material — used to verify
//	                          the envelope signature)
//	NONCE_START          = the 12-byte GCM nonce
//	RECIPIENT_KEY_ID     = be64(content-key epoch) — identifies WHICH channel
//	                          key decrypts this message
//	CONTEXT              = the channel's ECIES context (domain separator)
//	TIMESTAMP            = sender clock, unix milliseconds
//
// ciphertext = AES-256-GCM(channelContentKey, nonce, plaintext, aad=encBytes):
// the header is bound as AAD (the deployed protected-publication pattern), and
// signature = ed25519(senderPriv, "SDN-CHN-MSG\0" || encBytes || ciphertext)
// binds the sender to the exact header + body. Non-members hold no content key
// and see only ciphertext; members reject envelopes with bad signatures.

const (
	gcmNonceBytes = 12
	sigBytes      = ed25519.SignatureSize
	// symmetricAlgoAES256GCM is the deployed SDK raw-byte encoding of
	// AES-256-GCM in ENC.SYMMETRIC (the published enum only names AES_256_CTR=0).
	symmetricAlgoAES256GCM = 1
)

// messageSigPrefix domain-separates the envelope signature.
var messageSigPrefix = []byte("SDN-CHN-MSG\x00")

// Message is a decrypted + verified channel chat message.
type Message struct {
	// Plaintext is the decrypted message body.
	Plaintext []byte
	// SenderPublicKey is the sender's ed25519 public key (signature verified).
	SenderPublicKey []byte
	// Epoch is the content-key epoch the sender encrypted under.
	Epoch uint64
	// TimestampMs is the sender-reported unix-milliseconds clock.
	TimestampMs uint64
}

// EncryptOptions pins nonce/timestamp for deterministic cross-runtime vectors;
// zero values mean random nonce / caller-supplied timestamp of 0.
type EncryptOptions struct {
	Nonce       []byte // 12 bytes; nil = random
	TimestampMs uint64
}

// EncryptMessage seals a chat message for the channel: AES-256-GCM under the
// channel content key with the $ENC header as AAD, signed by the sender's
// ed25519 key. context/epoch must be the channel's current Context()/Epoch().
func EncryptMessage(contentKey []byte, senderPriv ed25519.PrivateKey, context string, epoch uint64, plaintext []byte, opts EncryptOptions) ([]byte, error) {
	if len(contentKey) != contentKeyBytes {
		return nil, fmt.Errorf("channelkeys: content key must be %d bytes", contentKeyBytes)
	}
	if len(senderPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("channelkeys: sender private key must be ed25519")
	}
	nonce := opts.Nonce
	if nonce == nil {
		nonce = make([]byte, gcmNonceBytes)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, err
		}
	}
	if len(nonce) != gcmNonceBytes {
		return nil, fmt.Errorf("channelkeys: nonce must be %d bytes", gcmNonceBytes)
	}
	senderPub := senderPriv.Public().(ed25519.PublicKey)

	encBytes := buildMessageENC(senderPub, nonce, epoch, context, opts.TimestampMs)

	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, encBytes)

	sigMsg := make([]byte, 0, len(messageSigPrefix)+len(encBytes)+len(ciphertext))
	sigMsg = append(sigMsg, messageSigPrefix...)
	sigMsg = append(sigMsg, encBytes...)
	sigMsg = append(sigMsg, ciphertext...)
	sig := ed25519.Sign(senderPriv, sigMsg)

	out := make([]byte, 0, 4+len(encBytes)+sigBytes+len(ciphertext))
	var lenLE [4]byte
	binary.LittleEndian.PutUint32(lenLE[:], uint32(len(encBytes)))
	out = append(out, lenLE[:]...)
	out = append(out, encBytes...)
	out = append(out, sig...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecryptMessage opens a channel chat envelope with the channel content key,
// verifying the sender signature and the AAD-bound header. expectedContext
// must match the channel context ("" accepts the header's context).
func DecryptMessage(contentKey, envelope []byte, expectedContext string) (*Message, error) {
	if len(contentKey) != contentKeyBytes {
		return nil, fmt.Errorf("channelkeys: content key must be %d bytes", contentKeyBytes)
	}
	if len(envelope) < 4 {
		return nil, errors.New("channelkeys: envelope too short")
	}
	encLen := binary.LittleEndian.Uint32(envelope[:4])
	if int(encLen) < 4 || len(envelope) < 4+int(encLen)+sigBytes {
		return nil, errors.New("channelkeys: envelope truncated")
	}
	encBytes := envelope[4 : 4+encLen]
	sig := envelope[4+encLen : 4+int(encLen)+sigBytes]
	ciphertext := envelope[4+int(encLen)+sigBytes:]

	if !enc.ENCBufferHasIdentifier(encBytes) {
		return nil, errors.New("channelkeys: envelope header is not $ENC")
	}
	header := enc.GetRootAsENC(encBytes, 0)
	if got := header.SYMMETRIC(); int8(got) != symmetricAlgoAES256GCM {
		return nil, fmt.Errorf("channelkeys: unsupported symmetric algo %d", got)
	}
	senderPub := header.EPHEMERAL_PUBLIC_KEYBytes()
	if len(senderPub) != ed25519.PublicKeySize {
		return nil, errors.New("channelkeys: header missing sender public key")
	}
	nonce := header.NONCE_STARTBytes()
	if len(nonce) != gcmNonceBytes {
		return nil, errors.New("channelkeys: header missing GCM nonce")
	}
	ctx := string(header.CONTEXT())
	if expectedContext != "" && ctx != expectedContext {
		return nil, fmt.Errorf("channelkeys: context mismatch: %q", ctx)
	}
	keyID := header.RECIPIENT_KEY_IDBytes()
	var epoch uint64
	if len(keyID) == 8 {
		epoch = binary.BigEndian.Uint64(keyID)
	}

	sigMsg := make([]byte, 0, len(messageSigPrefix)+len(encBytes)+len(ciphertext))
	sigMsg = append(sigMsg, messageSigPrefix...)
	sigMsg = append(sigMsg, encBytes...)
	sigMsg = append(sigMsg, ciphertext...)
	if !ed25519.Verify(ed25519.PublicKey(senderPub), sigMsg, sig) {
		return nil, errors.New("channelkeys: sender signature invalid")
	}

	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, encBytes)
	if err != nil {
		return nil, fmt.Errorf("channelkeys: decrypt failed (wrong key or tampered): %w", err)
	}
	return &Message{
		Plaintext:       plaintext,
		SenderPublicKey: append([]byte(nil), senderPub...),
		Epoch:           epoch,
		TimestampMs:     header.TIMESTAMP(),
	}, nil
}

// ChatTopic returns the gossipsub topic for a channel's encrypted chat,
// mirroring the sdn-js CHANNEL_TOPIC_PREFIX convention.
func ChatTopic(channelID string) string {
	return "/spacedatanetwork/channels/" + channelID + "/chat"
}

func buildMessageENC(senderPub ed25519.PublicKey, nonce []byte, epoch uint64, context string, timestampMs uint64) []byte {
	b := flatbuffers.NewBuilder(160)
	pubOff := b.CreateByteVector(senderPub)
	nonceOff := b.CreateByteVector(nonce)
	var epochID [8]byte
	binary.BigEndian.PutUint64(epochID[:], epoch)
	ridOff := b.CreateByteVector(epochID[:])
	ctxOff := b.CreateString(context)
	enc.ENCStart(b)
	enc.ENCAddVERSION(b, 1)
	enc.ENCAddSYMMETRIC(b, enc.SymmetricAlgo(symmetricAlgoAES256GCM))
	enc.ENCAddEPHEMERAL_PUBLIC_KEY(b, pubOff)
	enc.ENCAddNONCE_START(b, nonceOff)
	enc.ENCAddRECIPIENT_KEY_ID(b, ridOff)
	enc.ENCAddCONTEXT(b, ctxOff)
	enc.ENCAddTIMESTAMP(b, timestampMs)
	root := enc.ENCEnd(b)
	enc.FinishENCBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}
