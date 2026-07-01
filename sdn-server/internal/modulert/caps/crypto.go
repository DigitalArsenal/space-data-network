package caps

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/hkdf"
)

// secp256k1PrivKeySize is the byte length of a secp256k1 private key.
const secp256k1PrivKeySize = 32

// NewCryptoCapFactory returns a CapFactory for all crypto_* capabilities.
// A single factory handles all crypto operations — register it for each capability:
//
//	reg.Register("crypto_hash", caps.NewCryptoCapFactory())
//	reg.Register("crypto_sign", caps.NewCryptoCapFactory())
//	reg.Register("crypto_verify", caps.NewCryptoCapFactory())
//	reg.Register("crypto_encrypt", caps.NewCryptoCapFactory())
//	reg.Register("crypto_decrypt", caps.NewCryptoCapFactory())
//	reg.Register("crypto_key_agreement", caps.NewCryptoCapFactory())
//	reg.Register("crypto_kdf", caps.NewCryptoCapFactory())
//
// Supported operations (all prefixed "crypto."):
//
//	crypto.hash          — {"algorithm":"sha256|sha512", "data":"base64"} → {"hash":"hex"}
//	crypto.sign          — {"algorithm":"ed25519|secp256k1", "key":"base64 seed/privkey", "data":"base64"} → {"signature":"base64"}
//	crypto.verify        — {"algorithm":"ed25519|secp256k1", "key":"base64 pubkey", "data":"base64", "signature":"base64"} → {"valid":bool}
//
// secp256k1 SDK operations (ECDSA over sha256(message), DER-encoded signatures — matches EPM SIGNATURE):
//
//	crypto.secp256k1.publicKeyFromPrivate — {"privateKey":"base64 32-byte"} → bytes (33-byte compressed pubkey)
//	crypto.secp256k1.sign                 — {"message":"base64", "privateKey":"base64 32-byte"} → bytes (DER ECDSA signature)
//	crypto.secp256k1.verify               — {"message":"base64", "signature":"base64 DER", "publicKey":"base64 33/65-byte"} → {"result":bool}
//	crypto.encrypt       — {"algorithm":"aes-256-gcm", "key":"base64", "nonce":"base64", "data":"base64"} → {"ciphertext":"base64","nonce":"base64"}
//	crypto.decrypt       — {"algorithm":"aes-256-gcm", "key":"base64", "nonce":"base64", "ciphertext":"base64"} → {"plaintext":"base64"}
//	crypto.key_agreement — {"algorithm":"x25519", "private_key":"base64", "public_key":"base64"} → {"shared_secret":"base64"}
//	crypto.kdf           — {"algorithm":"hkdf-sha256", "ikm":"base64", "salt":"base64", "info":"base64", "length":32} → {"key":"base64"}
//	crypto.generate_key  — {"algorithm":"ed25519|x25519"} → {"private_key":"base64","public_key":"base64"}
//	crypto.random        — {"length":32} → {"bytes":"base64"}
func NewCryptoCapFactory() modulert.CapFactory {
	return func(_ *modulert.Module) modulert.CapHandler {
		return cryptoCapHandle
	}
}

