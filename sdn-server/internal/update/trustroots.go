package update

// A release can replace the node's update roots (owner 2026-10-09: releases are
// signed with a distribution key entered each time in the updater module, not
// with host-01's node key). Roots live outside the swapped payload, so nothing
// else can move them: a binary release signed by a CURRENT root carries
// trust_roots, and once its swap succeeds the node writes them to the file it
// reads roots from. The roots are signed policy, not payload: rolling the
// binary back leaves them in place.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sec001"
)

// rootKeyIDLen is the length of a root's key id: the same 12 hex characters
// updatesign.Signer.KeyID derives.
const rootKeyIDLen = 12

// RootKeyID is the key id a root is listed under: the first 12 hex characters
// of sha256 over the raw Ed25519 public key.
func RootKeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:rootKeyIDLen]
}

// THE COMPILED-IN ROOTS. default_update_roots.json is a copy of
// deployment/release/fleet-trust-roots.json (TestDefaultUpdateRootsMatchTheFleetRoots
// holds the two equal; go:embed cannot reach outside this module). It is a
// safety net for a bundle whose trust/update-roots.json is missing, and
// nothing more: LoadTrustRoots reads it ONLY when that file is absent. A
// present file always wins, so a release that rotates the roots (trust_roots)
// can still drop one of these.
//
//go:embed default_update_roots.json
var defaultUpdateRootsJSON []byte

// DefaultTrustRoots returns the compiled-in update roots.
func DefaultTrustRoots() (TrustedRoots, error) {
	return parseTrustRoots(defaultUpdateRootsJSON, "compiled-in update trust roots")
}

// parseTrustRoots decodes a roots document and refuses an empty set or a
// SEC-001 root. The document must be exactly one JSON object with no key
// listed twice (strictjson.go): a repeated id would keep only its last key,
// and anything after the first value used to be ignored.
func parseTrustRoots(data []byte, source string) (TrustedRoots, error) {
	if err := assertStrictJSONDocument(data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	var roots TrustedRoots
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&roots); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("%s are empty", source)
	}
	if err := refuseDeniedRoots(roots); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return roots, nil
}

// deniedRootKeyIDs is the key id of every SEC-001 Ed25519 key. The leaked
// node key 0d80e1fd… is d4a971a7e534, the root every bundle up to v1.1.1
// shipped.
var deniedRootKeyIDs = func() map[string]bool {
	ids := make(map[string]bool)
	for _, keyHex := range sec001.Ed25519Keys() {
		raw, err := hex.DecodeString(keyHex)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			panic("internal/sec001 holds a malformed Ed25519 key: " + keyHex)
		}
		ids[RootKeyID(raw)] = true
	}
	return ids
}()

// ErrDeniedRoot marks a root (or signing key) refused because it belongs to
// the identity leaked in SEC-001.
var ErrDeniedRoot = errors.New("is a key of the identity leaked in SEC-001, which was derived from a published seed: anyone can sign as it, so it can never be an update trust root")

// refuseDeniedRoot refuses a root listed under a SEC-001 key id, or whose key
// is a SEC-001 key, whatever id it is listed under. encoded may be empty when
// only the id is known.
func refuseDeniedRoot(id, encoded string) error {
	if deniedRootKeyIDs[strings.ToLower(strings.TrimSpace(id))] {
		return fmt.Errorf("update root %s %w", id, ErrDeniedRoot)
	}
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	if sec001.DeniedEd25519Hex(encoded) {
		return fmt.Errorf("update root %s (raw hex key) %w", id, ErrDeniedRoot)
	}
	// A root that does not decode is refused where it is used; here only a
	// decodable SEC-001 key matters.
	if key, err := decodeTrustedPublicKey(encoded); err == nil && sec001.DeniedEd25519(key) {
		return fmt.Errorf("update root %s (key id %s) %w", id, RootKeyID(key), ErrDeniedRoot)
	}
	return nil
}

func refuseDeniedRoots(roots TrustedRoots) error {
	for id, encoded := range roots {
		if err := refuseDeniedRoot(id, encoded); err != nil {
			return err
		}
	}
	return nil
}

// assertTrustRoots checks a release's replacement roots: at least one, each a
// valid Ed25519 key listed under its own key id, none of them a SEC-001 key,
// and only on a release that goes through the swap.
func (m *Manifest) assertTrustRoots() error {
	if m.TrustRoots == nil {
		return nil
	}
	if m.IsUIPackage() {
		return errors.New("a UI package cannot replace the update roots")
	}
	if len(m.TrustRoots) == 0 {
		return errors.New("a release that replaces the update roots must name at least one")
	}
	for id, encoded := range m.TrustRoots {
		if err := refuseDeniedRoot(id, encoded); err != nil {
			return fmt.Errorf("this release's trust_roots: %w", err)
		}
		key, err := decodeTrustedPublicKey(encoded)
		if err != nil {
			return fmt.Errorf("update root %s: %w", id, err)
		}
		if RootKeyID(key) != id {
			return fmt.Errorf("update root %s is listed under the wrong key id (its key id is %s)", id, RootKeyID(key))
		}
	}
	return nil
}

// trustRootsPath is the file LoadTrustRoots reads.
func trustRootsPath(paths Paths) string {
	if path := strings.TrimSpace(os.Getenv(TrustRootsEnv)); path != "" {
		return path
	}
	return paths.Trust
}

// installTrustRoots replaces the node's update roots atomically with those of
// m. A home bundle keeps m itself instead (recordRotation): its roots are
// computed from the installed roots through the signed rotations.
func installTrustRoots(paths Paths, m *Manifest) error {
	roots := m.TrustRoots
	if err := refuseDeniedRoots(roots); err != nil {
		return err
	}
	if paths.IsHome() {
		return recordRotation(paths, m)
	}
	data, err := json.MarshalIndent(roots, "", "  ")
	if err != nil {
		return err
	}
	path := trustRootsPath(paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
