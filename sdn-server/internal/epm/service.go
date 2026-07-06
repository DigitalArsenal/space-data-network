// Package epm provides EPM (Entity Profile Message) lifecycle management.
// It creates, stores, and serves the node's identity card (EPM), which
// contains cryptographic keys, contact information, and network addresses.
package epm

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/mr-tron/base58"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/ripemd160"
	"golang.org/x/crypto/sha3"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var log = logging.Logger("sdn-epm")

const (
	identityAttestationVersion           = "1"
	identityChainBitcoin                 = "bitcoin"
	identityChainEthereum                = "ethereum"
	identityChainSolana                  = "solana"
	identityAttestationAlgorithmBitcoin  = "secp256k1-compact-bitcoin"
	identityAttestationAlgorithmEthereum = "secp256k1-compact-ethereum"
	identityAttestationAlgorithmSolana   = "ed25519"
)

const (
	identityAttestationBitcoinSigEncoding  = "compact"
	identityAttestationEthereumSigEncoding = "compact"
	identityAttestationSolanaSigEncoding   = "raw-ed25519"
)

const (
	hdXPubPurpose         = 44
	hdXPubCoinType        = 0
	hdXPubPublicVersion   = 0x0488b21e
	hdXPubSigningChain    = uint32(0)
	hdXPubEncryptionChain = uint32(1)
)

const bech32Alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Gen = []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

type xpubPublicNode struct {
	chainCode []byte
	publicKey []byte
}

type xpubDerivedPublicIdentityKeys struct {
	XPub                string
	SigningPublicKey    string
	EncryptionPublicKey string
	SigningKeyPath      string
	EncryptionKeyPath   string
}

// PublicIdentityKeys holds public identity keys derived from an xpub.
type PublicIdentityKeys struct {
	XPub                string
	SigningPublicKey    string
	EncryptionPublicKey string
	SigningKeyPath      string
	EncryptionKeyPath   string
}

type IdentityAttestationChainProof struct {
	Chain              string `json:"chain"`
	Address            string `json:"address"`
	KeyPath            string `json:"key_path"`
	PublicKeyHex       string `json:"public_key_hex"`
	Signature          string `json:"signature"`
	SignedPayloadHex   string `json:"signed_payload_hex"`
	SignatureAlgorithm string `json:"signature_algorithm"`
	SignatureEncoding  string `json:"signature_encoding"`
}

// IdentityAttestation is a chain attestation that binds the signing public key to
// chain-owned identities (Bitcoin, Ethereum, Solana).
type IdentityAttestation struct {
	Version           string `json:"version"`
	XPub              string `json:"xpub"`
	IdentityPubKeyHex string `json:"identity_pubkey_hex"`
	IdentityKeyPath   string `json:"identity_key_path"`
	SigningPubKeyHex  string `json:"signing_pubkey_hex"`
	SigningKeyPath    string `json:"signing_key_path"`
	IssuedAt          int64  `json:"issued_at"`

	BitcoinAddress  string `json:"bitcoin_address"`
	BitcoinKeyPath  string `json:"bitcoin_key_path"`
	EthereumAddress string `json:"ethereum_address"`
	EthereumKeyPath string `json:"ethereum_key_path"`
	SolanaAddress   string `json:"solana_address"`
	SolanaKeyPath   string `json:"solana_key_path"`

	ChainProofs []IdentityAttestationChainProof `json:"chain_proofs"`
}

type identityAttestationPayload struct {
	Version           string `json:"version"`
	XPub              string `json:"xpub"`
	IdentityPubKeyHex string `json:"identity_pubkey_hex"`
	IdentityKeyPath   string `json:"identity_key_path"`
	SigningPubKeyHex  string `json:"signing_pubkey_hex"`
	SigningKeyPath    string `json:"signing_key_path"`
	BitcoinAddress    string `json:"bitcoin_address"`
	BitcoinKeyPath    string `json:"bitcoin_key_path"`
	EthereumAddress   string `json:"ethereum_address"`
	EthereumKeyPath   string `json:"ethereum_key_path"`
	SolanaAddress     string `json:"solana_address"`
	SolanaKeyPath     string `json:"solana_key_path"`
	IssuedAt          int64  `json:"issued_at"`
}

// SignedPayload returns the bytes that were signed by each chain key.
func (a *IdentityAttestation) SignedPayload() ([]byte, error) {
	if a == nil {
		return nil, errors.New("attestation is nil")
	}

	payload := identityAttestationPayload{
		Version:           a.Version,
		XPub:              a.XPub,
		IdentityPubKeyHex: a.IdentityPubKeyHex,
		IdentityKeyPath:   a.IdentityKeyPath,
		SigningPubKeyHex:  a.SigningPubKeyHex,
		SigningKeyPath:    a.SigningKeyPath,
		BitcoinAddress:    a.BitcoinAddress,
		BitcoinKeyPath:    a.BitcoinKeyPath,
		EthereumAddress:   a.EthereumAddress,
		EthereumKeyPath:   a.EthereumKeyPath,
		SolanaAddress:     a.SolanaAddress,
		SolanaKeyPath:     a.SolanaKeyPath,
		IssuedAt:          a.IssuedAt,
	}

	return json.Marshal(payload)
}

// Verify validates all chain proofs against the signed payload.
func (a *IdentityAttestation) Verify() (bool, error) {
	if a == nil {
		return false, errors.New("attestation is nil")
	}
	if strings.TrimSpace(a.IdentityPubKeyHex) == "" || len(a.ChainProofs) == 0 {
		return false, errors.New("attestation missing required fields")
	}

	proofsByChain := map[string]IdentityAttestationChainProof{}
	for _, proof := range a.ChainProofs {
		chain := strings.ToLower(strings.TrimSpace(proof.Chain))
		if chain == "" {
			continue
		}
		proofsByChain[chain] = proof
	}

	payload, err := a.SignedPayload()
	if err != nil {
		return false, err
	}

	if proof, ok := proofsByChain[identityChainBitcoin]; ok {
		if err := verifyBitcoinChainProof(payload, proof, a.BitcoinAddress); err != nil {
			return false, err
		}
	} else {
		return false, errors.New("attestation missing bitcoin chain proof")
	}

	if proof, ok := proofsByChain[identityChainEthereum]; ok {
		if err := verifyEthereumChainProof(payload, proof, a.EthereumAddress); err != nil {
			return false, err
		}
	} else {
		return false, errors.New("attestation missing ethereum chain proof")
	}

	if proof, ok := proofsByChain[identityChainSolana]; ok {
		if err := verifySolanaChainProof(payload, proof, a.SolanaAddress); err != nil {
			return false, err
		}
	} else {
		return false, errors.New("attestation missing solana chain proof")
	}

	return true, nil
}

func verifyBitcoinChainProof(payload []byte, proof IdentityAttestationChainProof, expectedAddress string) error {
	if strings.TrimSpace(proof.SignedPayloadHex) != "" && proof.SignedPayloadHex != hex.EncodeToString(payload) {
		return errors.New("bitcoin proof signed payload does not match attested payload")
	}
	if strings.TrimSpace(proof.Address) == "" {
		return errors.New("missing bitcoin proof address")
	}
	if proof.SignatureAlgorithm != identityAttestationAlgorithmBitcoin {
		return fmt.Errorf("unexpected bitcoin signature algorithm %q", proof.SignatureAlgorithm)
	}
	if strings.TrimSpace(proof.Signature) == "" {
		return errors.New("missing bitcoin proof signature")
	}
	if strings.TrimSpace(proof.PublicKeyHex) == "" {
		return errors.New("missing bitcoin proof public key")
	}
	if strings.TrimSpace(proof.KeyPath) == "" {
		return errors.New("missing bitcoin key path")
	}
	if proof.SignatureEncoding != identityAttestationBitcoinSigEncoding {
		return fmt.Errorf("unsupported bitcoin signature encoding %q", proof.SignatureEncoding)
	}
	if expectedAddress != "" && normalizeChainAddress(proof.Address, identityChainBitcoin) != normalizeChainAddress(expectedAddress, identityChainBitcoin) {
		return errors.New("bitcoin proof address mismatch")
	}

	signature, err := hex.DecodeString(proof.Signature)
	if err != nil {
		return fmt.Errorf("invalid bitcoin signature: %w", err)
	}
	if len(signature) != 65 {
		return fmt.Errorf("invalid bitcoin signature length: %d", len(signature))
	}
	publicKey, err := hex.DecodeString(proof.PublicKeyHex)
	if err != nil {
		return fmt.Errorf("invalid bitcoin public key: %w", err)
	}
	if len(publicKey) != secp256k1.PubKeyBytesLenCompressed {
		return fmt.Errorf("invalid bitcoin public key length: %d", len(publicKey))
	}

	messageHash := bitcoinSignedMessageHash(payload)
	recoveredPubKey, _, err := ecdsa.RecoverCompact(signature, messageHash)
	if err != nil {
		return fmt.Errorf("invalid bitcoin signature: %w", err)
	}
	if !bytes.Equal(recoveredPubKey.SerializeCompressed(), publicKey) {
		return errors.New("bitcoin signature does not match proof public key")
	}

	recoveredAddress, err := bitcoinAddressFromCompressedPublicKey(recoveredPubKey.SerializeCompressed())
	if err != nil {
		return fmt.Errorf("bitcoin proof public key could not be converted to address: %w", err)
	}
	if normalizeChainAddress(recoveredAddress, identityChainBitcoin) != normalizeChainAddress(proof.Address, identityChainBitcoin) {
		return errors.New("bitcoin proof address does not match recovered key")
	}

	return nil
}

