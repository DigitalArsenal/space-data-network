package pmm

// The self-serve submission lane: an ANONYMOUS, no-admin-wallet path for a
// third party to list a PLAINTEXT module with this provider.
//
// WHAT THE LANE IS. The manifest (/.well-known/sdn/modules.pmm) and the
// artifact bytes (/modules/) are anonymous by construction — they live OUTSIDE
// the daemon's /api/ auth wall, which gates /api/ and /orbpro-key-broker/
// only. Listing a module with ACCESS_POLICY=ANONYMOUS has therefore never
// required an admin wallet; what has been missing is any way for a third party
// to get a listing AT ALL — the catalog is operator hand-edited on disk
// (sdn-server/internal/node/pmm_plugin.go:35, documented in the repo's
// docs/pmm-listing-submission.md). This lane is that missing path.
//
// WHAT THE LANE IS NOT. It is not the protected publish lane. The admin-wallet
// gate on the encrypted-bundle lane (internal/license/publish_protocol.go:203
// refuses a non-admin signer) is untouched: a submission can never carry an
// ENTITLED access policy, can never claim CORE or RECOMMENDED trust, and can
// never request an encrypted artifact. The lane materializes exactly three
// facts about every submission, and those facts are policy, not data — a
// submitter cannot negotiate them:
//
//	ACCESS_POLICY  = ANONYMOUS   (the bytes are plaintext and publicly served)
//	TRUST_TIER     = OPTIONAL    (listed, NOT endorsed by this node)
//	ENTRY_STATE    = ACTIVE      (withdrawal is the operator's act)
//	DEFAULT_ENABLED= false       (a client auto-enables only endorsed modules)
//
// The manifest stays signed by the node key over the canonical statement, so
// an anonymous consumer verifies integrity and provenance exactly as before;
// the node vouches for the bytes it serves (content hash), never for the
// module's behavior. The self-serve lane is a connector, like the rest of this
// package: it persists DATA (a submission record + the artifact bytes the node
// will serve), and the same LoadCatalog -> BuildManifest -> Sign -> Serve path
// publishes it. No tier table, no storefront logic, no licensing logic is
// added to Go.
//
// TRUST MODEL FOR THE OPERATOR. A submission becomes a signed manifest entry
// after a store->rebuild round. The manifest therefore CONTAINS what strangers
// send, with these protections:
//
//   - The operator catalog always wins on MODULE_ID collisions (MergeSubmissions).
//   - An entry cannot be submitted whose MODULE_ID already lives in the
//     operator catalog (409 at submit time).
//   - Submissions are content-addressed: the node hashes the artifact bytes it
//     actually received and stored; a declared hash is never trusted.
//   - A stored submission that cannot be re-hashed from disk (deleted,
//     tampered record, corrupt file) is dropped from the merge with a logged
//     skip, so one wedged submission can never take the whole $PMM surface
//     down. Deleting a submission's record (or its artifact) is therefore a
//     withdrawal, and is exactly as durable as the operator wants it to be.
//
// ARTIFACT_SIGNATURE is deliberately left EMPTY on submitted entries, same as
// on operator entries: the field stays blank pending an owner ruling on the
// Seal Council's domain-separation dissent (see Entry.ArtifactSignature and
// Sign). PMM.SIGNATURE already covers every CONTENT_HASH through the canonical
// statement, so an empty value costs a bandwidth optimisation, not integrity.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxSubmissionArtifactBytes caps a single submitted artifact. 64 MiB mirrors
// the node's content-bound module signer cap (internal/modulesign:MaxArtifactBytes)
// and stays far below anything that could wedge the daemon by memory pressure.
const MaxSubmissionArtifactBytes = 64 << 20

// maxSubmissionBodyBytes caps the whole multipart request (artifact + small
// metadata envelope) and lets the handler reject greedy callers before any
// parsing work begins.
const maxSubmissionBodyBytes = MaxSubmissionArtifactBytes + 1<<20

