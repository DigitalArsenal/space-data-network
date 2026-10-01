package format4test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/internal/cidv1"
)

// Conformance is the shared suite (contract §5.2): every subtest opens a
// fresh store with newAPI (which registers its own cleanup) and checks one
// part of §3.5-§3.8 that both the Fake and the engine must answer alike. It
// uses real OMM FlatBuffers, so the engine's own extraction runs.
func Conformance(t *testing.T, newAPI func(t *testing.T) format4.API) {
	for _, c := range conformanceCases {
		t.Run(c.name, func(t *testing.T) {
			api := newAPI(t)
			c.run(t, api)
		})
	}
}

type conformanceCase struct {
	name string
	run  func(t *testing.T, api format4.API)
}

// Two producers and one lane, at fixed times (the engine's clock is never
// read: every ingest call carries At).
const (
	peerA = "12D3KooWConformanceProducerA"
	peerB = "12D3KooWConformanceProducerB"
	at0   = int64(1790000000)
)

var (
	laneGP  = format4.Tag{Provider: "celestrak", Source: "celestrak-gp", SourceURL: "https://example.test/gp", Batch: "b1"}
	laneGP2 = format4.Tag{Provider: "celestrak", Source: "celestrak-gp", SourceURL: "https://example.test/gp2", Batch: "b2"}
	laneSup = format4.Tag{Provider: "celestrak", Source: "celestrak-supgp", Batch: "s1"}
)

// OMMRecord builds a deterministic OMM record (size-prefixed, as the SDN
// builders emit it).
func OMMRecord(norad uint32, objectID, epoch string) []byte {
	return sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectID(objectID).WithObjectName(fmt.Sprintf("SAT-%d", norad)).
		WithEpoch(epoch).WithCreationDate("2026-09-30T00:00:00Z").Build()
}

// In returns a PUT record of plain at source time ts.
func In(plain []byte, ts int64) format4.In {
	return format4.In{CID: cidv1.Of(plain), Plain: plain, TS: ts}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func registerOMM(t *testing.T, api format4.API) {
	t.Helper()
	spec, err := format4.TypeSpecFor("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.RegisterType(spec); err != nil {
		t.Fatalf("RegisterType: %v", err)
	}
}

// fixture is three OMM records: 25544 (two epochs) and 43013.
type fixture struct {
	iss1, iss2, sat []byte
	cids            []string
	seqs            []int64
}

func loadFixture(t *testing.T, api format4.API) fixture {
	t.Helper()
	registerOMM(t, api)
	fx := fixture{
		iss1: OMMRecord(25544, "1998-067A", "2026-09-01T00:00:00Z"),
		iss2: OMMRecord(25544, "1998-067A", "2026-09-02T00:00:00Z"),
		sat:  OMMRecord(43013, "2017-073A", "2026-08-15T12:00:00Z"),
	}
	ins := []format4.In{In(fx.iss1, at0-30), In(fx.iss2, at0-20), In(fx.sat, at0-10)}
	out := mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP}, At: at0, Records: ins})
	for i, o := range out {
		if o.Action != format4.ActNew || o.Seq <= 0 {
			t.Fatalf("record %d: %+v, want NEW", i, o)
		}
		if i > 0 && o.Seq <= out[i-1].Seq {
			t.Fatalf("seqs not ascending: %+v", out)
		}
		fx.cids = append(fx.cids, ins[i].CID)
		fx.seqs = append(fx.seqs, o.Seq)
	}
	return fx
}

func mustPut(t *testing.T, api format4.API, b format4.Batch) []format4.Outcome {
	t.Helper()
	out, err := api.Put(ctxT(t), b)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(out) != len(b.Records) {
		t.Fatalf("Put returned %d outcomes for %d records", len(out), len(b.Records))
	}
	return out
}

