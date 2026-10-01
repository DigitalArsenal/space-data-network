package format4proof

import (
	"context"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/format4test"
)

// The format-4 digest and verify, on the contract's executable reference.

func fakeWithOMM(t *testing.T) *format4test.Fake {
	t.Helper()
	f := format4test.New()
	spec, err := format4.TypeSpecFor("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	return f
}

func putOMM(t *testing.T, f format4.API, peer string, tag format4.Tag, recs [][]byte) {
	t.Helper()
	b := format4.Batch{Type: "OMM", Peer: peer, Tags: []format4.Tag{tag}, At: 1790000000}
	for _, r := range recs {
		b.Records = append(b.Records, format4.In{CID: storage.ComputeCID(r), Plain: r, TS: 1790000000})
	}
	out, err := f.Put(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range out {
		if o.Action == format4.ActRejected {
			t.Fatalf("record %d rejected: %s", i, format4.RejectReason(o.Reject))
		}
	}
}

func TestDigestAPIOnTheFake(t *testing.T) {
	ctx := context.Background()
	f := fakeWithOMM(t)
	recs := CrashRecords(1, 0, 300)
	gp := format4.Tag{Provider: FixtureProvider, Source: "celestrak-gp", Batch: "b1", SourceURL: "https://example.test/gp"}
	putOMM(t, f, "source:celestrak", gp, recs)
	// A second producer holds the first 100 again (copies), under its own lane.
	other := format4.Tag{Provider: FixtureProvider, Source: "mirror", Batch: "m1"}
	putOMM(t, f, "source:mirror", other, recs[:100])

	got, err := DigestAPI(ctx, f, []string{"OMM.fbs"})
	if err != nil {
		t.Fatal(err)
	}
	d := got["OMM.fbs"]
	if d.Records != 300 || d.Copies != 400 {
		t.Fatalf("records %d copies %d, want 300 and 400", d.Records, d.Copies)
	}
	var want Multiset
	var bytes int64
	for _, r := range recs {
		want.Add(storage.ComputeCID(r), digest(r))
		bytes += int64(len(r))
	}
	if d.Data != want.String() || d.Bytes != bytes {
		t.Fatalf("data %s (%d B), want %s (%d B)", d.Data, d.Bytes, want.String(), bytes)
	}
	var tags Multiset
	for i, r := range recs {
		cid := storage.ComputeCID(r)
		tags.Add(tagFields(cid, storage.SourceTags{ProviderID: gp.Provider, SourceName: gp.Source, SourceURL: gp.SourceURL, BatchID: gp.Batch})...)
		if i < 100 {
			tags.Add(tagFields(cid, storage.SourceTags{ProviderID: other.Provider, SourceName: other.Source, BatchID: other.Batch})...)
		}
	}
	if d.Tags != tags.String() {
		t.Fatalf("tags %s, want %s", d.Tags, tags.String())
	}
	if len(d.Partitions) != 2 {
		t.Fatalf("partitions %v", d.Partitions)
	}
	// A deletion shows in every part of the digest.
	if _, err := f.Delete(ctx, "OMM", []string{storage.ComputeCID(recs[200])}); err != nil {
		t.Fatal(err)
	}
	after, err := DigestAPI(ctx, f, []string{"OMM.fbs"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := DigestDiff(d, after["OMM.fbs"]); len(diff) < 4 {
		t.Fatalf("a delete must change records, copies, bytes, data and tags: %v", diff)
	}
	probs, err := VerifyAPI(ctx, f)
	if err != nil || len(probs) != 0 {
		t.Fatalf("verify: %v %v", probs, err)
	}
}
