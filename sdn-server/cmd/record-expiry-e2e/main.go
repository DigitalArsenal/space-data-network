// record-expiry-e2e exercises the real format-4 engine and the daemon's
// retention entry point on three weather lanes. All data is synthetic.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}

func weather(id string, at time.Time) []byte {
	b := flatbuffers.NewBuilder(2048)
	field := b.CreateString(id)
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 1)
	WXF.WXFGridAddNLON(b, 1)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, field)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALID_TIME_MS(b, uint64(at.UnixMilli()))
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func run() error {
	n := flag.Int("records", 24000, "expired records, split across three weather lanes")
	flag.Parse()
	require(*n >= 6 && *n%3 == 0, "records must be a multiple of three, at least six")
	dir, err := os.MkdirTemp("", "record-expiry-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	os.Setenv("SDN_STORE_FORMAT", "4")
	cache := storage.EngineAOTCacheDir()
	_, _, err = flatsqlrt.PrewarmEngineAOT(cache)
	check(err)
	_, _, err = flatsqlrt.PrewarmP4ThreadsAOT(cache)
	check(err)
	ctx := context.Background()
	e, err := format4.Open(ctx, format4.Options{DataRoot: dir, Create: format4.CreateFresh, GseqFloor: 1, AOTCacheDir: cache})
	check(err)
	spec, err := format4.TypeSpecFor("WXF.fbs")
	check(err)
	check(e.RegisterType(spec))
	now := time.Now().Truncate(time.Second)
	cutoff := now.Add(-72 * time.Hour).Unix()
	lanes := []string{"goes-east", "goes-west", "himawari"}
	kept := map[string][]byte{}
	var probeCIDs []string
	seed := func(lane string, count int, ts int64) {
		for offset := 0; offset < count; offset += 500 {
			b := format4.Batch{Type: "WXF", Peer: "expiry-e2e", Tags: []format4.Tag{{Provider: "weather", Source: lane, Batch: "fixture"}}}
			for i := offset; i < min(offset+500, count); i++ {
				data := weather(fmt.Sprintf("%s:%d:%d", lane, ts, i), now.Add(-time.Duration(i%96)*time.Hour))
				c, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}).Sum(data)
				check(err)
				b.Records = append(b.Records, format4.In{CID: c.String(), Plain: data, TS: ts})
				if ts >= cutoff || lane == "other" {
					kept[c.String()] = data
				}
				if lane == "probe" {
					probeCIDs = append(probeCIDs, c.String())
				}
			}
			out, err := e.Put(ctx, b)
			check(err)
			for _, r := range out {
				require(r.Action == format4.ActNew, fmt.Sprintf("seed rejected: %+v", r))
			}
		}
	}
	for _, lane := range lanes {
		seed(lane, *n/3, cutoff-86400)
		seed(lane, 3, cutoff)
	}
	seed("other", 3, cutoff-86400)
	seed("probe", 16, cutoff-86400)
	check(e.Close(ctx))
	v, err := sds.NewValidator(nil)
	check(err)
	s, err := storage.NewFlatSQLStore(dir, v)
	check(err)
	defer func() {
		if s != nil {
			check(s.Close())
		}
	}()
	start := time.Now()
	for _, c := range probeCIDs {
		check(s.Delete("WXF.fbs", c))
	}
	fmt.Printf("single_delete records=%d total=%s per_delete=%s\n", len(probeCIDs), time.Since(start), time.Since(start)/time.Duration(len(probeCIDs)))
	start = time.Now()
	for _, lane := range lanes {
		q := storage.RecordExpiryQuery{SchemaName: "WXF.fbs", ProviderID: "weather", SourceName: lane, BeforeUnix: cutoff, Batch: 500}
		rows, count, err := s.RecordIndexPage(storage.RecordIndexPageQuery{SchemaName: q.SchemaName, SourceName: lane, ArrivedBefore: &cutoff, Limit: 1})
		check(err)
		require(len(rows) == 1 && count == int64(*n/3), fmt.Sprintf("old count %s=%d", lane, count))
		st := time.Now()
		r, err := s.ExpireRecords(q)
		check(err)
		fmt.Printf("expiry lane=%s deleted=%d missing=%d capped=%t duration=%s per_delete=%s\n", lane, r.Deleted, r.Missing, r.Capped, time.Since(st), time.Since(st)/time.Duration(max(r.Deleted, 1)))
		require(r.Deleted == *n/3 && r.Missing == 0 && !r.Capped, fmt.Sprintf("bad expiry: %+v", r))
		r, err = s.ExpireRecords(q)
		check(err)
		require(r.Deleted == 0 && !r.Capped, "second pass was not empty")
	}
	fmt.Printf("pass records=%d duration=%s\n", *n, time.Since(start))
	check(s.Close())
	s = nil
	s, err = storage.NewFlatSQLStore(dir, v)
	check(err)
	count, err := s.Count("WXF.fbs")
	check(err)
	require(count == int64(len(kept)), fmt.Sprintf("reopened count=%d want=%d", count, len(kept)))
	for c, data := range kept {
		r, err := s.GetRawRecord("WXF.fbs", c)
		check(err)
		require(bytes.Equal(r.Data, data), "survivor bytes changed")
	}
	fmt.Printf("PASS: %d expired; %d cutoff/other-lane survivors byte-identical after reopen\n", *n, len(kept))
	return nil
}

func main() { check(run()) }
