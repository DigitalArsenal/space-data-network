package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationBaseline is opt-in because its full data sets exceed CI's
// functional-test scope. JSON rows are flushed after each measurement.
func TestPublicationBaseline(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION_BASELINE") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION_BASELINE=1 for the full baseline")
	}
	resultPath := os.Getenv("SDN_E2E_PUBLICATION_RESULT")
	if resultPath == "" {
		t.Fatal("SDN_E2E_PUBLICATION_RESULT must name the JSON result file")
	}
	output, err := os.Create(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	encoder := json.NewEncoder(output)
	exe := publicationBinary(t)
	var rows []publicationMeasurement
	for round := 0; round < 3; round++ {
		t.Run(fmt.Sprintf("round-%d", round+1), func(t *testing.T) {
			a := newPublicationDaemon(t, exe, "baseline-producer", "", "", "keep-all")
			a.start(t)
			b := newPublicationDaemon(t, exe, "baseline-receiver", a.address, a.id, "keep-all")
			b.start(t)
			// The probe is an admin-only operation, independent of import trust.
			a.request(t, "PUT", "/api/v1/admin/peers/"+b.id+"/trust", "application/json", []byte(`{"trust_level":"admin"}`), http.StatusOK)
			b.kuboRPC(t, "config", url.Values{"arg": {"Experimental.Libp2pStreamMounting", "true"}, "json": {"true"}})
			record := func(row publicationMeasurement) {
				row.Round = round + 1
				rows = append(rows, row)
				if err := encoder.Encode(row); err != nil {
					t.Fatal(err)
				}
				if err := output.Sync(); err != nil {
					t.Fatal(err)
				}
			}
			row := measurePublication(t, a, b, "wire-only", "filler", func() (int64, int64) {
				n := publicationTransfer(t, b, a.id, syncwire.OpWireSpeedProbe, syncwire.QRPFields{MaxBytes: 256 << 20}, io.Discard)
				return n, 0
			})
			record(row)
			for _, data := range []struct {
				schema     string
				count      int
				makeRecord func(int) []byte
			}{
				{"OMM.fbs", 2_000_000, baselineOMM},
				{"OEM.fbs", 1024, baselineOEM},
			} {
				source := strings.TrimSuffix(data.schema, ".fbs")
				row = measurePublication(t, a, b, "direct-ingest", source, func() (int64, int64) {
					return baselineIngest(t, a, data.schema, source, "first", 0, data.count, data.makeRecord), int64(data.count)
				})
				record(row)
				// The existing publish API exports and announces in one call. Its
				// request-to-readable interval is explicitly labeled as an upper bound
				// on announcement-to-readable latency.
				var pub publicationResult
				row = measurePublication(t, a, b, "export-announce-import", source, func() (int64, int64) {
					pub = a.publish(t, data.schema, source, "first")
					baselineAwaitCount(t, b, data.schema, source, int64(data.count))
					return int64(len(data.makeRecord(0))) * int64(data.count), int64(data.count)
				})
				row.Note = "Includes export before announcement; polling resolution 1 s; byte count uses fixed-size generated frames"
				record(row)
				row = measurePublication(t, a, b, "download-only", source, func() (int64, int64) {
					return publicationTransfer(t, b, a.id, syncwire.OpReadShard, syncwire.QRPFields{SchemaName: data.schema, ProviderID: "publication-e2e", SourceName: source, BatchID: "first", CID: pub.ShardCID}, io.Discard), 0
				})
				row.Note = "One published shard via B's Kubo stream forwarder; already imported, so cache is warm; no import on this request"
				record(row)
				sizes := publicationStoreSizes(t, b.store)
				b.stop(t)
				start := time.Now()
				b.start(t)
				readySeconds := time.Since(start).Seconds()
				var point, window []float64
				row = measurePublication(t, a, b, "second-direct-ingest-with-reads", source, func() (int64, int64) {
					stop := make(chan struct{})
					done := make(chan struct{})
					var mu sync.Mutex
					go func() {
						defer close(done)
						client := http.Client{Timeout: 10 * time.Minute}
						for {
							select {
							case <-stop:
								return
							default:
							}
							p0 := time.Now()
							response, err := client.Get(b.base + "/api/v1/data/records/" + data.schema + "/" + storage.ComputeCID(data.makeRecord(0)[4:]))
							if err != nil {
								t.Error(err)
								return
							}
							io.Copy(io.Discard, response.Body)
							response.Body.Close()
							if response.StatusCode != http.StatusOK {
								t.Errorf("point read HTTP %d", response.StatusCode)
								return
							}
							p := time.Since(p0).Seconds()
							w0 := time.Now()
							request, _ := http.NewRequest("POST", b.base+"/api/v1/query", bytes.NewReader(baselineSQL(source)))
							request.Header.Set("Content-Type", publicationStreamType)
							response, err = client.Do(request)
							if err != nil {
								t.Error(err)
								return
							}
							io.Copy(io.Discard, response.Body)
							response.Body.Close()
							if response.StatusCode != http.StatusOK {
								t.Errorf("window SQL HTTP %d", response.StatusCode)
								return
							}
							mu.Lock()
							point = append(point, p)
							window = append(window, time.Since(w0).Seconds())
							mu.Unlock()
						}
					}()
					defer func() { close(stop); <-done }()
					n := baselineIngest(t, b, data.schema, source, "second", data.count, data.count, data.makeRecord)
					return n, int64(data.count)
				})
				row.StoreBytesByKind = sizes
				row.ReadySeconds = readySeconds
				row.PointReadSeconds = publicationPercentiles(point)
				row.WindowSQLSeconds = publicationPercentiles(window)
				record(row)
			}
		})
	}
	summary := map[string]any{"kind": "summary", "target_bytes_per_second": 250000000, "groups": publicationSummaries(rows)}
	if err := encoder.Encode(summary); err != nil {
		t.Fatal(err)
	}
}

