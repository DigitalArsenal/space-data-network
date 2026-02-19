// Package epm provides EPM (Entity Profile Message) lifecycle management.
// It creates, stores, and serves the node's identity card (EPM), which
// contains cryptographic keys, contact information, and network addresses.
package epm

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mr-tron/base58"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/ripemd160"
	"golang.org/x/crypto/sha3"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var log = logging.Logger("sdn-epm")

const (
	identityAttestationVersion    = "1"
	identityChainBitcoin          = "bitcoin"
	identityChainEthereum         = "ethereum"
	identityChainSolana           = "solana"
	identityAttestationAlgorithmBitcoin  = "secp256k1-compact-bitcoin"
	identityAttestationAlgorithmEthereum = "secp256k1-compact-ethereum"
	identityAttestationAlgorithmSolana   = "ed25519"
)

const (
	identityAttestationBitcoinSigEncoding   = "compact"
	identityAttestationEthereumSigEncoding  = "compact"
	identityAttestationSolanaSigEncoding    = "raw-ed25519"
)

const bech32Alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Gen = []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

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
	Version          string `json:"version"`
	XPub             string `json:"xpub"`
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
		IdentityPubKeyHex:  a.IdentityPubKeyHex,
		IdentityKeyPath:    a.IdentityKeyPath,
		SigningPubKeyHex:   a.SigningPubKeyHex,
		SigningKeyPath:     a.SigningKeyPath,
		BitcoinAddress:     a.BitcoinAddress,
		BitcoinKeyPath:     a.BitcoinKeyPath,
		EthereumAddress:    a.EthereumAddress,
		EthereumKeyPath:    a.EthereumKeyPath,
		SolanaAddress:      a.SolanaAddress,
		SolanaKeyPath:      a.SolanaKeyPath,
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

	epmBytes []byte // current node EPM (size-prefixed FlatBuffer)
	profile  *Profile
	identityAttestation *IdentityAttestation

	mu sync.RWMutex
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

// Init loads or creates the node's EPM profile and builds the initial EPM.
func (s *Service) Init() error {
	// Load existing profile or create default
	profile, err := LoadProfile(s.dataDir)
	if err != nil {
		log.Infof("No existing EPM profile, creating default")
		profile = s.defaultProfile()
	}
	s.profile = profile

	// Build EPM from profile + identity
	if err := s.rebuildEPM(); err != nil {
		return fmt.Errorf("failed to build node EPM: %w", err)
	}

	log.Infof("EPM service initialized (PeerID=%s, hasIdentity=%v)", s.peerID, s.identity != nil)
	return nil
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

// GetNodeVCard returns the node's EPM as a vCard 4.0 string.
func (s *Service) GetNodeVCard() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return "", fmt.Errorf("no EPM available")
	}
	return vcard.EPMToVCard(s.epmBytes)
}

// GetNodeQR returns a QR code PNG of the node's vCard.
func (s *Service) GetNodeQR(size int) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return nil, fmt.Errorf("no EPM available")
	}
	return vcard.EPMToQR(s.epmBytes, size)
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

	// Add identity metadata
	result["peer_id"] = s.peerID.String()
	if s.identityAttestation != nil {
		result["identity_attestation"] = s.identityAttestation
	}

	return result
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
	defer s.mu.Unlock()

	s.profile = profile
	if err := s.rebuildEPMLocked(); err != nil {
		return fmt.Errorf("failed to rebuild EPM: %w", err)
	}

	// Persist profile
	if err := SaveProfile(s.dataDir, profile); err != nil {
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
func (s *Service) rebuildEPMLocked() error {
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

	if len(keyOffsets) > 0 {
		EPM.EPMStartKEYSVector(builder, len(keyOffsets))
		for i := len(keyOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(keyOffsets[i])
		}
		keysOff = builder.EndVector(len(keyOffsets))
	}

	// Multiformat addresses (IPNS)
	var multiAddrOff flatbuffers.UOffsetT
	peerIDStr := s.peerID.String()
	ipnsAddr := "/ipns/" + peerIDStr
	addrOff := builder.CreateString(ipnsAddr)
	EPM.EPMStartMULTIFORMAT_ADDRESSVector(builder, 1)
	builder.PrependUOffsetT(addrOff)
	multiAddrOff = builder.EndVector(1)

	// Build EPM table
	EPM.EPMStart(builder)
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
	epmOff := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epmOff)

	if err := s.rebuildIdentityAttestationLocked(); err != nil {
		log.Warnf("Failed to build identity attestation: %v", err)
	}

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	s.epmBytes = result

	return nil
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
		IdentityPubKeyHex:  hex.EncodeToString(identityPubRaw),
		IdentityKeyPath:    s.identity.IdentityKeyPath,
		SigningPubKeyHex:   hex.EncodeToString(signingPubRaw),
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
		IdentityPubKeyHex:  payload.IdentityPubKeyHex,
		IdentityKeyPath:    payload.IdentityKeyPath,
		SigningPubKeyHex:   payload.SigningPubKeyHex,
		SigningKeyPath:     payload.SigningKeyPath,
		IssuedAt:           payload.IssuedAt,
		BitcoinAddress:     payload.BitcoinAddress,
		BitcoinKeyPath:     payload.BitcoinKeyPath,
		EthereumAddress:    payload.EthereumAddress,
		EthereumKeyPath:    payload.EthereumKeyPath,
		SolanaAddress:      payload.SolanaAddress,
		SolanaKeyPath:      payload.SolanaKeyPath,
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

// defaultProfile creates a default profile with the node's PeerID as DN.
func (s *Service) defaultProfile() *Profile {
	return &Profile{
		DN: "SDN Node " + s.peerID.ShortString(),
	}
}
