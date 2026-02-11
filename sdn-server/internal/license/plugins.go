package license

import (
	"crypto/aes"
	"crypto/cipher"
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
)

const (
	defaultPluginCatalogFile      = "catalog.json"
	defaultPluginRootDirName      = "plugins"
	defaultPluginContentType      = "application/wasm"
	defaultPluginCacheControl     = "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400"
	defaultPluginRequiredScope    = "orbpro:base"
	defaultKeyEnvelopeAlgorithm   = "X25519+SHA256+AES-256-GCM"
	defaultKeyEnvelopeLifetimeSec = int64(120)
)

var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// PluginCatalogFile is the on-disk plugin catalog format.
type PluginCatalogFile struct {
	Plugins []PluginCatalogEntry `json:"plugins"`
}

// PluginCatalogEntry describes an encrypted plugin bundle and its key material location.
type PluginCatalogEntry struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	RequiredScope string `json:"required_scope"`
	EncryptedPath string `json:"encrypted_path"`
	KeyPath       string `json:"key_path"`
	ContentType   string `json:"content_type,omitempty"`
	CacheControl  string `json:"cache_control,omitempty"`
}

// PluginDescriptor is safe to return publicly (no key path information).
type PluginDescriptor struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	RequiredScope string `json:"required_scope"`
	ContentType   string `json:"content_type"`
	CacheControl  string `json:"cache_control"`
	BundleSHA256  string `json:"bundle_sha256"`
	SizeBytes     int64  `json:"size_bytes"`
}

// PluginAsset is an in-memory validated plugin metadata record.
type PluginAsset struct {
	ID            string
	Version       string
	RequiredScope string
	ContentType   string
	CacheControl  string
	BundleSHA256  string
	SizeBytes     int64

	encryptedPath string
	keyPath       string
}

func (a *PluginAsset) clone() *PluginAsset {
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

func (a *PluginAsset) Descriptor() PluginDescriptor {
	return PluginDescriptor{
		ID:            a.ID,
		Version:       a.Version,
		RequiredScope: a.RequiredScope,
		ContentType:   a.ContentType,
		CacheControl:  a.CacheControl,
		BundleSHA256:  a.BundleSHA256,
		SizeBytes:     a.SizeBytes,
	}
}

// PluginRegistry manages encrypted plugin artifacts and key material pointers.
type PluginRegistry struct {
	rootPath string

	mu     sync.RWMutex
	assets map[string]*PluginAsset
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

// ReadEncryptedBundle reads the encrypted plugin bytes.
func (r *PluginRegistry) ReadEncryptedBundle(id string) ([]byte, *PluginAsset, error) {
	asset, ok := r.Get(id)
	if !ok {
		return nil, nil, os.ErrNotExist
	}
	data, err := os.ReadFile(asset.encryptedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read encrypted plugin %q: %w", id, err)
	}
	return data, asset, nil
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
	clampX25519PrivateKey(serverPriv)

	serverPub, err := curve25519.X25519(serverPriv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive server x25519 public key: %w", err)
	}
	sharedSecret, err := curve25519.X25519(serverPriv, clientX25519Pub)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}

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
	encryptedPath, err := resolveRelativePath(rootAbs, entry.EncryptedPath)
	if err != nil {
		return nil, fmt.Errorf("encrypted_path: %w", err)
	}
	keyPath, err := resolveRelativePath(rootAbs, entry.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("key_path: %w", err)
	}
	contentType := strings.TrimSpace(entry.ContentType)
	if contentType == "" {
		contentType = defaultPluginContentType
	}
	cacheControl := strings.TrimSpace(entry.CacheControl)
	if cacheControl == "" {
		cacheControl = defaultPluginCacheControl
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

	return &PluginAsset{
		ID:            id,
		Version:       version,
		RequiredScope: requiredScope,
		ContentType:   contentType,
		CacheControl:  cacheControl,
		BundleSHA256:  sum,
		SizeBytes:     info.Size(),
		encryptedPath: encryptedPath,
		keyPath:       keyPath,
	}, nil
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

func derivePluginWrapKey(sharedSecret []byte, aad string) [32]byte {
	input := make([]byte, 0, len(sharedSecret)+len(aad))
	input = append(input, sharedSecret...)
	input = append(input, []byte(aad)...)
	return sha256.Sum256(input)
}