// maxSubmissionMetadataBytes caps the JSON metadata part. It is tiny by
// design: every meaningful field has a tighter cap in validateSubmission.
const maxSubmissionMetadataBytes = 64 << 10

// Lane-enforced policy (see the package comment for why the submitter cannot
// negotiate these).
const (
	submissionAccessPolicy   = "ANONYMOUS"
	submissionTrustTier      = "OPTIONAL"
	submissionEntryState     = "ACTIVE"
	submissionDefaultEnabled = false
)

// moduleIDRe is the on-disk-safe MODULE_ID grammar. Reverse-DNS style IDs
// ("com.orbpro.sgp4") are the house convention; anything that could escape the
// artifact directory (slash, backslash, ".." as a path element, control
// characters) is refused outright rather than sanitized — a listing is
// content-addressable identity, and an ID that had to be rewritten to be
// stored would be a lie.
var moduleIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// versionRe is the on-disk-safe VERSION grammar.
var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// wasmMagic is the WebAssembly binary preamble "\0asm" followed by the
// little-endian version. Requiring it the same way the node's signer does
// ("never sign a caller-supplied digest", internal/modulesign) keeps this lane
// from persisting bytes it can never meaningfully list.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// SubmissionMetadata is the public contract for what a third party sends.
//
// API keys are lowercase on purpose — the request envelope is API surface,
// not an SDS record; the IDL capitalization rule governs the record the node
// publishes (Entry), which this metadata is materialized into. Exactly one
// rule is imported from the record side: PLUGIN_TYPE must be a real
// `pluginCategory` IDL symbol, and a blank one is REJECTED, never defaulted —
// the enum's zero value is Sensor, and a defaulted blank is a wrong answer,
// not a missing one (see validPluginTypes).
type SubmissionMetadata struct {
	ModuleID         string   `json:"module_id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Version          string   `json:"version"`
	PluginType       string   `json:"plugin_type"`
	License          string   `json:"license"`
	DocumentationURL string   `json:"documentation_url"`
	IconURL          string   `json:"icon_url"`
	RuntimeTargets   []string `json:"runtime_targets"`
	RequiredSchemas  []string `json:"required_schemas"`
	MinPermissions   []string `json:"min_permissions"`
	SupersedesHash   string   `json:"supersedes_content_hash"`
	SubmitterContact string   `json:"submitter_contact"`
}

// StoredSubmission is what the lane persists on disk for one submission: the
// catalog-shaped entry (Entry, IDL-cased, source_artifact naming the bytes the
// node serves) plus the node's own provenance stamps. The Entry is merged into
// the catalog by MergeSubmissions; the provenance fields are node-local data
// that never reaches any projection of the manifest.
type StoredSubmission struct {
	Entry            Entry  `json:"entry"`
	SubmittedAt      string `json:"submitted_at"`
	SubmitterContact string `json:"submitter_contact,omitempty"`
}

// ErrDuplicateSubmission reports a submission that would change nothing: the
// same MODULE_ID, same VERSION and same artifact bytes are already listed.
// The node maps this to HTTP 409.
var ErrDuplicateSubmission = errors.New("pmm: module is already listed with identical bytes")

// SubmissionStore persists submissions under an artifact root, one record
// file and one artifact directory per MODULE_ID:
//
//	<root>/submissions/<MODULE_ID>.json                        (record)
//	<root>/submissions/artifacts/<MODULE_ID>/<VERSION>/module.wasm
//
// The SourceArtifact value stored in the record is relative to the artifact
// root, exactly like operator catalog entries, so the existing
// LoadCatalog/hash-from-disk machinery is reused unchanged for the bytes.
//
// The lane is write-once per (MODULE_ID, VERSION): a resubmission with the
// same VERSION is refused (a listed artifact path is immutable by design),
// while a NEW VERSION of the same MODULE_ID replaces the record and chains
// SUPERSEDES_CONTENT_HASH to the previous version's hash. The previous
// version's bytes stay on disk — they are content-addressed and may still be
// in a client's cache or a verifier's hands — until the operator cleans up.
type SubmissionStore struct {
	dir string // <artifactRoot>/submissions
}

// NewSubmissionStore returns the store for the given artifact root. The store
// only touches <root>/submissions; it never writes the operator catalog.
func NewSubmissionStore(artifactRoot string) *SubmissionStore {
	return &SubmissionStore{dir: filepath.Join(artifactRoot, "submissions")}
}

// Dir returns the on-disk store directory (used by callers that want to
// surface the location).
func (s *SubmissionStore) Dir() string { return s.dir }

// Exists reports whether a record file already exists for MODULE_ID.
//
// The MODULE_ID is used verbatim as the file name, exactly like Save: a caller
// may pass any string here, but only a validateSubmission-passed MODULE_ID
// (moduleIDRe, no "..") names a record this store could ever have created.
func (s *SubmissionStore) Exists(moduleID string) bool {
	if s == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.dir, moduleID+".json"))
	return err == nil
}

// Save validates, persists and returns a submission.
//
// It is the lane's choke point: every constraint that keeps the lane
// ANONYMOUS/plaintext/no-admin-wallet is enforced HERE, before anything is
// written. Callers should hold whatever serialization they need to make
// Save+publish atomic with respect to the manifest refresh.
func (s *SubmissionStore) Save(md *SubmissionMetadata, artifact []byte, now time.Time) (*StoredSubmission, error) {
	if s == nil {
		return nil, errors.New("pmm: submission store unavailable")
	}
	if md == nil {
		return nil, errors.New("pmm: submission metadata required")
	}
	if err := validateSubmission(md, artifact); err != nil {
		return nil, err
	}

	// MODULE_ID and VERSION are already validated safe for the filesystem
	// (moduleIDRe/versionRe) before Save is called; they are used verbatim so
	// the on-disk identity is byte-identical to the listed identity.
	file := filepath.Join(s.dir, md.ModuleID+".json")
	artifactDir := filepath.Join(s.dir, "artifacts", md.ModuleID, md.Version)
	artifactFile := filepath.Join(artifactDir, "module.wasm")

	existing, exists, existingErr := s.readRecord(md.ModuleID)
	if existingErr != nil {
		// A corrupt record cannot be chained onto (its CONTENT_HASH is
		// unknowable). Refuse and let the operator clear the record; Load
		// keeps dropping it with a logged reason in the meantime.
		return nil, existingErr
	}
	if exists {
		if existing.Entry.Version == md.Version {
			// Same version: bytes at this listing path are immutable. Refuse
			// whatever arrives, whether or not it differs.
			return nil, fmt.Errorf("%w: %s %s", ErrDuplicateSubmission, md.ModuleID, md.Version)
		}
		if err := s.writeFile(artifactFile, artifact); err != nil {
			return nil, err
		}
		hash, size, err := HashArtifact(artifactFile)
		if err != nil {
			return nil, err
		}
		supersedes := existing.Entry.ContentHash
		stored := s.materialize(md, hash, size, "submissions/artifacts/"+md.ModuleID+"/"+md.Version+"/module.wasm", supersedes, now)
		if err := s.writeFile(file, mustJSON(stored)); err != nil {
			return nil, err
		}
		return stored, nil
	}

	// New module: the record must be written BEFORE the artifact is visible to
	// a hash-from-disk load, so a torn write can never produce a record that
	// names bytes that are not there. The artifact is written first; a record
	// write failure leaves an orphan artifact (harmless, unreferenced).
	if err := s.writeFile(artifactFile, artifact); err != nil {
		return nil, err
	}
	hash, size, err := HashArtifact(artifactFile)
	if err != nil {
		return nil, err
	}
	stored := s.materialize(md, hash, size, "submissions/artifacts/"+md.ModuleID+"/"+md.Version+"/module.wasm", "", now)
	if err := s.writeFile(file, mustJSON(stored)); err != nil {
		return nil, err
	}
	return stored, nil
}

// materialize turns the public contract into the catalog-shaped Entry. The
// lane policy is applied here — a submitter can never set ACCESS_POLICY,
// TRUST_TIER, ENTRY_STATE or DEFAULT_ENABLED, because those fields are not in
// the contract (SubmissionMetadata has no such keys) and the values below are
// unconditional. The submitter's own keys (description, license, URLs,
// permissions...) pass through as DATA, exactly like operator catalog input.
func (s *SubmissionStore) materialize(md *SubmissionMetadata, hash string, size uint64, sourceArtifact, supersedes string, now time.Time) *StoredSubmission {
	stamp := now.UTC().Format("2006-01-02T15:04:05.000Z")
	e := Entry{
		ModuleID:         md.ModuleID,
		Name:             md.Name,
		Description:      md.Description,
		Version:          md.Version,
		ContentHash:      hash,
		SizeBytes:        size,
		ArtifactPath:     ArtifactPrefix + "submissions/" + md.ModuleID + "/" + md.Version + "/module.wasm",
		TrustTier:        submissionTrustTier,
		DefaultEnabled:   submissionDefaultEnabled,
		AccessPolicy:     submissionAccessPolicy,
		EntryState:       submissionEntryState,
		RuntimeTargets:   cleanStrings(md.RuntimeTargets),
		RequiredSchemas:  cleanStrings(md.RequiredSchemas),
		MinPermissions:   cleanStrings(md.MinPermissions),
		License:          md.License,
		DocumentationURL: md.DocumentationURL,
		IconURL:          md.IconURL,
		SupersedesHash:   supersedes,
		UpdatedAt:        stamp,
		PluginType:       md.PluginType,
		SourceArtifact:   sourceArtifact,
	}
	return &StoredSubmission{Entry: e, SubmittedAt: stamp, SubmitterContact: md.SubmitterContact}
}

// validateSubmission enforces the lane's contract. Every failure is a caller
// bug and must map to HTTP 400; only Save's duplicate rule maps to 409.
func validateSubmission(md *SubmissionMetadata, artifact []byte) error {
	if strings.TrimSpace(md.ModuleID) == "" {
		return errors.New("module_id is required")
	}
	if !moduleIDRe.MatchString(md.ModuleID) || strings.Contains(md.ModuleID, "..") {
		return errors.New("module_id must match [A-Za-z0-9][A-Za-z0-9._-]* and must not contain '..'")
	}
	if len(md.ModuleID) > 200 {
		return errors.New("module_id is too long (max 200 characters)")
	}
	if strings.TrimSpace(md.Name) == "" {
		return errors.New("name is required")
	}
	if len(md.Name) > 200 {
		return errors.New("name is too long (max 200 characters)")
	}
	if strings.TrimSpace(md.Version) == "" {
		return errors.New("version is required")
	}
	if !versionRe.MatchString(md.Version) || strings.Contains(md.Version, "..") {
		return errors.New("version must match [A-Za-z0-9][A-Za-z0-9._+-]* and must not contain '..'")
	}
	if len(md.Version) > 64 {
		return errors.New("version is too long (max 64 characters)")
	}
	if strings.TrimSpace(md.PluginType) == "" {
		return errors.New("plugin_type is required (a pluginCategory symbol, e.g. Propagator or Unspecified)")
	}
	if !validPluginTypes[md.PluginType] {
		return fmt.Errorf("plugin_type %q is not a pluginCategory symbol", md.PluginType)
	}
	for field, url := range map[string]string{"documentation_url": md.DocumentationURL, "icon_url": md.IconURL} {
		if url == "" {
			continue
		}
		if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
			return fmt.Errorf("%s must be an absolute http(s) URL or empty", field)
		}
		if len(url) > 512 {
			return fmt.Errorf("%s is too long (max 512 characters)", field)
		}
	}
	if len(md.License) > 256 {
		return errors.New("license is too long (max 256 characters)")
	}
	if len(md.SubmitterContact) > 256 {
		return errors.New("submitter_contact is too long (max 256 characters)")
	}
	for field, values := range map[string][]string{
		"runtime_targets":  md.RuntimeTargets,
		"required_schemas": md.RequiredSchemas,
		"min_permissions":  md.MinPermissions,
	} {
		if len(values) > 16 {
			return fmt.Errorf("%s has too many entries (max 16)", field)
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" || len(v) > 128 {
				return fmt.Errorf("%s entries must be non-empty and at most 128 characters", field)
			}
		}
	}
	if len(artifact) == 0 {
		return errors.New("artifact is required")
	}
	if len(artifact) > MaxSubmissionArtifactBytes {
		return fmt.Errorf("artifact exceeds the %d byte limit", MaxSubmissionArtifactBytes)
	}
	if !isWasm(artifact) {
		return errors.New("artifact is not a wasm binary (missing the \\x00asm magic with version 1)")
	}
	return nil
}

// isWasm reports whether the bytes begin with the WebAssembly magic and
// version 1. A trailed publication container still starts with the magic — the
// trailer appends (payload || REC || len || "$REC") — so a submitter may POST
// an already-published container and the lane hashes the same portable bytes
// the loader compiles (HashArtifact strips the trailer).
func isWasm(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	if b[0] != wasmMagic[0] || b[1] != wasmMagic[1] || b[2] != wasmMagic[2] || b[3] != wasmMagic[3] {
		return false
	}
	v := uint32(b[4]) | uint32(b[5])<<8 | uint32(b[6])<<16 | uint32(b[7])<<24
	return v == 1
}

// Load lists the stored submissions, re-hashing every artifact from disk, and
// reports per-record problems as skipped records.
//
// This is the resilience seam of the lane: a submission is merged into the
// catalog ONLY when its record decodes AND its bytes re-hash to the stored
// CONTENT_HASH. Anything else is skipped with a reported reason — a deleted
// artifact is a withdrawal, a tampered record is stopped at the door — so one
// bad submission can never take down the signed manifest the operator worked
// for. The operator catalog itself keeps its hard-fail behavior (LoadCatalog):
// a broken operator catalog is an operator bug; a broken submission is a
// stranger's.
func (s *SubmissionStore) Load() ([]StoredSubmission, []error) {
	if s == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // the lane never had a submission
		}
		return nil, []error{fmt.Errorf("pmm: read submission store: %w", err)}
	}
	var stored []StoredSubmission
	var skips []error
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		sub, ok, skipErr := s.readRecord(strings.TrimSuffix(de.Name(), ".json"))
		switch {
		case skipErr != nil:
			skips = append(skips, skipErr)
		case !ok:
			// Record disappeared mid-scan; treat as withdrawn.
			continue
		default:
			if err := s.verifyBytes(&sub); err != nil {
				skips = append(skips, err)
				continue
			}
			stored = append(stored, sub)
		}
	}
	return stored, skips
}

// readRecord decodes one record file. The bool reports existence: a record
// that vanished between list and read is simply not there. The error reports a
// corrupt record. Either way the file is skipped by Load.
func (s *SubmissionStore) readRecord(moduleID string) (StoredSubmission, bool, error) {
	var sub StoredSubmission
	raw, err := os.ReadFile(filepath.Join(s.dir, moduleID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return sub, false, nil
		}
		return sub, false, fmt.Errorf("pmm: read submission %s: %w", moduleID, err)
	}
	if err := json.Unmarshal(raw, &sub); err != nil {
		return sub, false, fmt.Errorf("pmm: submission %s is corrupt: %w", moduleID, err)
	}
	if strings.TrimSpace(sub.Entry.ModuleID) != moduleID {
		return sub, false, fmt.Errorf("pmm: submission file %s declares MODULE_ID %q", moduleID, sub.Entry.ModuleID)
	}
	return sub, true, nil
}

// verifyBytes re-hashes a stored submission's artifact and refuses the record
// when the bytes disagree with the stored CONTENT_HASH, the artifact is
// missing (withdrawal), or the record names no artifact.
func (s *SubmissionStore) verifyBytes(sub *StoredSubmission) error {
	if strings.TrimSpace(sub.Entry.SourceArtifact) == "" {
		return fmt.Errorf("pmm: submission %s declares no source artifact; skipped", sub.Entry.ModuleID)
	}
	full := filepath.Join(filepath.Dir(s.dir), sub.Entry.SourceArtifact)
	hash, _, err := HashArtifact(full)
	if err != nil {
		return fmt.Errorf("pmm: submission %s artifact unavailable (withdrawn?): %w", sub.Entry.ModuleID, err)
	}
	if sub.Entry.ContentHash == "" || sub.Entry.ContentHash != hash {
		return fmt.Errorf("pmm: submission %s CONTENT_HASH does not match the artifacts it names; skipped (tampered record?)", sub.Entry.ModuleID)
	}
	return nil
}

// MergeSubmissions appends stored submissions to the catalog. The operator
// catalog ALWAYS wins: any MODULE_ID already managed there (active, withdrawn,
// revoked — any state) suppresses the submission, which the submit path
// refused up front anyway; the merge rule is the belt, the 409 is the
// suspenders. The result is deterministic (input order) and the manifest
// builder sorts bytewise by MODULE_ID before signing.
func (cf *CatalogFile) MergeSubmissions(stored []StoredSubmission) (added int, suppressed []string) {
	managed := make(map[string]struct{}, len(cf.Entries))
	for i := range cf.Entries {
		managed[cf.Entries[i].ModuleID] = struct{}{}
	}
	for i := range stored {
		id := stored[i].Entry.ModuleID
		if _, dup := managed[id]; dup {
			suppressed = append(suppressed, id)
			continue
		}
		managed[id] = struct{}{}
		cf.Entries = append(cf.Entries, stored[i].Entry)
		added++
	}
	return added, suppressed
}

// SubmissionHandler is the HTTP surface of the lane. It needs three inputs:
//
//   - the store to persist into,
//   - a catalogHas callback answering "does the OPERATOR catalog manage this
//     MODULE_ID" (the lane refuses such IDs up front), and
//   - an optional publish callback that re-reads catalog + submissions and
//     re-signs the manifest. The caller decides its own serialization; the
//     callback is invoked AFTER a successful save, so a failed publish is
//     reported in the receipt ("stored, not yet listed") rather than losing
//     the submission.
//
// The handler is deliberately dumb HTTP in / policy data out: validation
// happens in validateSubmission, persistence in the store, publication in the
// callback — this file adds no policy table to Go.
type SubmissionHandler struct {
	store      *SubmissionStore
	catalogHas func(moduleID string) bool
	publish    func() error
}

// NewSubmissionHandler builds the lane handler. catalogHas may be nil (no
// operator-catalog conflict check); publish may be nil (stored and listed on
// the next refresh only).
func NewSubmissionHandler(store *SubmissionStore, catalogHas func(string) bool, publish func() error) *SubmissionHandler {
	return &SubmissionHandler{store: store, catalogHas: catalogHas, publish: publish}
}

// ServeHTTP implements http.Handler: POST only, multipart/form-data with a
// "metadata" JSON field and an "artifact" file field.
//
// Responses:
//
//	202                      accepted; receipt body (see writeReceipt)
//	400                      contract violation (missing/invalid fields, non-wasm bytes)
//	409                      MODULE_ID is managed by the operator catalog, or an
//	                         identical listing already exists
//	413                      request body over maxSubmissionBodyBytes
//	415                      content type is not multipart/form-data
//	500                      the lane could not persist
func (h *SubmissionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.store == nil {
		http.Error(w, "submission lane unavailable", http.StatusServiceUnavailable)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		http.Error(w, "multipart/form-data required (fields: metadata, artifact)", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmissionBodyBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusBadRequest
		if isTooLarge(err) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid multipart body", status)
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	rawMeta := r.FormValue("metadata")
	if len(rawMeta) > maxSubmissionMetadataBytes {
		http.Error(w, "metadata too large", http.StatusRequestEntityTooLarge)
		return
	}
	var md SubmissionMetadata
	if err := json.Unmarshal([]byte(rawMeta), &md); err != nil {
		http.Error(w, "metadata is not valid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	rf, _, err := r.FormFile("artifact")
	if err != nil {
		http.Error(w, "artifact file field is required", http.StatusBadRequest)
		return
	}
	defer func() { _ = rf.Close() }()
	artifact, err := io.ReadAll(io.LimitReader(rf, MaxSubmissionArtifactBytes+1))
	if err != nil {
		http.Error(w, "artifact could not be read", http.StatusBadRequest)
		return
	}
	if len(artifact) > MaxSubmissionArtifactBytes {
		http.Error(w, "artifact exceeds the byte limit", http.StatusRequestEntityTooLarge)
		return
	}
	if h.catalogHas != nil && h.catalogHas(md.ModuleID) {
		http.Error(w, "module_id is already managed in this provider's catalog", http.StatusConflict)
		return
	}

	stored, err := h.store.Save(&md, artifact, time.Now())
	if err != nil {
		if errors.Is(err, ErrDuplicateSubmission) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "submission not accepted: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.writeReceipt(w, stored, h.doPublish())
}

// doPublish invokes the caller's manifest-rebuild hook (nil-safe).
func (h *SubmissionHandler) doPublish() error {
	if h == nil || h.publish == nil {
		return nil
	}
	return h.publish()
}

func (h *SubmissionHandler) writeReceipt(w http.ResponseWriter, stored *StoredSubmission, publishErr error) {
	receipt := map[string]any{
		"status":                  "accepted",
		"submitted_at":            stored.SubmittedAt,
		"module_id":               stored.Entry.ModuleID,
		"name":                    stored.Entry.Name,
		"version":                 stored.Entry.Version,
		"plugin_type":             stored.Entry.PluginType,
		"content_hash":            stored.Entry.ContentHash,
		"artifact_size_bytes":     stored.Entry.SizeBytes,
		"access_policy":           stored.Entry.AccessPolicy,
		"trust_tier":              stored.Entry.TrustTier,
		"entry_state":             stored.Entry.EntryState,
		"default_enabled":         stored.Entry.DefaultEnabled,
		"supersedes_content_hash": stored.Entry.SupersedesHash,
		"artifact_path":           stored.Entry.ArtifactPath,
		"manifest_url":            Path,
		"listed":                  publishErr == nil,
	}
	note := ""
	if publishErr != nil {
		note = "stored, but the manifest could not be rebuilt: " + publishErr.Error() + ". The listing appears on the next refresh."
	}
	receipt["note"] = note
	body, err := json.MarshalIndent(receipt, "", " ")
	if err != nil {
		http.Error(w, "submission accepted but receipt could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(body)
}

// isTooLarge reports a multipart parse failure caused by the body cap.
func isTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// cleanStrings drops empty entries from a declared list (caller may send
// ["", "x"]); the record stays honest about what was declared.
func cleanStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *SubmissionStore) writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pmm: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".submission-*")
	if err != nil {
		return fmt.Errorf("pmm: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("pmm: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pmm: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pmm: rename %s: %w", path, err)
	}
	return nil
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		// Unmarshalable at this layer is a programming error; a submission
		// record that fails to serialize must fail the write, not silently
		// drop the artifact. writeFile would just as happily write nothing.
		panic(fmt.Sprintf("pmm: internal: submission record does not serialize: %v", err))
	}
	return b
}