func verifyEthereumChainProof(payload []byte, proof IdentityAttestationChainProof, expectedAddress string) error {
	if strings.TrimSpace(proof.SignedPayloadHex) != "" && proof.SignedPayloadHex != hex.EncodeToString(payload) {
		return errors.New("ethereum proof signed payload does not match attested payload")
	}
	if strings.TrimSpace(proof.Address) == "" {
		return errors.New("missing ethereum proof address")
	}
	if proof.SignatureAlgorithm != identityAttestationAlgorithmEthereum {
		return fmt.Errorf("unexpected ethereum signature algorithm %q", proof.SignatureAlgorithm)
	}
	if strings.TrimSpace(proof.Signature) == "" {
		return errors.New("missing ethereum proof signature")
	}
	if strings.TrimSpace(proof.PublicKeyHex) == "" {
		return errors.New("missing ethereum proof public key")
	}
	if strings.TrimSpace(proof.KeyPath) == "" {
		return errors.New("missing ethereum key path")
	}
	if proof.SignatureEncoding != identityAttestationEthereumSigEncoding {
		return fmt.Errorf("unsupported ethereum signature encoding %q", proof.SignatureEncoding)
	}
	if expectedAddress != "" && normalizeChainAddress(proof.Address, identityChainEthereum) != normalizeChainAddress(expectedAddress, identityChainEthereum) {
		return errors.New("ethereum proof address mismatch")
	}

	signature, err := hex.DecodeString(proof.Signature)
	if err != nil {
		return fmt.Errorf("invalid ethereum signature: %w", err)
	}
	if len(signature) != 65 {
		return fmt.Errorf("invalid ethereum signature length: %d", len(signature))
	}
	publicKey, err := hex.DecodeString(proof.PublicKeyHex)
	if err != nil {
		return fmt.Errorf("invalid ethereum public key: %w", err)
	}
	if len(publicKey) != secp256k1.PubKeyBytesLenCompressed {
		return fmt.Errorf("invalid ethereum public key length: %d", len(publicKey))
	}

	messageHash := ethereumSignedMessageHash(payload)
	recoveredPubKey, _, err := ecdsa.RecoverCompact(signature, messageHash)
	if err != nil {
		return fmt.Errorf("invalid ethereum signature: %w", err)
	}
	if !bytes.Equal(recoveredPubKey.SerializeCompressed(), publicKey) {
		return errors.New("ethereum signature does not match proof public key")
	}

	recoveredAddress, err := ethereumAddressFromCompressedPublicKey(recoveredPubKey.SerializeCompressed())
	if err != nil {
		return fmt.Errorf("ethereum proof public key could not be converted to address: %w", err)
	}
	if normalizeChainAddress(recoveredAddress, identityChainEthereum) != normalizeChainAddress(proof.Address, identityChainEthereum) {
		return errors.New("ethereum proof address does not match recovered key")
	}

	return nil
}

func verifySolanaChainProof(payload []byte, proof IdentityAttestationChainProof, expectedAddress string) error {
	if strings.TrimSpace(proof.SignedPayloadHex) != "" && proof.SignedPayloadHex != hex.EncodeToString(payload) {
		return errors.New("solana proof signed payload does not match attested payload")
	}
	if strings.TrimSpace(proof.Address) == "" {
		return errors.New("missing solana proof address")
	}
	if proof.SignatureAlgorithm != identityAttestationAlgorithmSolana {
		return fmt.Errorf("unexpected solana signature algorithm %q", proof.SignatureAlgorithm)
	}
	if strings.TrimSpace(proof.Signature) == "" {
		return errors.New("missing solana proof signature")
	}
	if strings.TrimSpace(proof.PublicKeyHex) == "" {
		return errors.New("missing solana proof public key")
	}
	if strings.TrimSpace(proof.KeyPath) == "" {
		return errors.New("missing solana key path")
	}
	if proof.SignatureEncoding != identityAttestationSolanaSigEncoding {
		return fmt.Errorf("unsupported solana signature encoding %q", proof.SignatureEncoding)
	}
	if expectedAddress != "" && strings.TrimSpace(proof.Address) != strings.TrimSpace(expectedAddress) {
		return errors.New("solana proof address mismatch")
	}

	signature, err := hex.DecodeString(proof.Signature)
	if err != nil {
		return fmt.Errorf("invalid solana signature: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("invalid solana signature length: %d", len(signature))
	}
	publicKey, err := hex.DecodeString(proof.PublicKeyHex)
	if err != nil {
		return fmt.Errorf("invalid solana public key: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid solana public key length: %d", len(publicKey))
	}

	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("invalid solana signature")
	}
	if base58.Encode(publicKey) != proof.Address {
		return errors.New("solana proof public key does not match address")
	}

	return nil
}

func normalizeChainAddress(address, chain string) string {
	value := strings.TrimSpace(address)
	switch chain {
	case identityChainBitcoin:
		return strings.ToLower(value)
	case identityChainEthereum:
		return strings.TrimPrefix(strings.ToLower(value), "0x")
	default:
		return value
	}
}

func compactSizePrefix(length int) []byte {
	if length < 0 {
		return []byte{0}
	}
	if length < 253 {
		return []byte{byte(length)}
	}
	if length <= 0xffff {
		return []byte{253, byte(length), byte(length >> 8)}
	}
	if length <= 0xffffffff {
		return []byte{
			254,
			byte(length),
			byte(length >> 8),
			byte(length >> 16),
			byte(length >> 24),
		}
	}
	return []byte{
		255,
		byte(length),
		byte(length >> 8),
		byte(length >> 16),
		byte(length >> 24),
		byte(length >> 32),
		byte(length >> 40),
		byte(length >> 48),
		byte(length >> 56),
	}
}

func bitcoinSignedMessageHash(message []byte) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("\x18Bitcoin Signed Message:\n"))
	_, _ = h.Write(compactSizePrefix(len(message)))
	_, _ = h.Write(message)
	stage1 := h.Sum(nil)
	stage2 := sha256.Sum256(stage1)
	return stage2[:]
}

func ethereumSignedMessageHash(message []byte) []byte {
	prefix := "\x19Ethereum Signed Message:\n" + strconv.Itoa(len(message))
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(prefix))
	_, _ = h.Write(message)
	return h.Sum(nil)
}

func bitcoinAddressFromCompressedPublicKey(compressedPubKey []byte) (string, error) {
	pubKey, err := secp256k1.ParsePubKey(compressedPubKey)
	if err != nil {
		return "", fmt.Errorf("invalid compressed secp256k1 pubkey: %w", err)
	}
	compressed := pubKey.SerializeCompressed()
	h := sha256.Sum256(compressed)
	r := ripemd160.New()
	_, _ = r.Write(h[:])
	program := r.Sum(nil)
	return bech32SegwitEncode("bc", 0, program)
}

func ethereumAddressFromCompressedPublicKey(compressedPubKey []byte) (string, error) {
	pubKey, err := secp256k1.ParsePubKey(compressedPubKey)
	if err != nil {
		return "", fmt.Errorf("invalid compressed secp256k1 pubkey: %w", err)
	}
	uncompressed := pubKey.SerializeUncompressed() // 65-byte uncompressed
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(uncompressed[1:])
	hash := h.Sum(nil)
	return eip55Checksum(fmt.Sprintf("%x", hash[12:])), nil
}

func bech32SegwitEncode(hrp string, witnessVersion byte, program []byte) (string, error) {
	if len(program) < 2 || len(program) > 40 {
		return "", fmt.Errorf("invalid witness program length: %d", len(program))
	}
	conv, err := bech32ConvertBits(program, 8, 5, true)
	if err != nil {
		return "", err
	}
	data := append([]byte{witnessVersion}, conv...)
	return bech32Encode(hrp, data, 1), nil // 1 = bech32 for witness v0
}

func bech32Encode(hrp string, data []byte, spec uint32) string {
	values := append(data, 0, 0, 0, 0, 0, 0)
	polymod := bech32Polymod(bech32HRPExpand(hrp), values) ^ spec
	var checksum [6]byte
	for i := 0; i < 6; i++ {
		checksum[i] = byte((polymod >> uint(5*(5-i))) & 31)
	}
	combined := append(data, checksum[:]...)
	var result strings.Builder
	result.WriteString(hrp)
	result.WriteByte('1')
	for _, b := range combined {
		result.WriteByte(bech32Alphabet[b])
	}
	return result.String()
}

func bech32HRPExpand(hrp string) []byte {
	ret := make([]byte, 0, len(hrp)*2+1)
	for _, c := range hrp {
		ret = append(ret, byte(c>>5))
	}
	ret = append(ret, 0)
	for _, c := range hrp {
		ret = append(ret, byte(c&31))
	}
	return ret
}

func bech32Polymod(hrp, values []byte) uint32 {
	chk := uint32(1)
	for _, v := range hrp {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= bech32Gen[i]
			}
		}
	}
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= bech32Gen[i]
			}
		}
	}
	return chk
}

func bech32ConvertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := uint32(0)
	bits := uint(0)
	maxv := uint32((1 << toBits) - 1)
	var ret []byte

	for _, value := range data {
		acc = (acc << fromBits) | uint32(value)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			ret = append(ret, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || (acc<<(toBits-bits))&maxv != 0 {
		return nil, fmt.Errorf("invalid bech32 bit group padding")
	}
	return ret, nil
}

func eip55Checksum(addrHex string) string {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(addrHex))
	hash := h.Sum(nil)

	var result strings.Builder
	result.WriteString("0x")
	for i, c := range addrHex {
		if c >= '0' && c <= '9' {
			result.WriteByte(byte(c))
			continue
		}
		if i%2 == 0 {
			nibble := hash[i/2] >> 4
			if nibble >= 8 {
				result.WriteByte(byte(c - 32))
			} else {
				result.WriteByte(byte(c))
			}
		} else {
			nibble := hash[i/2] & 0x0f
			if nibble >= 8 {
				result.WriteByte(byte(c - 32))
			} else {
				result.WriteByte(byte(c))
			}
		}
	}
	return result.String()
}

// Service manages the node's EPM (Entity Profile Message).
type Service struct {
	identity *wasm.DerivedIdentity
	registry *peers.Registry
	peerID   peer.ID
	xpub     string
	dataDir  string

	profileStore ProfileStore
	epmBytes     []byte // current node EPM (size-prefixed FlatBuffer)
	profile      *Profile
	// runtimeAddresses are non-profile addresses injected at runtime
	// (for example deterministic onion URLs).
	runtimeAddresses    []string
	runtimeSigningKey   ed25519.PrivateKey
	runtimeSigningPath  string
	identityAttestation *IdentityAttestation

	mu sync.RWMutex
}

