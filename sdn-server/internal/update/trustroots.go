package update

// A release can replace the node's update roots (owner 2026-10-09: releases are
// signed with a distribution key entered each time in the updater module, not
// with host-01's node key). Roots live outside the swapped payload, so nothing
// else can move them: a binary release signed by a CURRENT root carries
// trust_roots, and once its swap succeeds the node writes them to the file it
// reads roots from. The roots are signed policy, not payload: rolling the
// binary back leaves them in place.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// assertTrustRoots checks a release's replacement roots: at least one, each a
// valid Ed25519 key listed under its own key id, and only on a release that
// goes through the swap.
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

// installTrustRoots replaces the node's update roots atomically.
func installTrustRoots(paths Paths, roots TrustedRoots) error {
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
