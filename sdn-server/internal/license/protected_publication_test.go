package license

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	encsds "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENC"
	flatbuffers "github.com/google/flatbuffers/go"
)

// The generated $REC Go package does not compile at the vendored pin (see
// protected_publication.go), so the collections these tests synthesize are built
// with the raw builder. REC { version:string, RECORDS:[Record] };
// Record { value_type, value, standard }.
func buildRecordCollection(b *flatbuffers.Builder, records []flatbuffers.UOffsetT) []byte {
	b.StartVector(4, len(records), 4)
	for i := len(records) - 1; i >= 0; i-- {
		b.PrependUOffsetT(records[i])
	}
	recordsVector := b.EndVector(len(records))

	version := b.CreateString("1.0.0")
	b.StartObject(2)
	b.PrependUOffsetTSlot(0, version, 0)
	b.PrependUOffsetTSlot(1, recordsVector, 0)
	b.FinishWithFileIdentifier(b.EndObject(), []byte(publicationTrailerMagic))
	return b.FinishedBytes()
}

func appendTrailer(payload, collection []byte) []byte {
	out := make([]byte, 0, len(payload)+len(collection)+publicationFooterLength)
	out = append(out, payload...)
	out = append(out, collection...)
	footer := make([]byte, publicationFooterLength)
	binary.LittleEndian.PutUint32(footer, uint32(len(collection)))
	copy(footer[4:], publicationTrailerMagic)
	return append(out, footer...)
}

// The fixtures under testdata/protected-publication are produced by the REAL
// module-SDK encoders (space-data-module-sdk/src/transport/records.js) against a
// throwaway key, so these tests are a cross-implementation check of the Go
// reader against the JS writer — not a Go-to-Go round trip, which would have
// happily agreed with itself and shipped the same defect twice.
const fixtureDir = "testdata/protected-publication"

type fixtureManifest struct {
	Key      string `json:"key"`
	Fixtures []struct {
		Name            string   `json:"name"`
		Note            string   `json:"note"`
		BlobLen         int      `json:"blobLen"`
		PlaintextSha256 string   `json:"plaintextSha256"`
		PlaintextLen    int      `json:"plaintextLen"`
		AADSha256       string   `json:"aadSha256"`
		Symmetric       string   `json:"symmetric"`
		Context         string   `json:"context"`
		Records         []string `json:"records"`
	} `json:"fixtures"`
}

func loadFixtures(t *testing.T) (fixtureManifest, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	key, err := hex.DecodeString(manifest.Key)
	if err != nil || len(key) != 32 {
		t.Fatalf("fixture key is not 32 hex-encoded bytes: %v", err)
	}
	if len(manifest.Fixtures) == 0 {
		t.Fatal("fixture manifest is empty")
	}
	return manifest, key
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestProtectedPublicationDecryptsSDKArtifacts covers all three artifact
// generations that exist on the delivery fleet: ENC-only GCM (the OrbPro plugin
// lane), MBL+ENC+PNM GCM (the module-SDK publish lane, i.e. every com.orbpro.rf-*),
// and the 2026-07-10 AES-256-CTR generation the JS SDK refuses outright.
func TestProtectedPublicationDecryptsSDKArtifacts(t *testing.T) {
	manifest, key := loadFixtures(t)
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			blob := readFixture(t, fixture.Name+".bin")
			if len(blob) != fixture.BlobLen {
				t.Fatalf("fixture length drifted: got %d want %d", len(blob), fixture.BlobLen)
			}
			if !hasPublicationTrailer(blob) {
				t.Fatal("fixture does not carry a $REC trailer")
			}
			plaintext, err := decryptProtectedPublication(blob, key)
			if err != nil {
				t.Fatalf("decrypt %s (%s): %v", fixture.Name, fixture.Note, err)
			}
			if len(plaintext) != fixture.PlaintextLen {
				t.Fatalf("plaintext length: got %d want %d", len(plaintext), fixture.PlaintextLen)
			}
			if got := sha256Hex(plaintext); got != fixture.PlaintextSha256 {
				t.Fatalf("plaintext sha256: got %s want %s", got, fixture.PlaintextSha256)
			}
			if !bytes.HasPrefix(plaintext, []byte("\x00asm")) {
				t.Fatalf("plaintext is not WASM: % x", plaintext[:4])
			}
		})
	}
}

