package assetpin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ipfs/go-cid"
)

var ErrInvalidMetadata = errors.New("assetpin: invalid metadata")

// Metadata is the strict, versioned provenance document accepted with an
// asset upload. All fields except SchemaVersion are JSON strings.
type Metadata struct {
	SchemaVersion  int
	CandidateKey   string
	EntityID       string
	SourceRecordID string
	SourceURL      string
	LicenseName    string
	Attribution    string
	SHA256         string
	VAMID          string
}

// ParseCanonicalMetadata accepts only the canonical byte representation of
// the version-one asset metadata object and returns those exact bytes.
func ParseCanonicalMetadata(raw []byte) (Metadata, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	opening, err := decoder.Token()
	if err != nil {
		return Metadata{}, nil, invalidMetadata("decode object", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return Metadata{}, nil, invalidMetadata("top-level value must be an object", nil)
	}

	metadata := Metadata{}
	values := make(map[string]any, 9)
	seen := make(map[string]struct{}, 9)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Metadata{}, nil, invalidMetadata("decode object key", err)
		}
		key, ok := token.(string)
		if !ok {
			return Metadata{}, nil, invalidMetadata("object key must be a string", nil)
		}
		if _, duplicate := seen[key]; duplicate {
			return Metadata{}, nil, invalidMetadata("duplicate field "+key, nil)
		}
		seen[key] = struct{}{}

		var value any
		if err := decoder.Decode(&value); err != nil {
			return Metadata{}, nil, invalidMetadata("decode field "+key, err)
		}
		switch key {
		case "schemaVersion":
			number, ok := value.(json.Number)
			if !ok || number.String() != "1" {
				return Metadata{}, nil, invalidMetadata("schemaVersion must be numeric 1", nil)
			}
			metadata.SchemaVersion = 1
			values[key] = number
		case "candidateKey":
			if metadata.CandidateKey, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.CandidateKey
		case "entityId":
			if metadata.EntityID, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.EntityID
		case "sourceRecordId":
			if metadata.SourceRecordID, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.SourceRecordID
		case "sourceUrl":
			if metadata.SourceURL, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.SourceURL
		case "licenseName":
			if metadata.LicenseName, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.LicenseName
		case "attribution":
			if metadata.Attribution, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.Attribution
		case "sha256":
			if metadata.SHA256, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.SHA256
		case "vamId":
			if metadata.VAMID, ok = value.(string); !ok {
				return Metadata{}, nil, invalidMetadata(key+" must be a string", nil)
			}
			values[key] = metadata.VAMID
		default:
			return Metadata{}, nil, invalidMetadata("unknown field "+key, nil)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return Metadata{}, nil, invalidMetadata("decode object close", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return Metadata{}, nil, invalidMetadata("object is not closed", nil)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Metadata{}, nil, invalidMetadata("trailing data", err)
	}

	if _, ok := seen["schemaVersion"]; !ok {
		return Metadata{}, nil, invalidMetadata("schemaVersion is required", nil)
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "candidateKey", value: metadata.CandidateKey},
		{name: "sourceUrl", value: metadata.SourceURL},
		{name: "licenseName", value: metadata.LicenseName},
		{name: "sha256", value: metadata.SHA256},
	} {
		if strings.TrimSpace(required.value) == "" {
			return Metadata{}, nil, invalidMetadata(required.name+" is required", nil)
		}
	}
	for _, textual := range []struct {
		name  string
		value string
	}{
		{name: "candidateKey", value: metadata.CandidateKey},
		{name: "entityId", value: metadata.EntityID},
		{name: "sourceRecordId", value: metadata.SourceRecordID},
		{name: "sourceUrl", value: metadata.SourceURL},
		{name: "licenseName", value: metadata.LicenseName},
		{name: "attribution", value: metadata.Attribution},
		{name: "sha256", value: metadata.SHA256},
		{name: "vamId", value: metadata.VAMID},
	} {
		if strings.TrimSpace(textual.value) != textual.value {
			return Metadata{}, nil, invalidMetadata(textual.name+" must not contain surrounding whitespace", nil)
		}
	}
	if !isLowerSHA256(metadata.SHA256) {
		return Metadata{}, nil, invalidMetadata("sha256 must be lowercase 64-hex", nil)
	}

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(values); err != nil {
		return Metadata{}, nil, invalidMetadata("encode canonical object", err)
	}
	canonical := bytes.TrimSuffix(output.Bytes(), []byte{'\n'})
	if !bytes.Equal(raw, canonical) {
		return Metadata{}, nil, invalidMetadata("document is not canonical", nil)
	}
	return metadata, append([]byte(nil), canonical...), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func invalidMetadata(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrInvalidMetadata, message)
	}
	return fmt.Errorf("%w: %s: %v", ErrInvalidMetadata, message, cause)
}

