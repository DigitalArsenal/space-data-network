package keys

import (
	"runtime"
	"strconv"
	"strings"
)

// hardwareFingerprint returns a stable, deterministic string built from
// machine attributes that do NOT change across reboots or normal operation:
// a machine identifier, total RAM, CPU model, CPU count, and the Go arch/OS.
//
// Deliberately EXCLUDES the hostname — hostnames can be renamed, which would
// silently change a hostname-derived key and lock the node out of its own
// encrypted mnemonic. Hostname changes are tracked separately as a canary
// (see hostname_canary.go).
//
// Only intrinsically stable fields are used. Volatile values (free memory,
// current CPU frequency, uptime) are never included, so the same machine
// always yields the same fingerprint.
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
