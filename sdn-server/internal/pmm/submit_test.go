package pmm

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimalWasm is the smallest byte sequence that passes isWasm: the wasm magic
// plus version 1. HashArtifact only hashes these bytes (no trailer to strip),
// which is all the lane needs.
func minimalWasm() []byte {
	return []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
}

// minimalWasmWithTrailer appends opaque bytes to the portable payload. It still
// passes the wasm preamble check (a trailer appends after the magic). These
// bytes are NOT a well-formed SDS $REC trailer (see trailerFramed below), so
// they hash as part of the payload — the container is just a wasm-preamble
// byte string, which is all the lane guards.
func minimalWasmWithTrailer(b byte) []byte {
	return append(minimalWasm(), b)
}

// trailerFramed wraps a portable payload in a framing-valid SDS $REC
// publication trailer: payload || REC-bytes || uint32le(REC length) || "$REC".
// HashArtifact strips exactly this framing (internal/modulert), so the hash
// and size it reports must describe the payload, not the container.
func trailerFramed(payload []byte) []byte {
	rec := bytes.Repeat([]byte{0xAA}, 8)
	footer := make([]byte, 8)
	binary.LittleEndian.PutUint32(footer[:4], uint32(len(rec)))
	copy(footer[4:], "$REC")
	out := make([]byte, 0, len(payload)+len(rec)+8)
	out = append(out, payload...)
	out = append(out, rec...)
	out = append(out, footer...)
	return out
}

func sampleMetadata() *SubmissionMetadata {
	return &SubmissionMetadata{
		ModuleID:        "com.example.demo",
		Name:            "Demo Module",
		Description:     "A plaintext module listed by a third party",
		Version:         "1.0.0",
		PluginType:      "Propagator",
		License:         "MIT",
		RuntimeTargets:  []string{"wasmedge", "browser"},
		RequiredSchemas: []string{"$OMM"},
		MinPermissions:  []string{"http"},
	}
}

func newTestStore(t *testing.T) *SubmissionStore {
	t.Helper()
	return NewSubmissionStore(t.TempDir())
}

// The lane materializes exactly the fixed policy: ANONYMOUS, OPTIONAL, ACTIVE,
// not default-enabled. A submitter can never negotiate these — the request
// contract has no such keys, and even a hostile body that smuggles them in is
// ignored rather than obeyed.
func TestSubmissionMaterializesForcedPolicy(t *testing.T) {
	store := newTestStore(t)

	// Hostile extras declared in the body must not reach the record.
	blob := `{"module_id":"com.example.demo","name":"Demo","version":"1.0.0","plugin_type":"Propagator","access_policy":"ENTITLED","trust_tier":"CORE","entry_state":"REVOKED","default_enabled":true}`
	var hostile SubmissionMetadata
	if err := json.Unmarshal([]byte(blob), &hostile); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	stored, err := store.Save(&hostile, minimalWasm(), time.Now())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	e := stored.Entry
	if e.AccessPolicy != "ANONYMOUS" || e.TrustTier != "OPTIONAL" || e.EntryState != "ACTIVE" {
		t.Fatalf("lane policy not forced: %+v", e)
	}
	if e.DefaultEnabled {
		t.Fatal("submission must never default-enable a module at boot")
	}
	if e.ArtifactPath != "/modules/submissions/com.example.demo/1.0.0/module.wasm" {
		t.Fatalf("unexpected artifact path %q", e.ArtifactPath)
	}
	if e.ArtifactSignature != "" {
		t.Fatal("ARTIFACT_SIGNATURE must stay empty pending the owner ruling (bandwidth optimisation only)")
	}
	if e.ContentHash == "" || e.SizeBytes == 0 {
		t.Fatal("content hash and size must be computed from the stored bytes")
	}
	if want := 64; len(e.ContentHash) != want {
		t.Fatalf("content hash is %d chars, want %d", len(e.ContentHash), want)
	}

	// The saved record survives a reload and the bytes re-hash to it.
	loaded, skips := store.Load()
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(loaded) != 1 || loaded[0].Entry.ContentHash != e.ContentHash {
		t.Fatalf("reload mismatch: %+v", loaded)
	}
	if loaded[0].Entry.SourceArtifact != "submissions/artifacts/com.example.demo/1.0.0/module.wasm" {
		t.Fatalf("source artifact not relative to the artifact root: %q", loaded[0].Entry.SourceArtifact)
	}
	// The submitter's own keys pass through as data: the name the body declared
	// is the name that lists, and the forced-policy keys the body smuggled
	// stayed out of the record.
	if loaded[0].Entry.Name != "Demo" {
		t.Fatalf("submitter-declared name must pass through verbatim, got %q", loaded[0].Entry.Name)
	}
}

