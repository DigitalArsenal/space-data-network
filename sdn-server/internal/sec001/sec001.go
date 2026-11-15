// Package sec001 holds the public values of the node identity leaked in
// SEC-001. That identity was derived from a published seed, so anyone can sign
// as it: none of its keys may authorize anything, whatever a user store, a
// trust file or a signed manifest says.
//
// The values are public (deployment/identity/anchors.json denylist). They live
// in denylist.json in this leaf package, with no imports of the node's own, so
// every place that must refuse them reads ONE list: module publishing
// (internal/auth), the sealed transport, the update lane's trust roots
// (internal/update), and the release workflow's signing-key check
// (deployment/release/check-update-signing-key.mjs reads the same file).
package sec001

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed denylist.json
var denylistJSON []byte

type denylist struct {
	XPubs   []string `json:"xpubs"`
	Ed25519 []string `json:"ed25519"`
}

var deniedXPubs, deniedEd25519 = func() (map[string]bool, map[string]bool) {
	var list denylist
	if err := json.Unmarshal(denylistJSON, &list); err != nil {
		panic(fmt.Sprintf("internal/sec001/denylist.json: %v", err))
	}
	xpubs := make(map[string]bool, len(list.XPubs))
	for _, xpub := range list.XPubs {
		xpubs[strings.TrimSpace(xpub)] = true
	}
	keys := make(map[string]bool, len(list.Ed25519))
	for _, key := range list.Ed25519 {
		key = strings.ToLower(strings.TrimSpace(key))
		if raw, err := hex.DecodeString(key); err != nil || len(raw) != 32 {
			panic("internal/sec001/denylist.json holds a malformed Ed25519 key: " + key)
		}
		keys[key] = true
	}
	if len(xpubs) == 0 || len(keys) == 0 {
		panic("internal/sec001/denylist.json is empty")
	}
	return xpubs, keys
}()

// DeniedXPub reports whether xpub is the leaked identity's root xpub.
func DeniedXPub(xpub string) bool {
	return deniedXPubs[strings.TrimSpace(xpub)]
}

// DeniedEd25519Hex reports whether pubHex (raw 32-byte key, hex) is one of the
// leaked identity's Ed25519 keys.
func DeniedEd25519Hex(pubHex string) bool {
	return deniedEd25519[strings.ToLower(strings.TrimSpace(pubHex))]
}

// DeniedEd25519 reports whether pub (raw 32-byte key) is one of the leaked
// identity's Ed25519 keys.
func DeniedEd25519(pub []byte) bool {
	return deniedEd25519[hex.EncodeToString(pub)]
}

// Ed25519Keys returns the leaked Ed25519 keys (raw, lowercase hex), sorted.
func Ed25519Keys() []string {
	keys := make([]string, 0, len(deniedEd25519))
	for key := range deniedEd25519 {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
