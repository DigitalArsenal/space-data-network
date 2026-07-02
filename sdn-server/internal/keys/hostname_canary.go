package keys

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// hostnameCanaryFile is the file (inside the key directory) that stores a hash
// of the machine's hostname from the last successful start.
const hostnameCanaryFile = "hostname.hash"

// HostnameCanaryResult reports the outcome of a hostname-change check.
type HostnameCanaryResult struct {
	// Changed is true when the current hostname hash differs from the stored
	// one (i.e. the machine was renamed). It is false on the very first run
	// (no stored hash yet).
	Changed bool
	// FirstRun is true when there was no stored hash yet.
	FirstRun bool
	// CurrentHash is the hex SHA-256 of the current hostname.
	CurrentHash string
	// PreviousHash is the hex SHA-256 read from disk (empty on first run).
	PreviousHash string
}

func hostnameHashHex() string {
	h, _ := os.Hostname()
	sum := sha256.Sum256([]byte(strings.TrimSpace(h)))
	return hex.EncodeToString(sum[:])
}

// CheckAndUpdateHostnameCanary compares the current hostname hash against the
// one cached in keyDir and rewrites the cache to the current value.
//
// The hostname is intentionally NOT part of the at-rest encryption key (that is
// derived from stable hardware, see hardwareFingerprint). This canary exists
// only so a rename can be detected and surfaced to the operator, since a
// changed hostname is often a sign the box was cloned or re-provisioned.
func CheckAndUpdateHostnameCanary(keyDir string) (HostnameCanaryResult, error) {
	path := filepath.Join(keyDir, hostnameCanaryFile)
	current := hostnameHashHex()
	res := HostnameCanaryResult{CurrentHash: current}

	prev, err := os.ReadFile(path)
	switch {
	case err == nil:
		res.PreviousHash = strings.TrimSpace(string(prev))
		res.Changed = res.PreviousHash != "" && res.PreviousHash != current
	case os.IsNotExist(err):
		res.FirstRun = true
	default:
		return res, err
	}

	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return res, err
	}
	if writeErr := os.WriteFile(path, []byte(current+"\n"), 0o600); writeErr != nil {
		return res, writeErr
	}
	return res, nil
}
