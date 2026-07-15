// Machine-state derivation for the credential keystore root key.
//
// This is a self-contained port of the sdn-server internal/keys machine
// fingerprint + DeriveDefaultPassword: a deterministic value bound to STABLE
// hardware attributes (machine-id, total RAM, CPU model, CPU count, arch, OS).
// It is the first of the three inputs to the deterministic root-key derivation
// (see secrets.go DeriveRootKey/ResolveRoot) and is reproducible on every boot
// with no env var, prompt, or external secret — the owner's unattended-operation
// requirement.
//
// Ported here (not imported from sdn-server) so the kubo module tree carries no
// dependency on sdn-server internals.

package credstore

import (
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// machineKeySalt is the fixed Argon2id salt for the hardware-derived value. It
// is a domain-separation constant, not a secret; the credential keystore's
// per-write random salt provides the actual cryptographic salting. It matches
// the sdn-server constant so a keystore derived on a node that ran either
// codebase re-derives identically.
var machineKeySalt = []byte("sdn-mnemonic-machine-key/v2")

// deriveDefaultPassword derives a deterministic value from STABLE hardware
// attributes using Argon2id, so the node can resolve its keystore root
// unattended without a stored password.
//
// The hostname is deliberately excluded from the fingerprint (see
// hardwareFingerprint); the hostname is bound in separately as the THIRD input
// to DeriveRootKey, so a rename changes the root by that path — not by silently
// mutating the machine-state component.
func deriveDefaultPassword() string {
	derived := argon2.IDKey([]byte(hardwareFingerprint()), machineKeySalt, 1, 64*1024, 4, 32)
	return string(derived)
}

// hardwareFingerprint returns a stable, deterministic string built from machine
// attributes that do NOT change across reboots or normal operation: a machine
// identifier, total RAM, CPU model, CPU count, and the Go arch/OS.
//
// Deliberately EXCLUDES the hostname — hostnames can be renamed. Only
// intrinsically stable fields are used; volatile values (free memory, current
// CPU frequency, uptime) are never included, so the same machine always yields
// the same fingerprint.
func hardwareFingerprint() string {
	parts := []string{
		"v2", // derivation-scheme version; bump to force re-derivation
		"arch=" + runtime.GOARCH,
		"os=" + runtime.GOOS,
		"ncpu=" + strconv.Itoa(runtime.NumCPU()),
	}
	for _, kv := range platformHardwareAttributes() {
		v := strings.TrimSpace(kv.value)
		if v == "" {
			continue
		}
		parts = append(parts, kv.key+"="+v)
	}
	return strings.Join(parts, "|")
}

type hwAttr struct {
	key   string
	value string
}