// ProfileStore persists the editable node EPM profile in an encrypted
// FlatSQL-backed store while keeping public EPM bytes available as the EPM
// source of truth.
type ProfileStore interface {
	LoadLocalEPM(peerID string) ([]byte, error)
	SaveLocalEPM(peerID string, epmBytes []byte) error
}

// NewService creates a new EPM service.
// identity may be nil if using random keys (EPM will lack HD wallet fields).
func NewService(identity *wasm.DerivedIdentity, registry *peers.Registry, peerID peer.ID, xpub, dataDir string) *Service {
	return &Service{
		identity: identity,
		registry: registry,
		peerID:   peerID,
		xpub:     xpub,
		dataDir:  dataDir,
	}
}

// SetProfileStore configures encrypted FlatSQL-backed persistence for editable
// node EPM profile fields. When unset, the legacy profile JSON file is used.
func (s *Service) SetProfileStore(store ProfileStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profileStore = store
}

// Init loads or creates the node's EPM profile and builds the initial EPM.
func (s *Service) Init() error {
	// Load existing profile or create default
	profile, err := s.loadProfile()
	if err != nil {
		log.Infof("No existing EPM profile, creating default")
		profile = s.defaultProfile()
		if s.profileStore == nil {
			if saveErr := s.saveProfile(profile, nil); saveErr != nil {
				log.Warnf("Failed to persist default EPM profile: %v", saveErr)
			}
		}
	}
	s.profile = profile

	// Build EPM from profile + identity
	if err := s.rebuildEPM(); err != nil {
		return fmt.Errorf("failed to build node EPM: %w", err)
	}
	if err := s.persistCurrentProfile(); err != nil {
		log.Warnf("Failed to persist initial EPM profile: %v", err)
	}

	log.Infof("EPM service initialized (PeerID=%s, hasIdentity=%v)", s.peerID, s.identity != nil)
	return nil
}

func (s *Service) loadProfile() (*Profile, error) {
	if s.profileStore != nil {
		raw, err := s.profileStore.LoadLocalEPM(s.peerID.String())
		if err == nil {
			profile, err := profileFromEPMBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("decode encrypted EPM FlatBuffer profile: %w", err)
			}
			return profile, nil
		}
		log.Debugf("No encrypted FlatSQL EPM profile loaded for %s: %v", s.peerID, err)
	}
	return LoadProfile(s.dataDir)
}

func profileFromEPMBytes(epmBytes []byte) (profile *Profile, err error) {
	if len(epmBytes) == 0 {
		return nil, errors.New("empty EPM FlatBuffer")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid EPM FlatBuffer: %v", r)
		}
	}()

	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	p := &Profile{}
	if v := epm.DN(); v != nil {
		p.DN = string(v)
	}
	if v := epm.LEGAL_NAME(); v != nil {
		p.LegalName = string(v)
	}
	if v := epm.FAMILY_NAME(); v != nil {
		p.FamilyName = string(v)
	}
	if v := epm.GIVEN_NAME(); v != nil {
		p.GivenName = string(v)
	}
	if v := epm.ADDITIONAL_NAME(); v != nil {
		p.AdditionalName = string(v)
	}
	if v := epm.HONORIFIC_PREFIX(); v != nil {
		p.HonorificPrefix = string(v)
	}
	if v := epm.HONORIFIC_SUFFIX(); v != nil {
		p.HonorificSuffix = string(v)
	}
	if v := epm.JOB_TITLE(); v != nil {
		p.JobTitle = string(v)
	}
	if v := epm.OCCUPATION(); v != nil {
		p.Occupation = string(v)
	}
	if v := epm.EMAIL(); v != nil {
		p.Email = string(v)
	}
	if v := epm.TELEPHONE(); v != nil {
		p.Telephone = string(v)
	}

	addr := new(EPM.Address)
	if epm.ADDRESS(addr) != nil {
		p.Address = &Address{}
		if v := addr.COUNTRY(); v != nil {
			p.Address.Country = string(v)
		}
		if v := addr.REGION(); v != nil {
			p.Address.Region = string(v)
		}
		if v := addr.LOCALITY(); v != nil {
			p.Address.Locality = string(v)
		}
		if v := addr.POSTAL_CODE(); v != nil {
			p.Address.PostalCode = string(v)
		}
		if v := addr.STREET(); v != nil {
			p.Address.Street = string(v)
		}
		if v := addr.POST_OFFICE_BOX_NUMBER(); v != nil {
			p.Address.POBox = string(v)
		}
		if p.Address.IsEmpty() {
			p.Address = nil
		}
	}

	if n := epm.ALTERNATE_NAMESLength(); n > 0 {
		p.AlternateNames = make([]string, 0, n)
		for i := 0; i < n; i++ {
			if v := epm.ALTERNATE_NAMES(i); v != nil {
				p.AlternateNames = append(p.AlternateNames, string(v))
			}
		}
	}
	return p, nil
}

// ProfileFromEPMBytes extracts the editable profile fields from size-prefixed
// EPM FlatBuffer bytes.
func ProfileFromEPMBytes(epmBytes []byte) (*Profile, error) {
	return profileFromEPMBytes(epmBytes)
}

func (s *Service) saveProfile(profile *Profile, epmBytes []byte) error {
	if profile == nil {
		profile = &Profile{}
	}
	if s.profileStore != nil {
		return s.profileStore.SaveLocalEPM(s.peerID.String(), epmBytes)
	}
	return SaveProfile(s.dataDir, profile)
}

func (s *Service) persistCurrentProfile() error {
	profile := s.GetNodeProfile()
	if profile == nil {
		return nil
	}
	return s.saveProfile(profile, s.GetNodeEPM())
}

// GetNodeEPM returns the current EPM as a size-prefixed FlatBuffer.
func (s *Service) GetNodeEPM() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return nil
	}
	out := make([]byte, len(s.epmBytes))
	copy(out, s.epmBytes)
	return out
}

// GetNodeEPMCID returns the deterministic CID for the current node EPM.
func (s *Service) GetNodeEPMCID() (string, error) {
	return ComputeEPMCID(s.GetNodeEPM())
}

// GetNodeVCard returns the node's EPM as an iPhone-compatible vCard string.
func (s *Service) GetNodeVCard() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return "", fmt.Errorf("no EPM available")
	}
	vcardStr, err := vcard.EPMToVCard(s.epmBytes)
	if err != nil {
		return "", err
	}
	return s.decorateNodeVCardLocked(vcardStr), nil
}

// GetNodeQR returns a QR code PNG of the node's vCard.
func (s *Service) GetNodeQR(size int) ([]byte, error) {
	if size > 0 && size < 4096 {
		size = 4096
	}
	vcardStr, err := s.GetNodeQRVCard()
	if err != nil {
		return nil, err
	}
	return vcard.VCardToQR(vcardStr, size)
}

// GetNodeQRVCard returns a compact vCard intended for QR scanning.
// The downloadable vCard keeps the embedded binary EPM payload; QR codes use
// the CID and signature fields instead because embedding the full payload makes
// the QR too dense for phones and can exceed QR encoder capacity.
func (s *Service) GetNodeQRVCard() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return "", fmt.Errorf("no EPM available")
	}

	epmRecord := EPM.GetSizePrefixedRootAsEPM(s.epmBytes, 0)
	lines := []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"PRODID;VALUE=TEXT:-//Apple Inc.//iPhone OS 15.1.1//EN",
		"N:;;;;",
		"FN:" + escapeNodeQRVCardValue(nodeQRDisplayName(epmRecord, s.peerID)),
	}
	addNodeQRVCardLine(&lines, "ORG", stringFromBytes(epmRecord.LEGAL_NAME()))
	addNodeQRVCardLine(&lines, "EMAIL", stringFromBytes(epmRecord.EMAIL()))
	addNodeQRVCardLine(&lines, "TEL", stringFromBytes(epmRecord.TELEPHONE()))
	addNodeQRVCardLine(&lines, "TITLE", stringFromBytes(epmRecord.JOB_TITLE()))
	addNodeQRVCardLine(&lines, "ROLE", stringFromBytes(epmRecord.OCCUPATION()))
	addNodeQRVCardLine(&lines, "UID", s.peerID.String())
	addNodeQRVCardLine(&lines, "X-SDN-DIRECTORY-KIND", "node")
	addNodeQRVCardLine(&lines, "X-SDN-PEER-ID", s.peerID.String())
	if cid, err := ComputeEPMCID(s.epmBytes); err == nil {
		addNodeQRVCardLine(&lines, "X-SDN-EPM-CID", cid)
	}
	if signature := epmRecord.SIGNATURE(); signature != nil {
		addNodeQRVCardLine(&lines, "X-SDN-EPM-SIGNATURE", string(signature))
	}
	if ts := epmRecord.SIGNATURE_TIMESTAMP(); ts != 0 {
		addNodeQRVCardLine(&lines, "X-SDN-EPM-SIGNATURE-TIMESTAMP", strconv.FormatInt(ts, 10))
	}
	if s.identity != nil && s.identity.Addresses != nil && s.identity.Addresses.Bitcoin != nil {
		addNodeQRVCardLine(&lines, "X-SDN-BITCOIN-ADDRESS", s.identity.Addresses.Bitcoin.Address)
	}
	lines = append(lines, vcard.AppleIdentityLinesFromEPM(epmRecord, nil, false)...)
	lines = append(lines, nodeIdentityAddressEmailAliasLines(s.identity)...)
	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