// Asset pin recovery markers are deliberately owned by this package rather
// than storage or api so the retention worker can consume them without an
// import cycle. They contain only bounded provenance and token digests, never
// raw OIDC tokens or GLB bytes.
type AssetPinRecoveryPhase string

const (
	AssetPinRecoveryIntent            AssetPinRecoveryPhase = "pin_intent"
	AssetPinRecoveryPinnedUncommitted AssetPinRecoveryPhase = "pinned_uncommitted"

	AssetPinRecoveryMarkerMaxBytes = 128 << 10
	AssetPinRecoveryListMax        = 1000
)

var (
	ErrInvalidAssetPinRecoveryMarker  = errors.New("assetpin: invalid recovery marker")
	ErrAssetPinRecoveryMarkerExists   = errors.New("assetpin: recovery marker exists")
	ErrAssetPinRecoveryMarkerConflict = errors.New("assetpin: recovery marker conflict")
	ErrUnsafeAssetPinDirectory        = errors.New("assetpin: unsafe data directory")
)

// AssetPinRecoveryMarker is the durable compensation record spanning a Kubo
// pin and the journaled reference upsert. Its storage-independent primitives
// mirror the immutable reference and audit identity needed for reconciliation.
type AssetPinRecoveryMarker struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Phase         AssetPinRecoveryPhase `json:"phase"`
	ReferenceKey  string                `json:"referenceKey"`
	EventID       string                `json:"eventId"`
	CandidateKey  string                `json:"candidateKey"`
	CID           string                `json:"cid"`
	SHA256        string                `json:"sha256"`
	ByteCount     int64                 `json:"byteCount"`
	SourceURL     string                `json:"sourceUrl"`
	LicenseName   string                `json:"licenseName"`
	Attribution   string                `json:"attribution"`
	MetadataJSON  string                `json:"metadataJson"`
	TokenDigest   string                `json:"tokenDigest"`
	Repository    string                `json:"repository"`
	Ref           string                `json:"ref"`
	WorkflowRef   string                `json:"workflowRef"`
	Actor         string                `json:"actor"`
	WorkflowRunID string                `json:"workflowRunId"`
	RunAttempt    string                `json:"runAttempt"`
	CommitSHA     string                `json:"commitSha"`
	CreatedAt     time.Time             `json:"createdAt"`
	UpdatedAt     time.Time             `json:"updatedAt"`
	ExpiresAt     time.Time             `json:"expiresAt"`
}

// FileAssetPinRecoveryStore persists atomically replaced recovery markers
// below one resolved SDN data directory.
type FileAssetPinRecoveryStore struct {
	dataDir string
	mu      sync.Mutex
}

// NewFileAssetPinRecoveryStore binds a recovery store to an existing trusted
// data directory. Any symlinks in the supplied base are resolved once; child
// asset-pin directories are subsequently refused if they are symlinks.
func NewFileAssetPinRecoveryStore(dataDir string) (*FileAssetPinRecoveryStore, error) {
	resolved, err := resolveAssetPinDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	return &FileAssetPinRecoveryStore{dataDir: resolved}, nil
}

// SecureAssetPinTempDir returns a private, non-symlinked staging directory.
func SecureAssetPinTempDir(dataDir string) (string, error) {
	resolved, err := resolveAssetPinDataDir(dataDir)
	if err != nil {
		return "", err
	}
	return ensurePrivateAssetPinDirectory(resolved, "tmp")
}

// CreateIntent durably creates, but never replaces, a pre-Kubo intent.
func (s *FileAssetPinRecoveryStore) CreateIntent(marker AssetPinRecoveryMarker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if marker.Phase != AssetPinRecoveryIntent || marker.CID != "" {
		return fmt.Errorf("%w: new marker must be a CID-less pin intent", ErrInvalidAssetPinRecoveryMarker)
	}
	if err := validateAssetPinRecoveryMarker(marker); err != nil {
		return err
	}
	directory, err := ensurePrivateAssetPinDirectory(s.dataDir, "recovery")
	if err != nil {
		return err
	}
	path, err := assetPinRecoveryMarkerPath(directory, marker.ReferenceKey)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: reference already has a marker", ErrAssetPinRecoveryMarkerExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect recovery marker: %w", err)
	}
	return writeAssetPinRecoveryMarker(directory, path, marker, false)
}

