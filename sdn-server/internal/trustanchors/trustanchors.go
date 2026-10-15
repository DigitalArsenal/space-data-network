// Package trustanchors is the node's view of deployment/identity/anchors.json:
// the ONE committed file that names the production provider identity this
// network trusts (its PeerID, keys and addresses), the fleet update roots, and
// the denylist of the identity it replaced.
//
// SEC-001: the BIP-39 seed behind the previous sdn.spaceaware.io identity was
// committed to the public repository. Every value derived from it is on the
// denylist here, and the node refuses to bootstrap to, connect to, trust, or
// accept grants, provider records or update signatures from any of them —
// whatever a config file, a persisted registry or a remote peer says.
//
// anchors.json in this directory is a byte-for-byte copy of
// deployment/identity/anchors.json, kept in step by
// `node deployment/identity/sync-anchors.mjs` and checked by its --check mode
// (go:embed cannot reach outside the module).
package trustanchors

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

//go:embed anchors.json
var anchorsJSON []byte

// EmbeddedJSON returns a copy of the compiled-in anchors file. Tests build a
// throwaway denylist from it (the real denylisted private keys never exist in
// a test) and install it with Use.
func EmbeddedJSON() []byte { return append([]byte(nil), anchorsJSON...) }

// Schema is the anchors file schema this package reads.
const Schema = "org.spacedatanetwork.identity-anchors.v1"

// PlaceholderPrefix marks an anchor the owner has not filled in yet.
const PlaceholderPrefix = "PLACEHOLDER-"

// ErrDenied is wrapped by every refusal of a denylisted identity value.
var ErrDenied = errors.New("SEC-001: denylisted identity (its seed was published)")

// Endpoint is one trusted peer: its identity and the transports it is dialled on.
type Endpoint struct {
	Name                string   `json:"name"`
	PeerID              string   `json:"peerId"`
	BootstrapTransports []string `json:"bootstrapTransports"`
	BrowserTransports   []string `json:"browserTransports"`
}

// Provider is the production provider identity (sdn.spaceaware.io).
type Provider struct {
	Endpoint
	IdentityPublicKey      string `json:"identityPublicKey"`
	XPub                   string `json:"xpub"`
	SigningPublicKey       string `json:"signingPublicKey"`
	EncryptionPublicKey    string `json:"encryptionPublicKey"`
	PublisherPublicKey     string `json:"publisherPublicKey"`
	X25519PublicKey        string `json:"x25519PublicKey"`
	GrantVerifierPublicKey string `json:"grantVerifierPublicKey"`
	RootAdminPublicKey     string `json:"rootAdminPublicKey"`
	DiscoveryCID           string `json:"discoveryCid"`
}

// Denylist is every public value of a retired, compromised identity.
type Denylist struct {
	Reason              string   `json:"reason"`
	PeerIDs             []string `json:"peerIds"`
	Secp256k1PublicKeys []string `json:"secp256k1PublicKeys"`
	Ed25519PublicKeys   []string `json:"ed25519PublicKeys"`
	// X25519PublicKeys: the retired identity's wrapping key(s), which no public
	// surface carried; the owner adds them from a forensic export
	// (ROTATION.md step 5, new-node-identity.mjs --describe-backup).
	X25519PublicKeys []string `json:"x25519PublicKeys"`
	XPubs            []string `json:"xpubs"`
	UpdateRootKeyIDs []string `json:"updateRootKeyIds"`
	DiscoveryCIDs    []string `json:"discoveryCids"`
	ChainAddresses   []string `json:"chainAddresses"`
}

// Anchors is the parsed anchors file.
type Anchors struct {
	Schema      string            `json:"schema"`
	Status      string            `json:"status"`
	Rotation    string            `json:"rotation"`
	Provider    Provider          `json:"provider"`
	Peers       []Endpoint        `json:"peers"`
	UpdateRoots map[string]string `json:"updateRoots"`
	Denylist    Denylist          `json:"denylist"`

	denied map[string]struct{}
}

var (
	loadOnce sync.Once
	loaded   *Anchors
	loadErr  error
)

// Parse decodes an anchors file and indexes its denylist.
func Parse(data []byte) (*Anchors, error) {
	var a Anchors
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parse identity anchors: %w", err)
	}
	if a.Schema != Schema {
		return nil, fmt.Errorf("identity anchors schema %q, want %q", a.Schema, Schema)
	}
	a.denied = make(map[string]struct{})
	add := func(values []string, fold bool) {
		for _, v := range values {
			v = strings.TrimSpace(v)
			if fold {
				v = strings.ToLower(v)
			}
			if v != "" {
				a.denied[v] = struct{}{}
			}
		}
	}
	add(a.Denylist.PeerIDs, false)
	add(a.Denylist.Secp256k1PublicKeys, true)
	add(a.Denylist.Ed25519PublicKeys, true)
	add(a.Denylist.X25519PublicKeys, true)
	add(a.Denylist.XPubs, false)
	add(a.Denylist.UpdateRootKeyIDs, true)
	add(a.Denylist.DiscoveryCIDs, false)
	add(a.Denylist.ChainAddresses, false)
	if len(a.Denylist.PeerIDs) == 0 || len(a.Denylist.Ed25519PublicKeys) == 0 || len(a.Denylist.UpdateRootKeyIDs) == 0 {
		return nil, errors.New("identity anchors: the SEC-001 denylist must not be empty")
	}
	return &a, nil
}