func (s *Service) decorateNodeVCardLocked(vcardStr string) string {
	lines := []string{
		"X-SDN-DIRECTORY-KIND:node",
		"X-SDN-PEER-ID:" + s.peerID.String(),
	}
	if cid, err := ComputeEPMCID(s.epmBytes); err == nil && strings.TrimSpace(cid) != "" {
		lines = append(lines, "X-SDN-EPM-CID:"+cid)
	}
	if s.identity != nil && s.identity.Addresses != nil && s.identity.Addresses.Bitcoin != nil {
		if address := strings.TrimSpace(s.identity.Addresses.Bitcoin.Address); address != "" {
			lines = append(lines, "X-SDN-BITCOIN-ADDRESS:"+address)
		}
	}
	lines = append(lines, nodeIdentityAddressEmailAliasLines(s.identity)...)
	if s.profile != nil {
		if photoLine := vcardPhotoLine(s.profile.PhotoDataURL); photoLine != "" {
			lines = append(lines, photoLine)
		}
	}
	return insertVCardLines(vcardStr, lines)
}

func insertVCardLines(vcardStr string, lines []string) string {
	if len(lines) == 0 {
		return vcardStr
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(vcardStr), "\r\n", "\n")
	insert := strings.Join(lines, "\r\n")
	if strings.Contains(normalized, "\nEND:VCARD") {
		return strings.Replace(normalized, "\nEND:VCARD", "\r\n"+insert+"\r\nEND:VCARD", 1) + "\r\n"
	}
	return strings.TrimRight(normalized, "\n") + "\r\n" + insert + "\r\nEND:VCARD\r\n"
}

func nodeQRDisplayName(epmRecord *EPM.EPM, pid peer.ID) string {
	if epmRecord != nil {
		if dn := epmRecord.DN(); dn != nil && strings.TrimSpace(string(dn)) != "" {
			return string(dn)
		}
	}
	return "SDN Node " + pid.ShortString()
}

func addNodeQRVCardLine(lines *[]string, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	*lines = append(*lines, key+":"+escapeNodeQRVCardValue(value))
}

func stringFromBytes(value []byte) string {
	if value == nil {
		return ""
	}
	return string(value)
}

func escapeNodeQRVCardValue(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
		",", "\\,",
		";", "\\;",
	)
	return replacer.Replace(value)
}

func nodeIdentityAddressEmailAliasLines(identity *wasm.DerivedIdentity) []string {
	if identity == nil || identity.Addresses == nil {
		return nil
	}
	lines := make([]string, 0, 3)
	if identity.Addresses.Bitcoin != nil {
		if address := strings.TrimSpace(identity.Addresses.Bitcoin.Address); address != "" {
			lines = append(lines, "EMAIL;type=INTERNET;type=bitcoin:"+address+"@bitcoin.spacedatanetwork.org")
		}
	}
	if identity.Addresses.Ethereum != nil {
		if address := strings.TrimSpace(identity.Addresses.Ethereum.Address); address != "" {
			lines = append(lines, "EMAIL;type=INTERNET;type=ethereum:"+address+"@ethereum.spacedatanetwork.org")
		}
	}
	if identity.Addresses.Solana != nil {
		if address := strings.TrimSpace(identity.Addresses.Solana.Address); address != "" {
			lines = append(lines, "EMAIL;type=INTERNET;type=solana:"+address+"@solana.spacedatanetwork.org")
		}
	}
	return lines
}

func vcardPhotoLine(dataURL string) string {
	raw := strings.TrimSpace(dataURL)
	if raw == "" {
		return ""
	}
	mediaType := "image/png"
	payload := raw
	if strings.HasPrefix(raw, "data:") {
		header, body, ok := strings.Cut(raw, ",")
		if !ok || strings.TrimSpace(body) == "" {
			return ""
		}
		payload = body
		if media, _, ok := strings.Cut(strings.TrimPrefix(header, "data:"), ";"); ok && strings.TrimSpace(media) != "" {
			mediaType = strings.TrimSpace(media)
		}
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return ""
	}
	return "PHOTO;ENCODING=b;MEDIATYPE=" + mediaType + ":" + payload
}

// GetNodeProfile returns the current editable profile.
func (s *Service) GetNodeProfile() *Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.profile == nil {
		return nil
	}
	cp := *s.profile
	return &cp
}

// SetRuntimeAddresses updates non-profile runtime addresses and rebuilds EPM.
func (s *Service) SetRuntimeAddresses(addresses []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleaned := normalizeRuntimeAddresses(addresses)
	if stringSlicesEqual(cleaned, s.runtimeAddresses) {
		return nil
	}

	s.runtimeAddresses = cleaned
	if err := s.rebuildEPMLocked(); err != nil {
		return fmt.Errorf("failed to rebuild EPM with runtime addresses: %w", err)
	}
	return nil
}

// SetRuntimeSigningKey updates a runtime Ed25519 signing key for node profiles
// that do not have an HD wallet identity. This keeps local-server publication
// keys discoverable and makes the served EPM verifiable by peers.
func (s *Service) SetRuntimeSigningKey(privateKey ed25519.PrivateKey, keyAddress string) error {
	if s == nil {
		return fmt.Errorf("EPM service is nil")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("runtime signing key length = %d, want %d", len(privateKey), ed25519.PrivateKeySize)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.runtimeSigningKey = append(ed25519.PrivateKey(nil), privateKey...)
	s.runtimeSigningPath = strings.TrimSpace(keyAddress)
	if s.runtimeSigningPath == "" {
		s.runtimeSigningPath = "sdn/runtime-signing"
	}
	if err := s.rebuildEPMLocked(); err != nil {
		return fmt.Errorf("failed to rebuild EPM with runtime signing key: %w", err)
	}
	return nil
}

// GetNodeEPMJSON returns the EPM as a JSON-friendly structure.
func (s *Service) GetNodeEPMJSON() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return nil
	}

	epm := EPM.GetSizePrefixedRootAsEPM(s.epmBytes, 0)
	result := make(map[string]interface{})

	if dn := epm.DN(); dn != nil {
		result["dn"] = string(dn)
	}
	if ln := epm.LEGAL_NAME(); ln != nil {
		result["legal_name"] = string(ln)
	}
	if fn := epm.FAMILY_NAME(); fn != nil {
		result["family_name"] = string(fn)
	}
	if gn := epm.GIVEN_NAME(); gn != nil {
		result["given_name"] = string(gn)
	}
	if an := epm.ADDITIONAL_NAME(); an != nil {
		result["additional_name"] = string(an)
	}
	if hp := epm.HONORIFIC_PREFIX(); hp != nil {
		result["honorific_prefix"] = string(hp)
	}
	if hs := epm.HONORIFIC_SUFFIX(); hs != nil {
		result["honorific_suffix"] = string(hs)
	}
	if jt := epm.JOB_TITLE(); jt != nil {
		result["job_title"] = string(jt)
	}
	if oc := epm.OCCUPATION(); oc != nil {
		result["occupation"] = string(oc)
	}
	if em := epm.EMAIL(); em != nil {
		result["email"] = string(em)
	}
	if tel := epm.TELEPHONE(); tel != nil {
		result["telephone"] = string(tel)
	}
	if s.profile != nil && strings.TrimSpace(s.profile.PhotoDataURL) != "" {
		result["photo_data_url"] = s.profile.PhotoDataURL
	}

	// Address
	addr := new(EPM.Address)
	if epm.ADDRESS(addr) != nil {
		addrMap := make(map[string]string)
		if v := addr.COUNTRY(); v != nil {
			addrMap["country"] = string(v)
		}
		if v := addr.REGION(); v != nil {
			addrMap["region"] = string(v)
		}
		if v := addr.LOCALITY(); v != nil {
			addrMap["locality"] = string(v)
		}
		if v := addr.POSTAL_CODE(); v != nil {
			addrMap["postal_code"] = string(v)
		}
		if v := addr.STREET(); v != nil {
			addrMap["street"] = string(v)
		}
		if v := addr.POST_OFFICE_BOX_NUMBER(); v != nil {
			addrMap["po_box"] = string(v)
		}
		if len(addrMap) > 0 {
			result["address"] = addrMap
		}
	}

	// Alternate names
	if n := epm.ALTERNATE_NAMESLength(); n > 0 {
		names := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if v := epm.ALTERNATE_NAMES(i); v != nil {
				names = append(names, string(v))
			}
		}
		result["alternate_names"] = names
	}

	// Keys
	key := new(EPM.CryptoKey)
	if n := epm.KEYSLength(); n > 0 {
		keys := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			if epm.KEYS(key, i) {
				k := make(map[string]interface{})
				if v := key.PUBLIC_KEY(); v != nil {
					k["public_key"] = string(v)
				}
				if v := key.XPUB(); v != nil {
					k["xpub"] = string(v)
				}
				if v := key.KEY_ADDRESS(); v != nil {
					k["key_address"] = string(v)
				}
				if v := key.ADDRESS_TYPE(); v != nil {
					k["address_type"] = string(v)
				}
				switch key.KEY_TYPE() {
				case EPM.KeyTypeSigning:
					k["key_type"] = "signing"
				case EPM.KeyTypeEncryption:
					k["key_type"] = "encryption"
				}
				keys = append(keys, k)
			}
		}
		result["keys"] = keys
	}

	// Multiformat addresses
	if n := epm.MULTIFORMAT_ADDRESSLength(); n > 0 {
		addrs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if v := epm.MULTIFORMAT_ADDRESS(i); v != nil {
				addrs = append(addrs, string(v))
			}
		}
		result["multiformat_address"] = addrs
	}

	// Signature and timestamp
	if v := epm.SIGNATURE(); v != nil {
		result["signature"] = string(v)
	}
	if ts := epm.SIGNATURE_TIMESTAMP(); ts != 0 {
		result["signature_timestamp"] = ts
	}

	// Chain proofs
	chainProof := new(EPM.ChainProof)
	if n := epm.CHAIN_PROOFSLength(); n > 0 {
		proofs := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			if epm.CHAIN_PROOFS(chainProof, i) {
				p := make(map[string]interface{})
				if v := chainProof.CHAIN(); v != nil {
					p["chain"] = string(v)
				}
				if v := chainProof.ADDRESS(); v != nil {
					p["address"] = string(v)
				}
				if v := chainProof.PUBLIC_KEY(); v != nil {
					p["public_key"] = string(v)
				}
				if v := chainProof.KEY_PATH(); v != nil {
					p["key_path"] = string(v)
				}
				if v := chainProof.SIGNATURE(); v != nil {
					p["signature"] = string(v)
				}
				if v := chainProof.SIGNED_PAYLOAD(); v != nil {
					p["signed_payload"] = string(v)
				}
				if v := chainProof.ALGORITHM(); v != nil {
					p["algorithm"] = string(v)
				}
				if v := chainProof.ENCODING(); v != nil {
					p["encoding"] = string(v)
				}
				proofs = append(proofs, p)
			}
		}
		result["chain_proofs"] = proofs
	}

	s.overlayRuntimeIdentityFields(result)
	result["directory_kind"] = "node"
	result["entity_type"] = "node"
	result["peer_id"] = s.peerID.String()
	result["version"] = versioninfo.AgentVersion
	result["agent_version"] = versioninfo.AgentVersion
	result["suite_version"] = versioninfo.SuiteVersion
	result["standards_version"] = versioninfo.SpaceDataStandardsVersion
	result["advertisement_flag"] = versioninfo.CurrentAdvertisementFlag

	return result
}

