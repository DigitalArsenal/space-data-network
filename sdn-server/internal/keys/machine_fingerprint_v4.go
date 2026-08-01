package keys

import (
	"fmt"
	"os/user"
	"runtime"
	"strings"
)

// ============================================================================
// Scheme v4: bind the at-rest key to (machine, user) — and never degrade
// silently.
// ============================================================================
//
// v3 had a defect that forked the key space (graph task
// sdn-mnemonic-at-rest-key-break, measured on vm-orbit-det-01 2026-07-30): its
// only hardware source, the DMI product UUID, is root-readable on most
// distros, and an UNREADABLE source was silently skipped. The same machine and
// the same binary produced two different keys depending on the PRIVILEGE of
// the reading process — sealed as root, unopenable as the service user — and
// a container, a DMI-less VM and a de-privileged service all landed on the
// same degraded fingerprint with no error at seal time. Failing OPEN.
//
// v4 fixes both defects, per the owner ruling of 2026-07-30 ("we should have
// the username be used as well for the deterministic at-rest key, since if
// that changes / is removed should have to re-generate"):
//
//  1. The USER the process runs as is a REQUIRED input. The identity is bound
//     to the account that owns it; moving a daemon between accounts is a
//     deliberate act with a deliberate step (`key reseal`), never a silent
//     key-space fork. The previous root-sealed/user-opened failure becomes
//     CORRECT, documented behaviour — loudly diagnosed — instead of a
//     mysterious decrypt error.
//
//  2. ABSENT and UNREADABLE are distinguishable. A source that does not exist
//     (DMI-less VM, container) is recorded as explicitly absent INSIDE the
//     fingerprint, so its absence is a deliberate, reproducible part of the
//     key. A source that EXISTS but cannot be READ is an ERROR: derivation
//     refuses instead of sealing into a second key space. Fail closed.
//
// USERNAME NORMALISATION, recorded deliberately (design requirement 2): the
// input is the account NAME, not the uid — a name is what the owner asked for
// and is what survives a uid reshuffle. It is taken from user.Current(),
// trimmed, with a Windows-style "DOMAIN\" prefix stripped; it is otherwise
// used VERBATIM (no case folding: Unix account names are case-sensitive, and
// folding would alias distinct accounts). There is NO environment-variable
// fallback ($USER/$LOGNAME are config-mutable and would re-introduce exactly
// the silent-fork class this scheme exists to kill); an unknown or empty
// username is an error, never an empty string that silently degrades.

// SchemeV4 binds the at-rest key to (machine, user) and encodes source
// absence explicitly. Current generation for all new seals.
const SchemeV4 FingerprintScheme = "v4"

// Source states recorded per derivation input (design requirement 3).
const (
	// SourceStatePresent — the source was read and its value fed the key.
	SourceStatePresent = "present"
	// SourceStateAbsent — the source does not exist on this machine; its
	// absence is encoded in the fingerprint as a deliberate input.
	SourceStateAbsent = "absent"
)

// SourceState is one derivation source's recorded state: present-with-value
// (the value itself is never recorded) or explicitly absent. A source that
// exists but cannot be read has NO state — it is an error, not an omission.
type SourceState struct {
	Key   string `json:"key"`
	State string `json:"state"`
}

// DerivationInputs describes everything a v4-generation derivation consumed,
// for recording alongside the sealed blob (design requirement 5) so a later
// decrypt failure can name what changed (design requirement 4).
type DerivationInputs struct {
	Scheme FingerprintScheme
	States []SourceState
	// User is the account name the sealing process ran as. Recorded VERBATIM
	// (not hashed, unlike the hostname): it is required for the "sealed root,
	// running tjkoury" diagnostic, and it is no secret to anyone who can read
	// the 0600 record beside the key material — they ARE that user.
	User string
}

// PresentSourceKeys returns the keys of the present sources, for the legacy
// "sources" list older binaries still read from derivation.json.
func (d DerivationInputs) PresentSourceKeys() []string {
	out := make([]string, 0, len(d.States))
	for _, s := range d.States {
		if s.State == SourceStatePresent {
			out = append(out, s.Key)
		}
	}
	return out
}

// sourceReading is one platform source's read outcome. Exactly one of
// value/absent is meaningful; an unreadable source never becomes a reading —
// platformStableIdentifierReadings returns an error instead.
type sourceReading struct {
	key    string
	value  string
	absent bool
}

// fingerprintAbsentMarker canonicalises an explicitly-absent source inside the
// v4 fingerprint. The marker cannot collide with a real value: readings are
// trimmed and a platform UUID never takes this form.
const fingerprintAbsentMarker = "!ABSENT!"

