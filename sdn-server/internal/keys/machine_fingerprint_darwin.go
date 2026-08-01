//go:build darwin

package keys

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// platformHardwareAttributes reads stable hardware identifiers on macOS
// (development hosts). Sources are optional; missing ones are skipped.
func platformHardwareAttributes() []hwAttr {
	attrs := make([]hwAttr, 0, 4)

	if id := ioPlatformUUID(); id != "" {
		attrs = append(attrs, hwAttr{"machineid", id})
	}
	if mem, err := syscall.Sysctl("hw.memsize"); err == nil {
		// hw.memsize comes back as raw bytes in a string; keep as-is (stable).
		if v := strings.TrimSpace(mem); v != "" {
			attrs = append(attrs, hwAttr{"memtotal", v})
		}
	}
	if brand, err := syscall.Sysctl("machdep.cpu.brand_string"); err == nil {
		if v := strings.TrimSpace(brand); v != "" {
			attrs = append(attrs, hwAttr{"cpu", v})
		}
	}
	if model, err := syscall.Sysctl("hw.model"); err == nil {
		if v := strings.TrimSpace(model); v != "" {
			attrs = append(attrs, hwAttr{"model", v})
		}
	}
	return attrs
}

// platformStableIdentifiers returns identifiers that survive both an OS
// reinstall and a hardware/resource change. On macOS (development and local
// Docker hosts) that is the IOPlatformUUID; hw.memsize / CPU brand are
// deliberately excluded because they are exactly the resize-volatile class
// that orphaned v2 secrets.
func platformStableIdentifiers() []hwAttr {
	attrs := make([]hwAttr, 0, 1)
	if id := ioPlatformUUID(); id != "" {
		attrs = append(attrs, hwAttr{"platuuid", id})
	}
	return attrs
}

// platformStableIdentifierReadings is the v4 counterpart of
// platformStableIdentifiers, distinguishing ABSENT from UNREADABLE (see the
// Linux twin for the defect this kills). On macOS the IOPlatformUUID is read
// via ioreg, which needs no privilege — so an ioreg FAILURE is an error (the
// source exists on every Mac but could not be read), while a successful run
// that yields no UUID is recorded as explicitly absent.
func platformStableIdentifierReadings() ([]sourceReading, error) {
	id, err := ioPlatformUUIDStrict()
	if err != nil {
		return nil, fmt.Errorf(
			"machine fingerprint (v4): IOPlatformUUID could not be read (%w) — refusing to derive a privilege-degraded key; "+
				"fix ioreg access or set SDN_KEY_PASSWORD_FILE", err)
	}
	if id == "" {
		return []sourceReading{{key: "platuuid", absent: true}}, nil
	}
	return []sourceReading{{key: "platuuid", value: id}}, nil
}

// ioPlatformUUIDStrict is ioPlatformUUID with the failure modes separated:
// an exec error is returned (unreadable), while a clean run without a UUID
// line returns "" (absent). ioPlatformUUID itself is preserved unchanged for
// the v2/v3 derivations.
func ioPlatformUUIDStrict() (string, error) {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	return parseIOPlatformUUID(string(out)), nil
}

// parseIOPlatformUUID extracts the quoted IOPlatformUUID value from ioreg
// output, mirroring ioPlatformUUID's parsing exactly.
func parseIOPlatformUUID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "IOPlatformUUID") {
			if idx := strings.LastIndex(line, "\""); idx > 0 {
				trimmed := strings.TrimSuffix(line[:idx], "\"")
				if q := strings.LastIndex(trimmed, "\""); q >= 0 {
					return strings.TrimSpace(trimmed[q+1:])
				}
			}
		}
	}
	return ""
}

// ioPlatformUUID returns the stable per-machine IOPlatformUUID via ioreg.
func ioPlatformUUID() string {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "IOPlatformUUID") {
			if idx := strings.LastIndex(line, "\""); idx > 0 {
				trimmed := strings.TrimSuffix(line[:idx], "\"")
				if q := strings.LastIndex(trimmed, "\""); q >= 0 {
					return strings.TrimSpace(trimmed[q+1:])
				}
			}
		}
	}
	return ""
}
