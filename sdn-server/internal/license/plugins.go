package license

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// zeroBytes overwrites a byte slice with zeros to clear sensitive key material.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

const (
	defaultPluginCatalogFile      = "catalog.json"
	defaultPluginRootDirName      = "plugins"
	defaultPluginContentType      = "application/wasm"
	defaultPluginCacheControl     = "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400"
	defaultPluginRequiredScope    = "orbpro:base"
	defaultKeyEnvelopeAlgorithm   = "X25519+SHA256+AES-256-GCM"
	defaultKeyEnvelopeLifetimeSec = int64(120)
	pluginRuntimeStatusStopped    = "stopped"
	pluginRuntimeStatusRunning    = "running"
	pluginRuntimeStatusError      = "error"
	pluginCryptoContext           = "orbpro-plugin-v1"
	pluginBundleV2Format          = byte(0x02)
	pluginBundleV2Header          = 61
	pluginBundleV1Header          = 80
)

var envelopeKeyWrapInfos = [][]byte{
	[]byte("orbpro-key-server-artifact-wrap-v1"),
	[]byte("plugin-key-server-artifact-wrap-v1"),
}

// stagedArtifactEnvelope represents the JSON envelope produced by OrbPro
// key-server staging for encrypted-at-rest plugin artifacts.
type stagedArtifactEnvelope struct {
	KeyEncryption struct {
		Scheme                string `json:"scheme"`
		EphemeralPublicKeyHex string `json:"ephemeralPublicKeyHex"`
		HKDFSaltB64           string `json:"hkdfSaltB64"`
		WrapIvB64             string `json:"wrapIvB64"`
		WrappedKeyB64         string `json:"wrappedKeyB64"`
		WrappedKeyTagB64      string `json:"wrappedKeyTagB64"`
	} `json:"keyEncryption"`
	ContentEncryption struct {
		Algorithm     string `json:"algorithm"`
		IvB64         string `json:"ivB64"`
		TagB64        string `json:"tagB64"`
		CiphertextB64 string `json:"ciphertextB64"`
	} `json:"contentEncryption"`
}

var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// PluginCatalogFile is the on-disk plugin catalog format.
type PluginCatalogFile struct {
	Plugins []PluginCatalogEntry `json:"plugins"`
}

// PluginCatalogEntry describes a plugin bundle and its key material location.
type PluginCatalogEntry struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	RequiredScope string `json:"required_scope"`
	EncryptedPath string `json:"encrypted_path,omitempty"`
	KeyPath       string `json:"key_path,omitempty"`
	PlainPath     string `json:"plain_path,omitempty"`
	ContentType   string `json:"content_type,omitempty"`
	CacheControl  string `json:"cache_control,omitempty"`

	// Upload audit fields (set when uploaded via API).
	SignatureHex    string `json:"signature_hex,omitempty"`
	SignerPubKeyHex string `json:"signer_pubkey_hex,omitempty"`
	UploadedAt      string `json:"uploaded_at,omitempty"`
}

// PluginDescriptor is safe to return publicly (no key path information).
type PluginDescriptor struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	RequiredScope   string `json:"required_scope"`
	ContentType     string `json:"content_type"`
	CacheControl    string `json:"cache_control"`
	BundleSHA256    string `json:"bundle_sha256"`
	SizeBytes       int64  `json:"size_bytes"`
	SignatureHex    string `json:"signature_hex,omitempty"`
	SignerPubKeyHex string `json:"signer_pubkey_hex,omitempty"`
	UploadedAt      string `json:"uploaded_at,omitempty"`
	Status          string `json:"status"`
	StatusMessage   string `json:"status_message,omitempty"`
}

// PluginAsset is an in-memory validated plugin metadata record.
type PluginAsset struct {
	ID              string
	Version         string
	RequiredScope   string
	ContentType     string
	CacheControl    string
	BundleSHA256    string
	SizeBytes       int64
	SignatureHex    string
	SignerPubKeyHex string
	UploadedAt      string

	encryptedPath string
	keyPath       string
	plainPath     string

	runtimeStatus string
	statusMessage string
}

