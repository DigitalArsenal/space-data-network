package api

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/logservice"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Batch publish throughput through the real HTTP handler over a real store.
// Records are unique per NORAD so content-addressed dedupe cannot short-circuit
// the write path — this measures the fresh-record cost, the one that matters.
//
//	go test ./internal/api/ -run '^$' -bench BenchmarkBatchPublish -benchtime 3x
func BenchmarkBatchPublish(b *testing.B) {
	for _, n := range []int{500, 2000} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			mux := newBenchMux(b, false)
			var norad uint32 = 100000
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				body := benchBatchBody(n, &norad)
				b.StartTimer()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/data/publish/batch/OMM.fbs", bytes.NewReader(body))
				req.RemoteAddr = "127.0.0.1:54321"
				mux.ServeHTTP(rec, req)
				if rec.Code != http.StatusCreated {
					b.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
			}
			b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "records/s")
		})
	}
}

// The production shape: a PLG log service is attached, which appends one
// hash-chained entry (its own store write) per published record.
//
//	go test ./internal/api/ -run '^$' -bench BenchmarkBatchPublishWithPLG -benchtime 1x
func BenchmarkBatchPublishWithPLG(b *testing.B) {
	for _, n := range []int{500} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			mux := newBenchMux(b, true)
			var norad uint32 = 100000
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				body := benchBatchBody(n, &norad)
				b.StartTimer()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/data/publish/batch/OMM.fbs", bytes.NewReader(body))
				req.RemoteAddr = "127.0.0.1:54321"
				mux.ServeHTTP(rec, req)
				if rec.Code != http.StatusCreated {
					b.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
			}
			b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "records/s")
		})
	}
}

func newBenchMux(b *testing.B, withPLG bool) *http.ServeMux {
	b.Helper()
	dir, err := os.MkdirTemp("", "publish-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		b.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "db"), validator)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	cfg := &config.PublishingConfig{
		Enabled:           true,
		DefaultQuotaBytes: 1 << 40, // measure throughput, not the quota
		MaxRecordBytes:    1 << 20,
		MinTrustLevel:     "untrusted",
	}
	h := NewPublishHandler(store, validator, NewStorageQuotaManager(store, cfg.DefaultQuotaBytes), cfg, nil)
	if withPLG {
		key, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			b.Fatal(err)
		}
		h.SetLogService(logservice.NewService(store, key, "bench-peer"))
	}
	mux := http.NewServeMux()
	h.RegisterLocalLaneRoutes(mux, localLanePrincipal)
	return mux
}

// benchBatchBody frames n unique OMMs as the server expects: u32 LE length prefix.
func benchBatchBody(n int, norad *uint32) []byte {
	var buf bytes.Buffer
	var lenBuf [4]byte
	for i := 0; i < n; i++ {
		*norad++
		rec := sds.NewOMMBuilder().
			WithNoradCatID(*norad).
			WithObjectName(fmt.Sprintf("BENCH-%d", *norad)).
			WithEpoch("2026-08-27T12:00:00Z").
			Build()
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(rec)))
		buf.Write(lenBuf[:])
		buf.Write(rec)
	}
	return buf.Bytes()
}
