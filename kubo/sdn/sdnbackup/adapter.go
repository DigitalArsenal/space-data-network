package sdnbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a backup unit — what the blob IS and how to re-stage it
// (spec 1.5). The value is stable, lowercase, and appears in the provider
// object key (see ObjectKey) so a provider listing is organized by kind.
type Kind string

const (
	KindModuleWASM     Kind = "module_wasm"
	KindFlowBundle     Kind = "flow_bundle"
	KindSDSRecord      Kind = "sds_record"
	KindAppManifest    Kind = "app_manifest"
	KindModuleRegistry Kind = "module_registry"
	KindConfig         Kind = "config"
)

// knownKinds gates ObjectKey / kind-prefix scans to the defined set.
var knownKinds = map[Kind]bool{
	KindModuleWASM:     true,
	KindFlowBundle:     true,
	KindSDSRecord:      true,
	KindAppManifest:    true,
	KindModuleRegistry: true,
	KindConfig:         true,
}

// Meta carries the re-stage hints a unit needs to be put back where it came
// from. Which fields are set depends on Kind (a module carries PluginID +
// Enabled; a record carries Source + SDSType; a flow carries ProgramID).
type Meta struct {
	PluginID  string `json:"pluginId,omitempty"`
	Name      string `json:"name,omitempty"`
	Version   string `json:"version,omitempty"`
	Enabled   bool   `json:"enabled,omitempty"`
	Source    string `json:"source,omitempty"`
	SDSType   string `json:"sdsType,omitempty"`
	ProgramID string `json:"programId,omitempty"`
	// FilePath is the node-relative destination for module_registry / config
	// units re-staged as raw files. Never an absolute path from an untrusted
	// blob — the restager resolves it under its own configured root.
	FilePath  string `json:"filePath,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
}

// BackupBlob is one content-addressed backup unit: {contentHash, kind, meta,
// bytes} where contentHash is the lowercase hex SHA-256 of bytes (spec 1.5).
type BackupBlob struct {
	ContentHash string
	Kind        Kind
	Meta        Meta
	Bytes       []byte
}

// Ref returns the BlobRef addressing this blob, carrying the kind as the
// fast-path key hint.
func (b BackupBlob) Ref() BlobRef {
	return BlobRef{ContentHash: b.ContentHash, Kind: b.Kind}
}

// BlobRef addresses a blob by content hash. Kind is an OPTIONAL hint: the
// spec's object key is sdn-backup/<kind>/<hh>/<hash>, so knowing the kind lets
// an adapter form the exact key (a cheap existence check). Callers on the
// backup path always know the kind (they hold the unit) and on the restore path
// read it from the receipt, so the hint is normally present; when absent an
// adapter falls back to a prefix scan across kinds.
type BlobRef struct {
	ContentHash string
	Kind        Kind
}

// PutAck acknowledges one stored blob (spec A.1).
type PutAck struct {
	ContentHash       string
	ProviderKey       string
	ProviderVersionID string
	SizeStored        int
	Encrypted         bool
	// AlreadyPresent reports that the byte-identical blob already existed; the
	// put was an idempotent no-op that still returns this ack.
	AlreadyPresent bool
}

// Presence is a bytes-free existence check result — the incremental lever
// (spec C.3).
type Presence struct {
	ContentHash       string
	Present           bool
	ProviderVersionID string
}

// ListQuery parameterizes an adapter listing.
type ListQuery struct {
	Prefix     string
	KindFilter Kind
	PageToken  string
	Limit      int
}

// ListEntry is one entry in a listing.
type ListEntry struct {
	ContentHash       string
	Kind              Kind
	Size              int
	ProviderKey       string
	ProviderVersionID string
}

// ListPage is one page of a listing.
type ListPage struct {
	Entries       []ListEntry
	NextPageToken string
}

// DeleteAck acknowledges a delete (spec A.1).
type DeleteAck struct {
	ContentHash string
	Deleted     bool
}

// AdapterCapabilities are the operation flags an adapter advertises. A
// read-only mirror declares Delete=false; a native content-addressed store
// declares NativeHash=true.
type AdapterCapabilities struct {
	Put        bool `json:"put"`
	Get        bool `json:"get"`
	Has        bool `json:"has"`
	List       bool `json:"list"`
	Delete     bool `json:"delete"`
	Versioning bool `json:"versioning"`
	NativeHash bool `json:"nativeHash"`
}

// AdapterDescriptor is the read-only, credential-free self-description returned
// by Describe (spec A.1 adapter_describe; the $STF-shaped descriptor the spec
// names — carried here as a plain struct since $STF is not in the vendored go
// lib, per the spec's alternative in A.3).
type AdapterDescriptor struct {
	ProviderID       string              `json:"providerId"`
	Capabilities     AdapterCapabilities `json:"capabilities"`
	MaxBlobSize      int64               `json:"maxBlobSize"` // 0 = unbounded
	CredentialLane   string              `json:"credentialLane,omitempty"`
	AddressingScheme string              `json:"addressingScheme"`
}

// Adapter is the uniform, content-addressed blob interface every storage
// provider implements (spec A.1). Put is idempotent (a byte-identical put is a
// no-op that still acks); Get/Has address by content hash; the caller
// re-hashes on Get and rejects a mismatch (verify-by-hash, mirroring
// appmanifest.ResolveModuleByContentHash).
type Adapter interface {
	Describe(ctx context.Context) (AdapterDescriptor, error)
	Put(ctx context.Context, blob BackupBlob) (PutAck, error)
	Get(ctx context.Context, ref BlobRef) (BackupBlob, error)
	Has(ctx context.Context, ref BlobRef) (Presence, error)
	List(ctx context.Context, q ListQuery) (ListPage, error)
	Delete(ctx context.Context, ref BlobRef) (DeleteAck, error)
}

// ErrorCode is the typed error class an adapter surfaces so the flow can branch
// (retry / fail over / skip) without decoding provider text (spec A.1 notes).
type ErrorCode string

const (
	ErrAuthFailed    ErrorCode = "auth_failed"
	ErrNotFound      ErrorCode = "not_found"
	ErrQuotaExceeded ErrorCode = "quota_exceeded"
	ErrTooLarge      ErrorCode = "too_large"
	ErrUnsupported   ErrorCode = "unsupported"
	ErrProvider      ErrorCode = "provider_error"
)

// AdapterError carries a typed ErrorCode alongside a human message.
type AdapterError struct {
	Code    ErrorCode
	Op      string
	Message string
}

func (e *AdapterError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("sdnbackup: %s: %s: %s", e.Op, e.Code, e.Message)
	}
	return fmt.Sprintf("sdnbackup: %s: %s", e.Code, e.Message)
}

func adapterErr(code ErrorCode, op, format string, args ...interface{}) *AdapterError {
	return &AdapterError{Code: code, Op: op, Message: fmt.Sprintf(format, args...)}
}

// CodeOf extracts an AdapterError's code, or ErrProvider for a plain error and
// "" for nil — the branch key the runner uses for failover.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var ae *AdapterError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ErrProvider
}

// IsNotFound reports whether err is a typed not_found — the common failover
// signal (this adapter does not have the blob; try the next one).
func IsNotFound(err error) bool {
	return CodeOf(err) == ErrNotFound
}

// HashBytes returns the lowercase hex SHA-256 of b — the content hash a backup
// unit is addressed by.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NormalizeContentHash validates a content hash (64 lowercase hex chars) and
// returns it lowercased, mirroring appmanifest's guard so a malformed hash is
// rejected rather than producing a key that silently misses.
func NormalizeContentHash(contentHash string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(contentHash))
	if len(h) != 2*sha256.Size {
		return "", fmt.Errorf("sdnbackup: content hash must be %d hex chars, got %d", 2*sha256.Size, len(h))
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("sdnbackup: content hash is not valid hex: %w", err)
	}
	return h, nil
}

// ObjectKey derives the provider-native object key from a unit's kind and
// content hash: sdn-backup/<kind>/<hh>/<contentHash>, where <hh> is the first
// two hex chars of the hash (a fan-out prefix). The key IS the hash, so a put
// is idempotent and a has() is a cheap key-existence check (spec B).
func ObjectKey(kind Kind, contentHash string) (string, error) {
	h, err := NormalizeContentHash(contentHash)
	if err != nil {
		return "", err
	}
	if !knownKinds[kind] {
		return "", fmt.Errorf("sdnbackup: unknown backup kind %q", kind)
	}
	return "sdn-backup/" + string(kind) + "/" + h[:2] + "/" + h, nil
}

// keyPrefix is the shared root of every object key (used for list scans).
const keyPrefix = "sdn-backup/"
