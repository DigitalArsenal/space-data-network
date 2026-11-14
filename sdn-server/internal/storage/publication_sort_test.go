package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestPublicationDigestOrderMatchesCID(t *testing.T) {
	var digests [][32]byte
	for i := 0; i < 10000; i++ {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], uint64(i))
		digests = append(digests, sha256.Sum256(raw[:]))
	}
	sort.Slice(digests, func(i, j int) bool { return publicationDigestLess(digests[i], digests[j]) })
	for i := 1; i < len(digests); i++ {
		if publicationDigestCID(digests[i-1]) >= publicationDigestCID(digests[i]) {
			t.Fatal("CID lexical order changed")
		}
	}
	for bit := 0; bit < 256; bit++ {
		var a, b [32]byte
		a[bit/8] = 1 << uint(bit%8)
		if publicationDigestLess(a, b) != (publicationDigestCID(a) < publicationDigestCID(b)) {
			t.Fatalf("bit boundary %d", bit)
		}
	}
}

func TestPublicationSelectionCost(t *testing.T) {
	if os.Getenv("SDN_SELECTION_COST") != "1" {
		t.Skip("explicit timing run")
	}
	f := &publicationRefFile{Path: "synthetic.sds", Tags: SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "batch"}}
	refs := make([]publicationRecordRef, 100000)
	for i := range refs {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], uint64(i))
		refs[i] = publicationRecordRef{Digest: sha256.Sum256(raw[:]), file: f}
	}
	s := &FlatSQLStore{basePath: t.TempDir(), publicationEntries: []publicationEntry{{Applied: true, ReadReady: true, Sequence: 1, Schema: "CAT.fbs", Tags: f.Tags, Refs: refs}}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	p, err := s.retainedSelectionLocked("CAT.fbs", "synthetic", "index", "batch", 1)
	ns := time.Since(start).Nanoseconds()
	runtime.ReadMemStats(&after)
	if err != nil || len(p.refs) != 100000 {
		t.Fatal("selection failed", err)
	}
	fmt.Printf("SELECTION_COST {\"nanoseconds\":%d,\"allocations\":%d,\"bytes\":%d}\n", ns, after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc)
}
