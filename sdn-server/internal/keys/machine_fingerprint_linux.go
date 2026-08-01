//go:build linux

package keys

import (
	"fmt"
	"os"
	"strings"
)

// platformHardwareAttributes reads stable hardware identifiers on Linux.
// Every source is optional; a missing source is simply skipped so derivation
// never fails. Only fields that are invariant for the life of the install are
// read (machine-id, MemTotal, CPU model) — never volatile ones.
func platformHardwareAttributes() []hwAttr {
	attrs := make([]hwAttr, 0, 3)

	if id := firstNonEmptyFile("/etc/machine-id", "/var/lib/dbus/machine-id"); id != "" {
		attrs = append(attrs, hwAttr{"machineid", id})
	}
	if mem := grepFieldPrefix("/proc/meminfo", "MemTotal:"); mem != "" {
		attrs = append(attrs, hwAttr{"memtotal", mem})
	}
	if cpu := grepFieldPrefix("/proc/cpuinfo", "model name"); cpu != "" {
		attrs = append(attrs, hwAttr{"cpu", cpu})
	}
	return attrs
}

// platformStableIdentifiers returns identifiers that survive BOTH an OS
// rebuild and an instance resize (see machine_fingerprint.go for the measured
// fleet table).
//
// The DMI product UUID is the only such hardware source on Linux, and it is
// BEST-EFFORT ONLY: /sys/class/dmi/id/product_uuid is root-readable on most
// distros and is unreadable on at least one live node (vm-orbit-det-01, no
// sudo). It must therefore never be REQUIRED — if it were, the same host would
// derive two different keys depending on whether the daemon ran as root, which
// is its own lockout. When it is absent the derivation falls back to
// hostname+os+arch, which is weaker but stable.
func platformStableIdentifiers() []hwAttr {
	attrs := make([]hwAttr, 0, 1)
	if id := firstNonEmptyFile("/sys/class/dmi/id/product_uuid"); id != "" {
		attrs = append(attrs, hwAttr{"platuuid", id})
	}
	return attrs
}

// platuuidPath is the only rebuild- and resize-stable hardware source on
// Linux. It is root-readable on most distros — which is exactly why v4 reads
// it through platformStableIdentifierReadings and not best-effort.
const platuuidPath = "/sys/class/dmi/id/product_uuid"

// platformStableIdentifierReadings is the v4 counterpart of
// platformStableIdentifiers: the same sources, but ABSENT and UNREADABLE are
// distinguished instead of both being silently skipped (the defect that
// forked the key space on vm-orbit-det-01 — sealed as root with the UUID,
// opened as the service user without it).
//
//   - The file does not exist (DMI-less VM, container): explicitly ABSENT —
//     a deliberate, reproducible derivation input.
//   - The file exists but cannot be read (privilege): an ERROR. Sealing under
//     a privilege-degraded fingerprint is failing open, and v4 refuses.
func platformStableIdentifierReadings() ([]sourceReading, error) {
	b, err := os.ReadFile(platuuidPath)
	switch {
	case err == nil:
		return []sourceReading{{key: "platuuid", value: strings.TrimSpace(string(b))}}, nil
	case os.IsNotExist(err):
		return []sourceReading{{key: "platuuid", absent: true}}, nil
	default:
		return nil, fmt.Errorf(
			"machine fingerprint (v4): %s exists but cannot be read (%w) — the at-rest key would silently depend on process privilege; "+
				"grant this account read access, run as the account that sealed the material, or set SDN_KEY_PASSWORD_FILE",
			platuuidPath, err)
	}
}

func firstNonEmptyFile(paths ...string) string {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return ""
}

// grepFieldPrefix returns the value of the first line beginning with prefix,
// taking everything after the first ':' (MemTotal / cpuinfo layout).
func grepFieldPrefix(path, prefix string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			if idx := strings.IndexByte(line, ':'); idx >= 0 {
				return strings.TrimSpace(line[idx+1:])
			}
		}
	}
	return ""
}
