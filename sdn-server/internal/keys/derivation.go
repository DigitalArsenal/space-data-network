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
// last successful seal. It contains source NAMES and a hostname hash only —
// never the source values, and never key material — so it is safe to read when
// diagnosing a node that will not start.
const derivationRecordFile = "derivation.json"

// DerivationRecord is the on-disk shape of derivationRecordFile.
type DerivationRecord struct {
	Scheme       string   `json:"scheme"`
	Sources      []string `json:"sources"`
	HostnameHash string   `json:"hostname_hash"`
	SealedAt     string   `json:"sealed_at"`
}

// WriteDerivationRecord records the derivation sources used to seal secrets in
// keyDir. Best-effort: a failure here must never block the node from starting,
// because the record is diagnostic only and is not an input to any key.
func WriteDerivationRecord(keyDir string, scheme FingerprintScheme, sources []string) error {
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return err
	}
	rec := DerivationRecord{
		Scheme:       string(scheme),
		Sources:      sources,
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
// seal time, the sources available now, and the recovery routes — so the
// failure is actionable instead of an opaque AEAD error.
//
// It deliberately does NOT suggest regenerating the identity: a node that
// silently mints a new identity is worse than a node that refuses to start.
func DerivationFailureHint(keyDir string) string {
	_, nowSources := machineFingerprintV3()
	var b strings.Builder
	b.WriteString("machine-derived key did not open this secret — the machine identity changed.\n")

	if rec, ok := ReadDerivationRecord(keyDir); ok {
		b.WriteString(fmt.Sprintf("  sealed under: scheme=%s sources=[%s] at %s\n",
			rec.Scheme, strings.Join(rec.Sources, ","), rec.SealedAt))
		if rec.HostnameHash != "" && rec.HostnameHash != hostnameHashHex() {
			b.WriteString("  HOSTNAME CHANGED since seal — this is the most likely cause.\n")
		}
	} else {
		b.WriteString(fmt.Sprintf("  no derivation record in %s (sealed before records existed)\n", keyDir))
	}
	b.WriteString(fmt.Sprintf("  available now: sources=[%s]\n", strings.Join(nowSources, ",")))
	b.WriteString("  RECOVERY, in order of preference:\n")
	b.WriteString("    1. restore the previous hostname, then restart (no data loss)\n")
	b.WriteString("    2. set SDN_KEY_PASSWORD_FILE to the operator passphrase file for this node\n")
	b.WriteString("    3. re-import the mnemonic / key material from escrow\n")
	b.WriteString("  The node will NOT mint a replacement identity; doing so would change its PeerID.\n")
	return b.String()
}
