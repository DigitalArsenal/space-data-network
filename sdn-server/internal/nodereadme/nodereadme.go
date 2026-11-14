// Package nodereadme is the Space Data Network README every node pins: a
// short Markdown document, for a newcomer, of what the network is and how it
// works. It is embedded in the binary, so its bytes, and therefore its CID,
// are the same on every node that runs the same release.
package nodereadme

import _ "embed"

// Name is the file name the document is added and served under.
const Name = "README.md"

//go:embed README.md
var text []byte

// Bytes returns a copy of the document.
func Bytes() []byte { return append([]byte(nil), text...) }