// DirectoryRecordJSON returns the node EPM in a directory-friendly JSON shape.
func (s *Service) DirectoryRecordJSON() map[string]any {
	return s.GetNodeEPMJSON()
}

func (s *Service) overlayRuntimeIdentityFields(result map[string]interface{}) {
	if result == nil || s.identity == nil {
		return
	}

	info := s.identity.Info()
	derived, hasDerived := derivePublicIdentityKeysFromXPub(s.xpub, info.Account)

	if strings.TrimSpace(info.IdentityPubKeyHex) != "" && result["identity_pubkey_hex"] == nil {
		result["identity_pubkey_hex"] = info.IdentityPubKeyHex
	}
	if hasDerived && result["signing_pubkey_hex"] == nil {
		result["signing_pubkey_hex"] = derived.SigningPublicKey
	} else if strings.TrimSpace(info.SigningPubKeyHex) != "" && result["signing_pubkey_hex"] == nil {
		result["signing_pubkey_hex"] = info.SigningPubKeyHex
	}
	if hasDerived && result["encryption_pubkey_hex"] == nil {
		result["encryption_pubkey_hex"] = derived.EncryptionPublicKey
	} else if strings.TrimSpace(info.EncryptionPubHex) != "" && result["encryption_pubkey_hex"] == nil {
		result["encryption_pubkey_hex"] = info.EncryptionPubHex
	}
	if strings.TrimSpace(info.IdentityKeyPath) != "" && result["identity_key_path"] == nil {
		result["identity_key_path"] = info.IdentityKeyPath
	}
	if hasDerived && result["signing_key_path"] == nil {
		result["signing_key_path"] = derived.SigningKeyPath
	} else if strings.TrimSpace(info.SigningKeyPath) != "" && result["signing_key_path"] == nil {
		result["signing_key_path"] = info.SigningKeyPath
	}
	if hasDerived && result["encryption_key_path"] == nil {
		result["encryption_key_path"] = derived.EncryptionKeyPath
	} else if strings.TrimSpace(info.EncryptionKeyPath) != "" && result["encryption_key_path"] == nil {
		result["encryption_key_path"] = info.EncryptionKeyPath
	}
	if strings.TrimSpace(s.xpub) != "" && result["xpub"] == nil {
		result["xpub"] = s.xpub
	}
	if info.Addresses != nil {
		if info.Addresses.Bitcoin != nil {
			if strings.TrimSpace(info.Addresses.Bitcoin.Address) != "" && result["bitcoin_address"] == nil {
				result["bitcoin_address"] = info.Addresses.Bitcoin.Address
			}
			if strings.TrimSpace(info.Addresses.Bitcoin.Path) != "" && result["bitcoin_key_path"] == nil {
				result["bitcoin_key_path"] = info.Addresses.Bitcoin.Path
			}
		}
		if info.Addresses.Ethereum != nil {
			if strings.TrimSpace(info.Addresses.Ethereum.Address) != "" && result["ethereum_address"] == nil {
				result["ethereum_address"] = info.Addresses.Ethereum.Address
			}
			if strings.TrimSpace(info.Addresses.Ethereum.Path) != "" && result["ethereum_key_path"] == nil {
				result["ethereum_key_path"] = info.Addresses.Ethereum.Path
			}
		}
		if info.Addresses.Solana != nil {
			if strings.TrimSpace(info.Addresses.Solana.Address) != "" && result["solana_address"] == nil {
				result["solana_address"] = info.Addresses.Solana.Address
			}
			if strings.TrimSpace(info.Addresses.Solana.Path) != "" && result["solana_key_path"] == nil {
				result["solana_key_path"] = info.Addresses.Solana.Path
			}
		}
	}

	if keys := runtimeIdentityKeys(info, s.xpub); len(keys) > 0 && runtimeKeysMissing(result["keys"]) {
		result["keys"] = keys
	}
}

func derivePublicIdentityKeysFromXPub(xpub string, account uint32) (*xpubDerivedPublicIdentityKeys, bool) {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return nil, false
	}
	accountNode, err := parseXPub(trimmed)
	if err != nil {
		return nil, false
	}
	signingChange, err := deriveXPubPublicChild(accountNode, hdXPubSigningChain)
	if err != nil {
		return nil, false
	}
	signingKey, err := deriveXPubPublicChild(signingChange, 0)
	if err != nil {
		return nil, false
	}
	encryptionChange, err := deriveXPubPublicChild(accountNode, hdXPubEncryptionChain)
	if err != nil {
		return nil, false
	}
	encryptionKey, err := deriveXPubPublicChild(encryptionChange, 0)
	if err != nil {
		return nil, false
	}
	return &xpubDerivedPublicIdentityKeys{
		XPub:                trimmed,
		SigningPublicKey:    hex.EncodeToString(signingKey.publicKey),
		EncryptionPublicKey: hex.EncodeToString(encryptionKey.publicKey),
		SigningKeyPath:      xpubSigningKeyPath(account),
		EncryptionKeyPath:   xpubEncryptionKeyPath(account),
	}, true
}

// PublicIdentityKeysFromXPub derives the public signing/encryption identity
// keys that EPM service rebuilds from an xpub.
func PublicIdentityKeysFromXPub(xpub string, account uint32) (PublicIdentityKeys, bool) {
	derived, ok := derivePublicIdentityKeysFromXPub(xpub, account)
	if !ok {
		return PublicIdentityKeys{}, false
	}
	return PublicIdentityKeys{
		XPub:                derived.XPub,
		SigningPublicKey:    derived.SigningPublicKey,
		EncryptionPublicKey: derived.EncryptionPublicKey,
		SigningKeyPath:      derived.SigningKeyPath,
		EncryptionKeyPath:   derived.EncryptionKeyPath,
	}, true
}

func parseXPub(xpub string) (*xpubPublicNode, error) {
	decoded, err := base58.Decode(xpub)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 82 {
		return nil, fmt.Errorf("invalid xpub length")
	}
	body := decoded[:78]
	checksum := decoded[78:]
	first := sha256.Sum256(body)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(checksum, second[:4]) {
		return nil, fmt.Errorf("invalid xpub checksum")
	}
	if binary.BigEndian.Uint32(body[:4]) != hdXPubPublicVersion {
		return nil, fmt.Errorf("unsupported xpub version")
	}
	publicKey := append([]byte(nil), body[45:78]...)
	if _, err := secp256k1.ParsePubKey(publicKey); err != nil {
		return nil, err
	}
	return &xpubPublicNode{
		chainCode: append([]byte(nil), body[13:45]...),
		publicKey: publicKey,
	}, nil
}

func deriveXPubPublicChild(node *xpubPublicNode, index uint32) (*xpubPublicNode, error) {
	if node == nil {
		return nil, fmt.Errorf("missing xpub node")
	}
	var indexBytes [4]byte
	binary.BigEndian.PutUint32(indexBytes[:], index)
	mac := hmac.New(sha512.New, node.chainCode)
	mac.Write(node.publicKey)
	mac.Write(indexBytes[:])
	digest := mac.Sum(nil)
	tweak := digest[:32]
	curve := secp256k1.S256()
	tweakN := new(big.Int).SetBytes(tweak)
	if tweakN.Sign() <= 0 || tweakN.Cmp(curve.Params().N) >= 0 {
		return nil, fmt.Errorf("invalid xpub child tweak")
	}
	tweakX, tweakY := curve.ScalarBaseMult(tweak)
	parent, err := secp256k1.ParsePubKey(node.publicKey)
	if err != nil {
		return nil, err
	}
	childX, childY := curve.Add(tweakX, tweakY, parent.X(), parent.Y())
	if childX == nil || childY == nil {
		return nil, fmt.Errorf("invalid xpub child point")
	}
	return &xpubPublicNode{
		chainCode: append([]byte(nil), digest[32:]...),
		publicKey: compressSecp256k1Point(childX, childY),
	}, nil
}

