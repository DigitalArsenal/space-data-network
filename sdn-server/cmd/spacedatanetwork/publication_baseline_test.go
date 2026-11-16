package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	smallCount, largeCount, repetitions := 2_000_000, 1024, 3
	smoke := os.Getenv("SDN_E2E_PUBLICATION_BASELINE_SMOKE") == "1"
	if smoke {
		smallCount, largeCount, repetitions = 2048, 4, 1
	}
	var rows []publicationMeasurement
	for round := 0; round < repetitions; round++ {
		t.Run(fmt.Sprintf("round-%d", round+1), func(t *testing.T) {
			a := newPublicationDaemon(t, exe, "baseline-producer", "", "", "keep-all")
			a.env = append(a.env, "SDN_E2E_PUBLICATION_OBSERVATIONS="+a.logPath+".announcements.jsonl")
			a.start(t)
			b := newPublicationDaemon(t, exe, "baseline-receiver", a.address, a.id, "keep-all")
			b.start(t)
			// The probe is an admin-only operation, independent of import trust.
			a.request(t, "PUT", "/api/v1/admin/peers/"+b.id+"/trust", "application/json", []byte(`{"trust_level":"admin"}`), http.StatusOK)
			// Kubo's config RPC cannot update this identity-managed repository.
			// Set the test-only stream mount flag while the child is stopped.
			b.stop(t)
			configPath := filepath.Join(b.store, "kubo", "config")
			configBytes, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var kuboConfig map[string]any
			if err := json.Unmarshal(configBytes, &kuboConfig); err != nil {
				t.Fatal(err)
			}
			experimental, ok := kuboConfig["Experimental"].(map[string]any)
			if !ok {
				experimental = map[string]any{}
				kuboConfig["Experimental"] = experimental
			}
			experimental["Libp2pStreamMounting"] = true
			configBytes, err = json.Marshal(kuboConfig)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
				t.Fatal(err)
			}
			b.start(t)
			record := func(row publicationMeasurement) {
				row.Round = round + 1
				row.Smoke = smoke
				rows = append(rows, row)
				if err := encoder.Encode(row); err != nil {
					t.Fatal(err)
				}
				if err := output.Sync(); err != nil {
					t.Fatal(err)
				}
			}
			recordPeaks := func(phase string, producer, receiver int64) {
				if err := encoder.Encode(map[string]any{"kind": "process_exit_peak_rss", "round": round + 1, "phase": phase, "instrumentation_smoke": smoke, "producer_peak_rss_bytes": producer, "receiver_peak_rss_bytes": receiver}); err != nil {
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
			if os.Getenv("SDN_E2E_PUBLICATION_BASELINE_CASE") == "wire" {
				recordPeaks("wire-only", baselineStopRSS(t, a), baselineStopRSS(t, b))
				return
			}
			for _, data := range []struct {
				schema     string
				count      int
				makeRecord func(int) []byte
			}{
				{"OMM.fbs", smallCount, baselineOMM},
				{"OEM.fbs", largeCount, baselineOEM},
			} {
				source := strings.TrimSuffix(data.schema, ".fbs")
				fixtureRoot := publicationTemp(t, "baseline-inputs-")
				firstPath, firstBytes := baselinePrepare(t, fixtureRoot, "first.sds", 0, data.count, data.makeRecord)
				secondPath, _ := baselinePrepare(t, fixtureRoot, "second.sds", data.count, data.count, data.makeRecord)
				pointURL := b.base + "/api/v1/data/records/" + data.schema + "/" + storage.ComputeCID(data.makeRecord(0)[4:])
				windowQuery := baselineSQL(source)
				beforeSizes, _ := publicationStoreSizes(t, b.store)
				row = measurePublication(t, a, b, "direct-ingest", source, func() (int64, int64) {
					return baselineIngest(t, a, data.schema, source, "first", firstPath, data.count), int64(data.count)
				})
				record(row)
				// The existing publish API exports and announces in one call. Its
				// request interval is retained alongside the separate actual-announcement
				// interval observed by concurrent receiver polling.
				var pub publicationResult
				var readableAt time.Time
				row = measurePublication(t, a, b, "export-announce-import", source, func() (int64, int64) {
					ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
					defer cancel()
					type observation struct {
						at  time.Time
						err error
					}
					observed := make(chan observation, 1)
					go func() {
						at, err := baselineAwaitCount(ctx, b, data.schema, source, int64(data.count))
						observed <- observation{at, err}
					}()
					pub = a.publish(t, data.schema, source, "first")
					if pub.RecordCount != data.count {
						t.Fatalf("published %d records, want %d", pub.RecordCount, data.count)
					}
					result := <-observed
					if result.err != nil {
						t.Fatal(result.err)
					}
					readableAt = result.at
					return firstBytes, int64(data.count)
				})
				announced := baselineFirstAnnouncement(t, a, pub)
				row.FirstAnnouncementUnixNano = announced.UnixNano()
				row.ReadableUnixNano = readableAt.UnixNano()
				row.AnnouncementToReadableSeconds = readableAt.Sub(announced).Seconds()
				if row.AnnouncementToReadableSeconds <= 0 {
					t.Fatal("invalid announcement/readability interval")
				}
				row.AnnouncementBytesPerSecond = float64(firstBytes) / row.AnnouncementToReadableSeconds
				row.AnnouncementRecordsPerSecond = float64(data.count) / row.AnnouncementToReadableSeconds
				row.Note = "CPU/RSS and primary duration cover export/API completion too; announcement rate uses the first successful PNM publication call and concurrent 100 ms readability polling"
				record(row)
				row = measurePublication(t, a, b, "download-only", source, func() (int64, int64) {
					parts := pub.Publications
					if len(parts) == 0 {
						parts = []publicationResult{pub}
					}
					var total int64
					for _, part := range parts {
						total += publicationTransfer(t, b, a.id, syncwire.OpReadShard, syncwire.QRPFields{SchemaName: data.schema, ProviderID: "publication-e2e", SourceName: source, BatchID: "first", CID: part.ShardCID}, io.Discard)
					}
					return total, int64(data.count)
				})
				row.Note = "Every published shard via B's Kubo stream forwarder; already imported, so cache is warm; no import on this request"
				record(row)
				sizes, logical := publicationStoreSizes(t, b.store)
				recordPeaks(source+"-before-restart", 0, baselineStopRSS(t, b))
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
							response, err := client.Get(pointURL)
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
							request, _ := http.NewRequest("POST", b.base+"/api/v1/query", bytes.NewReader(windowQuery))
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
					n := baselineIngest(t, b, data.schema, source, "second", secondPath, data.count)
					return n, int64(data.count)
				})
				row.StoreBytesByKind = sizes
				row.StoreLogicalBytesByKind = logical
				row.StoreToRecordRatio = float64(sizes["total"]-beforeSizes["total"]) / float64(firstBytes)
				row.StoreAllocatedBeforeDataset = beforeSizes
				row.ReadySeconds = readySeconds
				row.PointReadSeconds = publicationPercentiles(point)
				row.WindowSQLSeconds = publicationPercentiles(window)
				record(row)
				if err := os.RemoveAll(fixtureRoot); err != nil {
					t.Fatal(err)
				}
			}
			recordPeaks("round-complete", baselineStopRSS(t, a), baselineStopRSS(t, b))
		})
		if t.Failed() {
			t.Fatal("baseline instrumentation failed; remaining repetitions and summary are omitted")
		}
	}
	summary := map[string]any{"kind": "summary", "target_bytes_per_second": 250000000, "groups": publicationSummaries(rows)}
	if err := encoder.Encode(summary); err != nil {
		t.Fatal(err)
	}
}

