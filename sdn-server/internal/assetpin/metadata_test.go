package assetpin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonicalMetadataAcceptsExactTypedDocument(t *testing.T) {
	sha256 := strings.Repeat("a", 64)
	raw := []byte(`{"attribution":"A < B","candidateKey":"candidate-001","entityId":"vehicle-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceRecordId":"source-001","sourceUrl":"https://example.test/model.glb","vamId":"vam-001"}`)

	metadata, canonical, err := ParseCanonicalMetadata(raw)
	if err != nil {
		t.Fatalf("ParseCanonicalMetadata() error = %v", err)
	}
	if string(canonical) != string(raw) {
		t.Fatalf("canonical metadata = %q, want exact input %q", canonical, raw)
	}
	if metadata.SchemaVersion != 1 ||
		metadata.CandidateKey != "candidate-001" ||
		metadata.EntityID != "vehicle-001" ||
		metadata.SourceRecordID != "source-001" ||
		metadata.SourceURL != "https://example.test/model.glb" ||
		metadata.LicenseName != "CC0-1.0" ||
		metadata.Attribution != "A < B" ||
		metadata.SHA256 != sha256 ||
		metadata.VAMID != "vam-001" {
		t.Fatalf("metadata = %+v, want all typed fields preserved", metadata)
	}
}

func TestAssetPinRecoveryStorePersistsAtomicBoundedMarkers(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewFileAssetPinRecoveryStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileAssetPinRecoveryStore() error = %v", err)
	}
	marker := testAssetPinRecoveryMarker("candidate-recovery-b", "b")
	if err := store.CreateIntent(marker); err != nil {
		t.Fatalf("CreateIntent() error = %v", err)
	}
	if err := store.CreateIntent(marker); !errors.Is(err, ErrAssetPinRecoveryMarkerExists) {
		t.Fatalf("duplicate CreateIntent() error = %v, want ErrAssetPinRecoveryMarkerExists", err)
	}

	recoveryDir := filepath.Join(dataDir, "asset-pins", "recovery")
	dirInfo, err := os.Stat(recoveryDir)
	if err != nil {
		t.Fatalf("stat recovery dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("recovery dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
	entries, err := os.ReadDir(recoveryDir)
	if err != nil {
		t.Fatalf("read recovery dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != marker.ReferenceKey+".json" {
		t.Fatalf("recovery files = %v, want only final marker", entries)
	}
	markerPath := filepath.Join(recoveryDir, entries[0].Name())
	markerInfo, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if markerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode = %o, want 600", markerInfo.Mode().Perm())
	}
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if bytes.Contains(markerBytes, []byte("raw-jwt")) || bytes.Contains(markerBytes, []byte("rawToken")) {
		t.Fatalf("marker persisted raw token material: %s", markerBytes)
	}

	loaded, ok, err := store.Load(marker.ReferenceKey)
	if err != nil || !ok || !reflect.DeepEqual(loaded, marker) {
		t.Fatalf("Load() = %+v, %v, %v; want exact marker", loaded, ok, err)
	}
	if err := store.MarkPinned(marker.ReferenceKey, testRecoveryCID); err != nil {
		t.Fatalf("MarkPinned() error = %v", err)
	}
	pinned, ok, err := store.Load(marker.ReferenceKey)
	if err != nil || !ok {
		t.Fatalf("Load(pinned) = %+v, %v, %v", pinned, ok, err)
	}
	if pinned.Phase != AssetPinRecoveryPinnedUncommitted || pinned.CID != testRecoveryCID {
		t.Fatalf("pinned marker = %+v", pinned)
	}
	if err := store.MarkPinned(marker.ReferenceKey, testRecoveryCID); err != nil {
		t.Fatalf("idempotent MarkPinned() error = %v", err)
	}
	if err := store.MarkPinned(marker.ReferenceKey, "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"); !errors.Is(err, ErrAssetPinRecoveryMarkerConflict) {
		t.Fatalf("conflicting MarkPinned() error = %v, want ErrAssetPinRecoveryMarkerConflict", err)
	}

	second := testAssetPinRecoveryMarker("candidate-recovery-a", "a")
	if err := store.CreateIntent(second); err != nil {
		t.Fatalf("CreateIntent(second) error = %v", err)
	}
	listed, err := store.List(1)
	if err != nil {
		t.Fatalf("List(1) error = %v", err)
	}
	wantFirst := marker.ReferenceKey
	if second.ReferenceKey < wantFirst {
		wantFirst = second.ReferenceKey
	}
	if len(listed) != 1 || listed[0].ReferenceKey != wantFirst {
		t.Fatalf("List(1) = %+v, want deterministic reference-key order", listed)
	}
	if _, err := store.List(0); err == nil {
		t.Fatal("List(0) succeeded, want positive-limit error")
	}
	if err := store.Remove(marker.ReferenceKey); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, ok, err := store.Load(marker.ReferenceKey); err != nil || ok {
		t.Fatalf("Load(removed) ok/error = %v/%v, want false/nil", ok, err)
	}
}

func TestAssetPinRecoveryStoreCreateIntentNeverReplacesAcrossInstances(t *testing.T) {
	dataDir := t.TempDir()
	first, err := NewFileAssetPinRecoveryStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileAssetPinRecoveryStore(first) error = %v", err)
	}
	second, err := NewFileAssetPinRecoveryStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileAssetPinRecoveryStore(second) error = %v", err)
	}
	marker := testAssetPinRecoveryMarker("candidate-create-race", "9")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*FileAssetPinRecoveryStore{first, second} {
		go func(store *FileAssetPinRecoveryStore) {
			<-start
			results <- store.CreateIntent(marker)
		}(store)
	}
	close(start)
	firstResult := <-results
	secondResult := <-results
	if (firstResult == nil) == (secondResult == nil) {
		t.Fatalf("CreateIntent() results = %v, %v; want exactly one success", firstResult, secondResult)
	}
	failure := firstResult
	if failure == nil {
		failure = secondResult
	}
	if !errors.Is(failure, ErrAssetPinRecoveryMarkerExists) {
		t.Fatalf("losing CreateIntent() error = %v, want ErrAssetPinRecoveryMarkerExists", failure)
	}
	loaded, ok, err := first.Load(marker.ReferenceKey)
	if err != nil || !ok || !reflect.DeepEqual(loaded, marker) {
		t.Fatalf("Load() = %+v, %v, %v; want intact winning marker", loaded, ok, err)
	}
}

