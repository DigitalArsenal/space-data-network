package auth

import "strings"

// SEC-001 containment hotfix (host-01): the public values of the leaked node
// identity. Its root xpub and Ed25519 keys may never authorize a module
// publish, whatever rows the user store holds: the module-publish protocol is
// reachable over public libp2p, so the loopback rule for root sign-in does not
// cover it. Values are public (deployment/identity/anchors.json denylist).
var sec001DeniedXPubs = map[string]bool{
	"xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa": true,
}

var sec001DeniedEd25519 = map[string]bool{
	"0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c": true,
	"8e2f6db0048925ce413512df5b4261cee5ad7e04e65588d1a9af26cd4254fa2b": true,
	"ac42adf4cf2746567a9cd49ae1c5c57054810fd64186ca7d701fe24acf1e8c0d": true,
}

// DeniedPublishPrincipal reports whether xpub or signingPubKeyHex belongs to
// the leaked SEC-001 identity.
func DeniedPublishPrincipal(xpub, signingPubKeyHex string) bool {
	return sec001DeniedXPubs[strings.TrimSpace(xpub)] ||
		sec001DeniedEd25519[strings.ToLower(strings.TrimSpace(signingPubKeyHex))]
}