func TestSubmissionValidationRefusals(t *testing.T) {
	store := newTestStore(t)
	cases := []struct {
		name string
		mut  func(*SubmissionMetadata)
		art  []byte
		want string
	}{
		{"module_id required", func(m *SubmissionMetadata) { m.ModuleID = "  " }, minimalWasm(), "module_id is required"},
		{"module_id unsafe slash", func(m *SubmissionMetadata) { m.ModuleID = "a/b" }, minimalWasm(), "must match"},
		{"module_id traversal", func(m *SubmissionMetadata) { m.ModuleID = "a..b" }, minimalWasm(), "must not contain"},
		{"module_id backslash", func(m *SubmissionMetadata) { m.ModuleID = `a\b` }, minimalWasm(), "must match"},
		{"module_id dotfile", func(m *SubmissionMetadata) { m.ModuleID = ".hidden" }, minimalWasm(), "must match"},
		{"name required", func(m *SubmissionMetadata) { m.Name = "" }, minimalWasm(), "name is required"},
		{"version required", func(m *SubmissionMetadata) { m.Version = "" }, minimalWasm(), "version is required"},
		{"version unsafe", func(m *SubmissionMetadata) { m.Version = "1.0/rc1" }, minimalWasm(), "must match"},
		{"plugin_type required", func(m *SubmissionMetadata) { m.PluginType = "" }, minimalWasm(), "plugin_type is required"},
		{"plugin_type not a symbol", func(m *SubmissionMetadata) { m.PluginType = "TotallyMadeUp" }, minimalWasm(), "not a pluginCategory symbol"},
		{"artifact required", func(m *SubmissionMetadata) {}, nil, "artifact is required"},
		{"artifact not wasm", func(m *SubmissionMetadata) {}, []byte("#!/usr/bin/env python3"), "not a wasm"},
		{"doc url scheme", func(m *SubmissionMetadata) { m.DocumentationURL = "javascript:alert(1)" }, minimalWasm(), "absolute http(s) URL"},
		{"doc url long", func(m *SubmissionMetadata) { m.DocumentationURL = "https://" + strings.Repeat("x", 600) }, minimalWasm(), "too long"},
		{"permissions too many", func(m *SubmissionMetadata) {
			for i := 0; i < 17; i++ {
				m.MinPermissions = append(m.MinPermissions, "cap")
			}
		}, minimalWasm(), "too many"},
		{"artifact oversized", func(m *SubmissionMetadata) {}, bytes.Repeat(minimalWasm(), (MaxSubmissionArtifactBytes/8)+1), "exceeds the"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := sampleMetadata()
			tc.mut(md)
			_, err := store.Save(md, tc.art, time.Now())
			if err == nil {
				t.Fatal("expected a refusal, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// The lane is write-once per (MODULE_ID, VERSION): a resubmission with the
// same version is a duplicate no matter what bytes arrive; a new version
// replaces the record and chains SUPERSEDES_CONTENT_HASH to the old hash.
func TestSubmissionDuplicateAndVersionReplacement(t *testing.T) {
	store := newTestStore(t)

	md := sampleMetadata()
	first, err := store.Save(md, minimalWasm(), time.Now())
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Same module, same version: duplicate regardless of the bytes (different,
	// still valid wasm-preamble container).
	_, err = store.Save(md, minimalWasmWithTrailer(0x01), time.Now())
	if !errors.Is(err, ErrDuplicateSubmission) {
		t.Fatalf("expected ErrDuplicateSubmission, got %v", err)
	}
	// The duplicate attempt changed nothing on disk.
	loaded, skips := store.Load()
	if len(skips) != 0 || len(loaded) != 1 || loaded[0].Entry.ContentHash != first.Entry.ContentHash {
		t.Fatalf("duplicate attempt mutated the store: %d loaded, %v skips", len(loaded), skips)
	}

	// New version replaces the record and chains the old hash.
	md.Version = "1.1.0"
	next, err := store.Save(md, minimalWasm(), time.Now())
	if err != nil {
		t.Fatalf("version save: %v", err)
	}
	if next.Entry.SupersedesHash == "" {
		t.Fatal("replacement must chain SUPERSEDES_CONTENT_HASH to the previous version")
	}
	loaded, skips = store.Load()
	if len(skips) != 0 || len(loaded) != 1 {
		t.Fatalf("expected exactly one live submission, got %d plus skips %v", len(loaded), skips)
	}
	if loaded[0].Entry.Version != "1.1.0" {
		t.Fatalf("replacement did not take effect: %s", loaded[0].Entry.Version)
	}
}

// Operator catalog wins: a submission whose MODULE_ID the operator already
// manages (in ANY state) is suppressed by the merge.
func TestSubmissionMergeOperatorAlwaysWins(t *testing.T) {
	operator := &CatalogFile{
		Entries: []Entry{
			{ModuleID: "com.example.managed", TrustTier: "CORE", AccessPolicy: "ANONYMOUS", EntryState: "ACTIVE", PluginType: "Propagator"},
			{ModuleID: "com.example.withdrawn", TrustTier: "OPTIONAL", AccessPolicy: "ANONYMOUS", EntryState: "WITHDRAWN", PluginType: "Comms"},
		},
	}
	subs := []StoredSubmission{
		{Entry: Entry{ModuleID: "com.example.managed", Version: "9.9.9", AccessPolicy: "ANONYMOUS", TrustTier: "OPTIONAL", EntryState: "ACTIVE", PluginType: "Propagator"}},
		{Entry: Entry{ModuleID: "com.example.withdrawn"}},
		{Entry: Entry{ModuleID: "com.example.fresh", AccessPolicy: "ANONYMOUS", TrustTier: "OPTIONAL", EntryState: "ACTIVE", PluginType: "Propagator"}},
	}
	added, suppressed := operator.MergeSubmissions(subs)
	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}
	if len(suppressed) != 2 {
		t.Fatalf("expected 2 suppressed (managed + withdrawn), got %v", suppressed)
	}
	if got := operator.Entries[len(operator.Entries)-1].ModuleID; got != "com.example.fresh" {
		t.Fatalf("fresh submission not appended: %q", got)
	}
}

// The store's load resilience: a corrupt record, a deleted artifact and a
// vandalized artifact all become skips — never a hard failure — so one wedged
// submission cannot take the signed manifest down.
func TestSubmissionLoadSkipsBrokenRecords(t *testing.T) {
	// Corrupt record file: undecodable JSON.
	store := newTestStore(t)
	if _, err := store.Save(sampleMetadata(), minimalWasm(), time.Now()); err != nil {
		t.Fatalf("save1: %v", err)
	}
	record := filepath.Join(store.Dir(), "com.example.demo.json")
	if err := os.WriteFile(record, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}
	if _, skips := store.Load(); len(skips) != 1 {
		t.Fatalf("corrupt record must skip, got %v", skips)
	}

	// Deleted artifact: a withdrawal, skipped not fatal.
	store2 := newTestStore(t)
	if _, err := store2.Save(sampleMetadata(), minimalWasm(), time.Now()); err != nil {
		t.Fatalf("save2: %v", err)
	}
	artifact2 := artifactPathFor(store2)
	if err := os.Remove(artifact2); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}
	if _, skips := store2.Load(); len(skips) != 1 {
		t.Fatalf("deleted artifact must skip, got %v", skips)
	}

	// Vandalized bytes: no longer match the stored CONTENT_HASH.
	store3 := newTestStore(t)
	if _, err := store3.Save(sampleMetadata(), minimalWasm(), time.Now()); err != nil {
		t.Fatalf("save3: %v", err)
	}
	if err := os.WriteFile(artifactPathFor(store3), bytes.Repeat([]byte{0x07}, 8), 0o644); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}
	if _, skips := store3.Load(); len(skips) != 1 {
		t.Fatalf("tampered artifact must skip, got %v", skips)
	}
}

// artifactPathFor is the on-disk artifact file the store writes for the
// sample submission: <root>/submissions/artifacts/com.example.demo/1.0.0/module.wasm.
func artifactPathFor(store *SubmissionStore) string {
	return filepath.Join(filepath.Dir(store.Dir()), "submissions", "artifacts", "com.example.demo", "1.0.0", "module.wasm")
}

func multipartPost(t *testing.T, md *SubmissionMetadata, artifact []byte, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	raw, err := json.Marshal(md)
	if err != nil {
		t.Fatalf("metadata marshal: %v", err)
	}
	if err := w.WriteField("metadata", string(raw)); err != nil {
		t.Fatalf("field: %v", err)
	}
	fw, err := w.CreateFormFile("artifact", "module.wasm")
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if _, err := fw.Write(artifact); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, SubmissionPath, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// End-to-end over HTTP: multipart POST with metadata + artifact, the publish
// callback runs, and the receipt says exactly what the manifest now contains.
func TestSubmissionHandlerAccept(t *testing.T) {
	dir := t.TempDir()
	store := NewSubmissionStore(dir)
	published := 0
	h := NewSubmissionHandler(store, nil, func() error { published++; return nil })

	rec := multipartPost(t, sampleMetadata(), minimalWasm(), h)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if published != 1 {
		t.Fatalf("publish callback must run exactly once per accepted submission, ran %d", published)
	}
	var receipt map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if receipt["status"] != "accepted" || receipt["module_id"] != "com.example.demo" ||
		receipt["access_policy"] != "ANONYMOUS" || receipt["trust_tier"] != "OPTIONAL" ||
		receipt["entry_state"] != "ACTIVE" || receipt["default_enabled"] != false || receipt["listed"] != true {
		t.Fatalf("receipt wrong: %v", receipt)
	}
	if hash, _ := receipt["content_hash"].(string); len(hash) != 64 {
		t.Fatalf("receipt content_hash wrong: %v", receipt["content_hash"])
	}
	if receipt["manifest_url"] != Path {
		t.Fatalf("receipt must name the manifest URL: %v", receipt["manifest_url"])
	}
}

// A publish failure is a stored-but-not-yet-listed receipt, never a lost
// submission: the receipt says listed:false and the note says why.
func TestSubmissionHandlerPublishFailureIsStored(t *testing.T) {
	store := NewSubmissionStore(t.TempDir())
	h := NewSubmissionHandler(store, nil, func() error { return errors.New("manifest rebuild exploded") })

	rec := multipartPost(t, sampleMetadata(), minimalWasm(), h)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	var receipt map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if receipt["listed"] != false {
		t.Fatalf("receipt must say listed:false on publish failure: %v", receipt)
	}
	if !strings.Contains(receipt["note"].(string), "manifest could not be rebuilt") {
		t.Fatalf("note must explain the stored-but-unlisted state: %v", receipt["note"])
	}
	// And the submission survived for the next refresh to pick up.
	if got, skips := store.Load(); len(got) != 1 || len(skips) != 0 {
		t.Fatalf("submission must survive a publish failure: %d loaded, %v skips", len(got), skips)
	}
}

// The handler maps the lane's refusals to the right status codes: 405 for non
// POST, 415 for non-multipart, 409 for an operator-catalog collision and for a
// duplicate, and a failed acceptance never touches the store.
func TestSubmissionHandlerConflictsAndRefusals(t *testing.T) {
	empty := NewSubmissionHandler(NewSubmissionStore(t.TempDir()), nil, nil)

	r := httptest.NewRecorder()
	empty.ServeHTTP(r, httptest.NewRequest(http.MethodGet, SubmissionPath, nil))
	if r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET must be refused with 405, got %d", r.Code)
	}
	r = httptest.NewRecorder()
	empty.ServeHTTP(r, httptest.NewRequest(http.MethodPost, SubmissionPath, strings.NewReader("{}")))
	if r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON POST must be refused with 415, got %d", r.Code)
	}

	// Operator-catalog collision -> 409, and the store stays untouched.
	conflictStore := NewSubmissionStore(t.TempDir())
	conflict := func(id string) bool { return id == "com.example.demo" }
	confStore := NewSubmissionHandler(conflictStore, conflict, nil)
	if r := multipartPost(t, sampleMetadata(), minimalWasm(), confStore); r.Code != http.StatusConflict {
		t.Fatalf("catalog conflict must be 409, got %d (%s)", r.Code, r.Body.String())
	}
	if entries, skips := conflictStore.Load(); len(entries) != 0 || len(skips) != 0 {
		t.Fatalf("conflicted submission touched the store: %d entries, %v skips", len(entries), skips)
	}

	// Duplicate over HTTP -> 409 too, via the store's write-once rule.
	dupStore := NewSubmissionStore(t.TempDir())
	dupHandler := NewSubmissionHandler(dupStore, nil, func() error { return nil })
	if r := multipartPost(t, sampleMetadata(), minimalWasm(), dupHandler); r.Code != http.StatusAccepted {
		t.Fatalf("first POST must accept, got %d", r.Code)
	}
	if r := multipartPost(t, sampleMetadata(), minimalWasmWithTrailer(0x02), dupHandler); r.Code != http.StatusConflict {
		t.Fatalf("duplicate POST must be 409, got %d (%s)", r.Code, r.Body.String())
	}
	if entries, _ := dupStore.Load(); len(entries) != 1 {
		t.Fatalf("duplicate changed the store: %d entries", len(entries))
	}
}

// Publication-trailer containers still pass the wasm preamble check, and the
// lane hashes the PORTABLE payload — the bytes the loader compiles — exactly
// like the operator lane: for a well-formed SDS $REC trailer, CONTENT_HASH and
// ARTIFACT_SIZE_BYTES describe the payload, not the uploaded container.
func TestSubmissionTrailedArtifactHashesPortable(t *testing.T) {
	trailed := trailerFramed(minimalWasm())
	if !isWasm(trailed) {
		t.Fatal("trailed container must pass the wasm preamble check")
	}

	store := newTestStore(t)
	stored, err := store.Save(sampleMetadata(), trailed, time.Now())
	if err != nil {
		t.Fatalf("save trailed: %v", err)
	}
	diskHash, diskSize, err := HashArtifact(artifactPathFor(store))
	if err != nil {
		t.Fatalf("hash stored artifact: %v", err)
	}
	if diskHash != stored.Entry.ContentHash {
		t.Fatal("CONTENT_HASH must cover the trailer-stripped payload, not the uploaded container")
	}
	if want := uint64(len(minimalWasm())); diskSize != want {
		t.Fatalf("portable size is %d, want %d", diskSize, want)
	}
	if stored.Entry.SizeBytes != diskSize {
		t.Fatalf("ARTIFACT_SIZE_BYTES must describe the portable payload, got %d want %d", stored.Entry.SizeBytes, diskSize)
	}
}

// Negative control: trailing bytes that are NOT a well-formed $REC trailer are
// NOT stripped — no framing to trust, so they hash as part of the payload.
// This keeps the lane honest: a stray byte can never inflate a hash or alias a
// listing against the real portable payload.
func TestSubmissionTrailingGarbageIsNotStripped(t *testing.T) {
	garbage := minimalWasmWithTrailer(0x42)
	if !isWasm(garbage) {
		t.Fatal("wasm-preamble byte string must pass the preamble check")
	}

	store := newTestStore(t) // fresh store, sample MODULE_ID keeps artifactPathFor honest
	stored, err := store.Save(sampleMetadata(), garbage, time.Now())
	if err != nil {
		t.Fatalf("save garbage: %v", err)
	}
	diskHash, diskSize, err := HashArtifact(artifactPathFor(store))
	if err != nil {
		t.Fatalf("hash stored artifact: %v", err)
	}
	if diskHash != stored.Entry.ContentHash || diskSize != stored.Entry.SizeBytes {
		t.Fatalf("hash/size disagree with the record: %s/%d vs %s/%d", diskHash, diskSize, stored.Entry.ContentHash, stored.Entry.SizeBytes)
	}
	if want := uint64(len(garbage)); diskSize != want {
		t.Fatalf("non-trailer bytes must hash in full, got %d want %d", diskSize, want)
	}
}