// MarkPinned performs the only allowed marker transition. An identical
// repeated transition is idempotent; a different CID is a conflict.
func (s *FileAssetPinRecoveryStore) MarkPinned(referenceKey, cidValue string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := ensurePrivateAssetPinDirectory(s.dataDir, "recovery")
	if err != nil {
		return err
	}
	marker, ok, err := loadAssetPinRecoveryMarker(directory, referenceKey)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: pin intent is missing", ErrAssetPinRecoveryMarkerConflict)
	}
	cidValue, err = canonicalRecoveryCID(cidValue)
	if err != nil {
		return err
	}
	if marker.Phase == AssetPinRecoveryPinnedUncommitted {
		if marker.CID == cidValue {
			return nil
		}
		return fmt.Errorf("%w: pinned marker CID differs", ErrAssetPinRecoveryMarkerConflict)
	}
	if marker.Phase != AssetPinRecoveryIntent || marker.CID != "" {
		return fmt.Errorf("%w: marker phase cannot transition", ErrAssetPinRecoveryMarkerConflict)
	}
	marker.Phase = AssetPinRecoveryPinnedUncommitted
	marker.CID = cidValue
	if err := validateAssetPinRecoveryMarker(marker); err != nil {
		return err
	}
	path, err := assetPinRecoveryMarkerPath(directory, referenceKey)
	if err != nil {
		return err
	}
	return writeAssetPinRecoveryMarker(directory, path, marker, true)
}

// Load reads and strictly validates one bounded marker.
func (s *FileAssetPinRecoveryStore) Load(referenceKey string) (AssetPinRecoveryMarker, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := ensurePrivateAssetPinDirectory(s.dataDir, "recovery")
	if err != nil {
		return AssetPinRecoveryMarker{}, false, err
	}
	return loadAssetPinRecoveryMarker(directory, referenceKey)
}

// List returns up to limit markers in deterministic reference-key order.
// The caller must request a positive bound; larger requests are capped.
func (s *FileAssetPinRecoveryStore) List(limit int) ([]AssetPinRecoveryMarker, error) {
	if limit <= 0 {
		return nil, errors.New("asset pin recovery marker limit must be positive")
	}
	if limit > AssetPinRecoveryListMax {
		limit = AssetPinRecoveryListMax
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := ensurePrivateAssetPinDirectory(s.dataDir, "recovery")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("list asset pin recovery markers: %w", err)
	}
	markers := make([]AssetPinRecoveryMarker, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len(markers) == limit {
			break
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("%w: recovery marker is not a regular file", ErrUnsafeAssetPinDirectory)
		}
		referenceKey := strings.TrimSuffix(name, ".json")
		marker, ok, err := loadAssetPinRecoveryMarker(directory, referenceKey)
		if err != nil {
			return nil, err
		}
		if ok {
			markers = append(markers, marker)
		}
	}
	sort.Slice(markers, func(i, j int) bool { return markers[i].ReferenceKey < markers[j].ReferenceKey })
	return markers, nil
}

// Remove durably removes a marker. A missing marker is already reconciled.
func (s *FileAssetPinRecoveryStore) Remove(referenceKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := ensurePrivateAssetPinDirectory(s.dataDir, "recovery")
	if err != nil {
		return err
	}
	path, err := assetPinRecoveryMarkerPath(directory, referenceKey)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect recovery marker before removal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: recovery marker is not a regular file", ErrUnsafeAssetPinDirectory)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove asset pin recovery marker: %w", err)
	}
	return syncAssetPinDirectory(directory)
}

func resolveAssetPinDataDir(dataDir string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", fmt.Errorf("%w: data directory is required", ErrUnsafeAssetPinDirectory)
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve asset pin data directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: resolve data directory: %v", ErrUnsafeAssetPinDirectory, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: data directory is not an existing directory", ErrUnsafeAssetPinDirectory)
	}
	return resolved, nil
}

func ensurePrivateAssetPinDirectory(dataDir, leaf string) (string, error) {
	if leaf != "tmp" && leaf != "recovery" {
		return "", fmt.Errorf("%w: unsupported asset pin directory", ErrUnsafeAssetPinDirectory)
	}
	current := dataDir
	for _, component := range []string{"asset-pins", leaf} {
		parent := current
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return "", fmt.Errorf("create private asset pin directory: %w", mkdirErr)
			}
			info, err = os.Lstat(current)
			if err == nil {
				if syncErr := syncAssetPinDirectory(parent); syncErr != nil {
					return "", syncErr
				}
			}
		}
		if err != nil {
			return "", fmt.Errorf("inspect private asset pin directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%w: asset pin directory component is not a real directory", ErrUnsafeAssetPinDirectory)
		}
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(current, 0o700); err != nil {
				return "", fmt.Errorf("secure asset pin directory: %w", err)
			}
			if err := syncAssetPinDirectory(current); err != nil {
				return "", err
			}
		}
	}
	return current, nil
}

