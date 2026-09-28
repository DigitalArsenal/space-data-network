// Package wasm provides WebAssembly integration for FlatBuffers operations.
package wasm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sdsSchemaDir is the node's embedded SDS schema set (flat <FAMILY>.fbs files).
const sdsSchemaDir = "../sds/schemas"

func TestEmbeddedFlatcArtifact(t *testing.T) {
	sum := sha256.Sum256(EmbeddedFlatcWasm())
	if got := hex.EncodeToString(sum[:]); got != FlatcWasiSHA256 {
		t.Fatalf("embedded flatc-wasi.wasm sha256 = %s, want %s (run npm run sync:flatc-wasi and update the pin)", got, FlatcWasiSHA256)
	}

	// The embed must be the artifact package.json pins (embed == pin).
	manifest, err := os.ReadFile("../../../package.json")
	if err != nil {
		t.Fatalf("read repo package.json: %v", err)
	}
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(manifest, &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if got, want := pkg.DevDependencies["flatc-wasi"], "npm:"+FlatcWasiPackage; got != want {
		t.Fatalf("package.json flatc-wasi = %q, want %q", got, want)
	}

	// When the package is installed, the embed must equal its file and the
	// package's own digest.
	installed, err := os.ReadFile("../../../node_modules/flatc-wasi/dist/flatc-wasi.wasm")
	if errors.Is(err, os.ErrNotExist) {
		t.Log("node_modules/flatc-wasi not installed; package byte comparison not run")
		return
	}
	if err != nil {
		t.Fatalf("read installed flatc-wasi.wasm: %v", err)
	}
	if !bytes.Equal(installed, EmbeddedFlatcWasm()) {
		t.Fatal("embedded flatc-wasi.wasm differs from node_modules/flatc-wasi/dist/flatc-wasi.wasm")
	}
	digest, err := os.ReadFile("../../../node_modules/flatc-wasi/dist/flatc-wasi.wasm.sha256")
	if err != nil {
		t.Fatalf("read package digest: %v", err)
	}
	if fields := strings.Fields(string(digest)); len(fields) == 0 || fields[0] != FlatcWasiSHA256 {
		t.Fatalf("package digest %q, want %s", strings.TrimSpace(string(digest)), FlatcWasiSHA256)
	}
}

func newTestFlatc(t *testing.T) *FlatcModule {
	t.Helper()
	fm, err := NewEmbeddedFlatcModule(context.Background())
	if err != nil {
		t.Fatalf("NewEmbeddedFlatcModule: %v", err)
	}
	t.Cleanup(func() { fm.Close(context.Background()) })
	return fm
}

// putSDSSchemas puts every embedded SDS schema into the file map at
// <FAMILY>/main.fbs, the layout their includes expect.
func putSDSSchemas(t *testing.T, fm *FlatcModule) {
	t.Helper()
	entries, err := os.ReadDir(sdsSchemaDir)
	if err != nil {
		t.Fatalf("read %s: %v", sdsSchemaDir, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".fbs") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(sdsSchemaDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := fm.PutFile(context.Background(), strings.TrimSuffix(e.Name(), ".fbs")+"/main.fbs", content); err != nil {
			t.Fatalf("PutFile %s: %v", e.Name(), err)
		}
		n++
	}
	if n == 0 {
		t.Fatalf("no schemas in %s", sdsSchemaDir)
	}
}

func addSDSSchema(t *testing.T, fm *FlatcModule, family string) int {
	t.Helper()
	id, err := fm.AddSchema(context.Background(), family+"/main.fbs", nil)
	if err != nil {
		t.Fatalf("AddSchema %s: %v", family, err)
	}
	return id
}

func decodeJSON(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode JSON %q: %v", data, err)
	}
	return v
}

func TestFlatcModuleLoadsEmbeddedArtifact(t *testing.T) {
	fm := newTestFlatc(t)
	if fm.Version() == "" {
		t.Fatal("converter reported no FlatBuffers version")
	}
}

func TestNewFlatcModuleNoPath(t *testing.T) {
	_, err := NewFlatcModule(context.Background(), "")
	if !errors.Is(err, ErrNoModule) {
		t.Fatalf("empty path: err = %v, want ErrNoModule", err)
	}
}

func TestNewFlatcModuleInvalidPath(t *testing.T) {
	_, err := NewFlatcModule(context.Background(), "/nonexistent/path/to/flatc-wasi.wasm")
	if !errors.Is(err, ErrNoModule) {
		t.Fatalf("missing file: err = %v, want ErrNoModule", err)
	}
}

// A missing or corrupt converter must fail with a message that says so, never
// hand back a nil module that callers then treat as "optional".
func TestFlatcLoadFailsLoudly(t *testing.T) {
	ctx := context.Background()

	_, err := loadFlatcModule(ctx, nil, FlatcWasiSHA256, "test artifact")
	if !errors.Is(err, ErrNoModule) || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing artifact: err = %v", err)
	}

	corrupt := append([]byte(nil), EmbeddedFlatcWasm()...)
	corrupt[len(corrupt)/2] ^= 0xff
	_, err = loadFlatcModule(ctx, corrupt, FlatcWasiSHA256, "test artifact")
	if !errors.Is(err, ErrNoModule) || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("digest mismatch: err = %v", err)
	}

	// Without a digest pin, garbage still fails at instantiation.
	_, err = NewFlatcModuleFromBytes(ctx, []byte("not a wasm module"))
	if !errors.Is(err, ErrNoModule) || !strings.Contains(err.Error(), "instantiate") {
		t.Fatalf("garbage bytes: err = %v", err)
	}

	// A valid module without the ABI is refused by name.
	empty := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	_, err = NewFlatcModuleFromBytes(ctx, empty)
	if !errors.Is(err, ErrNoModule) || !strings.Contains(err.Error(), "missing exports") {
		t.Fatalf("module without the ABI: err = %v", err)
	}
}