// TestEncodeENCRecordMatchesSDKBytes is the byte-exactness guard on the AAD.
//
// The GCM AAD is the $ENC record RE-ENCODED as a standalone buffer, so a single
// byte of drift between the Go and JS flatbuffer builders — a field default, an
// omitted empty vector, a creation-order change — silently breaks every GCM
// artifact with an authentication failure that looks like key corruption. The
// fixture *.aad.bin files are the JS encodeEncRecord() output verbatim.
func TestEncodeENCRecordMatchesSDKBytes(t *testing.T) {
	manifest, _ := loadFixtures(t)
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			blob := readFixture(t, fixture.Name+".bin")
			wantAAD := readFixture(t, fixture.Name+".aad.bin")

			_, recordCollection, err := parsePublicationTrailer(blob)
			if err != nil {
				t.Fatalf("parse trailer: %v", err)
			}
			rec, err := selectENCRecord(recordCollection)
			if err != nil {
				t.Fatalf("select ENC: %v", err)
			}
			gotAAD := encodeENCRecord(rec)

			if !bytes.Equal(gotAAD, wantAAD) {
				t.Fatalf("re-encoded $ENC is not byte-identical to the SDK's\n go (%d): % x\nsdk (%d): % x", len(gotAAD), gotAAD, len(wantAAD), wantAAD)
			}
			if got := sha256Hex(gotAAD); got != fixture.AADSha256 {
				t.Fatalf("AAD sha256: got %s want %s", got, fixture.AADSha256)
			}
			if !encsds.ENCBufferHasIdentifier(gotAAD) {
				t.Fatal("re-encoded AAD is not a $ENC buffer")
			}
			if rec.Context != fixture.Context {
				t.Fatalf("context: got %q want %q", rec.Context, fixture.Context)
			}
		})
	}
}

// rewrapWithUnionOrdinals rebuilds a REC collection carrying the same $ENC
// record but with caller-chosen union ordinals, so a reader that dispatches on
// value_type is provably broken by it and a reader that keys on Record.standard
// is provably not.
func rewrapWithUnionOrdinals(t *testing.T, payload []byte, encBytes []byte, ordinals map[string]int) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(1024)

	// The ENC table has to be nested inside the collection, so re-create it
	// field by field from the standalone buffer.
	src := encsds.GetRootAsENC(encBytes, 0)
	ephemeral := b.CreateByteVector(src.EPHEMERAL_PUBLIC_KEYBytes())
	nonce := b.CreateByteVector(src.NONCE_STARTBytes())
	recipient := b.CreateByteVector(src.RECIPIENT_KEY_IDBytes())
	var context flatbuffers.UOffsetT
	if len(src.CONTEXT()) > 0 {
		context = b.CreateString(string(src.CONTEXT()))
	}
	schemaHash := b.CreateByteVector(src.SCHEMA_HASHBytes())
	var rootType flatbuffers.UOffsetT
	if len(src.ROOT_TYPE()) > 0 {
		rootType = b.CreateString(string(src.ROOT_TYPE()))
	}
	encsds.ENCStart(b)
	b.PrependByteSlot(0, src.VERSION(), 1)
	b.PrependInt8Slot(1, int8(src.KEY_EXCHANGE()), 0)
	b.PrependInt8Slot(2, int8(src.SYMMETRIC()), 0)
	b.PrependInt8Slot(3, int8(src.KEY_DERIVATION()), 0)
	b.PrependUOffsetTSlot(4, ephemeral, 0)
	b.PrependUOffsetTSlot(5, nonce, 0)
	b.PrependUOffsetTSlot(6, recipient, 0)
	b.PrependUOffsetTSlot(7, context, 0)
	b.PrependUOffsetTSlot(8, schemaHash, 0)
	b.PrependUOffsetTSlot(9, rootType, 0)
	b.PrependUint64Slot(10, src.TIMESTAMP(), 0)
	encTable := encsds.ENCEnd(b)

	standard := b.CreateString("ENC")
	b.StartObject(3)
	b.PrependByteSlot(0, byte(ordinals["ENC"]), 0)
	b.PrependUOffsetTSlot(1, encTable, 0)
	b.PrependUOffsetTSlot(2, standard, 0)
	encRecord := b.EndObject()

	return appendTrailer(payload, buildRecordCollection(b, []flatbuffers.UOffsetT{encRecord}))
}