func assetPinRecoveryMarkerPath(directory, referenceKey string) (string, error) {
	if !isLowerSHA256(referenceKey) {
		return "", fmt.Errorf("%w: reference key must be lowercase SHA-256 hex", ErrInvalidAssetPinRecoveryMarker)
	}
	return filepath.Join(directory, referenceKey+".json"), nil
}

func writeAssetPinRecoveryMarker(directory, path string, marker AssetPinRecoveryMarker, replace bool) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode asset pin recovery marker: %w", err)
	}
	if len(data) > AssetPinRecoveryMarkerMaxBytes {
		return fmt.Errorf("%w: encoded marker exceeds limit", ErrInvalidAssetPinRecoveryMarker)
	}
	temporary, err := os.CreateTemp(directory, ".asset-pin-recovery-*.tmp")
	if err != nil {
		return fmt.Errorf("create asset pin recovery temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure asset pin recovery temp file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write asset pin recovery marker: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync asset pin recovery marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close asset pin recovery marker: %w", err)
	}
	if replace {
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("replace asset pin recovery marker: %w", err)
		}
		temporaryPath = ""
	} else {
		if err := os.Link(temporaryPath, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: reference already has a marker", ErrAssetPinRecoveryMarkerExists)
			}
			return fmt.Errorf("install asset pin recovery marker: %w", err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return fmt.Errorf("remove linked asset pin recovery temp file: %w", err)
		}
		temporaryPath = ""
	}
	if err := syncAssetPinDirectory(directory); err != nil {
		return err
	}
	return nil
}

