// Package sealed is the $RPC envelope: an admin command the browser signs with
// its wallet key and seals so only this node can read it, and the node's reply,
// sealed back to the browser and signed by the node. It is what lets the
// dashboard administer a node over plain HTTP as safely as over HTTPS: the
// transport sees only ciphertext, and nothing unsigned is acted on.
//
// Wire (SDS $RPC, THEMIS ruling 2026-09-22):
//
//	RPC      outer record, plaintext: $ENC header, session id, timestamp,
//	         16-byte nonce, CIPHERTEXT, signer public key, two signatures
//	RPCBody  the size-prefixed plaintext inside CIPHERTEXT: method, route,
//	         body, status, the signer key again, and the reply key
//
// Encrypt, then sign. $ENC is AES-256-CTR with no MAC, so integrity comes from
// the signature, which covers the ciphertext and the header:
//
//	SIGNATURE                 Ed25519 over the size-prefixed RPC buffer with
//	                          both signature vectors zeroed in place
//	CANONICAL_JSON_SIGNATURE  Ed25519 over CanonicalJSON (see there)
//
// The signer key is repeated inside the ciphertext, so an envelope whose
// signature was stripped and replaced by another key fails to open.
package sealed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	rpc "github.com/DigitalArsenal/spacedatastandards.org/lib/go/RPC"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

const (
	// ContextRequest and ContextResponse domain-separate the two directions,
	// so a sealed request can never be opened as a response or the reverse.
	ContextRequest  = "RPC/1|request"
	ContextResponse = "RPC/1|response"

	// CiphertextField is RPC.CIPHERTEXT's field id; the seal is keyed by it.
	CiphertextField = 7

	// NonceSize is the length of RPC.NONCE.
	NonceSize = 16

	// SignatureType names the one algorithm both signatures use.
	SignatureType = "Ed25519"

	directionRequest  = 0
	directionResponse = 1
)

// Body is RPCBody.
type Body struct {
	Method        string
	Route         string
	Body          []byte
	BodyFileID    string
	Status        uint16
	SignerKeyID   []byte
	ReplyKey      []byte
	RequestNonce  []byte
	RequestDigest []byte
}

// Envelope is an RPC whose signatures have been verified.
type Envelope struct {
	Response    bool
	Header      ecies.Header
	SessionID   string
	SenderKeyID []byte
	Timestamp   time.Time
	Nonce       []byte
	Ciphertext  []byte
	Signer      ed25519.PublicKey
	// Raw is the envelope exactly as received, for REQUEST_DIGEST.
	Raw []byte
}