type publicationMeasurement struct {
	FirstAnnouncementUnixNano     int64              `json:"first_announcement_unix_nano,omitempty"`
	ReadableUnixNano              int64              `json:"readable_unix_nano,omitempty"`
	AnnouncementToReadableSeconds float64            `json:"announcement_to_readable_seconds,omitempty"`
	AnnouncementBytesPerSecond    float64            `json:"announcement_bytes_per_second,omitempty"`
	AnnouncementRecordsPerSecond  float64            `json:"announcement_records_per_second,omitempty"`
	Smoke                         bool               `json:"instrumentation_smoke"`
	Round                         int                `json:"round"`
	Operation                     string             `json:"operation"`
	Dataset                       string             `json:"dataset"`
	Bytes                         int64              `json:"bytes"`
	Records                       int64              `json:"records"`
	Seconds                       float64            `json:"seconds"`
	BytesPerSecond                float64            `json:"bytes_per_second"`
	RecordsPerSecond              float64            `json:"records_per_second"`
	Target                        int64              `json:"target_bytes_per_second"`
	ProducerCPUSeconds            float64            `json:"producer_cpu_seconds"`
	ReceiverCPUSeconds            float64            `json:"receiver_cpu_seconds"`
	ProducerSampledPeakRSS        int64              `json:"producer_sampled_peak_rss_bytes"`
	ReceiverSampledPeakRSS        int64              `json:"receiver_sampled_peak_rss_bytes"`
	LoadStart                     string             `json:"uptime_start"`
	LoadEnd                       string             `json:"uptime_end"`
	StoreBytesByKind              map[string]int64   `json:"receiver_store_bytes_by_kind,omitempty"`
	StoreLogicalBytesByKind       map[string]int64   `json:"receiver_store_logical_bytes_by_kind,omitempty"`
	StoreAllocatedBeforeDataset   map[string]int64   `json:"receiver_store_allocated_before_dataset,omitempty"`
	StoreToRecordRatio            float64            `json:"receiver_dataset_allocated_increase_to_record_bytes_ratio,omitempty"`
	ReadySeconds                  float64            `json:"receiver_ready_seconds,omitempty"`
	PointReadSeconds              map[string]float64 `json:"point_read_seconds,omitempty"`
	WindowSQLSeconds              map[string]float64 `json:"window_sql_seconds,omitempty"`
	Note                          string             `json:"note,omitempty"`
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

func baselinePrepare(t *testing.T, root, name string, offset, count int, makeRecord func(int) []byte) (string, int64) {
	t.Helper()
	path := filepath.Join(root, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	writer := bufio.NewWriterSize(f, 4<<20)
	var total int64
	for i := 0; i < count; i++ {
		frame := makeRecord(offset + i)
		n, err := writer.Write(frame)
		if err != nil {
			t.Fatal(err)
		}
		total += int64(n)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	return path, total
}

func baselineIngest(t *testing.T, d *publicationDaemon, schema, source, batch, path string, count int) int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 4<<20)
	var total int64
	for i := 0; i < count; {
		if t.Failed() {
			t.Fatal("measurement instrumentation failed; aborting remaining ingest")
		}
		var payload bytes.Buffer
		n := 0
		for i < count && payload.Len() < 4<<20 && n < 4096 {
			var prefix [4]byte
			if _, err := io.ReadFull(reader, prefix[:]); err != nil {
				t.Fatal(err)
			}
			length := binary.LittleEndian.Uint32(prefix[:])
			if length == 0 || length > 16<<20 {
				t.Fatalf("invalid generated frame size %d", length)
			}
			frame := make([]byte, 4+int(length))
			copy(frame, prefix[:])
			if _, err := io.ReadFull(reader, frame[4:]); err != nil {
				t.Fatal(err)
			}
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
	OEM.CelestialFrameWrapperStart(b)
	OEM.CelestialFrameWrapperAddFrame(b, OEM.CelestialFrameGCRF)
	celestial := OEM.CelestialFrameWrapperEnd(b)
	OEM.RFMStart(b)
	OEM.RFMAddREFERENCE_FRAME_type(b, OEM.RFMUnionCelestialFrameWrapper)
	OEM.RFMAddREFERENCE_FRAME(b, celestial)
	reference := OEM.RFMEnd(b)
	// The generated Go table's builder is package-private. These slots are
	// ephemerisDataBlock's declared fields in OEM, with no schema changes.
	b.StartObject(22)
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

func baselineAwaitCount(ctx context.Context, d *publicationDaemon, schema, source string, want int64) (time.Time, error) {
	body := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema, ProviderID: "publication-e2e", SourceName: source, Limit: 1})
	client := http.Client{Timeout: time.Minute}
	for {
		request, err := http.NewRequestWithContext(ctx, "POST", d.base+"/api/v1/data/scan", bytes.NewReader(body))
		if err != nil {
			return time.Time{}, err
		}
		request.Header.Set("Content-Type", publicationStreamType)
		response, err := client.Do(request)
		if err != nil {
			return time.Time{}, err
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		response.Body.Close()
		if err != nil {
			return time.Time{}, err
		}
		if response.StatusCode != http.StatusOK {
			return time.Time{}, fmt.Errorf("readability scan HTTP %d", response.StatusCode)
		}
		frames, err := syncwire.SplitFrames(raw)
		if err != nil || len(frames) == 0 {
			return time.Time{}, fmt.Errorf("invalid readability scan reply")
		}
		q, err := syncwire.ParseQRP(frames[0])
		if err != nil {
			return time.Time{}, err
		}
		if int64(q.TOTAL_COUNT()) == want {
			return time.Now(), nil
		}
		if int64(q.TOTAL_COUNT()) > want {
			return time.Time{}, fmt.Errorf("receiver count exceeds source count")
		}
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func baselineFirstAnnouncement(t *testing.T, a *publicationDaemon, pub publicationResult) time.Time {
	t.Helper()
	wanted := map[string]bool{pub.ManifestCID: true}
	for _, part := range pub.Publications {
		wanted[part.ManifestCID] = true
	}
	file, err := os.Open(a.logPath + ".announcements.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var earliest int64
	for {
		var event struct {
			Started     int64  `json:"started_unix_nano"`
			ManifestCID string `json:"manifest_cid"`
		}
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if wanted[event.ManifestCID] && (earliest == 0 || event.Started < earliest) {
			earliest = event.Started
		}
	}
	if earliest == 0 {
		t.Fatal("no successful announcement observation; rebuild the test daemon")
	}
	return time.Unix(0, earliest)
}

func baselineSQL(schema string) []byte {
	b := flatbuffers.NewBuilder(256)
	sql := "SELECT * FROM OEM WHERE CREATION_DATE >= '2026-01-01T00:00:00Z' AND CREATION_DATE < '2026-01-02T00:00:00Z' LIMIT 100"
	if schema == "OMM" {
		sql = "SELECT * FROM OMM WHERE EPOCH >= '2026-01-01T00:00:00Z' AND EPOCH < '2026-01-02T00:00:00Z' LIMIT 100"
	}
	text := b.CreateString(sql)
	QRP.QRPStart(b)
	QRP.QRPAddSQL(b, text)
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}
func publicationStoreSizes(t *testing.T, root string) (map[string]int64, map[string]int64) {
	t.Helper()
	sizes := map[string]int64{}
	logical := map[string]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			kind := filepath.Ext(path)
			if kind == "" {
				kind = "no-extension"
			}
			logical[kind] += info.Size()
			logical["total"] += info.Size()
			disk := info.Size()
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				disk = stat.Blocks * 512
			}
			sizes[kind] += disk
			sizes["total"] += disk
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sizes, logical
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
		if row.AnnouncementBytesPerSecond > 0 {
			key = "announce-to-readable/" + row.Dataset
			groups[key] = append(groups[key], row.AnnouncementBytesPerSecond)
		}
	}
	result := map[string]map[string]float64{}
	for key, values := range groups {
		result[key] = publicationPercentiles(values)
	}
	return result
}

// The operating system reports a lifetime high-water mark at process exit.
// Receiver restarts therefore produce one peak row per process lifetime.
func baselineStopRSS(t *testing.T, daemon *publicationDaemon) int64 {
	command := daemon.cmd
	daemon.stop(t)
	if command == nil || command.ProcessState == nil {
		t.Fatal("missing child resource usage")
	}
	usage, ok := command.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		t.Fatal("child resource usage is unavailable")
	}
	peak := usage.Maxrss
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	return peak
}
