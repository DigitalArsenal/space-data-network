// Machine-derived key migration and diagnosis.
//
// Secrets sealed under an older derivation generation must still open, and a
// secret that will NOT open must say why in operator terms. Both concerns live
// here so no caller reimplements the ladder or the error text.

package keys

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// derivationRecordFile records which sources fed the machine derivation at the
// last successful seal. It contains source NAMES and states, a hostname hash,
// and (from v4) the sealing account's name — never the other source values,
// and never key material — so it is safe to read when diagnosing a node that
// will not start. The username is recorded verbatim, deliberately: it is what
// makes "sealed root, running tjkoury" diagnosable, and it is no secret to
// anyone who can read this 0600 file beside the key material.
const derivationRecordFile = "derivation.json"

// DerivationRecord is the on-disk shape of derivationRecordFile.
type DerivationRecord struct {
	Scheme string `json:"scheme"`
	// Sources lists the PRESENT source keys (legacy shape, still written so
	// older binaries reading this record keep working).
	Sources []string `json:"sources"`
	// SourceStates records every source as present or explicitly absent
	// (v4+, design requirement 3/5) so a skipped source is a deliberate,
	// reproducible part of the fingerprint — never an ambiguity.
	SourceStates []SourceState `json:"source_states,omitempty"`
	// User is the account name the sealing process ran as (v4+).
	User         string `json:"user,omitempty"`
	HostnameHash string `json:"hostname_hash"`
	SealedAt     string `json:"sealed_at"`
}

// WriteDerivationRecord records the derivation inputs used to seal secrets in
// keyDir. Best-effort: a failure here must never block the node from starting,
// because the record is diagnostic only and is not an input to any key.
func WriteDerivationRecord(keyDir string, inputs DerivationInputs) error {
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return err
	}
	rec := DerivationRecord{
		Scheme:       string(inputs.Scheme),
		Sources:      inputs.PresentSourceKeys(),
		SourceStates: inputs.States,
		User:         inputs.User,
		HostnameHash: hostnameHashHex(),
		SealedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(keyDir, derivationRecordFile), append(blob, '\n'), 0o600)
}

// ReadDerivationRecord loads the record written by WriteDerivationRecord. A
// missing record is not an error; it reports ok=false.
func ReadDerivationRecord(keyDir string) (rec DerivationRecord, ok bool) {
	blob, err := os.ReadFile(filepath.Join(keyDir, derivationRecordFile))
	if err != nil {
		return DerivationRecord{}, false
	}
	if json.Unmarshal(blob, &rec) != nil {
		return DerivationRecord{}, false
	}
	return rec, true
}

