package auth

import "strings"

// IsValidXPub performs basic validation on a BIP-32 extended public key string.
// Standard xpubs are Base58Check-encoded and start with "xpub".
func IsValidXPub(xpub string) bool {
	xpub = strings.TrimSpace(xpub)
	return len(xpub) > 20 && strings.HasPrefix(xpub, "xpub")
}