func cids(recs []format4.Rec) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.CID
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var conformanceCases = []conformanceCase{
	{"unregistered type", func(t *testing.T, api format4.API) {
		_, err := api.Get(ctxT(t), "OMM", []string{cidv1.Of([]byte("x"))}, false, false)
		if !errors.Is(err, format4.ErrNoType) {
			t.Fatalf("Get on an unregistered type: %v, want ErrNoType", err)
		}
		registerOMM(t, api)
		registerOMM(t, api) // identical bytes: a no-op
	}},
	{"ingest actions", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		// The same records from another producer are copies with the holder's seqs.
		out := mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerB, Tags: []format4.Tag{laneSup}, At: at0 + 1,
			Records: []format4.In{In(fx.iss1, at0+100), In(fx.sat, at0+100)}})
		if out[0] != (format4.Outcome{Action: format4.ActCopy, Seq: fx.seqs[0]}) || out[1] != (format4.Outcome{Action: format4.ActCopy, Seq: fx.seqs[2]}) {
			t.Fatalf("copies: %+v (seqs %v)", out, fx.seqs)
		}
		// Again from the holder: the same tag is a DUP, a new batch a RETAG.
		out = mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP}, At: at0 + 2,
			Records: []format4.In{In(fx.iss1, at0)}})
		if out[0] != (format4.Outcome{Action: format4.ActDup, Seq: fx.seqs[0]}) {
			t.Fatalf("dup: %+v", out)
		}
		out = mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP2}, At: at0 + 3,
			Records: []format4.In{In(fx.iss1, at0)}})
		if out[0] != (format4.Outcome{Action: format4.ActRetag, Seq: fx.seqs[0]}) {
			t.Fatalf("retag: %+v", out)
		}
		// A new record gets a seq above every other.
		n := OMMRecord(48274, "2021-035A", "2026-09-03T00:00:00Z")
		out = mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP}, At: at0 + 4,
			Records: []format4.In{In(n, at0)}})
		if out[0].Action != format4.ActNew || out[0].Seq <= fx.seqs[2] {
			t.Fatalf("new after copies: %+v", out)
		}
	}},
	{"per-record rejects", func(t *testing.T, api format4.API) {
		registerOMM(t, api)
		good := OMMRecord(25544, "1998-067A", "2026-09-01T00:00:00Z")
		mismatch := In(good, at0)
		mismatch.CID = cidv1.Of([]byte("other bytes"))
		badForm := In(good, at0)
		badForm.CID = "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
		out := mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP}, At: at0,
			Records: []format4.In{badForm, mismatch, In(good, at0)}})
		if out[0].Action != format4.ActRejected || out[0].Reject != format4.RejectCIDForm {
			t.Fatalf("CID form: %+v", out[0])
		}
		if out[1].Action != format4.ActRejected || out[1].Reject != format4.RejectCIDMismatch {
			t.Fatalf("CID mismatch: %+v", out[1])
		}
		if out[2].Action != format4.ActNew {
			t.Fatalf("the good record beside rejects: %+v", out[2])
		}
		out = mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{{Provider: "p"}}, At: at0,
			Records: []format4.In{In(OMMRecord(1, "x", "2026-09-01T00:00:00Z"), at0)}})
		if out[0].Action != format4.ActRejected || out[0].Reject != format4.RejectTag {
			t.Fatalf("tag without a source: %+v", out[0])
		}
	}},
	{"get", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerB, Tags: []format4.Tag{laneSup}, At: at0 + 1,
			Records: []format4.In{In(fx.iss1, at0+100)}})
		miss := cidv1.Of([]byte("not stored"))
		recs, err := api.Get(ctxT(t), "OMM", []string{fx.cids[2], miss, fx.cids[0]}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if !eqStrings(cids(recs), []string{fx.cids[2], fx.cids[0]}) {
			t.Fatalf("Get order/miss: %v", cids(recs))
		}
		r := recs[1]
		if r.Seq != fx.seqs[0] || string(r.Data) != string(fx.iss1) || r.Len != int64(len(fx.iss1)) || r.TS != at0-30 ||
			r.Producer != ProducerToken(peerA) || r.Peer != peerA || !r.HasEpoch || r.Tag != nil || r.Key != "25544" {
			t.Fatalf("Get row: %+v", r)
		}
		all, err := api.Get(ctxT(t), "OMM", []string{fx.cids[0]}, true, false)
		if err != nil || len(all) != 2 || all[0].Producer != ProducerToken(peerA) || all[1].Producer != ProducerToken(peerB) ||
			all[0].Seq != all[1].Seq || all[0].Data != nil {
			t.Fatalf("Get every copy: %+v %v", all, err)
		}
		if all[1].TS != at0-30 {
			t.Fatalf("a copy keeps the holder's ts: %+v", all[1])
		}
		none, err := api.Get(ctxT(t), "OMM", []string{miss}, false, false)
		if err != nil || len(none) != 0 {
			t.Fatalf("a miss is no row and no error: %+v %v", none, err)
		}
	}},
	{"tags", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP2}, At: at0 + 5,
			Records: []format4.In{In(fx.iss1, at0)}})
		rows, err := api.Tags(ctxT(t), "OMM", []string{fx.cids[0]})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].Batch != "b1" || rows[0].At != at0 || rows[1].Batch != "b2" || rows[1].At != at0+5 ||
			rows[0].SourceURL != laneGP.SourceURL || rows[1].SourceURL != laneGP2.SourceURL || rows[0].Seq != fx.seqs[0] {
			t.Fatalf("Tags: %+v", rows)
		}
		// A DUP with a new URL updates the URL, not the time.
		moved := laneGP
		moved.SourceURL = "https://example.test/moved"
		mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{moved}, At: at0 + 9,
			Records: []format4.In{In(fx.iss1, at0)}})
		rows, err = api.Tags(ctxT(t), "OMM", []string{fx.cids[0]})
		if err != nil || rows[0].SourceURL != moved.SourceURL || rows[0].At != at0 {
			t.Fatalf("Tags after a URL change: %+v %v", rows, err)
		}
	}},
	{"scan", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		recs, err := api.Scan(ctxT(t), format4.Query{Type: "OMM"})
		if err != nil || !eqStrings(cids(recs), fx.cids) {
			t.Fatalf("Scan: %v %v", cids(recs), err)
		}
		if recs[0].Tag == nil || recs[0].Tag.Batch != "b1" || recs[0].Tag.At != at0 {
			t.Fatalf("Scan tag: %+v", recs[0].Tag)
		}
		recs, err = api.Scan(ctxT(t), format4.Query{Type: "OMM", Order: format4.OrderSeqDesc, Limit: 2})
		if err != nil || !eqStrings(cids(recs), []string{fx.cids[2], fx.cids[1]}) {
			t.Fatalf("Scan desc limit: %v %v", cids(recs), err)
		}
		recs, err = api.Scan(ctxT(t), format4.Query{Type: "OMM", SeqAfter: fx.seqs[0]})
		if err != nil || !eqStrings(cids(recs), fx.cids[1:]) {
			t.Fatalf("Scan after: %v %v", cids(recs), err)
		}
		recs, err = api.Scan(ctxT(t), format4.Query{Type: "OMM", Lane: format4.LaneFilter{Source: "celestrak-supgp"}})
		if err != nil || len(recs) != 0 {
			t.Fatalf("Scan by an empty lane: %v %v", cids(recs), err)
		}
		recs, err = api.Scan(ctxT(t), format4.Query{Type: "OMM", Preds: []format4.Pred{{Field: format4.FieldCol0, Op: format4.OpEq,
			Values: []format2.Cell{format2.Int(25544)}}}})
		if err != nil || !eqStrings(cids(recs), fx.cids[:2]) {
			t.Fatalf("Scan COL0 = 25544: %v %v", cids(recs), err)
		}
	}},
	{"head", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		h, err := api.Head(ctxT(t), format4.Query{Type: "OMM"})
		if err != nil {
			t.Fatal(err)
		}
		total := int64(len(fx.iss1) + len(fx.iss2) + len(fx.sat))
		if h.N != 3 || h.Bytes != total || h.MaxSeq != fx.seqs[2] || h.MaxTS != at0-10 || h.MaxAt != at0 || h.Through < fx.seqs[2] {
			t.Fatalf("Head: %+v", h)
		}
		// A byte cap walks WINDOW order (epoch desc): iss2, iss1, sat.
		h, err = api.Head(ctxT(t), format4.Query{Type: "OMM", ByteCap: int64(len(fx.iss2) + len(fx.iss1))})
		if err != nil || h.N != 2 || !h.More {
			t.Fatalf("Head with a byte cap: %+v %v", h, err)
		}
	}},
	{"window and index page", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		recs, err := api.Window(ctxT(t), format4.Query{Type: "OMM"})
		if err != nil || !eqStrings(cids(recs), []string{fx.cids[1], fx.cids[0], fx.cids[2]}) {
			t.Fatalf("Window (epoch desc): %v %v", cids(recs), err)
		}
		rows, err := api.IndexPage(ctxT(t), format4.Query{Type: "OMM", Limit: 2})
		if err != nil || len(rows) != 2 || rows[0].CID != fx.cids[1] || rows[0].Col0 == nil || *rows[0].Col0 != 25544 ||
			rows[0].Epoch == nil {
			t.Fatalf("IndexPage: %+v %v", rows, err)
		}
	}},
	{"epoch profiles", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Unix()
		recs, err := api.Epoch(ctxT(t), format4.EpochQuery{Query: format4.Query{Type: "OMM"}, Profile: format4.EpochAsOf, At: at})
		if err != nil || !eqStrings(cids(recs), []string{fx.cids[0], fx.cids[2]}) || recs[0].Key != "25544" {
			t.Fatalf("as_of: %+v %v", recs, err)
		}
		recs, err = api.Epoch(ctxT(t), format4.EpochQuery{Query: format4.Query{Type: "OMM"}, Profile: format4.EpochForward, At: at})
		if err != nil || !eqStrings(cids(recs), []string{fx.cids[1]}) {
			t.Fatalf("forward: %v %v", cids(recs), err)
		}
		recs, err = api.Epoch(ctxT(t), format4.EpochQuery{Query: format4.Query{Type: "OMM"}, Profile: format4.EpochNearest, At: at})
		if err != nil || !eqStrings(cids(recs), []string{fx.cids[0], fx.cids[2]}) {
			t.Fatalf("nearest (a tie goes to the earlier epoch): %v %v", cids(recs), err)
		}
		n, err := api.EpochCount(ctxT(t), format4.EpochQuery{Query: format4.Query{Type: "OMM"}, Profile: format4.EpochWindow})
		if err != nil || n != 3 {
			t.Fatalf("window count: %d %v", n, err)
		}
		cov, err := api.Coverage(ctxT(t), format4.EpochQuery{Query: format4.Query{Type: "OMM"}})
		if err != nil || len(cov) != 3 || cov[0].Day != "2026-08-15" || cov[2].Day != "2026-09-02" || cov[0].N != 1 {
			t.Fatalf("coverage: %+v %v", cov, err)
		}
	}},
	{"supersede", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerA, Tags: []format4.Tag{laneGP2}, At: at0 + 1,
			Records: []format4.In{In(fx.iss1, at0)}})
		dry, err := api.Supersede(ctxT(t), "OMM", "celestrak", "celestrak-gp", "b2", false)
		if err != nil || dry.TagsDeleted != 3 || dry.RecordsDeleted != 2 {
			t.Fatalf("count: %+v %v", dry, err)
		}
		if h, _ := api.Head(ctxT(t), format4.Query{Type: "OMM"}); h.N != 3 {
			t.Fatalf("a count changed the store: %+v", h)
		}
		res, err := api.Supersede(ctxT(t), "OMM", "celestrak", "celestrak-gp", "b2", true)
		if err != nil || res.TagsDeleted != dry.TagsDeleted || res.RecordsDeleted != dry.RecordsDeleted {
			t.Fatalf("apply: %+v %v", res, err)
		}
		recs, err := api.Scan(ctxT(t), format4.Query{Type: "OMM"})
		if err != nil || !eqStrings(cids(recs), fx.cids[:1]) {
			t.Fatalf("after supersede: %v %v", cids(recs), err)
		}
		lanes, err := api.Lanes(ctxT(t), "OMM")
		if err != nil || len(lanes) != 1 || lanes[0].Batch != "b2" || lanes[0].Records != 1 {
			t.Fatalf("lanes back to the live set: %+v %v", lanes, err)
		}
	}},
	{"delete", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerB, Tags: []format4.Tag{laneSup}, At: at0 + 1,
			Records: []format4.In{In(fx.iss1, at0)}})
		n, err := api.Delete(ctxT(t), "OMM", []string{fx.cids[0], cidv1.Of([]byte("absent"))})
		if err != nil || n != 2 {
			t.Fatalf("Delete: %d %v", n, err)
		}
		recs, err := api.Get(ctxT(t), "OMM", []string{fx.cids[0]}, true, false)
		if err != nil || len(recs) != 0 {
			t.Fatalf("after delete: %+v %v", recs, err)
		}
	}},
	{"summaries", func(t *testing.T, api format4.API) {
		fx := loadFixture(t, api)
		mustPut(t, api, format4.Batch{Type: "OMM", Peer: peerB, Tags: []format4.Tag{laneSup}, At: at0 + 1,
			Records: []format4.In{In(fx.iss1, at0)}})
		types, err := api.Types(ctxT(t))
		if err != nil || len(types) != 1 {
			t.Fatalf("Types: %+v %v", types, err)
		}
		ts := types[0]
		if ts.Type != "OMM" || ts.Records != 3 || ts.Copies != 4 || ts.Bytes != int64(len(fx.iss1)+len(fx.iss2)+len(fx.sat)) ||
			ts.CopyBytes != ts.Bytes+int64(len(fx.iss1)) || ts.MaxSeq != fx.seqs[2] || ts.MinEpoch == nil || ts.MaxEpoch == nil {
			t.Fatalf("type summary: %+v", ts)
		}
		parts, err := api.Partitions(ctxT(t))
		if err != nil || len(parts) != 2 || parts[0].Producer != ProducerToken(peerA) || parts[0].Records != 3 ||
			parts[1].Producer != ProducerToken(peerB) || parts[1].Records != 1 || parts[1].Peer != peerB {
			t.Fatalf("Partitions: %+v %v", parts, err)
		}
		lanes, err := api.Lanes(ctxT(t), "")
		if err != nil || len(lanes) != 2 || lanes[0].Source != "celestrak-gp" || lanes[0].Records != 3 || lanes[0].First != at0 ||
			lanes[0].SourceURL != laneGP.SourceURL || lanes[1].Source != "celestrak-supgp" || lanes[1].Records != 1 {
			t.Fatalf("Lanes: %+v %v", lanes, err)
		}
	}},
}