func (a *PluginAsset) clone() *PluginAsset {
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

func (a *PluginAsset) Descriptor() PluginDescriptor {
	status := strings.TrimSpace(a.runtimeStatus)
	if status == "" {
		status = pluginRuntimeStatusStopped
	}
	return PluginDescriptor{
		ID:              a.ID,
		Version:         a.Version,
		RequiredScope:   a.RequiredScope,
		ContentType:     a.ContentType,
		CacheControl:    a.CacheControl,
		BundleSHA256:    a.BundleSHA256,
		SizeBytes:       a.SizeBytes,
		SignatureHex:    a.SignatureHex,
		SignerPubKeyHex: a.SignerPubKeyHex,
		UploadedAt:      a.UploadedAt,
		Status:          status,
		StatusMessage:   strings.TrimSpace(a.statusMessage),
	}
}

// PluginRegistry manages encrypted plugin artifacts and key material pointers.
type PluginRegistry struct {
	rootPath string

	mu     sync.RWMutex
	assets map[string]*PluginAsset
}

// SetRuntimeStatus updates runtime status for a catalog plugin.
func (r *PluginRegistry) SetRuntimeStatus(id, status, message string) error {
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return errors.New("plugin id is required")
	}
	if status = strings.TrimSpace(status); status == "" {
		status = pluginRuntimeStatusStopped
	}
	if r == nil {
		return errors.New("plugin registry is nil")
	}
	message = strings.TrimSpace(message)

	r.mu.Lock()
	defer r.mu.Unlock()

	asset, ok := r.assets[normalized]
	if !ok {
		return os.ErrNotExist
	}
	asset.runtimeStatus = status
	asset.statusMessage = message
	return nil
}

// RuntimeStatus reports the current runtime status for a catalog plugin.
func (r *PluginRegistry) RuntimeStatus(id string) (string, string, bool) {
	asset, ok := r.Get(id)
	if !ok {
		return pluginRuntimeStatusStopped, "", false
	}
	status := strings.TrimSpace(asset.runtimeStatus)
	if status == "" {
		status = pluginRuntimeStatusStopped
	}
	return status, strings.TrimSpace(asset.statusMessage), true
}

// IsEncrypted reports whether a catalog entry stores encrypted bytes.
func (r *PluginRegistry) IsEncrypted(id string) (bool, error) {
	asset, ok := r.Get(id)
	if !ok {
		return false, os.ErrNotExist
	}
	return strings.TrimSpace(asset.keyPath) != "", nil
}