// Digest is REQUEST_DIGEST for an envelope's raw bytes.
func Digest(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

// SealOptions are what Seal needs beyond the body.
type SealOptions struct {
	Response     bool
	RecipientPub []byte
	KeyExchange  ecies.KeyExchange
	Signer       ed25519.PrivateKey
	SessionID    string
	Now          time.Time
	Rand         io.Reader
}

// Seal encrypts body for the recipient, then signs the envelope.
func Seal(body Body, opts SealOptions) ([]byte, error) {
	if len(opts.Signer) != ed25519.PrivateKeySize {
		return nil, errors.New("sealed: an Ed25519 signing key is required")
	}
	signerPub := opts.Signer.Public().(ed25519.PublicKey)
	body.SignerKeyID = append([]byte(nil), signerPub...)
	context := ContextRequest
	if opts.Response {
		context = ContextResponse
	}
	header, ciphertext, err := ecies.SealField(opts.RecipientPub, encodeBody(body), CiphertextField, ecies.WrapOptions{
		KeyExchange: opts.KeyExchange,
		Context:     context,
	})
	if err != nil {
		return nil, err
	}
	reader := opts.Rand
	if reader == nil {
		reader = rand.Reader
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(reader, nonce); err != nil {
		return nil, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	env := Envelope{
		Response:    opts.Response,
		Header:      header,
		SessionID:   opts.SessionID,
		SenderKeyID: signerPub,
		Timestamp:   time.UnixMilli(now.UnixMilli()),
		Nonce:       nonce,
		Ciphertext:  ciphertext,
		Signer:      signerPub,
	}
	buf := encodeEnvelope(env)
	fbSig := ed25519.Sign(opts.Signer, buf)
	jsonSig := ed25519.Sign(opts.Signer, CanonicalJSON(env))
	root := rpc.GetSizePrefixedRootAsRPC(buf, 0)
	for i := range fbSig {
		root.MutateSIGNATURE(i, fbSig[i])
		root.MutateCANONICAL_JSON_SIGNATURE(i, jsonSig[i])
	}
	return buf, nil
}

// Open parses an envelope and verifies both signatures. It does not decrypt.
func Open(raw []byte) (Envelope, error) {
	if len(raw) < 8 || !rpc.SizePrefixedRPCBufferHasIdentifier(raw) {
		return Envelope{}, errors.New("sealed: not a size-prefixed $RPC buffer")
	}
	buf := append([]byte(nil), raw...)
	var root *rpc.RPC
	if err := safely(func() { root = rpc.GetSizePrefixedRootAsRPC(buf, 0) }); err != nil {
		return Envelope{}, err
	}
	var env Envelope
	var fbSig, jsonSig []byte
	if err := safely(func() {
		env, fbSig, jsonSig = readEnvelope(root)
	}); err != nil {
		return Envelope{}, err
	}
	if root.VERSION() != 1 {
		return Envelope{}, fmt.Errorf("sealed: unsupported RPC version %d", root.VERSION())
	}
	if string(root.SIGNATURE_TYPE()) != SignatureType {
		return Envelope{}, fmt.Errorf("sealed: unsupported signature type %q", root.SIGNATURE_TYPE())
	}
	if len(env.Signer) != ed25519.PublicKeySize || len(fbSig) != ed25519.SignatureSize || len(jsonSig) != ed25519.SignatureSize {
		return Envelope{}, errors.New("sealed: signer key or signature has the wrong length")
	}
	if len(env.Nonce) != NonceSize || len(env.Ciphertext) == 0 {
		return Envelope{}, errors.New("sealed: nonce or ciphertext missing")
	}
	for i := 0; i < ed25519.SignatureSize; i++ {
		root.MutateSIGNATURE(i, 0)
		root.MutateCANONICAL_JSON_SIGNATURE(i, 0)
	}
	if !ed25519.Verify(env.Signer, buf, fbSig) {
		return Envelope{}, errors.New("sealed: FlatBuffer signature does not verify")
	}
	if !ed25519.Verify(env.Signer, CanonicalJSON(env), jsonSig) {
		return Envelope{}, errors.New("sealed: canonical JSON signature does not verify")
	}
	env.Raw = raw
	return env, nil
}

// Decrypt opens the ciphertext with the recipient's private key. The body must
// name the same signer the envelope was signed by.
func (e Envelope) Decrypt(recipientPriv []byte) (Body, error) {
	context := ContextRequest
	if e.Response {
		context = ContextResponse
	}
	plaintext, err := ecies.OpenField(recipientPriv, e.Header, e.Ciphertext, CiphertextField, context)
	if err != nil {
		return Body{}, err
	}
	body, err := decodeBody(plaintext)
	if err != nil {
		return Body{}, err
	}
	if !bytes.Equal(body.SignerKeyID, e.Signer) {
		return Body{}, errors.New("sealed: the sealed body names a different signer than the envelope")
	}
	return body, nil
}

func encodeBody(body Body) []byte {
	b := flatbuffers.NewBuilder(256 + len(body.Body))
	vec := func(v []byte) flatbuffers.UOffsetT {
		if len(v) == 0 {
			return 0
		}
		return b.CreateByteVector(v)
	}
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	method, route, bodyBytes, fileID := str(body.Method), str(body.Route), vec(body.Body), str(body.BodyFileID)
	signer, reply, reqNonce, reqDigest := vec(body.SignerKeyID), vec(body.ReplyKey), vec(body.RequestNonce), vec(body.RequestDigest)
	rpc.RPCBodyStart(b)
	add := func(off flatbuffers.UOffsetT, fn func(*flatbuffers.Builder, flatbuffers.UOffsetT)) {
		if off != 0 {
			fn(b, off)
		}
	}
	add(method, rpc.RPCBodyAddMETHOD)
	add(route, rpc.RPCBodyAddROUTE)
	add(bodyBytes, rpc.RPCBodyAddBODY)
	add(fileID, rpc.RPCBodyAddBODY_FILE_ID)
	rpc.RPCBodyAddSTATUS(b, body.Status)
	add(signer, rpc.RPCBodyAddSIGNER_KEY_ID)
	add(reply, rpc.RPCBodyAddREPLY_KEY)
	add(reqNonce, rpc.RPCBodyAddREQUEST_NONCE)
	add(reqDigest, rpc.RPCBodyAddREQUEST_DIGEST)
	rpc.FinishSizePrefixedRPCBodyBuffer(b, rpc.RPCBodyEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func decodeBody(buf []byte) (body Body, err error) {
	if len(buf) < 8 {
		return Body{}, errors.New("sealed: body too short")
	}
	err = safely(func() {
		r := rpc.GetSizePrefixedRootAsRPCBody(buf, 0)
		body = Body{
			Method:        string(r.METHOD()),
			Route:         string(r.ROUTE()),
			Body:          append([]byte(nil), r.BODYBytes()...),
			BodyFileID:    string(r.BODY_FILE_ID()),
			Status:        r.STATUS(),
			SignerKeyID:   append([]byte(nil), r.SIGNER_KEY_IDBytes()...),
			ReplyKey:      append([]byte(nil), r.REPLY_KEYBytes()...),
			RequestNonce:  append([]byte(nil), r.REQUEST_NONCEBytes()...),
			RequestDigest: append([]byte(nil), r.REQUEST_DIGESTBytes()...),
		}
	})
	return body, err
}

// encodeEnvelope builds the size-prefixed RPC with zeroed signature vectors.
func encodeEnvelope(env Envelope) []byte {
	b := flatbuffers.NewBuilder(512 + len(env.Ciphertext))
	eph := b.CreateByteVector(env.Header.EphemeralPub)
	nonceStart := b.CreateByteVector(env.Header.NonceStart)
	var rid flatbuffers.UOffsetT
	if len(env.Header.RecipientKeyID) > 0 {
		rid = b.CreateByteVector(env.Header.RecipientKeyID)
	}
	ctx := b.CreateString(env.Header.Context)
	rpc.ENCStart(b)
	rpc.ENCAddVERSION(b, 1)
	rpc.ENCAddKEY_EXCHANGE(b, rpc.KeyExchange(env.Header.KeyExchange))
	rpc.ENCAddSYMMETRIC(b, rpc.SymmetricAlgoAES_256_CTR)
	rpc.ENCAddKEY_DERIVATION(b, rpc.KDFHKDF_SHA256)
	rpc.ENCAddEPHEMERAL_PUBLIC_KEY(b, eph)
	rpc.ENCAddNONCE_START(b, nonceStart)
	if rid != 0 {
		rpc.ENCAddRECIPIENT_KEY_ID(b, rid)
	}
	rpc.ENCAddCONTEXT(b, ctx)
	encOff := rpc.ENCEnd(b)

	var session flatbuffers.UOffsetT
	if env.SessionID != "" {
		session = b.CreateString(env.SessionID)
	}
	sender := b.CreateByteVector(env.SenderKeyID)
	nonce := b.CreateByteVector(env.Nonce)
	ciphertext := b.CreateByteVector(env.Ciphertext)
	signer := b.CreateByteVector(env.Signer)
	sigType := b.CreateString(SignatureType)
	sig := b.CreateByteVector(make([]byte, ed25519.SignatureSize))
	jsonSig := b.CreateByteVector(make([]byte, ed25519.SignatureSize))

	rpc.RPCStart(b)
	rpc.RPCAddVERSION(b, 1)
	if env.Response {
		rpc.RPCAddDIRECTION(b, directionResponse)
	} else {
		rpc.RPCAddDIRECTION(b, directionRequest)
	}
	rpc.RPCAddENCRYPTION(b, encOff)
	if session != 0 {
		rpc.RPCAddSESSION_ID(b, session)
	}
	rpc.RPCAddSENDER_KEY_ID(b, sender)
	rpc.RPCAddTIMESTAMP(b, uint64(env.Timestamp.UnixMilli()))
	rpc.RPCAddNONCE(b, nonce)
	rpc.RPCAddCIPHERTEXT(b, ciphertext)
	rpc.RPCAddSIGNER_PUBLIC_KEY(b, signer)
	rpc.RPCAddSIGNATURE_TYPE(b, sigType)
	rpc.RPCAddSIGNATURE(b, sig)
	rpc.RPCAddCANONICAL_JSON_SIGNATURE(b, jsonSig)
	rpc.FinishSizePrefixedRPCBuffer(b, rpc.RPCEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func readEnvelope(root *rpc.RPC) (Envelope, []byte, []byte) {
	env := Envelope{
		Response:    root.DIRECTION() == directionResponse,
		SessionID:   string(root.SESSION_ID()),
		SenderKeyID: append([]byte(nil), root.SENDER_KEY_IDBytes()...),
		Timestamp:   time.UnixMilli(int64(root.TIMESTAMP())),
		Nonce:       append([]byte(nil), root.NONCEBytes()...),
		Ciphertext:  append([]byte(nil), root.CIPHERTEXTBytes()...),
		Signer:      append(ed25519.PublicKey(nil), root.SIGNER_PUBLIC_KEYBytes()...),
	}
	if e := root.ENCRYPTION(nil); e != nil {
		env.Header = ecies.Header{
			KeyExchange:    ecies.KeyExchange(e.KEY_EXCHANGE()),
			EphemeralPub:   append([]byte(nil), e.EPHEMERAL_PUBLIC_KEYBytes()...),
			NonceStart:     append([]byte(nil), e.NONCE_STARTBytes()...),
			Context:        string(e.CONTEXT()),
			RecipientKeyID: append([]byte(nil), e.RECIPIENT_KEY_IDBytes()...),
		}
	}
	return env, append([]byte(nil), root.SIGNATUREBytes()...), append([]byte(nil), root.CANONICAL_JSON_SIGNATUREBytes()...)
}

// CanonicalJSON is the text CANONICAL_JSON_SIGNATURE signs: the RPC's fields in
// IDL order with IDL capitalization, both signature fields omitted, byte
// vectors as padded standard base64, the direction as its enum name, no
// whitespace and no HTML escaping (so every runtime's JSON writer agrees).
// Empty optional fields are omitted.
func CanonicalJSON(env Envelope) []byte {
	type encDoc struct {
		VERSION              int    `json:"VERSION"`
		KEY_EXCHANGE         string `json:"KEY_EXCHANGE"`
		SYMMETRIC            string `json:"SYMMETRIC"`
		KEY_DERIVATION       string `json:"KEY_DERIVATION"`
		EPHEMERAL_PUBLIC_KEY []byte `json:"EPHEMERAL_PUBLIC_KEY"`
		NONCE_START          []byte `json:"NONCE_START"`
		RECIPIENT_KEY_ID     []byte `json:"RECIPIENT_KEY_ID,omitempty"`
		CONTEXT              string `json:"CONTEXT"`
	}
	type rpcDoc struct {
		VERSION           int    `json:"VERSION"`
		DIRECTION         string `json:"DIRECTION"`
		ENCRYPTION        encDoc `json:"ENCRYPTION"`
		SESSION_ID        string `json:"SESSION_ID,omitempty"`
		SENDER_KEY_ID     []byte `json:"SENDER_KEY_ID"`
		TIMESTAMP         uint64 `json:"TIMESTAMP"`
		NONCE             []byte `json:"NONCE"`
		CIPHERTEXT        []byte `json:"CIPHERTEXT"`
		SIGNER_PUBLIC_KEY []byte `json:"SIGNER_PUBLIC_KEY"`
		SIGNATURE_TYPE    string `json:"SIGNATURE_TYPE"`
	}
	direction := "Request"
	if env.Response {
		direction = "Response"
	}
	doc := rpcDoc{
		VERSION:   1,
		DIRECTION: direction,
		ENCRYPTION: encDoc{
			VERSION:              1,
			KEY_EXCHANGE:         rpc.EnumNamesKeyExchange[rpc.KeyExchange(env.Header.KeyExchange)],
			SYMMETRIC:            "AES_256_CTR",
			KEY_DERIVATION:       "HKDF_SHA256",
			EPHEMERAL_PUBLIC_KEY: env.Header.EphemeralPub,
			NONCE_START:          env.Header.NonceStart,
			RECIPIENT_KEY_ID:     env.Header.RecipientKeyID,
			CONTEXT:              env.Header.Context,
		},
		SESSION_ID:        env.SessionID,
		SENDER_KEY_ID:     env.SenderKeyID,
		TIMESTAMP:         uint64(env.Timestamp.UnixMilli()),
		NONCE:             env.Nonce,
		CIPHERTEXT:        env.Ciphertext,
		SIGNER_PUBLIC_KEY: env.Signer,
		SIGNATURE_TYPE:    SignatureType,
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(doc)
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// safely turns a panic from reading a malformed buffer into an error.
func safely(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sealed: malformed buffer: %v", r)
		}
	}()
	fn()
	return nil
}

func rpcRoot(buf []byte) *rpc.RPC { return rpc.GetSizePrefixedRootAsRPC(buf, 0) }
