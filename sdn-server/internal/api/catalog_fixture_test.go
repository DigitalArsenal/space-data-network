package api

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	cidlib "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"os"
	"path/filepath"
	"testing"
)

// generateCatalogHistory derives all editions from two synthetic SDS seed records.
// The OCM seed is a fitted synthetic example; daily edits change metadata only.
func generateCatalogHistory(t *testing.T, day int) map[string][]byte {
	t.Helper()
	seed := map[string][]byte{}
	for _, schema := range []string{"CAT", "OCM"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "catalog-history", "seed-"+schema+".sds"))
		if err != nil {
			t.Fatal(err)
		}
		seed[schema] = raw
	}
	result := map[string][]byte{}
	date := fmt.Sprintf("2026-11-%02dT00:00:00.000Z", day+1)
	for i := 0; i < 32; i++ {
		product := bytes.Clone(seed["OCM"])
		ocm := OCM.GetRootAsOCM(product, 4)
		copy(ocm.HEADER(nil).CREATION_DATE(), date)
		if !ocm.METADATA(nil).MutateNORAD_CAT_ID(uint32(910000 + i)) {
			t.Fatal("missing synthetic identifier")
		}
		h, err := mh.Sum(product, mh.SHA2_256, -1)
		if err != nil {
			t.Fatal(err)
		}
		id := cidlib.NewCidV1(cidlib.Raw, h).String()
		index := bytes.Clone(seed["CAT"])
		cat := CAT.GetRootAsCAT(index, 4)
		if !cat.MutateNORAD_CAT_ID(uint32(910000 + i)) {
			t.Fatal("missing synthetic identifier")
		}
		copy(cat.PRODUCTS_AS_OF(), date)
		var ref CAT.CATProduct
		if !cat.PRODUCTS(&ref, 0) {
			t.Fatal("missing reference")
		}
		copy(ref.RECORD_CID(), id)
		copy(ref.EPOCH(), date)
		result["OCM"] = append(result["OCM"], product...)
		result["CAT"] = append(result["CAT"], index...)
	}
	return result
}
func catalogGeneratedPath(t *testing.T, day int, schema string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "generated.sds")
	if err := os.WriteFile(path, generateCatalogHistory(t, day)[schema], 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestCatalogFixtureGenerator(t *testing.T) {
	expected := map[string]string{
		"day0-CAT.sds": "9bcd8656559164330cd015a7d74adc67a757ecdf02348943540de0e9b68d0d53",
		"day0-OCM.sds": "fc0cebe658a58b52bc1ec1433be1efb79f29d6768a7f9fb4205aaba44f0f8849",
		"day1-CAT.sds": "d48135bec468c9d1f4515c11f08cfed1b8d65e5b1b2a4472d2b96c86b886af5d",
		"day1-OCM.sds": "d228c3a6e95f2fc2f20abdf1f4bd6440a2f06b22bf2e4a113fb0e69a5464ced2",
		"day2-CAT.sds": "7a7341edcbc3cb590d436bbfd6c7ce223742ffee8fee427ec8bc7a21322369ed",
		"day2-OCM.sds": "e8b668ab83de2bcdf2532c0f6d5cd24a63f3e8c6a28e4204bf485dea42e08ff0",
		"day3-CAT.sds": "9f92874fcba9b9ca2db7ae0f993bb86b1a1131ae889f1395f00c21e269f330f9",
		"day3-OCM.sds": "5d135eeb8558983f4df2d0afebd0388c06b871730a481f7466fd11d7b1a281a7",
	}
	for day := 0; day < 4; day++ {
		for schema, generated := range generateCatalogHistory(t, day) {
			name := fmt.Sprintf("day%d-%s.sds", day, schema)
			got := fmt.Sprintf("%x", sha256.Sum256(generated))
			if got != expected[name] {
				t.Fatalf("generated fixture digest differs: %s", name)
			}
		}
	}
}