type publicationMeasurement struct {
	Round                  int                `json:"round"`
	Operation              string             `json:"operation"`
	Dataset                string             `json:"dataset"`
	Bytes                  int64              `json:"bytes"`
	Records                int64              `json:"records"`
	Seconds                float64            `json:"seconds"`
	BytesPerSecond         float64            `json:"bytes_per_second"`
	RecordsPerSecond       float64            `json:"records_per_second"`
	Target                 int64              `json:"target_bytes_per_second"`
	ProducerCPUSeconds     float64            `json:"producer_cpu_seconds"`
	ReceiverCPUSeconds     float64            `json:"receiver_cpu_seconds"`
	ProducerSampledPeakRSS int64              `json:"producer_sampled_peak_rss_bytes"`
	ReceiverSampledPeakRSS int64              `json:"receiver_sampled_peak_rss_bytes"`
	LoadStart              string             `json:"uptime_start"`
	LoadEnd                string             `json:"uptime_end"`
	StoreBytesByKind       map[string]int64   `json:"receiver_store_bytes_by_kind,omitempty"`
	ReadySeconds           float64            `json:"receiver_ready_seconds,omitempty"`
	PointReadSeconds       map[string]float64 `json:"point_read_seconds,omitempty"`
	WindowSQLSeconds       map[string]float64 `json:"window_sql_seconds,omitempty"`
	Note                   string             `json:"note,omitempty"`
}

