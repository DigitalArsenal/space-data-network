package auth

import "github.com/spacedatanetwork/sdn-server/internal/sec001"

// SEC-001 containment hotfix (host-01): the leaked node identity's root xpub
// and Ed25519 keys may never authorize a module publish, whatever rows the
// user store holds: the module-publish protocol is reachable over public
// libp2p, so the loopback rule for root sign-in does not cover it. The list
// itself lives in internal/sec001, which the update lane reads too.

// DeniedPublishPrincipal reports whether xpub or signingPubKeyHex belongs to
// the leaked SEC-001 identity.
func DeniedPublishPrincipal(xpub, signingPubKeyHex string) bool {
	return sec001.DeniedXPub(xpub) || sec001.DeniedEd25519Hex(signingPubKeyHex)
}
