//go:build windows || wasm || js

package node

// numFDs has no portable rlimit on these platforms; 0 lets the resource
// manager keep go-libp2p's scaled FD default, which is what kubo does too.
func numFDs() int { return 0 }