func measurePublication(t *testing.T, a, b *publicationDaemon, operation, dataset string, run func() (int64, int64)) publicationMeasurement {
	t.Helper()
	row := publicationMeasurement{Operation: operation, Dataset: dataset, Target: 250000000, LoadStart: publicationUptime(t)}
	cpuA, cpuB := publicationCPU(t, a), publicationCPU(t, b)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			for i, d := range []*publicationDaemon{a, b} {
				rss, _ := strconv.ParseInt(strings.TrimSpace(processField(d.cmd.Process.Pid, "rss")), 10, 64)
				rss *= 1024
				if i == 0 && rss > row.ProducerSampledPeakRSS {
					row.ProducerSampledPeakRSS = rss
				}
				if i == 1 && rss > row.ReceiverSampledPeakRSS {
					row.ReceiverSampledPeakRSS = rss
				}
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	var stopOnce sync.Once
	stopSampling := func() { stopOnce.Do(func() { close(done); <-stopped }) }
	defer stopSampling()
	start := time.Now()
	row.Bytes, row.Records = run()
	row.Seconds = time.Since(start).Seconds()
	row.ProducerCPUSeconds = publicationCPU(t, a) - cpuA
	row.ReceiverCPUSeconds = publicationCPU(t, b) - cpuB
	row.BytesPerSecond = float64(row.Bytes) / row.Seconds
	row.RecordsPerSecond = float64(row.Records) / row.Seconds
	row.LoadEnd = publicationUptime(t)
	stopSampling()
	return row
}

func publicationUptime(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("uptime").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
func publicationCPU(t *testing.T, d *publicationDaemon) float64 {
	t.Helper()
	value := strings.TrimSpace(processField(d.cmd.Process.Pid, "time"))
	parts := strings.Split(value, ":")
	total := 0.0
	for _, part := range parts {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			t.Fatalf("parse CPU time %q: %v", value, err)
		}
		total = total*60 + v
	}
	return total
}

func publicationTransfer(t *testing.T, b *publicationDaemon, target, op string, fields syncwire.QRPFields, dst io.Writer) int64 {
	t.Helper()
	port := freeLoopbackPort(t)
	address := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)
	b.kuboRPC(t, "p2p/forward", url.Values{"arg": {syncwire.ProtocolID(op), address, "/p2p/" + target}, "allow-custom-protocol": {"true"}})
	defer b.kuboRPC(t, "p2p/close", url.Values{"listen-address": {address}})
	connection, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(10 * time.Minute))
	fields.Kind = syncwire.QRPKindRequest
	if _, err := connection.Write(syncwire.BuildQRP(fields)); err != nil {
		t.Fatal(err)
	}
	header, err := syncwire.ReadFrame(connection, 0)
	if err != nil {
		t.Fatal(err)
	}
	q, err := syncwire.ParseQRP(header)
	if err != nil {
		t.Fatal(err)
	}
	if int8(q.KIND()) == syncwire.QRPKindError {
		t.Fatalf("%s: %s %s", op, q.ERROR_CODE(), q.MESSAGE())
	}
	n, err := io.Copy(dst, connection)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("transfer returned no bytes")
	}
	return n
}

