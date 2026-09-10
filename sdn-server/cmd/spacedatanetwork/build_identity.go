package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// servingAPI names the serving routes this build answers. A fleet harness
// refuses a mixed fleet by comparing build_sha256 across nodes instead of
// discovering an older binary through an HTTP 404 on a newer route
// (node-transfer tests, 2026-09-10: Vimpel had no /api/v1/data/remote).
var servingAPI = []string{
	"catalog",
	"catalog.index_state",
	"data.query",
	"data.search",
	"data.remote",
	"publish.batch",
}

// recordForm is the canonical stored record form every stream reader hands
// to consumers: a bare finished FlatBuffer, file identifier at byte 4.
const recordForm = "bare"

// executableSHA256 hashes the running executable once. An unreadable
// executable yields "" rather than a fabricated identity.
var executableSHA256 = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
})
