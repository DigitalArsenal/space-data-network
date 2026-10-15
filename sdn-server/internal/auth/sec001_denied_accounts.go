package auth

import "github.com/spacedatanetwork/sdn-server/internal/trustanchors"

// deniedAccount reports whether a stored or configured account belongs to a
// SEC-001 denylisted identity: its xpub or bound signing key derives from the
// published seed. Root sign-in persists the node's own account
// (RecordRootSignIn) at root trust, and operators enrolled the old root keys
// as admins, so after the re-key those rows would still sign in whoever holds
// the seed. Sign-in lookups (GetUser, GetUserBySigningPubKey) treat such an
// account as unknown: the challenge it gets can never be verified, and the row
// stays visible to `admin list` for an operator to remove (verifier r2).
func deniedAccount(u *User) bool {
	if u == nil {
		return false
	}
	if trustanchors.IsDenied(u.XPub) || trustanchors.IsDenied(u.SigningPubKeyHex) {
		log.Warnf("SEC-001: refusing sign-in for account %q: its xpub or signing key belongs to a denylisted identity; remove it with `spacedatanetwork admin remove`", u.Name)
		return true
	}
	return false
}
