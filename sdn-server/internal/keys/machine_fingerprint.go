package keys

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// ============================================================================
// Derivation-source stability: why v3 exists
// ============================================================================
//
// The at-rest key for node secrets is derived from machine attributes so the
// node can boot unattended. Which attributes is a SAFETY decision, not a
// security one: every attribute included is an attribute whose change BRICKS
// the node (fail-closed decrypt of its own mnemonic / identity key).
//
// Measured on the live fleet (Hephaestus, 2026-07-28, DigitalOcean droplets):
//
//	SOURCE                     REBUILD   RESIZE   VERDICT
//	hostname                   survives  survives STABLE  (owner: "machine name")
//	DMI product_uuid           survives  survives STABLE  (but often root-only)
//	/etc/machine-id            CHANGES   survives VOLATILE - regenerated on rebuild
//	MemTotal                   survives  CHANGES  VOLATILE - resize rewrites it
//	NumCPU                     survives  CHANGES  VOLATILE - resize rewrites it
//	CPU model name             survives  CHANGES  VOLATILE - host migration rewrites it
//
// The v2 scheme below included machine-id, MemTotal, NumCPU and the CPU model.
// That is why vm-orbit-det-01 could not be recovered after its 2026-07-24
// rebuild, and it means ANY droplet resize permanently destroys a v2 node's
// access to its own identity. v3 therefore derives from STABLE sources only.
//
// TRADEOFF, stated plainly (owner accepted, "not perfectly secure"): v3 has a
// small, guessable input space — anyone who knows the hostname and can read
// the DMI UUID can re-derive the key. This is obfuscation of data at rest, not
// secrecy. It defeats a stolen disk image or a stray backup tarball; it does
// NOT defend against code running on the same host. Operators who need real
// secrecy set SDN_KEY_PASSWORD / SDN_KEY_PASSWORD_FILE, which takes precedence.
//
// NOTHING machine-bound survives a destroy+recreate. Machine derivation alone
// can never guarantee identity across that; the survival path is the operator
// passphrase file plus key-material escrow (graph task nst-key-material-cli).

// FingerprintScheme names a derivation generation. Older schemes are retained
// so an existing on-disk secret can be opened and re-sealed under the current
// one instead of locking the node out.
type FingerprintScheme string

const (
	// SchemeV3 derives from rebuild- and resize-stable sources only.
	SchemeV3 FingerprintScheme = "v3"
	// SchemeV2 derives from hardware attributes including resize-volatile
	// ones (MemTotal/NumCPU/CPU model) and the rebuild-volatile machine-id.
	SchemeV2 FingerprintScheme = "v2"
	// SchemeLegacy is the pre-v2 hostname+homedir derivation.
	SchemeLegacy FingerprintScheme = "legacy"
)

type hwAttr struct {
	key   string
	value string
}

// machineFingerprintV3 builds the current-generation fingerprint from sources
// that survive both an OS rebuild and an instance resize. It returns the
// fingerprint string alongside the source keys that actually contributed, so a
// decrypt failure can be diagnosed against what was present at seal time.
//
// The hostname is INCLUDED here (owner: "a deterministic key created from the
// machine name and local machine metadata"). v2 deliberately excluded it to
// avoid rename-lockout; v3 accepts that risk because the alternative — the v2
// source set — demonstrably loses the key on the far more common events
// (rebuild, resize). A rename is operator-initiated and recoverable via the
// passphrase escape hatch; a resize is not.
func machineFingerprintV3() (fingerprint string, sources []string) {
	parts := []string{
		string(SchemeV3),
		"arch=" + runtime.GOARCH,
		"os=" + runtime.GOOS,
	}
	sources = []string{"arch", "os"}

	if h := strings.TrimSpace(hostnameForFingerprint()); h != "" {
		parts = append(parts, "host="+h)
		sources = append(sources, "hostname")
	}
	for _, kv := range platformStableIdentifiers() {
		v := strings.TrimSpace(kv.value)
		if v == "" {
			continue
		}
		parts = append(parts, kv.key+"="+v)
		sources = append(sources, kv.key)
	}
	return strings.Join(parts, "|"), sources
}

// hostnameForFingerprint returns the machine name, normalized so that trivial
// presentation differences (case, trailing dot, FQDN suffix) do not change the
// derived key. Only the leftmost label is used: a DHCP/DNS search-domain change
// must not brick the node.
func hostnameForFingerprint() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if idx := strings.IndexByte(h, '.'); idx > 0 {
		h = h[:idx]
	}
	return h
}

// hardwareFingerprint returns the v2 fingerprint.
//
// PRESERVED BYTE-FOR-BYTE. This function is no longer used to seal anything;
// it exists only so secrets written under v2 can still be opened and migrated
// to v3. Any edit here permanently orphans every v2 node in the fleet.
func hardwareFingerprint() string {
	parts := []string{
		string(SchemeV2), // derivation-scheme version; bump to force re-derivation
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
