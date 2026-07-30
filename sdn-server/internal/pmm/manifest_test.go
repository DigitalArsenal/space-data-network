package pmm

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
)

type edSigner struct{ priv ed25519.PrivateKey }

func (s edSigner) Sign(data []byte) ([]byte, error) { return ed25519.Sign(s.priv, data), nil }

func testManifest() *Manifest {
	return &Manifest{
		ProviderDomain: "sdn.spaceaware.io",
		Epoch:          7,
		ExpiresAt:      "2026-08-29T19:00:00.000Z",
		Trust: TrustAnchor{
			ProviderDomain:   "sdn.spaceaware.io",
			NodePeerID:       "16Uiu2HAmTest",
			SigningPublicKey: "038664c4",
		},
		Modules: []Entry{
			{
				ModuleID: "com.orbpro.sgp4", Version: "1.0.0",
				ContentHash: strings.Repeat("a", 64),
				TrustTier:   "CORE", AccessPolicy: "ANONYMOUS", DefaultEnabled: true,
				EntryState: "ACTIVE", ArtifactPath: "/modules/com.orbpro.sgp4/1.0.0/module.wasm",
				PluginType: "Propagator",
			},
			{
				ModuleID: "com.orbpro.rf-fspl", Version: "0.1.0",
				ContentHash: strings.Repeat("b", 64),
				TrustTier:   "OPTIONAL", AccessPolicy: "ENTITLED", DefaultEnabled: false,
				EntryState: "ACTIVE", PluginType: "Comms",
			},
		},
	}
}

// The canonical statement is the ONLY thing the signature covers, so its exact
// bytes are the contract. Sorted bytewise by MODULE_ID, booleans as 1/0, enums
// as IDL symbol names, every line LF-terminated including the last.
func TestCanonicalStatementIsExact(t *testing.T) {
	got := CanonicalStatement(testManifest())
	want := "SDN-MODULE-MANIFEST-V1\n" +
		"domain:sdn.spaceaware.io\n" +
		"peer:16Uiu2HAmTest\n" +
		"key:038664c4\n" +
		"epoch:7\n" +
		"expires:2026-08-29T19:00:00.000Z\n" +
		"module:com.orbpro.rf-fspl 0.1.0 " + strings.Repeat("b", 64) + " OPTIONAL ENTITLED 0 ACTIVE\n" +
		"module:com.orbpro.sgp4 1.0.0 " + strings.Repeat("a", 64) + " CORE ANONYMOUS 1 ACTIVE\n"
	if got != want {
		t.Fatalf("canonical statement mismatch\n got:\n%q\nwant:\n%q", got, want)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("statement must be LF-terminated on the last line")
	}
	if strings.Contains(got, "\r") {
		t.Fatal("statement must contain no CR")
	}
}

// "rf-fspl" < "sgp4" bytewise, so the ENTITLED entry sorts first even though it
// was declared second. Ordering is part of the signed bytes.
func TestModulesSortBytewise(t *testing.T) {
	m := testManifest()
	SortModules(m.Modules)
	if m.Modules[0].ModuleID != "com.orbpro.rf-fspl" {
		t.Fatalf("expected bytewise ascending order, got %s first", m.Modules[0].ModuleID)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	m := testManifest()
	if err := Sign(m, edSigner{priv}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig, err := hex.DecodeString(m.Signature)
	if err != nil {
		t.Fatalf("signature must be hex: %v", err)
	}
	if !ed25519.Verify(pub, []byte(m.SignedStatement), sig) {
		t.Fatal("signature does not verify over the carried statement")
	}
	if err := VerifyStatement(m); err != nil {
		t.Fatalf("rebuilt statement disagrees with carried copy: %v", err)
	}
}

// The carried SIGNED_STATEMENT must never become a second source of truth.
func TestTamperedStatementIsRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	m := testManifest()
	_ = Sign(m, edSigner{priv})
	m.SignedStatement = strings.Replace(m.SignedStatement, "OPTIONAL ENTITLED", "CORE ANONYMOUS", 1)
	if err := VerifyStatement(m); err == nil {
		t.Fatal("a statement that disagrees with the record's fields must be rejected")
	}
}

func TestUnsignedManifestIsRefused(t *testing.T) {
	if err := Sign(testManifest(), nil); err != ErrNoSigner {
		t.Fatal("a manifest with no signer must be refused, never served unsigned")
	}
}

func TestValidateRejectsDuplicateModuleID(t *testing.T) {
	m := testManifest()
	m.Modules = append(m.Modules, m.Modules[0])
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("MODULE_ID is a unique key; want duplicate error, got %v", err)
	}
}

