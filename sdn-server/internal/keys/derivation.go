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
//
// When keyDir holds no sealed identity at all, nothing failed to open: the
// node refused to CREATE one. That is a different situation with a different
// answer, and saying "did not open this secret" there sent operators looking
// for a secret that does not exist — so it gets its own explanation
// (noIdentityHint).
func DerivationFailureHint(keyDir string) string {
	if !hasSealedIdentity(keyDir) {
		return noIdentityHint(keyDir)
	}
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

	if generated := GeneratedKeyPasswordFile(keyDir); generated != "" {
		b.WriteString(GeneratedKeyPasswordFileNote(generated))
	}
	b.WriteString("  RECOVERY, in order of preference:\n")
	b.WriteString("    1. restore the sealing conditions above (same hostname, same user, same source readability), then restart (no data loss)\n")
	b.WriteString("    2. set SDN_KEY_PASSWORD_FILE to the operator passphrase file for this node\n")
	b.WriteString("    3. re-import the mnemonic / key material from escrow\n")
	b.WriteString("  The node will NOT mint a replacement identity; doing so would change its PeerID.\n")
	return b.String()
}

// identityFiles are the files whose presence means a node identity was
// sealed in a key directory: the mnemonic (the identity's root) and the
// libp2p node key.
var identityFiles = []string{"mnemonic", "node.key"}

// hasSealedIdentity reports whether keyDir holds identity material, or
// SDN_MNEMONIC_FILE names a mnemonic that exists (the node reads its identity
// from there instead). A file it cannot even stat (a permission error on the
// directory) counts as present, and so does an unknown key directory (""):
// this answer decides whether to tell an operator "there is nothing here", and
// that must never be said about material that may exist.
func hasSealedIdentity(keyDir string) bool {
	if strings.TrimSpace(keyDir) == "" {
		return true
	}
	if present(os.Getenv(envMnemonicFile)) {
		return true
	}
	for _, name := range identityFiles {
		if present(filepath.Join(keyDir, name)) {
			return true
		}
	}
	return false
}

// envMnemonicFile is the variable that points the node at a mnemonic outside
// its key directory (config.EnvMnemonicFile; repeated here because config
// imports this package).
const envMnemonicFile = "SDN_MNEMONIC_FILE"

// present: path is set and exists, or cannot even be stat'ed.
func present(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Lstat(strings.TrimSpace(path))
	return err == nil || !os.IsNotExist(err)
}

// GeneratedKeyPasswordFileName is the name the container entrypoint and the
// .deb/.rpm package give the key password file they generate for a fresh node
// whose machine fingerprint is refused, in the directory that holds the key
// directory (/app/data/key-password, /var/lib/spacedatanetwork/key-password).
const GeneratedKeyPasswordFileName = "key-password"

// GeneratedKeyPasswordFile returns that file for keyDir when it exists, else
// "". It is only ever NAMED to the operator (a command run outside the
// container entrypoint or the systemd unit does not have the variable those
// export); nothing reads it implicitly.
func GeneratedKeyPasswordFile(keyDir string) string {
	if strings.TrimSpace(keyDir) == "" {
		return ""
	}
	path := filepath.Join(filepath.Dir(filepath.Clean(keyDir)), GeneratedKeyPasswordFileName)
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

// GeneratedKeyPasswordFileNote tells the operator how to run a command
// against a node sealed under a generated key password file.
func GeneratedKeyPasswordFileNote(path string) string {
	return fmt.Sprintf("  this node has a generated key password file: %s\n"+
		"    (the container entrypoint and the .deb/.rpm package make it for a fresh node whose machine fingerprint is refused,\n"+
		"    and give it only to the daemon). Run this command with SDN_KEY_PASSWORD_FILE=%s:\n"+
		"      container: docker exec <container> /app/entrypoint.sh <command> ...   (or docker exec -e SDN_KEY_PASSWORD_FILE=%s ...)\n"+
		"      package:   sudo -u sdn env SDN_KEY_PASSWORD_FILE=%s /opt/spacedatanetwork/bin/spacedatanetwork -c /etc/spacedatanetwork/config.yaml <command> ...\n",
		path, path, path, path)
}

// HasSealedIdentity is hasSealedIdentity for callers outside the package
// (installers' fresh-node test, the CLI). True means "do not treat this node
// as fresh".
func HasSealedIdentity(keyDir string) bool { return hasSealedIdentity(keyDir) }

// noIdentityHint explains a refusal on a node that has no identity yet: what
// was refused, why, and the two ways forward. It never says a secret failed
// to open, because none exists.
func noIdentityHint(keyDir string) string {
	return noIdentityHintFor(keyDir, CheckMachineFingerprint())
}

// noIdentityHintFor renders noIdentityHint for a given fingerprint verdict,
// so every branch is testable on any machine.
func noIdentityHintFor(keyDir string, check FingerprintCheck) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("no node identity exists in %s yet: this is a first start, and nothing failed to open.\n", keyDir))
	if check.Status != FingerprintRefused {
		// The machine key is available, so whatever refused came from
		// somewhere else — in practice a configured key password source that
		// cannot be read. The error this hint follows names it.
		b.WriteString(fmt.Sprintf("  this account's machine fingerprint is available (%s), so the refusal above is not the machine's:\n", check.Reason))
		b.WriteString("  fix the key password source it names, or unset it, and start again.\n")
		return b.String()
	}
	who := check.User
	if who == "" {
		who = "the account the node runs as"
	}
	b.WriteString(fmt.Sprintf("  the node did not create one because this host's machine fingerprint is REFUSED for account %q:\n", who))
	b.WriteString("    " + check.Reason + "\n")
	if check.Why != "" {
		b.WriteString("    why: " + check.Why + "\n")
	}
	b.WriteString("  A new identity is never sealed under a key that depends on the privilege of the process reading it.\n")
	b.WriteString("  TO START THIS NODE, either:\n")
	b.WriteString(fmt.Sprintf("    1. give it a key password file (preferred): 64 random hex characters, mode 0400, owned by %q,\n", who))
	b.WriteString("       kept for the life of the node and backed up apart from the node's data:\n")
	// The umask is set BEFORE the file exists: created under the default one
	// (often 0022) and chmod-ed afterwards, it is readable by everyone for a
	// moment, and a descriptor opened then still reads it after the chmod.
	b.WriteString("         (umask 077 && head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \\n' > key-password) && chmod 0400 key-password\n")
	b.WriteString("       then set SDN_KEY_PASSWORD_FILE to its absolute path (or security.key_password_file in the node's config)\n")
	b.WriteString(fmt.Sprintf("    2. or let %q read the source named above; the identity is then bound to this machine and this account.\n", who))
	b.WriteString("  The installers do (1) automatically for a fresh node; see `spacedatanetwork key fingerprint-check`.\n")
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
