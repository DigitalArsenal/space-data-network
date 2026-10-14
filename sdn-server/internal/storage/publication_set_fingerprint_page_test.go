package storage

import (
	"testing"
)

// The publication-set fingerprint reads a lane a page at a time (multi-body
// round 2, coordinator D3: one window over 895,910 asteroid rows took the
// heap to ~3.4 GB). Paged or whole, the fingerprint and the count are the
// same.
func TestPublicationSetFingerprintIsTheSamePaged(t *testing.T) {
	store := openFormat4ForTest(t, t.TempDir())
	defer store.Close()
	if _, ok := store.rb.(format4Backend); !ok {
		t.Fatalf("the paged read is format 4's; this store is %T", store.rb)
	}
	const n = 250
	const batch = "paged-fingerprint"
	if _, err := store.StoreBatchWithSourceTags("IQC.fbs", identityTestBatch(n, "2026-10-13T00:00:00Z"), "module:sigmf", nil, identityTestTags(batch)); err != nil {
		t.Fatal(err)
	}
	whole, count, err := store.DatasetPublicationSetFingerprint("IQC.fbs", identityTestProvider, identityTestSource, batch)
	if err != nil || count != n {
		t.Fatalf("whole: count %d err %v", count, err)
	}
	saved := publicationSetFingerprintPage
	defer func() { publicationSetFingerprintPage = saved }()
	for _, page := range []int64{60, 100, 250, 251} {
		publicationSetFingerprintPage = page
		got, gotCount, err := store.PagedDatasetPublicationSetFingerprint("IQC.fbs", identityTestProvider, identityTestSource, batch)
		if err != nil || gotCount != n || got != whole {
			t.Fatalf("page %d: count %d fingerprint %s (whole %s) err %v", page, gotCount, got, whole, err)
		}
	}
}