func compressSecp256k1Point(x, y *big.Int) []byte {
	out := make([]byte, 33)
	if y.Bit(0) == 0 {
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	xBytes := x.Bytes()
	copy(out[33-len(xBytes):], xBytes)
	return out
}

func xpubSigningKeyPath(account uint32) string {
	return fmt.Sprintf("m/%d'/%d'/%d'/%d/0", hdXPubPurpose, hdXPubCoinType, account, hdXPubSigningChain)
}

func xpubEncryptionKeyPath(account uint32) string {
	return fmt.Sprintf("m/%d'/%d'/%d'/%d/0", hdXPubPurpose, hdXPubCoinType, account, hdXPubEncryptionChain)
}

func runtimeIdentityKeys(info wasm.IdentityInfo, xpub string) []map[string]interface{} {
	if derived, ok := derivePublicIdentityKeysFromXPub(xpub, info.Account); ok {
		keys := []map[string]interface{}{
			{
				"public_key":      derived.SigningPublicKey,
				"address_type":    "secp256k1",
				"key_address":     derived.SigningKeyPath,
				"derivation_path": derived.SigningKeyPath,
				"key_type":        "signing",
				"xpub":            derived.XPub,
			},
		}
		// Ed25519 signing key: the key behind EPM self-signatures and
		// PNM/dataset-publication signatures. Directory consumers
		// (ed25519PublicKeyFromDirectoryJSON) resolve it from this
		// projection, so it must not be dropped in the xpub branch.
		if strings.TrimSpace(info.SigningPubKeyHex) != "" {
			keys = append(keys, map[string]interface{}{
				"public_key":      info.SigningPubKeyHex,
				"address_type":    "ed25519",
				"key_address":     info.SigningKeyPath,
				"derivation_path": info.SigningKeyPath,
				"key_type":        "signing",
				"xpub":            derived.XPub,
			})
		}
		keys = append(keys, map[string]interface{}{
			"public_key":      derived.EncryptionPublicKey,
			"address_type":    "secp256k1",
			"key_address":     derived.EncryptionKeyPath,
			"derivation_path": derived.EncryptionKeyPath,
			"key_type":        "encryption",
			"xpub":            derived.XPub,
		})
		// X25519 encryption key, mirroring the wire EPM key set (WS2b).
		if strings.TrimSpace(info.EncryptionPubHex) != "" {
			keys = append(keys, map[string]interface{}{
				"public_key":   info.EncryptionPubHex,
				"address_type": "x25519",
				"key_address":  info.EncryptionKeyPath,
				"key_type":     "encryption",
			})
		}
		return keys
	}

	if strings.TrimSpace(info.IdentityPubKeyHex) == "" &&
		strings.TrimSpace(info.SigningPubKeyHex) == "" &&
		strings.TrimSpace(info.EncryptionPubHex) == "" {
		return nil
	}

	keys := make([]map[string]interface{}, 0, 3)
	if strings.TrimSpace(info.IdentityPubKeyHex) != "" {
		keys = append(keys, map[string]interface{}{
			"public_key":   info.IdentityPubKeyHex,
			"address_type": "secp256k1",
			"key_address":  info.IdentityKeyPath,
			"key_type":     "signing",
		})
	}
	if strings.TrimSpace(info.SigningPubKeyHex) != "" {
		signingKey := map[string]interface{}{
			"public_key":   info.SigningPubKeyHex,
			"address_type": "ed25519",
			"key_address":  info.SigningKeyPath,
			"key_type":     "signing",
		}
		if strings.TrimSpace(xpub) != "" {
			signingKey["xpub"] = xpub
		}
		keys = append(keys, signingKey)
	}
	if strings.TrimSpace(info.EncryptionPubHex) != "" {
		keys = append(keys, map[string]interface{}{
			"public_key":   info.EncryptionPubHex,
			"address_type": "x25519",
			"key_address":  info.EncryptionKeyPath,
			"key_type":     "encryption",
		})
	}
	return keys
}

func runtimeKeysMissing(value interface{}) bool {
	switch keys := value.(type) {
	case nil:
		return true
	case []map[string]interface{}:
		return len(keys) == 0
	case []interface{}:
		return len(keys) == 0
	default:
		return true
	}
}

// GetIdentityAttestation returns the node identity attestation for key binding.
func (s *Service) GetIdentityAttestation() *IdentityAttestation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identityAttestation == nil {
		return nil
	}

	att := *s.identityAttestation
	return &att
}

// UpdateProfile updates the node's EPM profile and rebuilds the EPM.
func (s *Service) UpdateProfile(profile *Profile) error {
	s.mu.Lock()

	s.profile = profile
	if err := s.rebuildEPMLocked(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to rebuild EPM: %w", err)
	}
	epmBytes := append([]byte(nil), s.epmBytes...)
	s.mu.Unlock()

	// Persist profile
	if err := s.saveProfile(profile, epmBytes); err != nil {
		log.Warnf("Failed to persist EPM profile: %v", err)
	}

	return nil
}

// rebuildEPM builds EPM bytes from the current profile + identity.
func (s *Service) rebuildEPM() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildEPMLocked()
}

// rebuildEPMLocked builds EPM bytes. Caller must hold s.mu.
//
// The EPM is built twice: first without a signature to obtain the exact wire
// bytes, then again with the embedded signature over the payload that
// canonicalSigningContentFromEPM derives from those wire bytes. Because the
// verifier (VerifyEPMSignature) recomputes the payload from the same wire
// fields, signer and verifier agree by construction; the signing payload can
// never drift from the published EPM. (The SIGNATURE field itself is not part
// of the canonical content, so the pass-1 payload is identical to what a
// verifier recomputes from the signed pass-2 bytes.)
func (s *Service) rebuildEPMLocked() error {
	// Build the identity attestation (chain proofs) once, up front, so both
	// build passes emit identical CHAIN_PROOFS.
	if err := s.rebuildIdentityAttestationLocked(); err != nil {
		log.Warnf("Failed to build identity attestation: %v", err)
	}

	// Timestamp for the EPM signature.
	signatureTimestamp := time.Now().Unix()

	unsigned, err := s.buildEPMBytesLocked("", signatureTimestamp)
	if err != nil {
		return err
	}

	signatureHex := ""
	if s.identity != nil || len(s.runtimeSigningKey) == ed25519.PrivateKeySize {
		payload, err := EPMSigningPayload(unsigned)
		if err != nil {
			return fmt.Errorf("derive EPM signing payload: %w", err)
		}
		if s.identity != nil {
			if sig, err := s.identity.SigningPrivKey.Sign(payload); err == nil {
				signatureHex = hex.EncodeToString(sig)
			}
		} else {
			signatureHex = hex.EncodeToString(ed25519.Sign(s.runtimeSigningKey, payload))
		}
	}
	if signatureHex == "" {
		s.epmBytes = unsigned
		return nil
	}

	signed, err := s.buildEPMBytesLocked(signatureHex, signatureTimestamp)
	if err != nil {
		return err
	}
	s.epmBytes = signed
	return nil
}

