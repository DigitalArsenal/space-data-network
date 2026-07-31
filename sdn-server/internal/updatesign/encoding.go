package updatesign

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
)

// stdBase64 is the encoding both verifiers use for the signature field:
// desktop/src/sdn-updater/manifest.js reads it with Buffer.from(sig, 'base64')
// and internal/update/manifest.go with base64.StdEncoding. Standard alphabet,
// padded — NOT base64url, which the desktop side would decode to different
// bytes for any signature containing a '-' or '_' position.
func stdBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// spkiBase64 renders an Ed25519 public key in the form the UPDATE TRUST STORE
// must hold.
//
// This is not cosmetic. The two verifiers accept different sets of encodings:
// internal/update/manifest.go's decodeTrustedPublicKey takes SPKI DER, a raw
// 32-byte key, or hex; the desktop verifier's publicKeyFromBase64
// (desktop/src/sdn-updater/manifest.js:70-76) constructs the key with
// type:'spki', format:'der' and so accepts SPKI DER ONLY. A trust root
// installed as raw hex therefore works on the fleet and fails on the desktop —
// a split-brain trust store that would look correct on whichever side an
// operator happened to test. Emitting the strictest form keeps one value
// working everywhere.
func spkiBase64(pub ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// MarshalPKIXPublicKey has no failure mode for a well-formed Ed25519
		// key, and NewSigner has already established that this one is.
		return ""
	}
	return base64.StdEncoding.EncodeToString(der)
}