// TestSelectENCRecordIgnoresUnionOrdinals is the regression guard for JANUS's
// binding ABI ruling of 2026-08-07: the RecordType union has renumbered at least
// three times, and each renumber silently invalidates every trailer ever
// written. Record.standard is the only stable discriminator.
//
// The 2026-07-10 artifacts still on the delivery node carry MBL=67 ENC=34
// PNM=98; a later generation shifted PNM 113 -> 114 when $PGM was inserted mid-
// union (SDS c1580d4700). This test replays all three generations against ONE
// artifact and requires identical plaintext from each. It is what makes this
// reader ordinal-proof rather than merely correct today.
func TestSelectENCRecordIgnoresUnionOrdinals(t *testing.T) {
	manifest, key := loadFixtures(t)
	blob := readFixture(t, manifest.Fixtures[0].Name+".bin")
	payload, recordCollection, err := parsePublicationTrailer(blob)
	if err != nil {
		t.Fatalf("parse trailer: %v", err)
	}
	rec, err := selectENCRecord(recordCollection)
	if err != nil {
		t.Fatalf("select ENC: %v", err)
	}
	encBytes := encodeENCRecord(rec)
	want := sha256Hex(mustDecrypt(t, blob, key))

	generations := []struct {
		name     string
		ordinals map[string]int
	}{
		{"2026-07-10 generation (MBL=67 ENC=34 PNM=98)", map[string]int{"MBL": 67, "ENC": 34, "PNM": 98}},
		{"pre-$PGM generation (PNM=113)", map[string]int{"MBL": 80, "ENC": 39, "PNM": 113}},
		{"post-$PGM generation (PNM=114)", map[string]int{"MBL": 80, "ENC": 39, "PNM": 114}},
		{"absurd future renumber", map[string]int{"MBL": 3, "ENC": 251, "PNM": 7}},
	}
	for _, generation := range generations {
		t.Run(generation.name, func(t *testing.T) {
			rewrapped := rewrapWithUnionOrdinals(t, payload, encBytes, generation.ordinals)
			plaintext, err := decryptProtectedPublication(rewrapped, key)
			if err != nil {
				t.Fatalf("ordinal %d must not matter, but decrypt failed: %v", generation.ordinals["ENC"], err)
			}
			if got := sha256Hex(plaintext); got != want {
				t.Fatalf("plaintext differs across union renumber: got %s want %s", got, want)
			}
		})
	}
}

