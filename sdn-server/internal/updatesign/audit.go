package updatesign

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Append-only update-manifest signing audit.
//
// This is the same gate internal/modulesign/audit.go implements, for the same
// reason and with the same properties: one JSON line per request (issued,
// refused, or failed), O_APPEND so a concurrent writer cannot interleave a
// partial record, fsync'd, 0600 in a 0700 directory. A line names WHICH release
// the bonded publisher key was asked to bless and by whom.
//
// It is a SEPARATE FILE from the module audit, not a shared one. The two
// surfaces answer different operational questions ("what modules did we
// publish?" vs "what did we ship to the fleet?"), they are read by different
// people at different times, and a single file would make either question
// require filtering the other's traffic out first. The domain registry already
// keeps the two statement spaces disjoint; keeping the audits disjoint too
// means an operator grepping one lane never has to reason about the other.
//
// WHAT IS DELIBERATELY NOT IN A LINE: key material, session tokens, raw xpubs
// (fingerprint only), and the manifest document itself. The canonical hash
// identifies the manifest; the bytes stay with the caller.

// EventKind values.
const (
	EventIssued  = "signature_issued"
	EventRefused = "signature_refused"
	EventFailed  = "signature_failed"
)

// auditLogEnv overrides the audit file location. Present so a test, a container
// with a read-only home, or an operator with a dedicated audit volume can
// redirect it; NOT present so the audit can be turned off — there is no value
// that disables auditing.
const auditLogEnv = "SDN_UPDATE_SIGNING_AUDIT_LOG"

// Entry is one audit record. Field names are lowercase snake_case: this is a
// node-synthesized operational record, not an SDS record, so the SDS
// capitalization law does not apply and the house lowercase convention does.
type Entry struct {
	Timestamp       time.Time `json:"ts"`
	Event           string    `json:"event"`
	ContentHash     string    `json:"content_hash,omitempty"`
	StatementDomain string    `json:"statement_domain,omitempty"`
	SignerPubKeyHex string    `json:"signer_pubkey_hex,omitempty"`
	SignatureHex    string    `json:"signature_hex,omitempty"`
	SubmittedBytes  int       `json:"submitted_bytes"`
	CanonicalBytes  int       `json:"canonical_bytes,omitempty"`

	// The release this signature ships. These are the fields that make the
	// audit answer "what went to the fleet, and when" without cross-referencing
	// the feed tree — which is mutable operator state and could be rewritten.
	UpdateID string `json:"update_id,omitempty"`
	Version  string `json:"version,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
	Channel  string `json:"channel,omitempty"`
	Target   string `json:"target,omitempty"` // "<kind>/<platform>/<arch>"

	// Resigned reports that the submitted document already carried a signature
	// which canonicalization dropped. Recorded because "this release was signed
	// twice" is a fact an operator should be able to see rather than infer.
	Resigned bool `json:"resigned,omitempty"`

	Requester string `json:"requester,omitempty"`
	RemoteIP  string `json:"remote_ip,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// AuditLog is a serialized append-only JSONL writer.
type AuditLog struct {
	mu   sync.Mutex
	path string
}

// NewAuditLog returns a log writing to path. The file and its parent directory
// are created lazily on first append, so constructing a signer never has a side
// effect on disk.
func NewAuditLog(path string) *AuditLog {
	return &AuditLog{path: strings.TrimSpace(path)}
}

// Path is the file this log appends to.
func (a *AuditLog) Path() string { return a.path }

// DefaultAuditPath resolves the audit file: SDN_UPDATE_SIGNING_AUDIT_LOG when
// set, else ~/.spacedatanetwork/logs/update-signing.audit.jsonl — beside the
// module-signing audit and the rest of the node's operator-visible state.
//
// Returns "" only when neither the env var nor a home directory is available,
// which the caller then treats as "cannot audit" and therefore "cannot sign".
func DefaultAuditPath() string {
	if v := strings.TrimSpace(os.Getenv(auditLogEnv)); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".spacedatanetwork", "logs", "update-signing.audit.jsonl")
}

// Append writes one line. It returns an error rather than swallowing one: the
// caller (Signer.Sign) treats an unwritable audit as a refusal to sign.
func (a *AuditLog) Append(entry Entry) error {
	if a == nil || a.path == "" {
		return fmt.Errorf("update signing audit log has no path configured")
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	entry.Timestamp = entry.Timestamp.UTC()

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode audit entry: %w", err)
	}
	line = append(line, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()

	if dir := filepath.Dir(a.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create audit log directory: %w", err)
		}
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append audit line: %w", err)
	}
	// Durability matters more than throughput on a surface that issues at most
	// a handful of signatures per release.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync audit log: %w", err)
	}
	return nil
}

// PrincipalFingerprintLen is the truncation width, in hex characters, of every
// principal fingerprint this package emits. It is 12 to match
// internal/modulesign.PrincipalFingerprintLen and deployment/signing.json's
// xpub sha256[:12] convention; the two audit files must join to the same truth
// file, so they cannot use two widths.
const PrincipalFingerprintLen = 12

// FingerprintPrincipal reduces a calling principal's identifier (an admin
// session xpub) to a short non-reversible label for the audit line. Empty input
// yields "" rather than the hash of the empty string, so "no principal" and
// "some principal" never collide.
func FingerprintPrincipal(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:PrincipalFingerprintLen]
}