// buildEPMBytesLocked serializes the node EPM wire bytes with the given
// signature (hex, empty for the unsigned pre-image pass) and signature
// timestamp. Caller must hold s.mu. The output is deterministic for a fixed
// service state, signature, and timestamp.
func (s *Service) buildEPMBytesLocked(signatureHex string, signatureTimestamp int64) ([]byte, error) {
	builder := flatbuffers.NewBuilder(2048)

	p := s.profile
	if p == nil {
		p = &Profile{}
	}

	// Create string offsets
	var dnOff, legalNameOff, familyNameOff, givenNameOff flatbuffers.UOffsetT
	var additionalNameOff, prefixOff, suffixOff flatbuffers.UOffsetT
	var jobTitleOff, occupationOff, emailOff, telephoneOff flatbuffers.UOffsetT

	if p.DN != "" {
		dnOff = builder.CreateString(p.DN)
	}
	if p.LegalName != "" {
		legalNameOff = builder.CreateString(p.LegalName)
	}
	if p.FamilyName != "" {
		familyNameOff = builder.CreateString(p.FamilyName)
	}
	if p.GivenName != "" {
		givenNameOff = builder.CreateString(p.GivenName)
	}
	if p.AdditionalName != "" {
		additionalNameOff = builder.CreateString(p.AdditionalName)
	}
	if p.HonorificPrefix != "" {
		prefixOff = builder.CreateString(p.HonorificPrefix)
	}
	if p.HonorificSuffix != "" {
		suffixOff = builder.CreateString(p.HonorificSuffix)
	}
	if p.JobTitle != "" {
		jobTitleOff = builder.CreateString(p.JobTitle)
	}
	if p.Occupation != "" {
		occupationOff = builder.CreateString(p.Occupation)
	}
	if p.Email != "" {
		emailOff = builder.CreateString(p.Email)
	}
	if p.Telephone != "" {
		telephoneOff = builder.CreateString(p.Telephone)
	}

	// Address
	var addressOff flatbuffers.UOffsetT
	if p.Address != nil && !p.Address.IsEmpty() {
		var countryOff, regionOff, localityOff, postalOff, streetOff, poBoxOff flatbuffers.UOffsetT
		if p.Address.Country != "" {
			countryOff = builder.CreateString(p.Address.Country)
		}
		if p.Address.Region != "" {
			regionOff = builder.CreateString(p.Address.Region)
		}
		if p.Address.Locality != "" {
			localityOff = builder.CreateString(p.Address.Locality)
		}
		if p.Address.PostalCode != "" {
			postalOff = builder.CreateString(p.Address.PostalCode)
		}
		if p.Address.Street != "" {
			streetOff = builder.CreateString(p.Address.Street)
		}
		if p.Address.POBox != "" {
			poBoxOff = builder.CreateString(p.Address.POBox)
		}

		EPM.AddressStart(builder)
		if countryOff != 0 {
			EPM.AddressAddCOUNTRY(builder, countryOff)
		}
		if regionOff != 0 {
			EPM.AddressAddREGION(builder, regionOff)
		}
		if localityOff != 0 {
			EPM.AddressAddLOCALITY(builder, localityOff)
		}
		if postalOff != 0 {
			EPM.AddressAddPOSTAL_CODE(builder, postalOff)
		}
		if streetOff != 0 {
			EPM.AddressAddSTREET(builder, streetOff)
		}
		if poBoxOff != 0 {
			EPM.AddressAddPOST_OFFICE_BOX_NUMBER(builder, poBoxOff)
		}
		addressOff = EPM.AddressEnd(builder)
	}

	// Alternate names
	var altNamesOff flatbuffers.UOffsetT
	if len(p.AlternateNames) > 0 {
		offsets := make([]flatbuffers.UOffsetT, len(p.AlternateNames))
		for i, name := range p.AlternateNames {
			offsets[i] = builder.CreateString(name)
		}
		EPM.EPMStartALTERNATE_NAMESVector(builder, len(offsets))
		for i := len(offsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(offsets[i])
		}
		altNamesOff = builder.EndVector(len(offsets))
	}

	// Build CryptoKey entries from identity
	var keysOff flatbuffers.UOffsetT
	var keyOffsets []flatbuffers.UOffsetT

	if s.identity != nil {
		if derived, ok := derivePublicIdentityKeysFromXPub(s.xpub, s.identity.Account); ok {
			xpubOff := builder.CreateString(derived.XPub)
			addrTypeOff := builder.CreateString("secp256k1")

			signingPubOff := builder.CreateString(derived.SigningPublicKey)
			signingPathOff := builder.CreateString(derived.SigningKeyPath)
			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, signingPubOff)
			EPM.CryptoKeyAddXPUB(builder, xpubOff)
			EPM.CryptoKeyAddADDRESS_TYPE(builder, addrTypeOff)
			EPM.CryptoKeyAddKEY_ADDRESS(builder, signingPathOff)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
			keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))

			// Ed25519 signing key. This is the key that produces the EPM
			// self-signature (and PNM/dataset-publication signatures), so it
			// must ride the wire: VerifyEPMSignature and the directory
			// Ed25519-key lookup both read it from the EPM KEYS vector.
			if s.identity.SigningPubKey != nil {
				if sigPubBytes, err := s.identity.SigningPubKey.Raw(); err == nil && len(sigPubBytes) > 0 {
					ed25519PubOff := builder.CreateString(hex.EncodeToString(sigPubBytes))
					ed25519AddrTypeOff := builder.CreateString("ed25519")
					ed25519PathOff := builder.CreateString(s.identity.SigningKeyPath)
					EPM.CryptoKeyStart(builder)
					EPM.CryptoKeyAddPUBLIC_KEY(builder, ed25519PubOff)
					EPM.CryptoKeyAddXPUB(builder, xpubOff)
					EPM.CryptoKeyAddADDRESS_TYPE(builder, ed25519AddrTypeOff)
					EPM.CryptoKeyAddKEY_ADDRESS(builder, ed25519PathOff)
					EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
					keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))
				}
			}

			encryptionPubOff := builder.CreateString(derived.EncryptionPublicKey)
			encryptionPathOff := builder.CreateString(derived.EncryptionKeyPath)
			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, encryptionPubOff)
			EPM.CryptoKeyAddXPUB(builder, xpubOff)
			EPM.CryptoKeyAddADDRESS_TYPE(builder, addrTypeOff)
			EPM.CryptoKeyAddKEY_ADDRESS(builder, encryptionPathOff)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
			keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))

			// Also advertise the X25519 encryption key alongside the secp256k1
			// one, so a sender can wrap a content key to either curve (X25519
			// default, or secp256k1 ECIES). The EPM carries both encryption keys
			// (WS2b). The identity's X25519 encryption key is always available
			// here since s.identity is non-nil.
			if len(s.identity.EncryptionPub) > 0 {
				x25519PubOff := builder.CreateString(hex.EncodeToString(s.identity.EncryptionPub))
				x25519AddrOff := builder.CreateString("x25519")
				x25519PathOff := builder.CreateString(s.identity.EncryptionKeyPath)
				EPM.CryptoKeyStart(builder)
				EPM.CryptoKeyAddPUBLIC_KEY(builder, x25519PubOff)
				EPM.CryptoKeyAddADDRESS_TYPE(builder, x25519AddrOff)
				EPM.CryptoKeyAddKEY_ADDRESS(builder, x25519PathOff)
				EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
				keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))
			}
		} else {
			// Identity key (secp256k1) for provider descriptor and direct EPM parsing.
			identityPubBytes, _ := s.identity.IdentityPubKey.Raw()
			identityPubHex := hex.EncodeToString(identityPubBytes)
			identityPubOff := builder.CreateString(identityPubHex)
			identityAddrTypeOff := builder.CreateString("secp256k1")
			identityPathOff := builder.CreateString(s.identity.IdentityKeyPath)

			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, identityPubOff)
			EPM.CryptoKeyAddADDRESS_TYPE(builder, identityAddrTypeOff)
			EPM.CryptoKeyAddKEY_ADDRESS(builder, identityPathOff)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
			identityKeyOff := EPM.CryptoKeyEnd(builder)
			keyOffsets = append(keyOffsets, identityKeyOff)

			// Signing key (Ed25519)
			sigPubBytes, _ := s.identity.SigningPubKey.Raw()
			sigPubHex := hex.EncodeToString(sigPubBytes)

			sigPubOff := builder.CreateString(sigPubHex)
			var sigXpubOff flatbuffers.UOffsetT
			if s.xpub != "" {
				sigXpubOff = builder.CreateString(s.xpub)
			}
			sigAddrTypeOff := builder.CreateString("ed25519")
			sigPathOff := builder.CreateString(s.identity.SigningKeyPath)

			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, sigPubOff)
			if sigXpubOff != 0 {
				EPM.CryptoKeyAddXPUB(builder, sigXpubOff)
			}
			EPM.CryptoKeyAddADDRESS_TYPE(builder, sigAddrTypeOff)
			EPM.CryptoKeyAddKEY_ADDRESS(builder, sigPathOff)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
			sigKeyOff := EPM.CryptoKeyEnd(builder)
			keyOffsets = append(keyOffsets, sigKeyOff)

			// Encryption key (X25519)
			encPubHex := hex.EncodeToString(s.identity.EncryptionPub)
			encPubOff := builder.CreateString(encPubHex)
			encAddrTypeOff := builder.CreateString("x25519")
			encPathOff := builder.CreateString(s.identity.EncryptionKeyPath)

			EPM.CryptoKeyStart(builder)
			EPM.CryptoKeyAddPUBLIC_KEY(builder, encPubOff)
			EPM.CryptoKeyAddADDRESS_TYPE(builder, encAddrTypeOff)
			EPM.CryptoKeyAddKEY_ADDRESS(builder, encPathOff)
			EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
			encKeyOff := EPM.CryptoKeyEnd(builder)
			keyOffsets = append(keyOffsets, encKeyOff)
		}
	}
	if len(s.runtimeSigningKey) == ed25519.PrivateKeySize {
		pub := s.runtimeSigningKey.Public().(ed25519.PublicKey)
		pubOff := builder.CreateString(hex.EncodeToString(pub))
		addrTypeOff := builder.CreateString("ed25519")
		path := strings.TrimSpace(s.runtimeSigningPath)
		if path == "" {
			path = "sdn/runtime-signing"
		}
		pathOff := builder.CreateString(path)

		EPM.CryptoKeyStart(builder)
		EPM.CryptoKeyAddPUBLIC_KEY(builder, pubOff)
		EPM.CryptoKeyAddADDRESS_TYPE(builder, addrTypeOff)
		EPM.CryptoKeyAddKEY_ADDRESS(builder, pathOff)
		EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
		keyOffsets = append(keyOffsets, EPM.CryptoKeyEnd(builder))
	}

	if len(keyOffsets) > 0 {
		EPM.EPMStartKEYSVector(builder, len(keyOffsets))
		for i := len(keyOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(keyOffsets[i])
		}
		keysOff = builder.EndVector(len(keyOffsets))
	}

	// Multiformat addresses (IPNS + runtime addresses such as onion URL)
	var multiAddrOff flatbuffers.UOffsetT
	peerIDStr := s.peerID.String()
	ipnsAddr := "/ipns/" + peerIDStr
	addresses := make([]string, 0, 1+len(s.runtimeAddresses))
	addresses = append(addresses, ipnsAddr)
	addresses = append(addresses, s.runtimeAddresses...)
	addresses = normalizeRuntimeAddresses(addresses)

	if len(addresses) > 0 {
		addrOffsets := make([]flatbuffers.UOffsetT, len(addresses))
		for i, addr := range addresses {
			addrOffsets[i] = builder.CreateString(addr)
		}
		EPM.EPMStartMULTIFORMAT_ADDRESSVector(builder, len(addrOffsets))
		for i := len(addrOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(addrOffsets[i])
		}
		multiAddrOff = builder.EndVector(len(addrOffsets))
	}

	// Build ChainProof FlatBuffer entries from identity attestation
	// (rebuilt once by rebuildEPMLocked before both build passes)
	var chainProofsOff flatbuffers.UOffsetT
	if s.identityAttestation != nil && len(s.identityAttestation.ChainProofs) > 0 {
		proofOffsets := make([]flatbuffers.UOffsetT, len(s.identityAttestation.ChainProofs))
		for i, proof := range s.identityAttestation.ChainProofs {
			chainOff := builder.CreateString(proof.Chain)
			addrOff := builder.CreateString(proof.Address)
			pubOff := builder.CreateString(proof.PublicKeyHex)
			pathOff := builder.CreateString(proof.KeyPath)
			sigOff := builder.CreateString(proof.Signature)
			payloadOff := builder.CreateString(proof.SignedPayloadHex)
			algOff := builder.CreateString(proof.SignatureAlgorithm)
			encOff := builder.CreateString(proof.SignatureEncoding)

			EPM.ChainProofStart(builder)
			EPM.ChainProofAddCHAIN(builder, chainOff)
			EPM.ChainProofAddADDRESS(builder, addrOff)
			EPM.ChainProofAddPUBLIC_KEY(builder, pubOff)
			EPM.ChainProofAddKEY_PATH(builder, pathOff)
			EPM.ChainProofAddSIGNATURE(builder, sigOff)
			EPM.ChainProofAddSIGNED_PAYLOAD(builder, payloadOff)
			EPM.ChainProofAddALGORITHM(builder, algOff)
			EPM.ChainProofAddENCODING(builder, encOff)
			proofOffsets[i] = EPM.ChainProofEnd(builder)
		}
		EPM.EPMStartCHAIN_PROOFSVector(builder, len(proofOffsets))
		for i := len(proofOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(proofOffsets[i])
		}
		chainProofsOff = builder.EndVector(len(proofOffsets))
	}

	// Embedded signature (computed by rebuildEPMLocked over the wire-derived
	// canonical payload; empty during the unsigned pre-image pass).
	var signatureOff flatbuffers.UOffsetT
	if signatureHex != "" {
		signatureOff = builder.CreateString(signatureHex)
	}

	// Build EPM table
	EPM.EPMStart(builder)
	EPM.EPMAddENTITY_TYPE(builder, EPM.EntityTypeNode)
	if dnOff != 0 {
		EPM.EPMAddDN(builder, dnOff)
	}
	if legalNameOff != 0 {
		EPM.EPMAddLEGAL_NAME(builder, legalNameOff)
	}
	if familyNameOff != 0 {
		EPM.EPMAddFAMILY_NAME(builder, familyNameOff)
	}
	if givenNameOff != 0 {
		EPM.EPMAddGIVEN_NAME(builder, givenNameOff)
	}
	if additionalNameOff != 0 {
		EPM.EPMAddADDITIONAL_NAME(builder, additionalNameOff)
	}
	if prefixOff != 0 {
		EPM.EPMAddHONORIFIC_PREFIX(builder, prefixOff)
	}
	if suffixOff != 0 {
		EPM.EPMAddHONORIFIC_SUFFIX(builder, suffixOff)
	}
	if jobTitleOff != 0 {
		EPM.EPMAddJOB_TITLE(builder, jobTitleOff)
	}
	if occupationOff != 0 {
		EPM.EPMAddOCCUPATION(builder, occupationOff)
	}
	if addressOff != 0 {
		EPM.EPMAddADDRESS(builder, addressOff)
	}
	if altNamesOff != 0 {
		EPM.EPMAddALTERNATE_NAMES(builder, altNamesOff)
	}
	if emailOff != 0 {
		EPM.EPMAddEMAIL(builder, emailOff)
	}
	if telephoneOff != 0 {
		EPM.EPMAddTELEPHONE(builder, telephoneOff)
	}
	if keysOff != 0 {
		EPM.EPMAddKEYS(builder, keysOff)
	}
	if multiAddrOff != 0 {
		EPM.EPMAddMULTIFORMAT_ADDRESS(builder, multiAddrOff)
	}
	if signatureOff != 0 {
		EPM.EPMAddSIGNATURE(builder, signatureOff)
	}
	EPM.EPMAddSIGNATURE_TIMESTAMP(builder, signatureTimestamp)
	if chainProofsOff != 0 {
		EPM.EPMAddCHAIN_PROOFS(builder, chainProofsOff)
	}
	epmOff := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epmOff)

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	return result, nil
}