// machineFingerprintV4 builds the current-generation fingerprint:
//
//	v4|arch=<GOARCH>|os=<GOOS>|host=<label>|user=<name>|platuuid=<uuid or !ABSENT!>
//
// Unlike v3, it can FAIL — and must (design requirement 3): an unknown user,
// an unreadable hostname, or a platform source that exists but cannot be read
// all refuse derivation rather than silently sealing into a forked key space.
func machineFingerprintV4() (string, DerivationInputs, error) {
	host := hostnameForFingerprint()
	if host == "" {
		return "", DerivationInputs{}, fmt.Errorf("machine fingerprint (v4): hostname unavailable; refusing to derive a degraded key")
	}
	userName, err := currentProcessUsername()
	if err != nil {
		return "", DerivationInputs{}, err
	}
	readings, err := platformStableIdentifierReadings()
	if err != nil {
		return "", DerivationInputs{}, err
	}
	return fingerprintV4From(host, userName, readings)
}

// fingerprintV4From canonicalises the v4 fingerprint from already-read inputs.
// Split from machineFingerprintV4 so the canonical form is testable without
// this machine's actual sources.
func fingerprintV4From(host, userName string, readings []sourceReading) (string, DerivationInputs, error) {
	if strings.TrimSpace(host) == "" {
		return "", DerivationInputs{}, fmt.Errorf("machine fingerprint (v4): empty hostname")
	}
	if strings.TrimSpace(userName) == "" {
		return "", DerivationInputs{}, fmt.Errorf("machine fingerprint (v4): empty username; the at-rest key is bound to (machine, user) and never degrades silently")
	}

	parts := []string{
		string(SchemeV4),
		"arch=" + runtime.GOARCH,
		"os=" + runtime.GOOS,
		"host=" + host,
		"user=" + userName,
	}
	inputs := DerivationInputs{
		Scheme: SchemeV4,
		States: []SourceState{
			{Key: "arch", State: SourceStatePresent},
			{Key: "os", State: SourceStatePresent},
			{Key: "hostname", State: SourceStatePresent},
			{Key: "user", State: SourceStatePresent},
		},
		User: userName,
	}
	for _, r := range readings {
		v := strings.TrimSpace(r.value)
		switch {
		case r.absent || v == "":
			// Explicitly absent: encoded IN the fingerprint, so "this machine
			// has no such source" is a deliberate, reproducible input — not
			// indistinguishable from a skipped one (the v3 defect).
			parts = append(parts, r.key+"="+fingerprintAbsentMarker)
			inputs.States = append(inputs.States, SourceState{Key: r.key, State: SourceStateAbsent})
		default:
			parts = append(parts, r.key+"="+v)
			inputs.States = append(inputs.States, SourceState{Key: r.key, State: SourceStatePresent})
		}
	}
	return strings.Join(parts, "|"), inputs, nil
}

// currentProcessUsername resolves the account NAME the process runs as (see
// the normalisation note in the package comment above: name not uid, verbatim
// case, DOMAIN\ prefix stripped, no env fallback, empty = error).
func currentProcessUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("machine fingerprint (v4): cannot resolve the current user (%w); the at-rest key is bound to (machine, user) — run as a named account or set SDN_KEY_PASSWORD_FILE", err)
	}
	name := strings.TrimSpace(u.Username)
	if idx := strings.LastIndexByte(name, '\\'); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "", fmt.Errorf("machine fingerprint (v4): current user has an empty name; refusing to derive a degraded key")
	}
	return name, nil
}

// machineFingerprintV3Degraded reproduces the v3 fingerprint EXACTLY AS A
// PROCESS THAT COULD READ NO PLATFORM IDENTIFIER computed it:
//
//	v3|arch=<GOARCH>|os=<GOOS>|host=<label>
//
// This is not a new derivation — it is a historically-produced v3 output (the
// privilege-degraded branch of machineFingerprintV3), reconstructed so the
// candidate ladder can open material SEALED by an unprivileged process even
// when the opener CAN read the platform UUID (e.g. sealed as the service
// user, opened as root). machineFingerprintV3 itself is preserved
// byte-for-byte; this function must mirror its degraded branch exactly.
func machineFingerprintV3Degraded() string {
	parts := []string{
		string(SchemeV3),
		"arch=" + runtime.GOARCH,
		"os=" + runtime.GOOS,
	}
	if h := strings.TrimSpace(hostnameForFingerprint()); h != "" {
		parts = append(parts, "host="+h)
	}
	return strings.Join(parts, "|")
}