// An ENTITLED module must not publish a path anonymous clients could fetch, and
// an ANONYMOUS one is useless without a path. Both are fail-closed checks.
func TestValidateEnforcesAccessPolicyPathCoupling(t *testing.T) {
	m := testManifest()
	m.Modules[1].ArtifactPath = "/modules/com.orbpro.rf-fspl/0.1.0/module.wasm"
	if err := m.Validate(); err == nil {
		t.Fatal("ENTITLED entry must not publish an anonymous ARTIFACT_PATH")
	}

	m2 := testManifest()
	m2.Modules[0].ArtifactPath = ""
	if err := m2.Validate(); err == nil {
		t.Fatal("ANONYMOUS entry with no ARTIFACT_PATH must be rejected")
	}
}

func TestValidateRejectsBadEnumsAndHashes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"tier", func(m *Manifest) { m.Modules[0].TrustTier = "PREMIUM" }},
		{"access", func(m *Manifest) { m.Modules[0].AccessPolicy = "FREE" }},
		{"state", func(m *Manifest) { m.Modules[0].EntryState = "PENDING" }},
		{"short hash", func(m *Manifest) { m.Modules[0].ContentHash = "abc" }},
		{"upper hash", func(m *Manifest) { m.Modules[0].ContentHash = strings.Repeat("A", 64) }},
	} {
		m := testManifest()
		tc.mutate(m)
		if err := m.Validate(); err == nil {
			t.Fatalf("%s: expected rejection", tc.name)
		}
	}
}

func TestBuildManifestRefusesUnboundedTTL(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	cf := &CatalogFile{Entries: testManifest().Modules}
	trust := testManifest().Trust
	if _, err := BuildManifest(cf, trust, 1, "u", time.Now(), 0, edSigner{priv}); err == nil {
		t.Fatal("an unexpiring signed manifest cannot be withdrawn; ttl must be positive")
	}
}