func TestAssetPinRecoveryStoreRejectsMalformedAndOversizedMarkers(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewFileAssetPinRecoveryStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileAssetPinRecoveryStore() error = %v", err)
	}
	marker := testAssetPinRecoveryMarker("candidate-strict-marker", "c")
	if err := store.CreateIntent(marker); err != nil {
		t.Fatalf("CreateIntent() error = %v", err)
	}
	path := filepath.Join(dataDir, "asset-pins", "recovery", marker.ReferenceKey+".json")
	valid := mustJSON(t, marker)
	unknown := append([]byte(nil), valid[:len(valid)-1]...)
	unknown = append(unknown, []byte(`,"rawToken":"raw-jwt"}`)...)

	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: unknown},
		{name: "trailing value", data: append(valid, []byte(`{}`)...)},
		{name: "oversized", data: bytes.Repeat([]byte{'x'}, AssetPinRecoveryMarkerMaxBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatalf("write malformed marker: %v", err)
			}
			if _, _, err := store.Load(marker.ReferenceKey); !errors.Is(err, ErrInvalidAssetPinRecoveryMarker) {
				t.Fatalf("Load() error = %v, want ErrInvalidAssetPinRecoveryMarker", err)
			}
		})
	}
}

func TestAssetPinDirectoriesRejectSymlinkComponents(t *testing.T) {
	dataDir := t.TempDir()
	target := t.TempDir()
	assetPinsPath := filepath.Join(dataDir, "asset-pins")
	if err := os.Symlink(target, assetPinsPath); err != nil {
		t.Fatalf("symlink asset-pins: %v", err)
	}

	if _, err := SecureAssetPinTempDir(dataDir); !errors.Is(err, ErrUnsafeAssetPinDirectory) {
		t.Fatalf("SecureAssetPinTempDir() error = %v, want ErrUnsafeAssetPinDirectory", err)
	}
	store, err := NewFileAssetPinRecoveryStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileAssetPinRecoveryStore() error = %v", err)
	}
	if err := store.CreateIntent(testAssetPinRecoveryMarker("candidate-symlink", "d")); !errors.Is(err, ErrUnsafeAssetPinDirectory) {
		t.Fatalf("CreateIntent() error = %v, want ErrUnsafeAssetPinDirectory", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target was modified: %v", entries)
	}
}

const testRecoveryCID = "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e"