// Load returns the anchors compiled into this binary.
func Load() *Anchors {
	loadOnce.Do(func() {
		loaded, loadErr = Parse(anchorsJSON)
	})
	if loadErr != nil {
		// The embedded file is checked by this package's tests and by
		// sync-anchors --check; a binary that cannot read it was never tested.
		panic(loadErr)
	}
	return loaded
}

// IsPlaceholder reports whether an anchor value has not been filled in.
func IsPlaceholder(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), PlaceholderPrefix)
}

// IsDenied reports whether a value (PeerID, hex public key, xpub, update-root
// key id, discovery CID or chain address) belongs to a denylisted identity.
func (a *Anchors) IsDenied(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, ok := a.denied[value]; ok {
		return true
	}
	_, ok := a.denied[strings.ToLower(value)]
	return ok
}

// IsDenied reports whether value is denylisted by the compiled-in anchors.
func IsDenied(value string) bool { return Load().IsDenied(value) }

// IsDeniedPeer reports whether a libp2p peer is a denylisted identity.
func IsDeniedPeer(p peer.ID) bool {
	if p == "" {
		return false
	}
	return Load().IsDenied(p.String())
}

// IsDeniedKey reports whether raw public key bytes (33-byte secp256k1 or
// 32-byte Ed25519/X25519) belong to a denylisted identity.
func IsDeniedKey(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	a := Load()
	if a.IsDenied(hex.EncodeToString(raw)) {
		return true
	}
	// The same secp256k1 point uncompressed (04 || X || Y): compare its
	// compressed form (02/03 by the parity of Y, then X).
	if len(raw) == 65 && raw[0] == 0x04 {
		prefix := byte(0x02)
		if raw[64]&1 == 1 {
			prefix = 0x03
		}
		return a.IsDenied(hex.EncodeToString(append([]byte{prefix}, raw[1:33]...)))
	}
	return false
}

// CheckPeer returns an ErrDenied-wrapping error for a denylisted peer.
func CheckPeer(p peer.ID, what string) error {
	if IsDeniedPeer(p) {
		return fmt.Errorf("%s %s: %w", what, p, ErrDenied)
	}
	return nil
}

// CheckValue returns an ErrDenied-wrapping error for a denylisted value.
func CheckValue(value, what string) error {
	if IsDenied(value) {
		return fmt.Errorf("%s: %w", what, ErrDenied)
	}
	return nil
}

// BootstrapAddresses returns the provider's and the other trusted peers'
// libp2p bootstrap multiaddrs (transport + /p2p/<PeerID>). An identity still
// at its placeholder, or one on the denylist, contributes nothing.
func (a *Anchors) BootstrapAddresses() []string {
	var out []string
	appendEndpoint := func(e Endpoint) {
		if e.PeerID == "" || IsPlaceholder(e.PeerID) || a.IsDenied(e.PeerID) {
			return
		}
		if _, err := peer.Decode(e.PeerID); err != nil {
			return
		}
		for _, transport := range e.BootstrapTransports {
			transport = strings.TrimRight(strings.TrimSpace(transport), "/")
			if transport != "" {
				out = append(out, transport+"/p2p/"+e.PeerID)
			}
		}
	}
	appendEndpoint(a.Provider.Endpoint)
	for _, e := range a.Peers {
		appendEndpoint(e)
	}
	return out
}

// BootstrapAddresses returns the compiled-in bootstrap multiaddrs.
func BootstrapAddresses() []string { return Load().BootstrapAddresses() }

// Placeholders lists every anchor still at its placeholder (JSON-ish paths).
func (a *Anchors) Placeholders() []string {
	var out []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch t := v.(type) {
		case string:
			if IsPlaceholder(t) {
				out = append(out, path)
			}
		case []any:
			for i, item := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), item)
			}
		case map[string]any:
			for k, item := range t {
				if IsPlaceholder(k) {
					out = append(out, path+"."+k)
				}
				walk(strings.TrimPrefix(path+"."+k, "."), item)
			}
		}
	}
	var raw any
	data, _ := json.Marshal(a)
	_ = json.Unmarshal(data, &raw)
	walk("", raw)
	return out
}

// ReleaseReady returns an error while any anchor is a placeholder or the file
// is not marked released: the gate a production release must pass.
func (a *Anchors) ReleaseReady() error {
	var problems []string
	if a.Status != "released" {
		problems = append(problems, fmt.Sprintf("status is %q, not \"released\"", a.Status))
	}
	for _, p := range a.Placeholders() {
		problems = append(problems, p+" is a placeholder")
	}
	for _, v := range []string{a.Provider.PeerID, a.Provider.IdentityPublicKey, a.Provider.XPub, a.Provider.PublisherPublicKey, a.Provider.GrantVerifierPublicKey} {
		if a.IsDenied(v) {
			problems = append(problems, "a provider anchor is denylisted")
		}
	}
	for id := range a.UpdateRoots {
		if a.IsDenied(id) {
			problems = append(problems, "update root "+id+" is denylisted")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("identity anchors are not release-ready: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Use replaces the compiled-in anchors until the returned restore func runs.
// It exists for tests that need a denylist naming a throwaway identity (the
// real denylisted seed is never available to a test, by design).
func Use(a *Anchors) (restore func()) {
	Load()
	previous := loaded
	loaded = a
	return func() { loaded = previous }
}