func mustDecrypt(t *testing.T, blob, key []byte) []byte {
	t.Helper()
	plaintext, err := decryptProtectedPublication(blob, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return plaintext
}

// TestProtectedPublicationNegativeControls proves the failures fail. Without
// these the fix is unfalsifiable: a decrypt path that accepts everything looks
// identical to one that works.
func TestProtectedPublicationNegativeControls(t *testing.T) {
	manifest, key := loadFixtures(t)
	// GCM fixture: authentication must actually be enforced.
	gcm := readFixture(t, "gcm-mbl-enc-pnm.bin")

	t.Run("wrong recipient key", func(t *testing.T) {
		wrong := make([]byte, 32)
		copy(wrong, key)
		wrong[0] ^= 0xff
		if _, err := decryptProtectedPublication(gcm, wrong); err == nil {
			t.Fatal("decrypt succeeded with the wrong key")
		}
	})

	t.Run("short recipient key", func(t *testing.T) {
		if _, err := decryptProtectedPublication(gcm, key[:16]); err == nil {
			t.Fatal("decrypt accepted a 16-byte key")
		}
	})

	t.Run("tampered ciphertext", func(t *testing.T) {
		tampered := append([]byte(nil), gcm...)
		tampered[0] ^= 0x01
		if _, err := decryptProtectedPublication(tampered, key); err == nil {
			t.Fatal("decrypt accepted tampered ciphertext")
		}
	})

	t.Run("tampered ENC record breaks the AAD binding", func(t *testing.T) {
		// Flip a byte inside the context string in the trailer. The bytes are
		// not secret and not MAC'd on their own — the ONLY thing that catches
		// this is the AAD, which is exactly the property being asserted.
		tampered := append([]byte(nil), gcm...)
		idx := bytes.Index(tampered, []byte("space-data-module-sdk/package"))
		if idx < 0 {
			t.Skip("fixture context string not found; nothing to tamper")
		}
		tampered[idx] = 'S'
		if _, err := decryptProtectedPublication(tampered, key); err == nil {
			t.Fatal("decrypt accepted a modified $ENC record: the AAD is not bound")
		}
	})

	t.Run("truncated trailer length", func(t *testing.T) {
		broken := append([]byte(nil), gcm...)
		binary.LittleEndian.PutUint32(broken[len(broken)-publicationFooterLength:], 0xffffff00)
		if _, err := decryptProtectedPublication(broken, key); err == nil {
			t.Fatal("decrypt accepted an out-of-range trailer length")
		} else if !strings.Contains(err.Error(), "exceeds artifact body") {
			t.Fatalf("unexpected error for out-of-range trailer: %v", err)
		}
	})

	t.Run("no trailer at all", func(t *testing.T) {
		if hasPublicationTrailer([]byte("not a publication")) {
			t.Fatal("plain bytes reported a $REC trailer")
		}
		if _, err := decryptProtectedPublication([]byte("not a publication"), key); err == nil {
			t.Fatal("decrypt accepted bytes with no trailer")
		}
	})

	t.Run("trailer with no ENC record", func(t *testing.T) {
		b := flatbuffers.NewBuilder(256)
		standard := b.CreateString("PNM")
		b.StartObject(3)
		b.PrependUOffsetTSlot(2, standard, 0)
		record := b.EndObject()
		blob := appendTrailer([]byte("payload"), buildRecordCollection(b, []flatbuffers.UOffsetT{record}))

		if _, err := decryptProtectedPublication(blob, key); err == nil {
			t.Fatal("decrypt accepted a trailer with no $ENC record")
		} else if !strings.Contains(err.Error(), `no "ENC" record`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	_ = manifest
}

// TestDecryptBundleUsesTheArtifactsOwnKey is the half of the defect that the
// format fix alone would not have caught: node.go passed the NODE IDENTITY key,
// while every artifact is sealed to the per-plugin bundle.key beside it. Here
// DecryptBundle is handed a deliberately wrong identity key — and nil — and must
// still open the artifact, because it resolves the key itself.
func TestDecryptBundleUsesTheArtifactsOwnKey(t *testing.T) {
	manifest, key := loadFixtures(t)
	blob := readFixture(t, "gcm-mbl-enc-pnm.bin")

	root := t.TempDir()
	pluginID := "com.example.protected"
	pluginDir := filepath.Join(root, pluginID)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "bundle.wasm.enc"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "bundle.key"), []byte(manifest.Key), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := fmt.Sprintf(`{"plugins":[{"id":%q,"version":"1.0.0","encrypted_path":%q,"key_path":%q}]}`,
		pluginID,
		filepath.Join(pluginID, "bundle.wasm.enc"),
		filepath.Join(pluginID, "bundle.key"),
	)
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	wrongIdentityKey := make([]byte, 32)
	for i := range wrongIdentityKey {
		wrongIdentityKey[i] = 0xAB
	}

	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{"wrong node identity key", wrongIdentityKey},
		{"nil key", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := registry.DecryptBundle(pluginID, tc.key)
			if err != nil {
				t.Fatalf("DecryptBundle: %v", err)
			}
			if !bytes.HasPrefix(got, []byte("\x00asm")) {
				t.Fatalf("decrypted bytes are not WASM: % x", got[:4])
			}
			want := mustDecrypt(t, blob, key)
			if !bytes.Equal(got, want) {
				t.Fatal("DecryptBundle returned different bytes than the direct decrypt")
			}
		})
	}

	t.Run("unusable bundle key is a named failure, not an HMAC lie", func(t *testing.T) {
		// The registry validates key_path at load, so corrupt the key in place
		// on the ALREADY-loaded registry: that is the runtime shape of the
		// failure, and the point is the diagnostic it produces.
		if err := os.WriteFile(filepath.Join(pluginDir, "bundle.key"), []byte("not-a-key"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := registry.DecryptBundle(pluginID, wrongIdentityKey)
		if err == nil {
			t.Fatal("expected a failure with no bundle key")
		}
		if strings.Contains(err.Error(), "HMAC") {
			t.Fatalf("a protected publication must never report a V1 HMAC failure: %v", err)
		}
		if !strings.Contains(err.Error(), "protected publication") {
			t.Fatalf("error should name the format: %v", err)
		}
	})
}

// TestRealDeliveryArtifacts is the opt-in check against PRODUCTION bytes. The
// artifacts and their keys are deliberately NOT committed — point it at a
// directory of <plugin-id>/{bundle.wasm.enc,bundle.key} copied off a node:
//
//	SDN_PROTECTED_ARTIFACT_DIR=/path/to/plugins go test ./internal/license -run RealDelivery -v
func TestRealDeliveryArtifacts(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("SDN_PROTECTED_ARTIFACT_DIR"))
	if dir == "" {
		t.Skip("set SDN_PROTECTED_ARTIFACT_DIR to verify against real delivery-node artifacts")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read artifact dir: %v", err)
	}
	checked, failed := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		blobPath := filepath.Join(dir, entry.Name(), "bundle.wasm.enc")
		keyPath := filepath.Join(dir, entry.Name(), "bundle.key")
		blob, err := os.ReadFile(blobPath)
		if err != nil {
			continue
		}
		rawKey, err := os.ReadFile(keyPath)
		if err != nil {
			continue
		}
		key, err := parseBundleKey(rawKey)
		if err != nil {
			t.Errorf("%s: bundle key: %v", entry.Name(), err)
			failed++
			continue
		}
		if !hasPublicationTrailer(blob) {
			t.Logf("%s: no $REC trailer (legacy V1/V2 artifact), skipped", entry.Name())
			continue
		}
		checked++
		plaintext, err := decryptProtectedPublication(blob, key)
		if err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
			failed++
			continue
		}
		// NOT every catalog artifact is WASM: the viewshed-shader fragments are
		// GLSL source and `.uniforms` / `.glsl-bundle` are JSON. Asserting the
		// WASM magic here would fail 18 perfectly good decrypts. What must hold
		// for all of them is that the payload opened at all — GCM already
		// authenticated it, and for the unauthenticated CTR generation a
		// recognisable content prefix is the check that the key schedule was
		// right rather than producing plausible-looking garbage.
		kind := "opaque"
		switch {
		case bytes.HasPrefix(plaintext, []byte("\x00asm")):
			kind = "WASM"
		case bytes.HasPrefix(plaintext, []byte("{")), bytes.HasPrefix(plaintext, []byte("[")):
			kind = "JSON"
		case utf8.Valid(plaintext):
			kind = "text/GLSL"
		}
		if kind == "opaque" {
			t.Errorf("%s: decrypted %d bytes of unrecognised content: % x", entry.Name(), len(plaintext), plaintext[:4])
			failed++
			continue
		}
		t.Logf("%s: %d B -> %d B %s", entry.Name(), len(blob), len(plaintext), kind)
	}
	if checked == 0 {
		t.Fatal("no protected-publication artifacts found in SDN_PROTECTED_ARTIFACT_DIR")
	}
	t.Logf("protected-publication artifacts: %d checked, %d failed", checked, failed)
}
