package format2

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"testing"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// The key order of cidOrderKey is the text CID order (a two-phase window in
// text CID order sorts keys, never text).
func TestCIDOrderKeyIsTheTextOrder(t *testing.T) {
	var bins [][]byte
	for i := 0; i < 20000; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprint(i)))
		m, err := mh.Encode(sum[:], mh.SHA2_256)
		if err != nil {
			t.Fatal(err)
		}
		bins = append(bins, cid.NewCidV1(cid.Raw, m).Bytes())
	}
	byText := append([][]byte(nil), bins...)
	sort.Slice(byText, func(i, j int) bool { return CIDText(byText[i]) < CIDText(byText[j]) })
	byKey := append([][]byte(nil), bins...)
	sort.Slice(byKey, func(i, j int) bool {
		a, _ := cidOrderKey(byKey[i])
		b, _ := cidOrderKey(byKey[j])
		return bytes.Compare(a[:], b[:]) < 0
	})
	for i := range byText {
		if !bytes.Equal(byText[i], byKey[i]) {
			t.Fatalf("position %d: text order %s, key order %s", i, CIDText(byText[i]), CIDText(byKey[i]))
		}
	}
	if _, ok := cidOrderKey(bins[0][:35]); ok {
		t.Fatal("a 35-byte CID has a key")
	}
}