func loadAssetPinRecoveryMarker(directory, referenceKey string) (AssetPinRecoveryMarker, bool, error) {
	path, err := assetPinRecoveryMarkerPath(directory, referenceKey)
	if err != nil {
		return AssetPinRecoveryMarker{}, false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return AssetPinRecoveryMarker{}, false, nil
	}
	if err != nil {
		return AssetPinRecoveryMarker{}, false, fmt.Errorf("inspect asset pin recovery marker: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return AssetPinRecoveryMarker{}, false, fmt.Errorf("%w: recovery marker file is unsafe", ErrUnsafeAssetPinDirectory)
	}
	file, err := os.Open(path)
	if err != nil {
		return AssetPinRecoveryMarker{}, false, fmt.Errorf("open asset pin recovery marker: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, AssetPinRecoveryMarkerMaxBytes+1))
	if err != nil {
		return AssetPinRecoveryMarker{}, false, fmt.Errorf("read asset pin recovery marker: %w", err)
	}
	if len(data) > AssetPinRecoveryMarkerMaxBytes {
		return AssetPinRecoveryMarker{}, false, fmt.Errorf("%w: marker exceeds limit", ErrInvalidAssetPinRecoveryMarker)
	}
	marker, err := decodeAssetPinRecoveryMarker(data)
	if err != nil {
		return AssetPinRecoveryMarker{}, false, err
	}
	if marker.ReferenceKey != referenceKey {
		return AssetPinRecoveryMarker{}, false, fmt.Errorf("%w: filename and reference differ", ErrInvalidAssetPinRecoveryMarker)
	}
	return marker, true, nil
}

func decodeAssetPinRecoveryMarker(data []byte) (AssetPinRecoveryMarker, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var marker AssetPinRecoveryMarker
	if err := decoder.Decode(&marker); err != nil {
		return AssetPinRecoveryMarker{}, fmt.Errorf("%w: decode marker: %v", ErrInvalidAssetPinRecoveryMarker, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return AssetPinRecoveryMarker{}, fmt.Errorf("%w: trailing marker data", ErrInvalidAssetPinRecoveryMarker)
	}
	canonical, err := json.Marshal(marker)
	if err != nil || !bytes.Equal(data, canonical) {
		return AssetPinRecoveryMarker{}, fmt.Errorf("%w: marker is not canonical JSON", ErrInvalidAssetPinRecoveryMarker)
	}
	if err := validateAssetPinRecoveryMarker(marker); err != nil {
		return AssetPinRecoveryMarker{}, err
	}
	return marker, nil
}

func validateAssetPinRecoveryMarker(marker AssetPinRecoveryMarker) error {
	invalid := func(message string) error {
		return fmt.Errorf("%w: %s", ErrInvalidAssetPinRecoveryMarker, message)
	}
	if marker.SchemaVersion != 1 {
		return invalid("schemaVersion must be 1")
	}
	if !isLowerSHA256(marker.ReferenceKey) || !isLowerSHA256(marker.EventID) ||
		!isLowerSHA256(marker.SHA256) || !isLowerSHA256(marker.TokenDigest) {
		return invalid("digest fields must be lowercase SHA-256 hex")
	}
	if marker.ReferenceKey != assetPinRecoveryStableID("asset-pin-reference:v1\n"+marker.CandidateKey) ||
		marker.EventID != assetPinRecoveryStableID("asset-pin-upsert:v1\n"+marker.ReferenceKey) {
		return invalid("deterministic identities do not match candidate")
	}
	if marker.ByteCount <= 0 {
		return invalid("byteCount must be positive")
	}
	for _, field := range []struct {
		name     string
		value    string
		required bool
	}{
		{name: "candidateKey", value: marker.CandidateKey, required: true},
		{name: "sourceUrl", value: marker.SourceURL, required: true},
		{name: "licenseName", value: marker.LicenseName, required: true},
		{name: "attribution", value: marker.Attribution},
		{name: "repository", value: marker.Repository, required: true},
		{name: "ref", value: marker.Ref, required: true},
		{name: "workflowRef", value: marker.WorkflowRef, required: true},
		{name: "actor", value: marker.Actor, required: true},
		{name: "workflowRunId", value: marker.WorkflowRunID, required: true},
		{name: "runAttempt", value: marker.RunAttempt, required: true},
		{name: "commitSha", value: marker.CommitSHA, required: true},
	} {
		if field.required && strings.TrimSpace(field.value) == "" {
			return invalid(field.name + " is required")
		}
		if strings.TrimSpace(field.value) != field.value {
			return invalid(field.name + " has surrounding whitespace")
		}
	}
	metadata, canonical, err := ParseCanonicalMetadata([]byte(marker.MetadataJSON))
	if err != nil || string(canonical) != marker.MetadataJSON ||
		metadata.CandidateKey != marker.CandidateKey || metadata.SHA256 != marker.SHA256 ||
		metadata.SourceURL != marker.SourceURL || metadata.LicenseName != marker.LicenseName ||
		metadata.Attribution != marker.Attribution {
		return invalid("canonical metadata does not match marker identity")
	}
	switch marker.Phase {
	case AssetPinRecoveryIntent:
		if marker.CID != "" {
			return invalid("pin intent must not contain a CID")
		}
	case AssetPinRecoveryPinnedUncommitted:
		canonicalCID, err := canonicalRecoveryCID(marker.CID)
		if err != nil || canonicalCID != marker.CID {
			return invalid("pinned marker requires canonical CIDv1")
		}
	default:
		return invalid("unknown phase")
	}
	if err := validateAssetPinRecoveryTime("createdAt", marker.CreatedAt); err != nil {
		return err
	}
	if err := validateAssetPinRecoveryTime("updatedAt", marker.UpdatedAt); err != nil {
		return err
	}
	if err := validateAssetPinRecoveryTime("expiresAt", marker.ExpiresAt); err != nil {
		return err
	}
	if marker.UpdatedAt.Before(marker.CreatedAt) || marker.ExpiresAt.Before(marker.UpdatedAt) {
		return invalid("timestamps are not monotonic")
	}
	return nil
}

func validateAssetPinRecoveryTime(name string, value time.Time) error {
	if value.IsZero() || !time.Unix(0, value.UnixNano()).UTC().Equal(value) {
		return fmt.Errorf("%w: %s is invalid or outside UnixNano range", ErrInvalidAssetPinRecoveryMarker, name)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%w: %s must be UTC", ErrInvalidAssetPinRecoveryMarker, name)
	}
	return nil
}

func canonicalRecoveryCID(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := cid.Decode(value)
	if err != nil || parsed.Version() != 1 || parsed.String() != value {
		return "", fmt.Errorf("%w: CID must be canonical CIDv1", ErrInvalidAssetPinRecoveryMarker)
	}
	return value, nil
}

func assetPinRecoveryStableID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func syncAssetPinDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open asset pin directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		// APFS may report EINVAL for directory fsync even though file fsync and
		// rename are supported. Linux failures, and every other Darwin error,
		// remain fatal.
		if runtime.GOOS == "darwin" && errors.Is(err, syscall.EINVAL) {
			return nil
		}
		return fmt.Errorf("sync asset pin directory: %w", err)
	}
	return nil
}
