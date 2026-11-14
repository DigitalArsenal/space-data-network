package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NEV"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// TestFormat4ConcurrentFlowLoad is an opt-in synthetic storage reproduction.
// SDN_F4_STRESS names the writer count; SDN_F4_SERIAL selects writes or all.
func TestFormat4ConcurrentFlowLoad(t *testing.T) {
	w, _ := strconv.Atoi(os.Getenv("SDN_F4_STRESS"))
	if w == 0 {
		t.Skip("set SDN_F4_STRESS to run the storage load")
	}
	duration := 5 * time.Minute
	if v := os.Getenv("SDN_F4_DURATION"); v != "" {
		var err error
		duration, err = time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	real := format4TestWasm(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache = filepath.Join(cache, "sdn-format4-test-aot")
	if os.Getenv("SDN_F4_PREWARM") == "1" {
		path, held, err := flatsqlrt.PrewarmP4ThreadsAOT(cache)
		t.Logf("prewarm path=%s existing=%v err=%v", path, held, err)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var traps, refused, calls, reads atomic.Int64
	var active sync.Map
	prev := format4OpenEngine
	format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
		opt.Wasm = real
		opt.AOTCacheDir = cache
		opt.CompileOnMiss = false
		failure := opt.OnFailure
		opt.OnFailure = func(err error) {
			traps.Add(1)
			t.Logf("TRAP %v", err)
			active.Range(func(k, v any) bool { t.Logf("inflight %v %v", k, v); return true })
			failure(err)
		}
		return prev(ctx, opt)
	}
	defer func() { format4OpenEngine = prev }()
	root := t.TempDir()
	s := openFormat4ForTest(t, root)
	defer s.Close()
	var mu sync.Mutex
	serial := os.Getenv("SDN_F4_SERIAL")
	call := func(id int, op string, write bool, fn func() error) error {
		if serial == "all" || serial == "writes" && write {
			mu.Lock()
			defer mu.Unlock()
		}
		active.Store(id, op)
		defer active.Delete(id)
		err := fn()
		if err != nil {
			n := refused.Add(1)
			if n <= 30 {
				t.Logf("REFUSED op=%s err=%v", op, err)
			}
		}
		return err
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()
	// Cancellation bounds the diagnostic even when the original mailbox stalls.
	timer := time.AfterFunc(duration, func() { close(stop); s.f4.cancel() })
	defer timer.Stop()
	for id := 0; id < w; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for round := 0; ; round++ {
				select {
				case <-stop:
					return
				default:
				}
				schema := "NEV.fbs"
				source := fmt.Sprintf("synthetic-state-%d", id)
				records := [][]byte{flowLoadRecord("NEV", id*1000000+round, 50*1024+round%40*1024)}
				ingest := func() error {
					tags := SourceTags{ProviderID: "synthetic", SourceName: source, BatchID: fmt.Sprintf("b-%d-%d", id, round)}
					if err := call(id, "count/"+schema, false, func() error {
						_, e := s.ReconcileSourceBatch(schema, tags.ProviderID, tags.SourceName, tags.BatchID, false)
						return e
					}); err != nil {
						return err
					}
					if err := call(id, "put/"+schema, true, func() error {
						_, e := s.StoreBatchWithSourceTags(schema, records, "synthetic-peer", nil, tags)
						return e
					}); err != nil {
						return err
					}
					calls.Add(1)
					return call(id, "supersede/"+schema, true, func() error {
						_, e := s.ReconcileSourceBatch(schema, tags.ProviderID, tags.SourceName, tags.BatchID, true)
						return e
					})
				}
				if err := ingest(); err != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				if round%16 == 0 {
					for _, typ := range []string{"OCM", "CAT"} {
						schema = typ + ".fbs"
						source = "synthetic-" + typ
						records = make([][]byte, 256)
						size := 128
						if typ == "OCM" {
							size = 9000
						}
						for j := range records {
							records[j] = flowLoadRecord(typ, id*1000000+round*256+j, size)
						}
						if err := ingest(); err != nil {
							break
						}
					}
				}
			}
		}(id)
	}
	for id := w; id < w+2; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = call(id, "index-page", false, func() error {
					_, _, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "CAT.fbs", Limit: 256})
					return err
				})
				reads.Add(1)
				time.Sleep(time.Millisecond)
			}
		}(id)
	}
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("RESULT W=%d R=2 serial=%q elapsed=%s traps=%d refused=%d writes=%d reads=%d writes_per_second=%.3f", w, serial, elapsed, traps.Load(), refused.Load(), calls.Load(), reads.Load(), float64(calls.Load())/elapsed.Seconds())
	if errors.Is(s.f4.ctx.Err(), context.Canceled) {
		t.Log("diagnostic duration reached; final refusals can be cancellation")
	}
}

func flowLoadRecord(typ string, n, size int) []byte {
	b := flatbuffers.NewBuilder(size + 256)
	text := b.CreateString(fmt.Sprintf("synthetic-%d-", n) + strings.Repeat("x", size))
	var root flatbuffers.UOffsetT
	switch typ {
	case "NEV":
		NEV.NEVStart(b)
		NEV.NEVAddNODE_ID(b, text)
		NEV.NEVAddGENERATED_AT_MS(b, int64(n))
		root = NEV.NEVEnd(b)
	case "CAT":
		CAT.CATStart(b)
		CAT.CATAddOBJECT_NAME(b, text)
		CAT.CATAddNORAD_CAT_ID(b, uint32(n+1))
		root = CAT.CATEnd(b)
	case "OCM":
		OCM.MetadataStart(b)
		OCM.MetadataAddOBJECT_NAME(b, text)
		meta := OCM.MetadataEnd(b)
		OCM.OCMStart(b)
		OCM.OCMAddMETADATA(b, meta)
		root = OCM.OCMEnd(b)
	}
	b.FinishSizePrefixedWithFileIdentifier(root, []byte("$"+typ))
	return append([]byte(nil), b.FinishedBytes()...)
}