func baselineIngest(t *testing.T, d *publicationDaemon, schema, source, batch string, offset, count int, makeRecord func(int) []byte) int64 {
	t.Helper()
	var total int64
	for i := 0; i < count; {
		var payload bytes.Buffer
		n := 0
		for i < count && payload.Len() < 4<<20 && n < 4096 {
			frame := makeRecord(offset + i)
			payload.Write(frame)
			total += int64(len(frame))
			n++
			i++
		}
		d.ingest(t, schema, source, batch, payload.Bytes(), n)
	}
	return total
}
func baselineOMM(i int) []byte {
	return sds.NewOMMBuilder().WithNoradCatID(uint32(1000000 + i)).WithObjectName(fmt.Sprintf("SYNTHETIC-%08d", i)).WithEpoch("2026-01-01T00:00:00Z").WithEpochTimestamp(1767225600).WithMeanMotion(15.5).Build()
}
func baselineOEM(i int) []byte {
	b := flatbuffers.NewBuilder(1 << 20)
	// 21,846 six-component position/velocity states: 1,048,608 data bytes.
	b.StartVector(8, 21846*6, 8)
	for j := 21846*6 - 1; j >= 0; j-- {
		b.PrependFloat64(float64(j%6) + float64(i)/10000)
	}
	states := b.EndVector(21846 * 6)
	start := b.CreateString("2026-01-01T00:00:00Z")
	stop := b.CreateString("2026-01-16T04:05:00Z")
	center := b.CreateString("EARTH")
	reference := b.CreateString("GCRF")
	// The generated Go table's builder is package-private. These slots are
	// ephemerisDataBlock's declared fields in OEM, with no schema changes.
	b.StartObject(24)
	b.PrependUOffsetTSlot(2, center, 0)
	b.PrependUOffsetTSlot(3, reference, 0)
	b.PrependUOffsetTSlot(7, start, 0)
	b.PrependUOffsetTSlot(10, stop, 0)
	b.PrependFloat64Slot(13, 60, 0)
	b.PrependUOffsetTSlot(15, states, 0)
	block := b.EndObject()
	OEM.OEMStartEPHEMERIS_DATA_BLOCKVector(b, 1)
	b.PrependUOffsetT(block)
	blocks := b.EndVector(1)
	origin := b.CreateString("SDN-TEST")
	id := b.CreateString(fmt.Sprintf("SYNTHETIC-%08d", i))
	OEM.OEMStart(b)
	OEM.OEMAddCCSDS_OEM_VERS(b, 3)
	OEM.OEMAddORIGINATOR(b, origin)
	OEM.OEMAddCREATION_DATE(b, start)
	OEM.OEMAddMESSAGE_ID(b, id)
	OEM.OEMAddEPHEMERIS_DATA_BLOCK(b, blocks)
	OEM.FinishSizePrefixedOEMBuffer(b, OEM.OEMEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func baselineAwaitCount(t *testing.T, d *publicationDaemon, schema, source string, want int64) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Hour)
	for time.Now().Before(deadline) {
		body := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema, ProviderID: "publication-e2e", SourceName: source, Limit: 1})
		raw := d.request(t, "POST", "/api/v1/data/scan", publicationStreamType, body, http.StatusOK)
		frames, err := syncwire.SplitFrames(raw)
		if err != nil || len(frames) == 0 {
			t.Fatal("invalid scan reply")
		}
		q, err := syncwire.ParseQRP(frames[0])
		if err != nil {
			t.Fatal(err)
		}
		if int64(q.TOTAL_COUNT()) == want {
			return
		}
		if int64(q.TOTAL_COUNT()) > want {
			t.Fatal("receiver count exceeds source count")
		}
		time.Sleep(time.Second)
	}
	t.Fatal("baseline import did not complete")
}
func baselineSQL(schema string) []byte {
	b := flatbuffers.NewBuilder(256)
	sql := "SELECT * FROM " + schema + " LIMIT 100"
	if schema == "OMM" {
		sql = "SELECT * FROM OMM WHERE EPOCH >= '2026-01-01T00:00:00Z' AND EPOCH < '2026-01-02T00:00:00Z' LIMIT 100"
	}
	text := b.CreateString(sql)
	QRP.QRPStart(b)
	QRP.QRPAddSQL(b, text)
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}
func publicationStoreSizes(t *testing.T, root string) map[string]int64 {
	t.Helper()
	sizes := map[string]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			kind := filepath.Ext(path)
			if kind == "" {
				kind = "no-extension"
			}
			sizes[kind] += info.Size()
			sizes["total"] += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sizes
}
func publicationPercentiles(values []float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	return map[string]float64{"samples": float64(len(values)), "median": values[len(values)/2], "p99": values[int(math.Ceil(float64(len(values))*.99))-1], "min": values[0], "max": values[len(values)-1]}
}
func publicationSummaries(rows []publicationMeasurement) map[string]map[string]float64 {
	groups := map[string][]float64{}
	for _, row := range rows {
		key := row.Operation + "/" + row.Dataset
		groups[key] = append(groups[key], row.BytesPerSecond)
	}
	result := map[string]map[string]float64{}
	for key, values := range groups {
		result[key] = publicationPercentiles(values)
	}
	return result
}