func cryptoCapHandle(operation string, payload []byte) ([]byte, error) {
	var p map[string]interface{}
	if len(payload) > 0 {
		json.Unmarshal(payload, &p) //nolint:errcheck
	}
	str := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	intVal := func(key string, def int) int {
		if p == nil {
			return def
		}
		if v, ok := p[key]; ok {
			switch vt := v.(type) {
			case float64:
				return int(vt)
			case int:
				return vt
			}
		}
		return def
	}
	bytes64 := func(key string) []byte {
		return decodeBase64Cap(str(key))
	}

	switch operation {
	case "crypto.ed25519.publicKeyFromSeed":
		seed := bytes64("seed")
		if len(seed) != ed25519.SeedSize {
			return errCapJSON(fmt.Sprintf("ed25519 seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))), nil
		}
		pubKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		return okCapRaw(pubKey), nil

	case "crypto.ed25519.sign":
		seed := bytes64("seed")
		message := bytes64("message")
		if len(seed) != ed25519.SeedSize {
			return errCapJSON(fmt.Sprintf("ed25519 seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))), nil
		}
		signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), message)
		return okCapRaw(signature), nil

	case "crypto.ed25519.verify":
		message := bytes64("message")
		signature := bytes64("signature")
		publicKey := bytes64("publicKey")
		if len(publicKey) != ed25519.PublicKeySize {
			return errCapJSON(fmt.Sprintf("ed25519 public key must be %d bytes, got %d", ed25519.PublicKeySize, len(publicKey))), nil
		}
		if len(signature) != ed25519.SignatureSize {
			return errCapJSON(fmt.Sprintf("ed25519 signature must be %d bytes, got %d", ed25519.SignatureSize, len(signature))), nil
		}
		return okCapJSON(ed25519.Verify(ed25519.PublicKey(publicKey), message, signature)), nil

	case "crypto.secp256k1.publicKeyFromPrivate":
		privateKey := bytes64("privateKey")
		if len(privateKey) != secp256k1PrivKeySize {
			return errCapJSON(fmt.Sprintf("secp256k1 private key must be %d bytes, got %d", secp256k1PrivKeySize, len(privateKey))), nil
		}
		priv := secp256k1.PrivKeyFromBytes(privateKey)
		return okCapRaw(priv.PubKey().SerializeCompressed()), nil

	case "crypto.secp256k1.sign":
		privateKey := bytes64("privateKey")
		message := bytes64("message")
		if len(privateKey) != secp256k1PrivKeySize {
			return errCapJSON(fmt.Sprintf("secp256k1 private key must be %d bytes, got %d", secp256k1PrivKeySize, len(privateKey))), nil
		}
		priv := secp256k1.PrivKeyFromBytes(privateKey)
		digest := sha256.Sum256(message)
		sig := ecdsa.Sign(priv, digest[:])
		return okCapRaw(sig.Serialize()), nil

	case "crypto.secp256k1.verify":
		message := bytes64("message")
		signature := bytes64("signature")
		publicKey := bytes64("publicKey")
		pub, err := secp256k1.ParsePubKey(publicKey)
		if err != nil {
			return errCapJSON("invalid secp256k1 public key: " + err.Error()), nil
		}
		sig, err := ecdsa.ParseDERSignature(signature)
		if err != nil {
			return errCapJSON("invalid secp256k1 DER signature: " + err.Error()), nil
		}
		digest := sha256.Sum256(message)
		return okCapJSON(sig.Verify(digest[:], pub)), nil

	case "crypto.hash":
		data := bytes64("data")
		algo := str("algorithm")
		switch algo {
		case "sha256", "":
			h := sha256.Sum256(data)
			return okCapJSON(map[string]interface{}{
				"hash":      encodeBase64Cap(h[:]),
				"algorithm": "sha256",
			}), nil
		case "sha512":
			h := sha512.Sum512(data)
			return okCapJSON(map[string]interface{}{
				"hash":      encodeBase64Cap(h[:]),
				"algorithm": "sha512",
			}), nil
		default:
			return errCapJSON(fmt.Sprintf("unsupported hash algorithm: %s", algo)), nil
		}

	case "crypto.sign":
		algo := str("algorithm")
		if algo == "" {
			algo = "ed25519"
		}
		switch algo {
		case "ed25519":
			seed := bytes64("key")
			data := bytes64("data")
			if len(seed) != ed25519.SeedSize {
				return errCapJSON(fmt.Sprintf("ed25519 seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))), nil
			}
			privKey := ed25519.NewKeyFromSeed(seed)
			sig := ed25519.Sign(privKey, data)
			return okCapJSON(map[string]interface{}{
				"signature": encodeBase64Cap(sig),
				"algorithm": "ed25519",
			}), nil
		case "secp256k1":
			priv := bytes64("key")
			data := bytes64("data")
			if len(priv) != secp256k1PrivKeySize {
				return errCapJSON(fmt.Sprintf("secp256k1 private key must be %d bytes, got %d", secp256k1PrivKeySize, len(priv))), nil
			}
			digest := sha256.Sum256(data)
			sig := ecdsa.Sign(secp256k1.PrivKeyFromBytes(priv), digest[:])
			return okCapJSON(map[string]interface{}{
				"signature": encodeBase64Cap(sig.Serialize()),
				"algorithm": "secp256k1",
			}), nil
		default:
			return errCapJSON(fmt.Sprintf("unsupported sign algorithm: %s", algo)), nil
		}

	case "crypto.verify":
		algo := str("algorithm")
		if algo == "" {
			algo = "ed25519"
		}
		switch algo {
		case "ed25519":
			pubKey := bytes64("key")
			data := bytes64("data")
			sig := bytes64("signature")
			if len(pubKey) != ed25519.PublicKeySize {
				return errCapJSON(fmt.Sprintf("ed25519 public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pubKey))), nil
			}
			valid := ed25519.Verify(ed25519.PublicKey(pubKey), data, sig)
			return okCapJSON(map[string]interface{}{"valid": valid}), nil
		case "secp256k1":
			pubKey := bytes64("key")
			data := bytes64("data")
			sigBytes := bytes64("signature")
			pub, err := secp256k1.ParsePubKey(pubKey)
			if err != nil {
				return errCapJSON("invalid secp256k1 public key: " + err.Error()), nil
			}
			sig, err := ecdsa.ParseDERSignature(sigBytes)
			if err != nil {
				return errCapJSON("invalid secp256k1 DER signature: " + err.Error()), nil
			}
			digest := sha256.Sum256(data)
			return okCapJSON(map[string]interface{}{"valid": sig.Verify(digest[:], pub)}), nil
		default:
			return errCapJSON(fmt.Sprintf("unsupported verify algorithm: %s", algo)), nil
		}

	case "crypto.encrypt":
		algo := str("algorithm")
		if algo == "" {
			algo = "aes-256-gcm"
		}
		if algo != "aes-256-gcm" {
			return errCapJSON(fmt.Sprintf("unsupported encrypt algorithm: %s", algo)), nil
		}
		key := bytes64("key")
		plaintext := bytes64("data")
		nonce := bytes64("nonce")

		block, err := aes.NewCipher(key)
		if err != nil {
			return errCapJSON("invalid key: " + err.Error()), nil
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return errCapJSON("gcm init failed: " + err.Error()), nil
		}
		if len(nonce) == 0 {
			nonce = make([]byte, gcm.NonceSize())
			if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
				return errCapJSON("nonce generation failed: " + err.Error()), nil
			}
		}
		ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
		return okCapJSON(map[string]interface{}{
			"ciphertext": encodeBase64Cap(ciphertext),
			"nonce":      encodeBase64Cap(nonce),
			"algorithm":  "aes-256-gcm",
		}), nil

	case "crypto.decrypt":
		algo := str("algorithm")
		if algo == "" {
			algo = "aes-256-gcm"
		}
		if algo != "aes-256-gcm" {
			return errCapJSON(fmt.Sprintf("unsupported decrypt algorithm: %s", algo)), nil
		}
		key := bytes64("key")
		nonce := bytes64("nonce")
		ciphertext := bytes64("ciphertext")

		block, err := aes.NewCipher(key)
		if err != nil {
			return errCapJSON("invalid key: " + err.Error()), nil
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return errCapJSON("gcm init failed: " + err.Error()), nil
		}
		plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return errCapJSON("decryption failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{
			"plaintext": encodeBase64Cap(plaintext),
			"algorithm": "aes-256-gcm",
		}), nil

	case "crypto.key_agreement":
		algo := str("algorithm")
		if algo == "" {
			algo = "x25519"
		}
		if algo != "x25519" {
			return errCapJSON(fmt.Sprintf("unsupported key agreement algorithm: %s", algo)), nil
		}
		privKeyBytes := bytes64("private_key")
		pubKeyBytes := bytes64("public_key")

		curve := ecdh.X25519()
		privKey, err := curve.NewPrivateKey(privKeyBytes)
		if err != nil {
			return errCapJSON("invalid private key: " + err.Error()), nil
		}
		pubKey, err := curve.NewPublicKey(pubKeyBytes)
		if err != nil {
			return errCapJSON("invalid public key: " + err.Error()), nil
		}
		shared, err := privKey.ECDH(pubKey)
		if err != nil {
			return errCapJSON("ECDH failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{
			"shared_secret": encodeBase64Cap(shared),
			"algorithm":     "x25519",
		}), nil

	case "crypto.kdf":
		algo := str("algorithm")
		if algo == "" {
			algo = "hkdf-sha256"
		}
		if algo != "hkdf-sha256" {
			return errCapJSON(fmt.Sprintf("unsupported kdf algorithm: %s", algo)), nil
		}
		ikm := bytes64("ikm")
		salt := bytes64("salt")
		info := bytes64("info")
		length := intVal("length", 32)
		if length <= 0 || length > 512 {
			return errCapJSON("length must be 1-512"), nil
		}

		r := hkdf.New(sha256.New, ikm, salt, info)
		key := make([]byte, length)
		if _, err := io.ReadFull(r, key); err != nil {
			return errCapJSON("kdf failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{
			"key":       encodeBase64Cap(key),
			"algorithm": "hkdf-sha256",
		}), nil

	case "crypto.generate_key":
		algo := str("algorithm")
		switch algo {
		case "ed25519", "":
			pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return errCapJSON("key generation failed: " + err.Error()), nil
			}
			seed := privKey.Seed()
			return okCapJSON(map[string]interface{}{
				"private_key": encodeBase64Cap(seed),
				"public_key":  encodeBase64Cap(pubKey),
				"algorithm":   "ed25519",
			}), nil
		case "x25519":
			curve := ecdh.X25519()
			privKey, err := curve.GenerateKey(rand.Reader)
			if err != nil {
				return errCapJSON("key generation failed: " + err.Error()), nil
			}
			return okCapJSON(map[string]interface{}{
				"private_key": encodeBase64Cap(privKey.Bytes()),
				"public_key":  encodeBase64Cap(privKey.PublicKey().Bytes()),
				"algorithm":   "x25519",
			}), nil
		default:
			return errCapJSON(fmt.Sprintf("unsupported key algorithm: %s", algo)), nil
		}

	case "crypto.random":
		length := intVal("length", 32)
		if length <= 0 || length > 8192 {
			return errCapJSON("length must be 1-8192"), nil
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return errCapJSON("random generation failed: " + err.Error()), nil
		}
		return okCapJSON(map[string]interface{}{
			"__type": "bytes",
			"base64": encodeBase64Cap(buf),
		}), nil

	default:
		return errCapJSON(fmt.Sprintf("unknown crypto operation: %s", operation)), nil
	}
}