func TestHandlerServesBinaryByDefaultAndJSONOnAccept(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	m := testManifest()
	if err := Sign(m, edSigner{priv}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	h := Handler(NewStaticSource(m, []BrowseHint{{ModuleID: "com.orbpro.sgp4", Family: "propagator", ScenarioVisible: true, Open: true}}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, Path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("binary: want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "flatbuffers") {
		t.Fatalf("default projection must be the FlatBuffer, got %q", ct)
	}
	// Size-prefixed buffer carrying the $PMM file identifier.
	if body := rec.Body.Bytes(); len(body) < 12 || string(body[8:12]) != "$PMM" {
		t.Fatalf("missing $PMM file identifier in size-prefixed buffer")
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, Path, nil)
	req.Header.Set("Accept", "application/json")
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("json: want 200, got %d", rec2.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	// IDL capitalization on record fields; enums as symbol names; synthesized
	// data confined to the lowercase sibling envelope.
	for _, k := range []string{"PROVIDER_DOMAIN", "MODULES", "SIGNATURE", "SIGNED_STATEMENT", "TRUST"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("missing IDL-capitalized field %q", k)
		}
	}
	if _, ok := doc["browse"]; !ok {
		t.Fatal("synthesized data must appear under the lowercase browse envelope")
	}
	mods := doc["MODULES"].([]any)
	first := mods[0].(map[string]any)
	if first["TRUST_TIER"] != "OPTIONAL" || first["ACCESS_POLICY"] != "ENTITLED" {
		t.Fatalf("enums must serialize as IDL symbol names, got %v/%v", first["TRUST_TIER"], first["ACCESS_POLICY"])
	}
	if _, bad := first["trust_tier"]; bad {
		t.Fatal("record fields must never be lowercased")
	}
}

func TestHandlerFailsClosedWithoutManifest(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(&StaticSource{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, Path, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when no signed manifest exists, got %d", rec.Code)
	}
}

func TestFreshnessCheck(t *testing.T) {
	m := testManifest()
	if !FreshnessCheck(m, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("manifest inside EXPIRES_AT must be fresh")
	}
	if FreshnessCheck(m, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("manifest past EXPIRES_AT must not be fresh")
	}
}

// PLUGIN_TYPE is the only sanctioned grouping key, and its enum has a trap:
// `Unspecified` is 21 because the enum is append-only and `Sensor` already holds
// 0. A blank family must therefore be REJECTED, not defaulted — defaulting to
// the zero value would publish every unfamilied module as a Sensor.
func TestValidateRejectsBlankPluginTypeSoNothingDefaultsToSensor(t *testing.T) {
	m := testManifest()
	m.Modules[0].PluginType = ""
	if err := m.Validate(); err == nil {
		t.Fatal("blank PLUGIN_TYPE must be rejected, never defaulted (0 == Sensor)")
	}
	m2 := testManifest()
	m2.Modules[0].PluginType = "Sensors" // not an IDL symbol
	if err := m2.Validate(); err == nil {
		t.Fatal("PLUGIN_TYPE must be an IDL pluginCategory symbol")
	}
	m3 := testManifest()
	m3.Modules[0].PluginType = "Unspecified"
	if err := m3.Validate(); err != nil {
		t.Fatalf("Unspecified is a legitimate honest value: %v", err)
	}
}

// The binding's symbol table must agree with the schema's documented values, or
// our encoding silently means something else on the wire.
func TestPluginCategorySymbolValuesMatchSchema(t *testing.T) {
	if got := sdspmm.EnumValuespluginCategory["Unspecified"]; byte(got) != 21 {
		t.Fatalf("Unspecified must be 21, got %d", byte(got))
	}
	if got := sdspmm.EnumValuespluginCategory["Sensor"]; byte(got) != 0 {
		t.Fatalf("Sensor must be 0, got %d", byte(got))
	}
}

// HashArtifact must describe the PORTABLE bytes — the ones a client compiles —
// not the published container. This is a regression test for a defect that
// reached production: com.orbpro.sgp4 was published with CONTENT_HASH over all
// 1171602 trailered bytes while clients compile only the first 1170638, so any
// client doing the correct strip-then-hash saw a mismatch on a valid module.
func TestHashArtifactStripsPublicationTrailer(t *testing.T) {
	payload := append([]byte("\x00asm\x01\x00\x00\x00"), bytes.Repeat([]byte{0xAB}, 512)...)
	wantHash := sha256.Sum256(payload)

	// payload || REC || uint32le(len(REC)) || "$REC"
	rec := bytes.Repeat([]byte{0x7F}, 964-8)
	trailered := append(append([]byte{}, payload...), rec...)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(rec)))
	trailered = append(append(trailered, n[:]...), []byte("$REC")...)

	dir := t.TempDir()
	pubPath := filepath.Join(dir, "published.wasm")
	if err := os.WriteFile(pubPath, trailered, 0o644); err != nil {
		t.Fatal(err)
	}
	gotHash, gotSize, err := HashArtifact(pubPath)
	if err != nil {
		t.Fatalf("hash published: %v", err)
	}
	if gotHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("trailered artifact: hash must cover the portable payload only\n got %s\nwant %s",
			gotHash, hex.EncodeToString(wantHash[:]))
	}
	if gotSize != uint64(len(payload)) {
		t.Fatalf("ARTIFACT_SIZE_BYTES must describe the same bytes as CONTENT_HASH: got %d want %d",
			gotSize, len(payload))
	}
	if gotSize == uint64(len(trailered)) {
		t.Fatal("size still describes the trailered container")
	}

	// An artifact with no trailer must be completely unaffected.
	plainPath := filepath.Join(dir, "plain.wasm")
	if err := os.WriteFile(plainPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	plainHash, plainSize, err := HashArtifact(plainPath)
	if err != nil {
		t.Fatalf("hash plain: %v", err)
	}
	if plainHash != gotHash || plainSize != gotSize {
		t.Fatalf("untrailered artifact must hash identically to the stripped one:\n plain %s/%d\n strip %s/%d",
			plainHash, plainSize, gotHash, gotSize)
	}
}
