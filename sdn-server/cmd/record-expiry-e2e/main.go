// record-expiry-e2e exercises the real format-4 engine and the daemon's
// retention entry point on three weather lanes. All data is synthetic.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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
	logs := &logCapture{}
	logging.SetPrimaryCore(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(logs), zapcore.InfoLevel))
	logging.SetAllLoggers(logging.LevelInfo)
	n := flag.Int("records", 24000, "expired records, split across three weather lanes")
	flag.Parse()
	require(*n >= 6 && *n%3 == 0, "records must be a multiple of three, at least six")
	require(wasmrt.SubstrateStatus().Patched(), "the retention E2E requires the patched release runtime")
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
	seed("capped", 7, cutoff-86400)
	// Delete's native count is copies, including two peers holding one CID.
	copyData := weather("two-peer-copies", now)
	copyCID, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}).Sum(copyData)
	check(err)
	for _, peer := range []string{"first-peer", "second-peer"} {
		_, err := e.Put(ctx, format4.Batch{Type: "WXF", Peer: peer,
			Tags:    []format4.Tag{{Provider: "weather", Source: "copies", Batch: "fixture"}},
			Records: []format4.In{{CID: copyCID.String(), Plain: copyData, TS: cutoff - 86400}}})
		check(err)
	}
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
	q := storage.RecordExpiryQuery{SchemaName: "WXF.fbs", SourceName: "capped", BeforeUnix: cutoff, Batch: 2, MaxDeletes: 3}
	r, err := s.ExpireRecords(q)
	check(err)
	require(r.Deleted == 3 && r.Capped, fmt.Sprintf("partial cap: %+v", r))
	q.MaxDeletes, q.MaxDuration = 4, time.Nanosecond
	r, err = s.ExpireRecords(q)
	check(err)
	require(r.Deleted == 0 && r.Capped, fmt.Sprintf("time cap: %+v", r))
	q.MaxDuration = 0
	r, err = s.ExpireRecords(q)
	check(err)
	require(r.Deleted == 4 && !r.Capped, fmt.Sprintf("exact cap: %+v", r))
	r, err = s.ExpireRecords(storage.RecordExpiryQuery{SchemaName: "WXF.fbs", SourceName: "copies", BeforeUnix: cutoff, MaxDeletes: 1})
	check(err)
	require(r.Deleted == 2 && r.Missing == 0 && !r.Capped, fmt.Sprintf("copy count: %+v", r))
	_, err = s.ExpireRecords(storage.RecordExpiryQuery{SchemaName: "WXF.fbs", BeforeUnix: cutoff})
	require(err != nil, "an unscoped expiry was accepted")
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
	check(s.Close())
	s = nil
	checkScheduler()
	for _, want := range []string{"Record expiry start", "Record expiry end", "deleted=8000", "missing=0", "capped=true", "duration=", "skipped: previous run still open", "cron \\\"expire\\\" start", "cron \\\"expire\\\" end"} {
		if want == "deleted=8000" {
			want = fmt.Sprintf("deleted=%d", *n/3)
		}
		require(strings.Contains(logs.String(), want), "missing Info log: "+want)
	}
	fmt.Println("PASS: work/time caps, exact boundary, lane guard, Info start/end/skip, nonoverlap and shutdown drain")
	return nil
}

// Exercise the public manager lifecycle with a deliberately open invocation.
// This proves the timer continues observing ticks and Close joins its worker.
type slowCron struct {
	calls   atomic.Int32
	started chan struct{}
	stopped atomic.Bool
}

func (p *slowCron) ID() string                                          { return "retention-e2e" }
func (p *slowCron) Start(context.Context, plugins.RuntimeContext) error { return nil }
func (p *slowCron) RegisterRoutes(*http.ServeMux)                       {}
func (p *slowCron) Close() error {
	require(p.stopped.Load(), "plugin closed before cron drained")
	return nil
}
func (p *slowCron) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{Method: "expire", DefaultInterval: "1s"}}
}
func (p *slowCron) CronFirstRunDelay(string, time.Duration) time.Duration { return time.Millisecond }
func (p *slowCron) InvokeCron(ctx context.Context, _ string, _ []byte) ([]byte, error) {
	require(p.calls.Add(1) == 1, "overlapping or queued invocation")
	close(p.started)
	<-ctx.Done()
	p.stopped.Store(true)
	return nil, ctx.Err()
}
func checkScheduler() {
	m := plugins.New()
	p := &slowCron{started: make(chan struct{})}
	check(m.Register(p))
	check(m.StartAll(context.Background(), plugins.RuntimeContext{}))
	select {
	case <-p.started:
	case <-time.After(10 * time.Second):
		panic("cron did not start")
	}
	time.Sleep(2200 * time.Millisecond)
	check(m.Close())
	require(p.calls.Load() == 1, "cron overlapped")
}

type logCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *logCapture) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func main() { check(run()) }
