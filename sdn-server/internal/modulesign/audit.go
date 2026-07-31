package modulesign

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

// Append-only signing audit (Hephaestus's non-optional property 3).
//
// Every request that reaches the signer — issued, refused, or failed — writes
// exactly one JSON line here, opened O_APPEND so a concurrent writer cannot
// interleave a partial record and a later writer cannot rewrite an earlier one.
// The file is 0600 in a 0700 directory: it names WHICH artifacts the bonded
// publisher key was asked to bless and by whom, which is operator-only
// information.
//
// WHAT IS DELIBERATELY NOT IN A LINE: key material of any kind, session tokens,
// raw xpubs (only a fingerprint — see FingerprintPrincipal), and the artifact
// bytes themselves. The content hash identifies the artifact; the bytes stay
// with the caller.

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
const auditLogEnv = "SDN_MODULE_SIGNING_AUDIT_LOG"

// Entry is one audit record. Field names are lowercase snake_case: this is a
// node-synthesized operational record, not an SDS record, so the SDS
// capitalization law (IDL casing for record fields) does not apply and the
// house lowercase convention for synthesized fields does.
type Entry struct {
	Timestamp       time.Time `json:"ts"`
	Event           string    `json:"event"`
	ContentHash     string    `json:"content_hash,omitempty"`
	StatementDomain string    `json:"statement_domain,omitempty"`
	SignerPubKeyHex string    `json:"signer_pubkey_hex,omitempty"`
	SignatureHex    string    `json:"signature_hex,omitempty"`
	SubmittedBytes  int       `json:"submitted_bytes"`
	PortableBytes   int       `json:"portable_bytes,omitempty"`
	TrailerStripped bool      `json:"trailer_stripped,omitempty"`
	Requester       string    `json:"requester,omitempty"`
	RemoteIP        string    `json:"remote_ip,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Detail          string    `json:"detail,omitempty"`
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

// DefaultAuditPath resolves the audit file: SDN_MODULE_SIGNING_AUDIT_LOG when
// set, else ~/.spacedatanetwork/logs/module-signing.audit.jsonl — beside the
// rest of the node's operator-visible state (config.Default uses the same
// ~/.spacedatanetwork root, internal/config/config.go:1134).
//
// Returns "" only when neither the env var nor a home directory is available,
// which NewSigner then treats as "cannot audit" and therefore "cannot sign".
func DefaultAuditPath() string {
	if v := strings.TrimSpace(os.Getenv(auditLogEnv)); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".spacedatanetwork", "logs", "module-signing.audit.jsonl")
}

// Append writes one line. It returns an error rather than swallowing one: the
// caller (Signer.Sign) treats an unwritable audit as a refusal to sign.
func (a *AuditLog) Append(entry Entry) error {
	if a == nil || a.path == "" {
		return fmt.Errorf("module signing audit log has no path configured")
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
// principal fingerprint this package emits.
//
// It is 12 by HEPHAESTUS'S RULING (Seal Council concurrence 2026-07-30,
// condition 2): deployment/signing.json already records admin principals as
// xpub sha256[:12] (e.g. "fa12d0d1924b"), and an audit line must join to that
// truth file without a second, incompatible convention. Changing this width
// silently orphans every previously written audit line from the seal file.
const PrincipalFingerprintLen = 12

// FingerprintPrincipal reduces a calling principal's identifier (an admin
// session xpub) to a short non-reversible label for the audit line.
//
// The node-admin contract is explicit that sessions are identified by
// FINGERPRINT, never by raw xpub; an audit file that recorded raw xpubs would
// quietly undo that. Empty input yields "" rather than the hash of the empty
// string, so "no principal" and "some principal" never collide.
func FingerprintPrincipal(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:PrincipalFingerprintLen]
}
