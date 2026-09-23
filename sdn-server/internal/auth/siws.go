package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// SIGN-IN WITH SOLANA — delegating the sealed-transport session key from an
// external Solana wallet (Phantom and other Wallet Standard wallets).
//
// A Solana address is an Ed25519 public key, so such a wallet can delegate the
// session key like the built-in wallet does. It must never do so by signing
// opaque bytes: raw Ed25519 from an external wallet is inadmissible as an SDN
// signature (the coincidence ban, hd-wallet-ui/external), because any site can
// ask it to sign anything. It signs this human-readable message instead, in
// the shape of EIP-4361 / Sign-In With Solana, which names the node's host so
// the person sees what they are authorizing. The node rebuilds the exact text
// from its own view of the request and verifies the signature over it.

// SIWSStatement is the fixed purpose line.
const SIWSStatement = "Authorize a browser session key to send signed, encrypted admin commands to this node."

// SIWSMessage is the text an external Solana wallet signs to delegate
// sessionPub. Every field is fixed by the node's request except the times,
// which the node bounds.
func SIWSMessage(domain, origin string, wallet ed25519.PublicKey, challenge []byte, sessionPub ed25519.PublicKey, issuedAt, expiresAt time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s wants you to administer this Space Data Network node with your Solana account:\n", domain)
	fmt.Fprintf(&b, "%s\n\n", Base58(wallet))
	fmt.Fprintf(&b, "%s\n\n", SIWSStatement)
	fmt.Fprintf(&b, "URI: %s\n", origin)
	b.WriteString("Version: 1\n")
	b.WriteString("Chain ID: solana\n")
	fmt.Fprintf(&b, "Nonce: %s\n", base64.RawURLEncoding.EncodeToString(challenge))
	fmt.Fprintf(&b, "Issued At: %s\n", issuedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	fmt.Fprintf(&b, "Expiration Time: %s\n", expiresAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	fmt.Fprintf(&b, "Session Key: %s", hex.EncodeToString(sessionPub))
	return b.String()
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Base58 is the Bitcoin/Solana base58 encoding (a Solana address is the
// base58 of its Ed25519 public key).
func Base58(b []byte) string {
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// DecodeBase58 reverses Base58.
func DecodeBase58(s string) ([]byte, error) {
	n := new(big.Int)
	base := big.NewInt(58)
	for _, c := range s {
		i := strings.IndexRune(base58Alphabet, c)
		if i < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", c)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(i)))
	}
	out := n.Bytes()
	for _, c := range s {
		if c != '1' {
			break
		}
		out = append([]byte{0}, out...)
	}
	return out, nil
}