func (s *Service) rebuildIdentityAttestationLocked() error {
	if s.identity == nil || strings.TrimSpace(s.xpub) == "" {
		s.identityAttestation = nil
		return nil
	}
	if s.identity.Addresses == nil || s.identity.Addresses.Bitcoin == nil ||
		s.identity.Addresses.Ethereum == nil || s.identity.Addresses.Solana == nil {
		s.identityAttestation = nil
		return fmt.Errorf("missing chain addresses required for identity attestation")
	}
	if len(s.identity.BitcoinPrivateKey) != 32 || len(s.identity.EthereumPrivateKey) != 32 || len(s.identity.SolanaPrivateKey) != 32 {
		s.identityAttestation = nil
		return fmt.Errorf("missing chain signing keys required for identity attestation")
	}

	identityPubRaw, err := s.identity.IdentityPubKey.Raw()
	if err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("failed to export identity public key: %w", err)
	}
	signingPubRaw, err := s.identity.SigningPubKey.Raw()
	if err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("failed to export signing public key: %w", err)
	}

	payload := identityAttestationPayload{
		Version:           identityAttestationVersion,
		XPub:              s.xpub,
		IdentityPubKeyHex: hex.EncodeToString(identityPubRaw),
		IdentityKeyPath:   s.identity.IdentityKeyPath,
		SigningPubKeyHex:  hex.EncodeToString(signingPubRaw),
		SigningKeyPath:    s.identity.SigningKeyPath,
		BitcoinAddress:    s.identity.Addresses.Bitcoin.Address,
		BitcoinKeyPath:    s.identity.BitcoinKeyPath,
		EthereumAddress:   s.identity.Addresses.Ethereum.Address,
		EthereumKeyPath:   s.identity.EthereumKeyPath,
		SolanaAddress:     s.identity.Addresses.Solana.Address,
		SolanaKeyPath:     s.identity.SolanaKeyPath,
		IssuedAt:          time.Now().Unix(),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("failed to serialize identity attestation payload: %w", err)
	}

	bitcoinProof, err := s.buildBitcoinAttestationProof(payloadBytes, payload)
	if err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("failed to build bitcoin chain proof: %w", err)
	}
	ethereumProof, err := s.buildEthereumAttestationProof(payloadBytes, payload)
	if err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("failed to build ethereum chain proof: %w", err)
	}
	solanaProof, err := s.buildSolanaAttestationProof(payloadBytes, payload)
	if err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("failed to build solana chain proof: %w", err)
	}

	s.identityAttestation = &IdentityAttestation{
		Version:           payload.Version,
		XPub:              payload.XPub,
		IdentityPubKeyHex: payload.IdentityPubKeyHex,
		IdentityKeyPath:   payload.IdentityKeyPath,
		SigningPubKeyHex:  payload.SigningPubKeyHex,
		SigningKeyPath:    payload.SigningKeyPath,
		IssuedAt:          payload.IssuedAt,
		BitcoinAddress:    payload.BitcoinAddress,
		BitcoinKeyPath:    payload.BitcoinKeyPath,
		EthereumAddress:   payload.EthereumAddress,
		EthereumKeyPath:   payload.EthereumKeyPath,
		SolanaAddress:     payload.SolanaAddress,
		SolanaKeyPath:     payload.SolanaKeyPath,
		ChainProofs: []IdentityAttestationChainProof{
			bitcoinProof,
			ethereumProof,
			solanaProof,
		},
	}

	if _, err := s.identityAttestation.Verify(); err != nil {
		s.identityAttestation = nil
		return fmt.Errorf("identity attestation validation failed: %w", err)
	}
	return nil
}

func (s *Service) buildBitcoinAttestationProof(payload []byte, payloadInfo identityAttestationPayload) (IdentityAttestationChainProof, error) {
	privKey := secp256k1.PrivKeyFromBytes(s.identity.BitcoinPrivateKey)
	if privKey == nil {
		return IdentityAttestationChainProof{}, fmt.Errorf("invalid bitcoin private key")
	}
	pubKey := privKey.PubKey().SerializeCompressed()
	signedHash := bitcoinSignedMessageHash(payload)
	signature := ecdsa.SignCompact(privKey, signedHash, true)

	proof := IdentityAttestationChainProof{
		Chain:              identityChainBitcoin,
		Address:            payloadInfo.BitcoinAddress,
		KeyPath:            payloadInfo.BitcoinKeyPath,
		PublicKeyHex:       hex.EncodeToString(pubKey),
		Signature:          hex.EncodeToString(signature),
		SignedPayloadHex:   hex.EncodeToString(payload),
		SignatureAlgorithm: identityAttestationAlgorithmBitcoin,
		SignatureEncoding:  identityAttestationBitcoinSigEncoding,
	}
	return proof, nil
}

func (s *Service) buildEthereumAttestationProof(payload []byte, payloadInfo identityAttestationPayload) (IdentityAttestationChainProof, error) {
	privKey := secp256k1.PrivKeyFromBytes(s.identity.EthereumPrivateKey)
	if privKey == nil {
		return IdentityAttestationChainProof{}, fmt.Errorf("invalid ethereum private key")
	}
	pubKey := privKey.PubKey().SerializeCompressed()
	signedHash := ethereumSignedMessageHash(payload)
	signature := ecdsa.SignCompact(privKey, signedHash, true)

	proof := IdentityAttestationChainProof{
		Chain:              identityChainEthereum,
		Address:            payloadInfo.EthereumAddress,
		KeyPath:            payloadInfo.EthereumKeyPath,
		PublicKeyHex:       hex.EncodeToString(pubKey),
		Signature:          hex.EncodeToString(signature),
		SignedPayloadHex:   hex.EncodeToString(payload),
		SignatureAlgorithm: identityAttestationAlgorithmEthereum,
		SignatureEncoding:  identityAttestationEthereumSigEncoding,
	}
	return proof, nil
}

func (s *Service) buildSolanaAttestationProof(payload []byte, payloadInfo identityAttestationPayload) (IdentityAttestationChainProof, error) {
	if len(s.identity.SolanaPrivateKey) != ed25519.SeedSize {
		return IdentityAttestationChainProof{}, fmt.Errorf("invalid solana private key length: %d", len(s.identity.SolanaPrivateKey))
	}
	privKey := ed25519.NewKeyFromSeed(s.identity.SolanaPrivateKey)
	pub := privKey.Public().(ed25519.PublicKey)
	signature := ed25519.Sign(privKey, payload)

	proof := IdentityAttestationChainProof{
		Chain:              identityChainSolana,
		Address:            payloadInfo.SolanaAddress,
		KeyPath:            payloadInfo.SolanaKeyPath,
		PublicKeyHex:       hex.EncodeToString(pub),
		Signature:          hex.EncodeToString(signature),
		SignedPayloadHex:   hex.EncodeToString(payload),
		SignatureAlgorithm: identityAttestationAlgorithmSolana,
		SignatureEncoding:  identityAttestationSolanaSigEncoding,
	}
	return proof, nil
}

func normalizeRuntimeAddresses(addresses []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(addresses))

	for _, raw := range addresses {
		addr := strings.TrimSpace(raw)
		if addr == "" {
			continue
		}
		key := strings.ToLower(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// defaultProfile creates a default profile with the node's PeerID as DN.
func (s *Service) defaultProfile() *Profile {
	return &Profile{
		DN: "SDN Node " + s.peerID.ShortString(),
	}
}
