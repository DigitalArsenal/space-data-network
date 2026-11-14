package api

import (
	"bytes"
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
func TestCatalogFixtureGenerator(t *testing.T) {
	for day := 0; day < 4; day++ {
		for schema, generated := range generateCatalogHistory(t, day) {
			raw, err := os.ReadFile(filepath.Join("testdata", "catalog-history", fmt.Sprintf("day%d-%s.sds", day, schema)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, generated) {
				t.Fatalf("day %d %s fixture differs from generator", day, schema)
			}
		}
	}
}
