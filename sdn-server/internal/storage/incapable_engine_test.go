package storage

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// incapableEngineSHA256 is the sha256 of flatsql 3.12.11's wasm/flatsql.wasm
// (its package's integrity.json): a published format-5 engine without the
// flatsql_capabilities export.
const incapableEngineSHA256 = "1f7b15d8945aaf98f19e878e85367e35ef9c8665c10f13e7e8f9cb68a86b497d"

// incapableEngineWasm is that engine (testdata/flatsql-3.12.11.wasm.gz,
// gzip -9n of the published bytes), checked against its sha256.
func incapableEngineWasm(t *testing.T) []byte {
	t.Helper()
	gz, err := os.ReadFile(filepath.Join("testdata", "flatsql-3.12.11.wasm.gz"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(wasm); hex.EncodeToString(sum[:]) != incapableEngineSHA256 {
		t.Fatalf("testdata engine sha256 %x, want %s", sum, incapableEngineSHA256)
	}
	return wasm
}