// PluginKeyEnvelope is returned by /api/v1/plugins/{id}/key-envelope.
type PluginKeyEnvelope struct {
	PluginID           string `json:"plugin_id"`
	Version            string `json:"version"`
	RequiredScope      string `json:"required_scope"`
	BundleSHA256       string `json:"bundle_sha256"`
	Algorithm          string `json:"alg"`
	ServerX25519PubKey string `json:"server_x25519_pubkey"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
	AssociatedData     string `json:"associated_data"`
	Issuer             string `json:"issuer"`
	Subject            string `json:"sub"`
	PeerID             string `json:"peer_id"`
	CapabilityTokenJTI string `json:"capability_token_jti"`
	ExpiresAt          int64  `json:"expires_at"`
}

type pluginKeyEnvelopePayload struct {
	Key           string `json:"key"`
	PluginID      string `json:"plugin_id"`
	Version       string `json:"version"`
	RequiredScope string `json:"required_scope"`
	BundleSHA256  string `json:"bundle_sha256"`
	Sub           string `json:"sub"`
	PeerID        string `json:"peer_id"`
	JTI           string `json:"jti"`
	Exp           int64  `json:"exp"`
}

// LoadPluginRegistry loads plugin catalog and validates each entry.
// Missing catalog is treated as empty plugin registry.
func LoadPluginRegistry(rootPath string) (*PluginRegistry, error) {
	root := strings.TrimSpace(rootPath)
	if root == "" {
		return nil, errors.New("plugin root path is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin root: %w", err)
	}
	if err := os.MkdirAll(rootAbs, 0700); err != nil {
		return nil, fmt.Errorf("create plugin root: %w", err)
	}

	reg := &PluginRegistry{
		rootPath: rootAbs,
		assets:   make(map[string]*PluginAsset),
	}

	catalogPath := filepath.Join(rootAbs, defaultPluginCatalogFile)
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return reg, nil
		}
		return nil, fmt.Errorf("read plugin catalog: %w", err)
	}

	var catalog PluginCatalogFile
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("decode plugin catalog: %w", err)
	}

	for _, entry := range catalog.Plugins {
		asset, err := validateCatalogEntry(rootAbs, entry)
		if err != nil {
			return nil, fmt.Errorf("plugin %q invalid: %w", entry.ID, err)
		}
		reg.assets[asset.ID] = asset
	}

	return reg, nil
}

// Count returns the number of configured plugin assets.
func (r *PluginRegistry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.assets)
}

// ListPublic returns safe, cache-friendly plugin metadata.
func (r *PluginRegistry) ListPublic() []PluginDescriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]PluginDescriptor, 0, len(r.assets))
	for _, asset := range r.assets {
		out = append(out, asset.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns a clone of plugin metadata.
func (r *PluginRegistry) Get(id string) (*PluginAsset, bool) {
	if r == nil {
		return nil, false
	}
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	asset, ok := r.assets[normalized]
	if !ok {
		return nil, false
	}
	return asset.clone(), true
}

// ReadEncryptedBundle reads plugin bytes (encrypted or plain).
func (r *PluginRegistry) ReadEncryptedBundle(id string) ([]byte, *PluginAsset, error) {
	asset, ok := r.Get(id)
	if !ok {
		return nil, nil, os.ErrNotExist
	}
	bundlePath := asset.encryptedPath
	if bundlePath == "" {
		bundlePath = asset.plainPath
	}
	if bundlePath == "" {
		return nil, nil, fmt.Errorf("plugin %q has no bundle path", id)
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, nil, fmt.Errorf("read plugin %q: %w", id, err)
	}
	return data, asset, nil
}

// DecryptBundle reads a plugin artifact and decrypts it using the supplied
// X25519 private key when encrypted. Plain assets are returned as-is.
func (r *PluginRegistry) DecryptBundle(id string, recipientPrivateKey []byte) ([]byte, error) {
	data, asset, err := r.ReadEncryptedBundle(id)
	if err != nil {
		return nil, err
	}
	if asset == nil {
		return nil, os.ErrNotExist
	}
	if asset.encryptedPath == "" {
		return data, nil
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("plugin %q bundle is empty", id)
	}
	if len(recipientPrivateKey) == 0 {
		return nil, fmt.Errorf("plugin %q requires a decryption key", id)
	}
	if recipientPrivateKey == nil {
		recipientPrivateKey = []byte{}
	}
	if len(recipientPrivateKey) != 32 {
		return nil, fmt.Errorf("invalid decryption key for plugin %q: expected 32 bytes, got %d", id, len(recipientPrivateKey))
	}

	if data[0] == pluginBundleV2Format {
		if len(data) < pluginBundleV2Header {
			return nil, fmt.Errorf("plugin %q invalid V2 payload: too short", id)
		}
		decrypted, err := decryptPluginBundleV2(data, recipientPrivateKey, []byte(pluginCryptoContext))
		if err != nil {
			return nil, fmt.Errorf("plugin %q failed to decrypt (V2): %w", id, err)
		}
		return decrypted, nil
	}

	if len(data) >= pluginBundleV1Header {
		decrypted, err := decryptPluginBundleV1(data, recipientPrivateKey, []byte(pluginCryptoContext))
		if err != nil {
			return nil, fmt.Errorf("plugin %q failed to decrypt (V1): %w", id, err)
		}
		return decrypted, nil
	}

	return nil, fmt.Errorf("plugin %q has unrecognized encrypted format", id)
}

// DecryptStagedArtifactEnvelope decrypts an OrbPro staged JSON artifact envelope
// into raw WASM/plugin bytes using a 32-byte X25519 private key.
func DecryptStagedArtifactEnvelope(envelopeJSON []byte, recipientPrivateKey []byte) ([]byte, error) {
	if len(recipientPrivateKey) != 32 {
		return nil, fmt.Errorf("invalid recipient private key: expected 32 bytes, got %d", len(recipientPrivateKey))
	}
	if len(envelopeJSON) == 0 {
		return nil, errors.New("encrypted artifact envelope is empty")
	}

	var envelope stagedArtifactEnvelope
	if err := json.Unmarshal(envelopeJSON, &envelope); err != nil {
		return nil, fmt.Errorf("decode envelope json: %w", err)
	}

	scheme := strings.TrimSpace(envelope.KeyEncryption.Scheme)
	if scheme != "ecies-x25519-hkdf-sha256-aes-256-gcm" {
		return nil, fmt.Errorf("unsupported envelope scheme: %q", scheme)
	}
	algorithm := strings.TrimSpace(envelope.ContentEncryption.Algorithm)
	if algorithm != "" && algorithm != "aes-256-gcm" {
		return nil, fmt.Errorf("unsupported envelope content algorithm: %q", algorithm)
	}

	ephemeralPub, err := hex.DecodeString(strings.TrimSpace(envelope.KeyEncryption.EphemeralPublicKeyHex))
	if err != nil {
		return nil, fmt.Errorf("decode envelope ephemeral public key: %w", err)
	}
	if len(ephemeralPub) != 32 {
		return nil, fmt.Errorf("invalid envelope ephemeral public key length: %d", len(ephemeralPub))
	}

	sharedSecret, err := curve25519.X25519(recipientPrivateKey, ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("derive envelope shared secret: %w", err)
	}
	defer zeroBytes(sharedSecret)

	hkdfSalt, err := decodeBase64Loose(envelope.KeyEncryption.HKDFSaltB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope hkdf salt: %w", err)
	}
	wrapIV, err := decodeBase64Loose(envelope.KeyEncryption.WrapIvB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope wrap iv: %w", err)
	}
	wrappedKey, err := decodeBase64Loose(envelope.KeyEncryption.WrappedKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope wrapped key: %w", err)
	}
	wrappedKeyTag, err := decodeBase64Loose(envelope.KeyEncryption.WrappedKeyTagB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope wrapped key tag: %w", err)
	}

	var contentKey []byte
	var lastWrapErr error
	for _, wrapInfo := range envelopeKeyWrapInfos {
		candidateWrapKey, deriveErr := deriveHKDFSHA256(sharedSecret, hkdfSalt, wrapInfo, 32)
		if deriveErr != nil {
			lastWrapErr = deriveErr
			continue
		}

		key, unwrapErr := decryptAESGCM(candidateWrapKey, wrapIV, wrappedKey, wrappedKeyTag, nil)
		zeroBytes(candidateWrapKey)
		if unwrapErr != nil {
			lastWrapErr = unwrapErr
			continue
		}
		contentKey = key
		break
	}
	if len(contentKey) == 0 {
		if lastWrapErr == nil {
			lastWrapErr = errors.New("failed to unwrap content key")
		}
		return nil, fmt.Errorf("unwrap envelope content key: %w", lastWrapErr)
	}
	defer zeroBytes(contentKey)

	contentIV, err := decodeBase64Loose(envelope.ContentEncryption.IvB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope content iv: %w", err)
	}
	contentTag, err := decodeBase64Loose(envelope.ContentEncryption.TagB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope content tag: %w", err)
	}
	contentCiphertext, err := decodeBase64Loose(envelope.ContentEncryption.CiphertextB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope ciphertext: %w", err)
	}

	plaintext, err := decryptAESGCM(contentKey, contentIV, contentCiphertext, contentTag, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt envelope ciphertext: %w", err)
	}
	return plaintext, nil
}

// ReadBundleKey reads and normalizes the plugin's symmetric content key.
func (r *PluginRegistry) ReadBundleKey(id string) ([]byte, error) {
	asset, ok := r.Get(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(asset.keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key material for plugin %q: %w", id, err)
	}
	key, err := parseBundleKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid key material for plugin %q: %w", id, err)
	}
	return key, nil
}

// ParseX25519PublicKey accepts 32-byte X25519 public keys in hex or base64 form.
func ParseX25519PublicKey(encoded string) ([]byte, error) {
	raw := strings.TrimSpace(encoded)
	raw = strings.TrimPrefix(raw, "0x")
	if raw == "" {
		return nil, errors.New("client_x25519_pubkey is required")
	}
	if len(raw) == 64 {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.RawStdEncoding.DecodeString,
		base64.StdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
	} {
		decoded, err := dec(raw)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("client_x25519_pubkey must decode to exactly 32 bytes")
}

// BuildPluginKeyEnvelope wraps plugin key material to the client X25519 public key.
func BuildPluginKeyEnvelope(asset *PluginAsset, pluginKey, clientX25519Pub []byte, claims *CapabilityClaims, issuer string, now time.Time) (*PluginKeyEnvelope, error) {
	if asset == nil {
		return nil, errors.New("plugin asset is required")
	}
	if len(pluginKey) != 32 {
		return nil, fmt.Errorf("plugin key must be 32 bytes, got %d", len(pluginKey))
	}
	if len(clientX25519Pub) != 32 {
		return nil, fmt.Errorf("client x25519 public key must be 32 bytes, got %d", len(clientX25519Pub))
	}
	if claims == nil {
		return nil, errors.New("capability claims are required")
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		issuer = "spaceaware-license"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	exp := now.Unix() + defaultKeyEnvelopeLifetimeSec
	if claims.Exp > 0 && claims.Exp < exp {
		exp = claims.Exp
	}
	if exp <= now.Unix() {
		return nil, errors.New("capability token already expired")
	}

	serverPriv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, serverPriv); err != nil {
		return nil, fmt.Errorf("generate ephemeral server key: %w", err)
	}
	// H10: Zero ephemeral private key when done.
	defer zeroBytes(serverPriv)
	clampX25519PrivateKey(serverPriv)

	serverPub, err := curve25519.X25519(serverPriv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive server x25519 public key: %w", err)
	}
	sharedSecret, err := curve25519.X25519(serverPriv, clientX25519Pub)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}
	// H10: Zero shared secret when done.
	defer zeroBytes(sharedSecret)

	aad := buildPluginEnvelopeAAD(asset, claims, issuer, exp)
	wrapKey := derivePluginWrapKey(sharedSecret, aad)
	block, err := aes.NewCipher(wrapKey[:])
	if err != nil {
		return nil, fmt.Errorf("create key-wrap cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create key-wrap AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate key-wrap nonce: %w", err)
	}

	payload := pluginKeyEnvelopePayload{
		Key:           base64.RawStdEncoding.EncodeToString(pluginKey),
		PluginID:      asset.ID,
		Version:       asset.Version,
		RequiredScope: asset.RequiredScope,
		BundleSHA256:  asset.BundleSHA256,
		Sub:           claims.Sub,
		PeerID:        claims.PeerID,
		JTI:           claims.JTI,
		Exp:           exp,
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope payload: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(aad))

	return &PluginKeyEnvelope{
		PluginID:           asset.ID,
		Version:            asset.Version,
		RequiredScope:      asset.RequiredScope,
		BundleSHA256:       asset.BundleSHA256,
		Algorithm:          defaultKeyEnvelopeAlgorithm,
		ServerX25519PubKey: base64.RawStdEncoding.EncodeToString(serverPub),
		Nonce:              base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:         base64.RawStdEncoding.EncodeToString(ciphertext),
		AssociatedData:     aad,
		Issuer:             issuer,
		Subject:            claims.Sub,
		PeerID:             claims.PeerID,
		CapabilityTokenJTI: claims.JTI,
		ExpiresAt:          exp,
	}, nil
}

func DefaultPluginRoot(baseDataPath string) string {
	return filepath.Join(baseDataPath, "license", defaultPluginRootDirName)
}

func validateCatalogEntry(rootAbs string, entry PluginCatalogEntry) (*PluginAsset, error) {
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	if !pluginIDPattern.MatchString(id) {
		return nil, errors.New("id contains invalid characters")
	}
	version := strings.TrimSpace(entry.Version)
	if version == "" {
		return nil, errors.New("version is required")
	}
	requiredScope := strings.TrimSpace(entry.RequiredScope)
	if requiredScope == "" {
		requiredScope = defaultPluginRequiredScope
	}
	contentType := strings.TrimSpace(entry.ContentType)
	if contentType == "" {
		contentType = defaultPluginContentType
	}
	cacheControl := strings.TrimSpace(entry.CacheControl)
	if cacheControl == "" {
		cacheControl = defaultPluginCacheControl
	}

	asset := &PluginAsset{
		ID:              id,
		Version:         version,
		RequiredScope:   requiredScope,
		ContentType:     contentType,
		CacheControl:    cacheControl,
		SignatureHex:    entry.SignatureHex,
		SignerPubKeyHex: entry.SignerPubKeyHex,
		UploadedAt:      entry.UploadedAt,
	}

	// Plain (uploaded) plugins have plain_path; encrypted have encrypted_path + key_path.
	if plainRel := strings.TrimSpace(entry.PlainPath); plainRel != "" {
		plainPath, err := resolveRelativePath(rootAbs, plainRel)
		if err != nil {
			return nil, fmt.Errorf("plain_path: %w", err)
		}
		info, err := os.Stat(plainPath)
		if err != nil {
			return nil, fmt.Errorf("stat plain_path: %w", err)
		}
		if info.IsDir() {
			return nil, errors.New("plain_path must be a file")
		}
		sum, err := hashFileSHA256(plainPath)
		if err != nil {
			return nil, fmt.Errorf("hash plain_path: %w", err)
		}
		asset.plainPath = plainPath
		asset.BundleSHA256 = sum
		asset.SizeBytes = info.Size()
		return asset, nil
	}

	encryptedPath, err := resolveRelativePath(rootAbs, entry.EncryptedPath)
	if err != nil {
		return nil, fmt.Errorf("encrypted_path: %w", err)
	}
	keyPath, err := resolveRelativePath(rootAbs, entry.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("key_path: %w", err)
	}

	info, err := os.Stat(encryptedPath)
	if err != nil {
		return nil, fmt.Errorf("stat encrypted_path: %w", err)
	}
	if info.IsDir() {
		return nil, errors.New("encrypted_path must be a file")
	}
	sum, err := hashFileSHA256(encryptedPath)
	if err != nil {
		return nil, fmt.Errorf("hash encrypted_path: %w", err)
	}
	if keyInfo, err := os.Stat(keyPath); err != nil {
		return nil, fmt.Errorf("stat key_path: %w", err)
	} else if keyInfo.IsDir() {
		return nil, errors.New("key_path must be a file")
	}

	asset.encryptedPath = encryptedPath
	asset.keyPath = keyPath
	asset.BundleSHA256 = sum
	asset.SizeBytes = info.Size()
	return asset, nil
}

// AddPlugin writes a plain (unencrypted) WASM bundle to the registry.
func (r *PluginRegistry) AddPlugin(id, version string, wasmData []byte, signatureHex, signerPubKeyHex string) (*PluginAsset, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("plugin id is required")
	}
	if !pluginIDPattern.MatchString(id) {
		return nil, errors.New("plugin id contains invalid characters")
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, errors.New("version is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pluginDir := filepath.Join(r.rootPath, id)
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		return nil, fmt.Errorf("create plugin directory: %w", err)
	}

	bundlePath := filepath.Join(pluginDir, "bundle.wasm")
	if err := os.WriteFile(bundlePath, wasmData, 0600); err != nil {
		return nil, fmt.Errorf("write plugin bundle: %w", err)
	}

	h := sha256.Sum256(wasmData)

	asset := &PluginAsset{
		ID:              id,
		Version:         version,
		RequiredScope:   defaultPluginRequiredScope,
		ContentType:     defaultPluginContentType,
		CacheControl:    defaultPluginCacheControl,
		BundleSHA256:    hex.EncodeToString(h[:]),
		SizeBytes:       int64(len(wasmData)),
		SignatureHex:    signatureHex,
		SignerPubKeyHex: signerPubKeyHex,
		UploadedAt:      time.Now().UTC().Format(time.RFC3339),
		plainPath:       bundlePath,
	}

	r.assets[id] = asset
	if err := r.saveCatalogLocked(); err != nil {
		// Roll back on catalog save failure.
		delete(r.assets, id)
		_ = os.Remove(bundlePath)
		return nil, fmt.Errorf("save catalog: %w", err)
	}
	return asset.clone(), nil
}

// saveCatalogLocked writes catalog.json from the current in-memory assets.
// Caller must hold r.mu.
func (r *PluginRegistry) saveCatalogLocked() error {
	entries := make([]PluginCatalogEntry, 0, len(r.assets))
	for _, a := range r.assets {
		entry := PluginCatalogEntry{
			ID:              a.ID,
			Version:         a.Version,
			RequiredScope:   a.RequiredScope,
			ContentType:     a.ContentType,
			CacheControl:    a.CacheControl,
			SignatureHex:    a.SignatureHex,
			SignerPubKeyHex: a.SignerPubKeyHex,
			UploadedAt:      a.UploadedAt,
		}
		if a.plainPath != "" {
			rel, err := filepath.Rel(r.rootPath, a.plainPath)
			if err != nil {
				return fmt.Errorf("relativize plain path for %q: %w", a.ID, err)
			}
			entry.PlainPath = rel
		} else {
			if a.encryptedPath != "" {
				rel, err := filepath.Rel(r.rootPath, a.encryptedPath)
				if err != nil {
					return fmt.Errorf("relativize encrypted path for %q: %w", a.ID, err)
				}
				entry.EncryptedPath = rel
			}
			if a.keyPath != "" {
				rel, err := filepath.Rel(r.rootPath, a.keyPath)
				if err != nil {
					return fmt.Errorf("relativize key path for %q: %w", a.ID, err)
				}
				entry.KeyPath = rel
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	catalog := PluginCatalogFile{Plugins: entries}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}
	catalogPath := filepath.Join(r.rootPath, defaultPluginCatalogFile)
	return os.WriteFile(catalogPath, data, 0600)
}

func resolveRelativePath(rootAbs, relPath string) (string, error) {
	rel := strings.TrimSpace(relPath)
	if rel == "" {
		return "", errors.New("value is required")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("must be relative to plugin root")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == string(filepath.Separator) {
		return "", errors.New("invalid relative path")
	}
	abs := filepath.Join(rootAbs, clean)
	abs = filepath.Clean(abs)
	prefix := rootAbs + string(filepath.Separator)
	if abs != rootAbs && !strings.HasPrefix(abs, prefix) {
		return "", errors.New("path escapes plugin root")
	}
	return abs, nil
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseBundleKey(raw []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" {
		trimmedNoPrefix := strings.TrimPrefix(trimmed, "0x")
		if decoded, err := hex.DecodeString(trimmedNoPrefix); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
		for _, dec := range []func(string) ([]byte, error){
			base64.RawStdEncoding.DecodeString,
			base64.StdEncoding.DecodeString,
			base64.RawURLEncoding.DecodeString,
			base64.URLEncoding.DecodeString,
		} {
			decoded, err := dec(trimmed)
			if err == nil && len(decoded) == 32 {
				return decoded, nil
			}
		}
	}
	if len(raw) == 32 {
		out := make([]byte, 32)
		copy(out, raw)
		return out, nil
	}
	return nil, errors.New("key must be 32-byte raw, hex, or base64")
}

func decryptPluginBundleV2(encrypted []byte, recipientPrivateKey []byte, context []byte) ([]byte, error) {
	ephemeralPublic := encrypted[1:33]
	iv := encrypted[33:45]
	tag := encrypted[45:61]
	ciphertext := encrypted[61:]

	sharedSecret, err := curve25519.X25519(recipientPrivateKey, ephemeralPublic)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}
	defer zeroBytes(sharedSecret)

	symmetricKey, err := derivePluginBundleKey(sharedSecret, context)
	if err != nil {
		return nil, fmt.Errorf("derive symmetric key: %w", err)
	}
	defer zeroBytes(symmetricKey)

	block, err := aes.NewCipher(symmetricKey)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	sealed := make([]byte, len(ciphertext)+len(tag))
	copy(sealed, ciphertext)
	copy(sealed[len(ciphertext):], tag)

	plaintext, err := gcm.Open(nil, iv, sealed, ephemeralPublic)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM auth failed: %w", err)
	}
	return plaintext, nil
}

func decryptPluginBundleV1(encrypted []byte, recipientPrivateKey []byte, context []byte) ([]byte, error) {
	ephemeralPublic := encrypted[:32]
	iv := encrypted[32:48]
	expectedMac := encrypted[48:80]
	ciphertext := encrypted[80:]

	sharedSecret, err := curve25519.X25519(recipientPrivateKey, ephemeralPublic)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}
	defer zeroBytes(sharedSecret)

	symmetricKey, err := derivePluginBundleKey(sharedSecret, context)
	if err != nil {
		return nil, fmt.Errorf("derive symmetric key: %w", err)
	}
	defer zeroBytes(symmetricKey)

	authData := make([]byte, len(encrypted)-48)
	copy(authData, ephemeralPublic)
	copy(authData[32:], iv)
	copy(authData[48:], ciphertext)

	mac := hmac.New(sha256.New, symmetricKey)
	_, _ = mac.Write(authData)
	if !hmac.Equal(mac.Sum(nil), expectedMac) {
		return nil, errors.New("HMAC verification failed")
	}

	block, err := aes.NewCipher(symmetricKey)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	stream := cipher.NewCTR(block, iv)
	plaintext := make([]byte, len(ciphertext))
	stream.XORKeyStream(plaintext, ciphertext)
	return plaintext, nil
}

func decodeBase64Loose(value string) ([]byte, error) {
	normalized := strings.TrimSpace(value)
	normalized = strings.ReplaceAll(normalized, "-", "+")
	normalized = strings.ReplaceAll(normalized, "_", "/")
	if normalized == "" {
		return nil, errors.New("empty base64 value")
	}
	return base64.StdEncoding.DecodeString(normalized)
}

func deriveHKDFSHA256(secret []byte, salt []byte, info []byte, outLen int) ([]byte, error) {
	if outLen <= 0 {
		return nil, errors.New("invalid hkdf output length")
	}
	out := make([]byte, outLen)
	kdf := hkdf.New(sha256.New, secret, salt, info)
	if _, err := io.ReadFull(kdf, out); err != nil {
		return nil, fmt.Errorf("hkdf read: %w", err)
	}
	return out, nil
}

func decryptAESGCM(key []byte, iv []byte, ciphertext []byte, tag []byte, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	if len(iv) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid gcm iv length: expected %d, got %d", gcm.NonceSize(), len(iv))
	}
	if len(tag) != gcm.Overhead() {
		return nil, fmt.Errorf("invalid gcm tag length: expected %d, got %d", gcm.Overhead(), len(tag))
	}
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	plaintext, err := gcm.Open(nil, iv, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm auth failed: %w", err)
	}
	return plaintext, nil
}

func derivePluginBundleKey(sharedSecret []byte, context []byte) ([]byte, error) {
	k := make([]byte, 32)
	kdf := hkdf.New(sha256.New, sharedSecret, make([]byte, 32), context)
	if _, err := io.ReadFull(kdf, k); err != nil {
		return nil, fmt.Errorf("hkdf read: %w", err)
	}
	return k, nil
}

func clampX25519PrivateKey(priv []byte) {
	if len(priv) != 32 {
		return
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
}

func buildPluginEnvelopeAAD(asset *PluginAsset, claims *CapabilityClaims, issuer string, exp int64) string {
	return fmt.Sprintf(
		"iss=%s|sub=%s|peer=%s|jti=%s|plugin=%s|version=%s|sha256=%s|scope=%s|exp=%d",
		issuer,
		claims.Sub,
		claims.PeerID,
		claims.JTI,
		asset.ID,
		asset.Version,
		asset.BundleSHA256,
		asset.RequiredScope,
		exp,
	)
}

// H12: Use proper HKDF (RFC 5869) instead of simple SHA-256 concatenation
// for key derivation from the shared secret.
func derivePluginWrapKey(sharedSecret []byte, aad string) [32]byte {
	info := []byte("sdn-plugin-key-wrap:" + aad)
	salt := []byte("sdn-plugin-key-v1")
	kdf := hkdf.New(sha256.New, sharedSecret, salt, info)
	var key [32]byte
	if _, err := io.ReadFull(kdf, key[:]); err != nil {
		// hkdf.Read should never fail for valid inputs; panic indicates a bug.
		panic("hkdf read failed: " + err.Error())
	}
	return key
}