// DecryptMnemonicAnyScheme opens an encrypted mnemonic under any known
// machine-derived generation, newest first. It returns the scheme that worked
// so the caller can re-seal under the current one.
func DecryptMnemonicAnyScheme(data []byte) (string, FingerprintScheme, error) {
	var firstErr error
	for _, cand := range CandidatePasswords() {
		m, err := DecryptMnemonic(data, cand.Password)
		if err == nil {
			return m, cand.Scheme, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", "", firstErr
}

// DecryptSecretAnyScheme opens an encrypted secret under any known
// machine-derived generation, newest first.
func DecryptSecretAnyScheme(data []byte) ([]byte, FingerprintScheme, error) {
	var firstErr error
	for _, cand := range CandidatePasswords() {
		b, err := DecryptSecret(data, cand.Password)
		if err == nil {
			return b, cand.Scheme, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, "", firstErr
}

// DerivationFailureHint builds the operator-facing explanation for a secret in
// keyDir that no known derivation could open. It names the sources recorded at
// seal time, the sources available now, and DIFFS them — user changed, source
// present-then/absent-now, source unreadable by this process — and lists the
// recovery routes, so the failure is actionable instead of an opaque AEAD
// error (design requirement 4: "cannot decrypt" cost this fleet a
// mis-diagnosis; "host matched, user differs" would have cost nothing).
//
// It deliberately does NOT suggest regenerating the identity: a node that
// silently mints a new identity is worse than a node that refuses to start.
func DerivationFailureHint(keyDir string) string {
	var b strings.Builder
	b.WriteString("machine-derived key did not open this secret — the derivation inputs changed.\n")

	// What THIS process derives right now (best-effort; a v4 refusal is itself
	// the diagnosis).
	nowUser, userErr := currentProcessUsername()
	_, nowInputs, v4Err := machineFingerprintV4()

	rec, haveRec := ReadDerivationRecord(keyDir)
	if haveRec {
		b.WriteString(fmt.Sprintf("  sealed under: scheme=%s sources=[%s]", rec.Scheme, describeRecordedSources(rec)))
		if rec.User != "" {
			b.WriteString(" user=" + rec.User)
		}
		b.WriteString(fmt.Sprintf(" at %s\n", rec.SealedAt))
	} else {
		b.WriteString(fmt.Sprintf("  no derivation record in %s (sealed before records existed)\n", keyDir))
	}

	if v4Err != nil {
		b.WriteString(fmt.Sprintf("  available now: DERIVATION REFUSED — %v\n", v4Err))
	} else {
		b.WriteString(fmt.Sprintf("  available now: sources=[%s] user=%s\n", describeStates(nowInputs.States), nowInputs.User))
	}

	// Named differences, most likely cause first.
	if haveRec {
		if rec.User != "" && userErr == nil && rec.User != nowUser {
			b.WriteString(fmt.Sprintf("  USER DIFFERS — sealed as %q, running as %q. The at-rest key is bound to (machine, user):\n"+
				"    run as %q, or re-seal deliberately with `spacedatanetwork key reseal` as the sealing account.\n",
				rec.User, nowUser, rec.User))
		}
		if rec.HostnameHash != "" && rec.HostnameHash != hostnameHashHex() {
			b.WriteString("  HOSTNAME CHANGED since seal — restore the previous hostname to recover without re-sealing.\n")
		}
		if v4Err == nil {
			for _, d := range diffSourceStates(rec, nowInputs.States) {
				b.WriteString("  " + d + "\n")
			}
		}
	}

	b.WriteString("  RECOVERY, in order of preference:\n")
	b.WriteString("    1. restore the sealing conditions above (same hostname, same user, same source readability), then restart (no data loss)\n")
	b.WriteString("    2. set SDN_KEY_PASSWORD_FILE to the operator passphrase file for this node\n")
	b.WriteString("    3. re-import the mnemonic / key material from escrow\n")
	b.WriteString("  The node will NOT mint a replacement identity; doing so would change its PeerID.\n")
	return b.String()
}

// describeRecordedSources renders a record's sources, preferring the explicit
// per-source states over the legacy present-only list.
func describeRecordedSources(rec DerivationRecord) string {
	if len(rec.SourceStates) > 0 {
		return describeStates(rec.SourceStates)
	}
	return strings.Join(rec.Sources, ",")
}

// describeStates renders source states as "key" (present) / "key:absent".
func describeStates(states []SourceState) string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		if s.State == SourceStateAbsent {
			out = append(out, s.Key+":absent")
			continue
		}
		out = append(out, s.Key)
	}
	return strings.Join(out, ",")
}

// diffSourceStates names per-source state changes between the sealed record
// and the states derivable now.
func diffSourceStates(rec DerivationRecord, now []SourceState) []string {
	sealed := map[string]string{}
	for _, s := range rec.SourceStates {
		sealed[s.Key] = s.State
	}
	if len(sealed) == 0 {
		// Legacy record: only present sources were recorded.
		for _, k := range rec.Sources {
			sealed[k] = SourceStatePresent
		}
	}
	var diffs []string
	for _, s := range now {
		was, known := sealed[s.Key]
		if known && was != s.State {
			diffs = append(diffs, fmt.Sprintf("SOURCE %q was %s at seal time but is %s now", s.Key, was, s.State))
		}
		delete(sealed, s.Key)
	}
	for k, was := range sealed {
		diffs = append(diffs, fmt.Sprintf("SOURCE %q was %s at seal time but is not derivable now", k, was))
	}
	return diffs
}