func TestFlatcAddSchemaWithoutModuleFails(t *testing.T) {
	fm := &FlatcModule{schemas: make(map[string]int)}
	id, err := fm.AddSchema(context.Background(), "test/main.fbs", []byte("table T { a: int; } root_type T;"))
	if err != ErrNoModule || id != 0 {
		t.Fatalf("AddSchema without module = (%d, %v), want (0, ErrNoModule)", id, err)
	}
	if _, ok := fm.GetSchemaID("test/main.fbs"); ok {
		t.Fatal("a schema the converter never loaded was recorded")
	}
}

func TestFlatcAddSchemaReportsErrors(t *testing.T) {
	fm := newTestFlatc(t)
	ctx := context.Background()

	_, err := fm.AddSchema(ctx, "BAD/main.fbs", []byte("table T { a: int; "))
	if status, _ := FlatcStatusOf(err); status != FlatcSchemaParse {
		t.Fatalf("syntax error: err = %v, want SCHEMA_PARSE", err)
	}
	var fe *FlatcError
	if !errors.As(err, &fe) || fe.Message == "" {
		t.Fatalf("syntax error carried no converter message: %v", err)
	}

	// MPE includes ../MET/main.fbs; without it in the file map the include
	// cannot resolve.
	mpe, err := os.ReadFile(filepath.Join(sdsSchemaDir, "MPE.fbs"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fm.AddSchema(ctx, "MPE/main.fbs", mpe)
	if status, _ := FlatcStatusOf(err); status != FlatcSchemaParse {
		t.Fatalf("unresolved include: err = %v, want SCHEMA_PARSE", err)
	}
	if _, ok := fm.GetSchemaID("MPE/main.fbs"); ok {
		t.Fatal("a schema that failed to parse was recorded")
	}

	_, err = fm.AddSchema(ctx, "NOPE/main.fbs", nil)
	if status, _ := FlatcStatusOf(err); status != FlatcFileNotFound {
		t.Fatalf("absent file: err = %v, want FILE_NOT_FOUND", err)
	}
}

// JSON -> binary -> JSON on SDS records, including MPE's MET include.
func TestFlatcRoundTripSDSRecords(t *testing.T) {
	fm := newTestFlatc(t)
	putSDSSchemas(t, fm)
	ctx := context.Background()

	cases := []struct {
		family string
		json   string
	}{
		{"OMM", `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":"2026-09-27T12:00:00.000000","MEAN_MOTION":15.50103472,"ECCENTRICITY":0.0006703,"INCLINATION":51.6416,"RA_OF_ASC_NODE":247.4627,"ARG_OF_PERICENTER":130.536,"MEAN_ANOMALY":325.0288,"EPHEMERIS_TYPE":"SGP4","CLASSIFICATION_TYPE":"U","NORAD_CAT_ID":25544,"ELEMENT_SET_NO":999,"REV_AT_EPOCH":46732,"BSTAR":0.00010270,"MEAN_MOTION_DOT":0.00001264,"MEAN_MOTION_DDOT":0,"MEAN_ELEMENT_THEORY":"SGP4","TIME_SYSTEM":"UTC"}`},
		{"CAT", `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","NORAD_CAT_ID":25544,"LAUNCH_DATE":"1998-11-20","LAUNCH_SITE":"TYMSC","PERIOD":92.9,"INCLINATION":51.64,"APOGEE":422,"PERIGEE":417}`},
		{"MPE", `{"ENTITY_ID":"25544","EPOCH":1790000000.25,"MEAN_MOTION":15.50103472,"ECCENTRICITY":0.0006703,"INCLINATION":51.6416,"RA_OF_ASC_NODE":247.4627,"ARG_OF_PERICENTER":130.536,"MEAN_ANOMALY":325.0288,"BSTAR":0.0001027,"MEAN_ELEMENT_THEORY":"SGP4"}`},
	}
	idents := map[string]string{"OMM": "$OMM", "CAT": "$CAT", "MPE": "$MPE"}
	for _, tc := range cases {
		t.Run(tc.family, func(t *testing.T) {
			id := addSDSSchema(t, fm, tc.family)
			opts := FlatcSizePrefixed | FlatcForceDefaults
			bin, err := fm.JSONToBinary(ctx, id, []byte(tc.json), opts)
			if err != nil {
				t.Fatalf("JSONToBinary: %v", err)
			}
			if got := binary.LittleEndian.Uint32(bin); int(got) != len(bin)-4 {
				t.Fatalf("size prefix %d, buffer %d", got, len(bin))
			}
			if got := string(bin[8:12]); got != idents[tc.family] {
				t.Fatalf("file identifier %q, want %q", got, idents[tc.family])
			}
			out, err := fm.BinaryToJSON(ctx, id, bin, FlatcSizePrefixed|FlatcCompactJSON)
			if err != nil {
				t.Fatalf("BinaryToJSON: %v", err)
			}
			if want, got := decodeJSON(t, []byte(tc.json)), decodeJSON(t, out); !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip changed values\n in: %v\nout: %v", want, got)
			}
			if err := fm.VerifyBinary(ctx, id, bin, FlatcSizePrefixed); err != nil {
				t.Fatalf("VerifyBinary: %v", err)
			}
		})
	}
}

func TestFlatcSizePrefixOption(t *testing.T) {
	fm := newTestFlatc(t)
	putSDSSchemas(t, fm)
	ctx := context.Background()
	id := addSDSSchema(t, fm, "OMM")
	record := []byte(`{"OBJECT_NAME":"X","NORAD_CAT_ID":7}`)

	bare, err := fm.JSONToBinary(ctx, id, record, 0)
	if err != nil {
		t.Fatal(err)
	}
	prefixed, err := fm.JSONToBinary(ctx, id, record, FlatcSizePrefixed)
	if err != nil {
		t.Fatal(err)
	}
	if string(bare[4:8]) != "$OMM" {
		t.Fatalf("bare buffer identifier %q", bare[4:8])
	}
	if int(binary.LittleEndian.Uint32(prefixed)) != len(prefixed)-4 || string(prefixed[8:12]) != "$OMM" {
		t.Fatalf("size-prefixed buffer malformed: % x", prefixed[:12])
	}

	// Each form reads back only under its own option.
	if _, err := fm.BinaryToJSON(ctx, id, bare, 0); err != nil {
		t.Fatalf("bare read: %v", err)
	}
	if _, err := fm.BinaryToJSON(ctx, id, prefixed, FlatcSizePrefixed); err != nil {
		t.Fatalf("prefixed read: %v", err)
	}
	if _, err := fm.BinaryToJSON(ctx, id, bare, FlatcSizePrefixed); !isStatus(err, FlatcInvalidBinary) {
		t.Fatalf("bare buffer read as size-prefixed: err = %v, want INVALID_BINARY", err)
	}
}

func TestFlatcForceDefaultsOption(t *testing.T) {
	fm := newTestFlatc(t)
	putSDSSchemas(t, fm)
	ctx := context.Background()
	id := addSDSSchema(t, fm, "OMM")
	// MEAN_MOTION_DDOT = 0 and EPHEMERIS_TYPE = SGP4 both equal their defaults.
	record := []byte(`{"NORAD_CAT_ID":25544,"MEAN_MOTION_DDOT":0,"EPHEMERIS_TYPE":"SGP4"}`)

	plain, err := fm.JSONToBinary(ctx, id, record, 0)
	if err != nil {
		t.Fatal(err)
	}
	forced, err := fm.JSONToBinary(ctx, id, record, FlatcForceDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(forced) <= len(plain) {
		t.Fatalf("force-defaults stored nothing extra: %d vs %d bytes", len(forced), len(plain))
	}

	plainOut := decodeJSON(t, mustJSON(t, fm, id, plain, FlatcCompactJSON))
	if _, ok := plainOut["MEAN_MOTION_DDOT"]; ok {
		t.Fatalf("default-valued field stored without force-defaults: %v", plainOut)
	}
	forcedOut := decodeJSON(t, mustJSON(t, fm, id, forced, FlatcCompactJSON))
	if !reflect.DeepEqual(forcedOut, decodeJSON(t, record)) {
		t.Fatalf("force-defaults round trip: got %v, want %v", forcedOut, decodeJSON(t, record))
	}

	// On output, force-defaults prints every scalar field, stored or not.
	allOut := decodeJSON(t, mustJSON(t, fm, id, plain, FlatcCompactJSON|FlatcForceDefaults))
	if _, ok := allOut["MEAN_MOTION_DDOT"]; !ok {
		t.Fatalf("force-defaults output omitted a default scalar: %v", allOut)
	}
	// Strings are not scalars: an absent one stays absent.
	if _, ok := allOut["OBJECT_NAME"]; ok {
		t.Fatalf("absent string printed: %v", allOut)
	}
}

func mustJSON(t *testing.T, fm *FlatcModule, id int, bin []byte, opts FlatcOption) []byte {
	t.Helper()
	out, err := fm.BinaryToJSON(context.Background(), id, bin, opts)
	if err != nil {
		t.Fatalf("BinaryToJSON: %v", err)
	}
	return out
}

func isStatus(err error, want FlatcStatus) bool {
	got, ok := FlatcStatusOf(err)
	return ok && got == want
}

// A record whose binary is more than twice its JSON outgrows the first guess
// and must come back through the out_len retry intact.
func TestFlatcOutLenRetry(t *testing.T) {
	fm := newTestFlatc(t)
	putSDSSchemas(t, fm)
	ctx := context.Background()
	id := addSDSSchema(t, fm, "MPE")

	const n = 4000
	residuals := strings.TrimSuffix(strings.Repeat("0,", n), ",")
	record := []byte(`{"ENTITY_ID":"retry","TARGETER":{"SOLVER":"dc","RESIDUALS":[` + residuals + `]}}`)

	before := fm.Retries()
	bin, err := fm.JSONToBinary(ctx, id, record, FlatcSizePrefixed)
	if err != nil {
		t.Fatalf("JSONToBinary: %v", err)
	}
	if len(bin) <= 2*len(record) {
		t.Fatalf("fixture does not exceed 2x its JSON: %d binary vs %d JSON", len(bin), len(record))
	}
	if fm.Retries() != before+1 {
		t.Fatalf("retries = %d, want %d", fm.Retries(), before+1)
	}

	out := decodeJSON(t, mustJSON(t, fm, id, bin, FlatcSizePrefixed|FlatcCompactJSON))
	targeter, _ := out["TARGETER"].(map[string]interface{})
	got, _ := targeter["RESIDUALS"].([]interface{})
	if len(got) != n {
		t.Fatalf("RESIDUALS came back with %d values, want %d", len(got), n)
	}
}

func TestFlatcConversionErrors(t *testing.T) {
	fm := newTestFlatc(t)
	putSDSSchemas(t, fm)
	ctx := context.Background()
	id := addSDSSchema(t, fm, "OMM")

	_, err := fm.JSONToBinary(ctx, id, []byte(`{"NORAD_CAT_ID": }`), 0)
	if !isStatus(err, FlatcJSONParse) || !strings.Contains(err.Error(), "JSON_PARSE") {
		t.Fatalf("malformed JSON: err = %v", err)
	}
	_, err = fm.JSONToBinary(ctx, id, []byte(`{"NOT_A_FIELD": 1}`), 0)
	if !isStatus(err, FlatcJSONParse) {
		t.Fatalf("unknown field: err = %v, want JSON_PARSE", err)
	}
	if _, err := fm.JSONToBinary(ctx, id, []byte(`{"NOT_A_FIELD": 1}`), FlatcSkipUnknownFields); err != nil {
		t.Fatalf("unknown field with skip: %v", err)
	}
	_, err = fm.JSONToBinary(ctx, 999999, []byte(`{}`), 0)
	if !isStatus(err, FlatcSchemaNotFound) {
		t.Fatalf("unknown schema: err = %v, want SCHEMA_NOT_FOUND", err)
	}
	_, err = fm.JSONToBinary(ctx, id, []byte(`{}`), 1<<10)
	if !isStatus(err, FlatcUnknownOption) {
		t.Fatalf("unknown option: err = %v, want UNKNOWN_OPTION", err)
	}

	good, err := fm.JSONToBinary(ctx, id, []byte(`{"OBJECT_NAME":"X"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fm.VerifyBinary(ctx, id, good[:len(good)-6], 0); !isStatus(err, FlatcInvalidBinary) {
		t.Fatalf("truncated buffer: err = %v, want INVALID_BINARY", err)
	}

	// The instance stays usable after every failure above.
	if _, err := fm.BinaryToJSON(ctx, id, good, 0); err != nil {
		t.Fatalf("converter unusable after errors: %v", err)
	}
}

func TestFlatcModuleGetSchemaID(t *testing.T) {
	fm := &FlatcModule{schemas: map[string]int{"test/main.fbs": 1}}
	if id, ok := fm.GetSchemaID("test/main.fbs"); !ok || id != 1 {
		t.Fatalf("GetSchemaID = (%d, %v)", id, ok)
	}
	if _, ok := fm.GetSchemaID("nonexistent/main.fbs"); ok {
		t.Fatal("found a schema that was never added")
	}
}

func TestFlatcModuleClose(t *testing.T) {
	fm := &FlatcModule{}
	if err := fm.Close(context.Background()); err != nil {
		t.Fatalf("Close on an unloaded module: %v", err)
	}
}

func TestErrNoModule(t *testing.T) {
	ctx := context.Background()
	fm := &FlatcModule{schemas: make(map[string]int)}

	if _, err := fm.JSONToBinary(ctx, 1, []byte(`{"test": true}`), 0); err != ErrNoModule {
		t.Errorf("JSONToBinary: %v", err)
	}
	if _, err := fm.BinaryToJSON(ctx, 1, []byte{0x01, 0x02}, 0); err != ErrNoModule {
		t.Errorf("BinaryToJSON: %v", err)
	}
	if err := fm.VerifyBinary(ctx, 1, []byte{0x01, 0x02}, 0); err != ErrNoModule {
		t.Errorf("VerifyBinary: %v", err)
	}
	if err := fm.PutFile(ctx, "a/main.fbs", nil); err != ErrNoModule {
		t.Errorf("PutFile: %v", err)
	}
	if _, err := fm.Encrypt(ctx, make([]byte, 32), []byte("test")); err != ErrNoModule {
		t.Errorf("Encrypt: %v", err)
	}
	if _, err := fm.Decrypt(ctx, make([]byte, 32), []byte("test")); err != ErrNoModule {
		t.Errorf("Decrypt: %v", err)
	}
	if _, err := fm.Sign(ctx, make([]byte, 64), []byte("test")); err != ErrNoModule {
		t.Errorf("Sign: %v", err)
	}
	if _, err := fm.Verify(ctx, make([]byte, 32), []byte("test"), make([]byte, 64)); err != ErrNoModule {
		t.Errorf("Verify: %v", err)
	}
}

// The converter does not carry flatc-encryption.wasm's crypto exports; the
// crypto calls say so instead of failing on a function lookup.
func TestFlatcCryptoExportsAbsentFromConverter(t *testing.T) {
	fm := newTestFlatc(t)
	if _, err := fm.Encrypt(context.Background(), make([]byte, 32), []byte("x")); !errors.Is(err, ErrNoModule) {
		t.Fatalf("Encrypt on the converter: err = %v, want ErrNoModule", err)
	}
}