func testAssetPinRecoveryMarker(candidateKey, discriminator string) AssetPinRecoveryMarker {
	sha := strings.Repeat(discriminator, 64)
	metadata := `{"candidateKey":"` + candidateKey + `","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha + `","sourceUrl":"https://example.test/model.glb"}`
	now := time.Date(2026, 7, 13, 15, 0, 0, 123, time.UTC)
	return AssetPinRecoveryMarker{
		SchemaVersion: 1,
		Phase:         AssetPinRecoveryIntent,
		ReferenceKey:  recoveryTestHash("asset-pin-reference:v1\n" + candidateKey),
		EventID:       recoveryTestHash("asset-pin-upsert:v1\n" + recoveryTestHash("asset-pin-reference:v1\n"+candidateKey)),
		CandidateKey:  candidateKey,
		SHA256:        sha,
		ByteCount:     128,
		SourceURL:     "https://example.test/model.glb",
		LicenseName:   "CC0-1.0",
		MetadataJSON:  metadata,
		TokenDigest:   strings.Repeat("e", 64),
		Repository:    "DigitalArsenal/asset-models",
		Ref:           "refs/heads/main",
		WorkflowRef:   "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main",
		Actor:         "review-bot",
		WorkflowRunID: "123456",
		RunAttempt:    "2",
		CommitSHA:     strings.Repeat("f", 40),
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(90 * 24 * time.Hour),
	}
}

func recoveryTestHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func TestCanonicalMetadataRejectsInvalidDocuments(t *testing.T) {
	sha256 := strings.Repeat("b", 64)
	valid := `{"candidateKey":"candidate-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`
	tests := []struct {
		name string
		raw  string
	}{
		{name: "noncanonical whitespace", raw: ` {"candidateKey":"candidate-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "noncanonical key order", raw: `{"schemaVersion":1,"candidateKey":"candidate-001","licenseName":"CC0-1.0","sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "trailing value", raw: valid + `{}`},
		{name: "unknown key", raw: strings.TrimSuffix(valid, "}") + `,"unknown":"value"}`},
		{name: "duplicate key", raw: `{"candidateKey":"candidate-001","candidateKey":"candidate-002","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "schema string", raw: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":"1"`, 1)},
		{name: "schema decimal", raw: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":1.0`, 1)},
		{name: "wrong schema", raw: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{name: "candidate nonstring", raw: strings.Replace(valid, `"candidateKey":"candidate-001"`, `"candidateKey":1`, 1)},
		{name: "candidate surrounding whitespace", raw: strings.Replace(valid, `"candidateKey":"candidate-001"`, `"candidateKey":" candidate-001"`, 1)},
		{name: "missing candidate", raw: `{"licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "empty candidate", raw: strings.Replace(valid, `"candidateKey":"candidate-001"`, `"candidateKey":""`, 1)},
		{name: "missing source", raw: `{"candidateKey":"candidate-001","licenseName":"CC0-1.0","schemaVersion":1,"sha256":"` + sha256 + `"}`},
		{name: "empty source", raw: strings.Replace(valid, `"sourceUrl":"https://example.test/model.glb"`, `"sourceUrl":""`, 1)},
		{name: "source surrounding whitespace", raw: strings.Replace(valid, `"sourceUrl":"https://example.test/model.glb"`, `"sourceUrl":"https://example.test/model.glb "`, 1)},
		{name: "missing license", raw: `{"candidateKey":"candidate-001","schemaVersion":1,"sha256":"` + sha256 + `","sourceUrl":"https://example.test/model.glb"}`},
		{name: "empty license", raw: strings.Replace(valid, `"licenseName":"CC0-1.0"`, `"licenseName":""`, 1)},
		{name: "license surrounding whitespace", raw: strings.Replace(valid, `"licenseName":"CC0-1.0"`, `"licenseName":" CC0-1.0"`, 1)},
		{name: "attribution surrounding whitespace", raw: strings.Replace(valid, `{`, `{"attribution":"Artist ",`, 1)},
		{name: "optional entity surrounding whitespace", raw: strings.Replace(valid, `"licenseName"`, `"entityId":" vehicle-001","licenseName"`, 1)},
		{name: "optional source record surrounding whitespace", raw: strings.Replace(valid, `"sourceUrl"`, `"sourceRecordId":"source-001 ","sourceUrl"`, 1)},
		{name: "optional VAM ID surrounding whitespace", raw: strings.TrimSuffix(valid, "}") + `,"vamId":" vam-001"}`},
		{name: "uppercase SHA", raw: strings.Replace(valid, sha256, strings.ToUpper(sha256), 1)},
		{name: "short SHA", raw: strings.Replace(valid, sha256, sha256[:63], 1)},
		{name: "SHA nonstring", raw: strings.Replace(valid, `"sha256":"`+sha256+`"`, `"sha256":1`, 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := ParseCanonicalMetadata([]byte(test.raw))
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("ParseCanonicalMetadata() error = %v, want ErrInvalidMetadata", err)
			}
		})
	}
}
