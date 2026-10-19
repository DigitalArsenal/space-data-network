// data-scan-flatbuffer-e2e boots a node built from this checkout against a
// seeded store of >= 1M WXF records and proves the data plane is FlatBuffer
// only: a full paged POST /api/v1/data/scan answers $QRP page headers around
// verbatim record frames, byte-identical and in order; JSON is refused; the
// stream route is gone; GET /api/v1/data/index answers one $QRP page.
//
//	data-scan-flatbuffer-e2e --binary <node> [--rows N] [--data DIR] [--seed-only] [--boot-only]
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	schema       = "WXF.fbs"
	streamType   = "application/vnd.sdn.flatbuffers.stream"
	seedBatch    = 4096
	pageLimit    = 50000
	provider     = "noaa-ncep"
	producerPeer = "data-scan-e2e"
)

// lanes rotate per seed batch, so every page crosses lane boundaries.
var lanes = []string{"gfs-0p25-sea", "gfs-0p25-land", "hrrr-conus"}

func laneOf(i int) string { return lanes[(i/seedBatch)%len(lanes)] }

// record builds the deterministic i-th WXF record (unprefixed root buffer).
func record(i int) []byte {
	b := flatbuffers.NewBuilder(128)
	field := b.CreateString(fmt.Sprintf("scan-%08d", i))
	WXF.WXFStartVALUESVector(b, 2)
	b.PrependFloat32(float32(i % 1000))
	b.PrependFloat32(280)
	values := b.EndVector(2)
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 1)
	WXF.WXFGridAddNLON(b, 2)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, field)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALUES(b, values)
	WXF.WXFAddVALID_TIME_MS(b, 1792238400000+uint64(i))
	WXF.FinishWXFBuffer(b, WXF.WXFEnd(b))
	return b.FinishedBytes()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Printf("DATA-SCAN-FLATBUFFER-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DATA-SCAN-FLATBUFFER-E2E PASS")
}

func run(ctx context.Context) (err error) {
	binary := flag.String("binary", "", "node binary to boot")
	rows := flag.Int("rows", 1_000_000, "records to seed")
	dataDir := flag.String("data", "", "seeded store directory to keep or reuse")
	seedOnly := flag.Bool("seed-only", false, "seed --data and exit")
	bootOnly := flag.Bool("boot-only", false, "boot the node on --data, print its URL, wait for a signal")
	flag.Parse()

	root := *dataDir
	if root == "" {
		if root, err = os.MkdirTemp("", "data-scan-e2e-store-"); err != nil {
			return err
		}
		defer os.RemoveAll(root)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".seeded")); statErr != nil {
		if err := seed(root, *rows); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	} else {
		fmt.Printf("reuse store=%s rows=%d\n", root, *rows)
	}
	if *seedOnly {
		return nil
	}
	if *binary == "" {
		return errors.New("--binary is required")
	}
	node, err := boot(ctx, *binary, root)
	if err != nil {
		return err
	}
	defer node.stop()
	if *bootOnly {
		fmt.Printf("BOOTED %s\n", node.base)
		<-ctx.Done()
		return nil
	}

	var failures []string
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		failures = append(failures, msg)
		fmt.Println("FAIL " + msg)
	}
	if err := fullScan(ctx, node.base, *rows); err != nil {
		fail("full scan: %v", err)
	}
	if err := refusesJSON(ctx, node.base); err != nil {
		fail("json refusal: %v", err)
	}
	if err := indexPage(ctx, node.base); err != nil {
		fail("index: %v", err)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func seed(root string, rows int) error {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	store, err := storage.NewFlatSQLStore(root, validator)
	if err != nil {
		return err
	}
	start := time.Now()
	for first := 0; first < rows; first += seedBatch {
		n := min(seedBatch, rows-first)
		batch := make([][]byte, n)
		for i := range batch {
			batch[i] = record(first + i)
		}
		tags := storage.SourceTags{ProviderID: provider, SourceName: laneOf(first), BatchID: fmt.Sprintf("b%04d", first/seedBatch), ProducerPeerID: producerPeer}
		stored, err := store.StoreBatchWithSourceTags(schema, batch, producerPeer, nil, tags)
		if err != nil {
			_ = store.Close()
			return err
		}
		if stored != n {
			_ = store.Close()
			return fmt.Errorf("batch at %d stored %d of %d", first, stored, n)
		}
	}
	if err := store.Close(); err != nil {
		return err
	}
	fmt.Printf("seed rows=%d time=%s format4=%t\n", rows, time.Since(start).Round(time.Millisecond), store.Format4())
	return os.WriteFile(filepath.Join(root, ".seeded"), []byte(fmt.Sprint(rows)), 0o644)
}

type node struct {
	base string
	cmd  *exec.Cmd
	home string
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// boot starts the node isolated, the way graph/e2e/sdn-server.mjs does, with
// the scan, stream and record routes open to anonymous callers.
func boot(ctx context.Context, binary, root string) (*node, error) {
	home, err := os.MkdirTemp("", "data-scan-e2e-home-")
	if err != nil {
		return nil, err
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		aot := filepath.Join(userHome, "Library", "Caches", "flatsql-aot")
		if _, err := os.Stat(aot); err == nil {
			_ = os.MkdirAll(filepath.Join(home, "Library", "Caches"), 0o755)
			_ = os.Symlink(aot, filepath.Join(home, "Library", "Caches", "flatsql-aot"))
		}
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	config := filepath.Join(home, "config.yaml")
	yaml := strings.Join([]string{
		"mode: full",
		"network:", "  listen:", "    - /ip4/127.0.0.1/tcp/0", "  bootstrap: []", "  edge_relays: []", "  enable_relay: false",
		"storage:", fmt.Sprintf("  path: %q", root), "  max_size: 20GB",
		"peers:", "  strict_mode: false", "  enable_dht: false", "  enable_mdns: false",
		"admin:", "  enabled: true", fmt.Sprintf("  listen_addr: \"127.0.0.1:%d\"", port), "  require_auth: true", "  tls_mode: disabled",
		"  public_paths:", "    - \"POST /api/v1/data/scan\"", "    - \"POST /api/v1/data/stream\"", "    - \"/api/v1/data/records/\"",
		"setup:", fmt.Sprintf("  data_path: %q", root), "",
	}, "\n")
	if err := os.WriteFile(config, []byte(yaml), 0o644); err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(home, "daemon.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, "daemon", "--allow-multi-daemon", "--config", config)
	cmd.Dir, cmd.Stdout, cmd.Stderr = home, logFile, logFile
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	n := &node{base: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd, home: home}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t0 := time.Now()
	for time.Since(t0) < 5*time.Minute {
		select {
		case err := <-exited:
			tail, _ := os.ReadFile(filepath.Join(home, "daemon.log"))
			n.cmd = nil
			n.stop()
			return nil, fmt.Errorf("node exited before health (%v): %s", err, lastLines(tail, 8))
		case <-ctx.Done():
			n.stop()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := http.Get(n.base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Printf("PASS boot pid=%d %s after %.1f s\n", cmd.Process.Pid, n.base, time.Since(t0).Seconds())
				return n, nil
			}
		}
	}
	n.stop()
	return nil, errors.New("no health 200 within 5 min")
}

// stop ends exactly the process group this harness started.
func (n *node) stop() {
	if n.cmd != nil && n.cmd.Process != nil {
		pgid := n.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		for i := 0; i < 60; i++ {
			if syscall.Kill(-pgid, 0) != nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = os.RemoveAll(n.home)
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func requestFrame(cursor string) []byte {
	b := flatbuffers.NewBuilder(256)
	schemaName := b.CreateString(schema)
	var cur flatbuffers.UOffsetT
	if cursor != "" {
		cur = b.CreateString(cursor)
	}
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, QRP.EnumValuesqrpKind["Request"])
	QRP.QRPAddSCHEMA_NAME(b, schemaName)
	QRP.QRPAddLIMIT(b, pageLimit)
	if cur != 0 {
		QRP.QRPAddCURSOR(b, cur)
	}
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}

// frames splits a size-prefixed stream into payloads (prefix removed).
func frames(body []byte) ([][]byte, error) {
	var out [][]byte
	for off := 0; off < len(body); {
		if len(body)-off < 4 {
			return nil, fmt.Errorf("truncated prefix at byte %d", off)
		}
		n := int(binary.LittleEndian.Uint32(body[off:]))
		if off+4+n > len(body) {
			return nil, fmt.Errorf("frame at byte %d overruns the body", off)
		}
		out = append(out, body[off+4:off+4+n])
		off += 4 + n
	}
	return out, nil
}

func isQRP(payload []byte) bool { return len(payload) >= 8 && string(payload[4:8]) == "$QRP" }

func post(ctx context.Context, url, contentType string, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data, err
}

// fullScan pages the whole lane set and checks every frame against the
// seeded record of the same position, byte for byte.
func fullScan(ctx context.Context, base string, rows int) error {
	cursor, seen, pages, runs := "", 0, 0, 0
	var payloadBytes int64
	var total uint64
	t0 := time.Now()
	var firstPage, slowest time.Duration
	for {
		tp := time.Now()
		status, hdr, body, err := post(ctx, base+"/api/v1/data/scan", streamType, requestFrame(cursor))
		if d := time.Since(tp); pages == 0 {
			firstPage = d
		} else if d > slowest {
			slowest = d
		}
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("page %d answered %d (%s)", pages+1, status, errorText(body))
		}
		if ct := hdr.Get("Content-Type"); ct != streamType {
			return fmt.Errorf("page %d Content-Type %q", pages+1, ct)
		}
		fs, err := frames(body)
		if err != nil {
			return fmt.Errorf("page %d: %w", pages+1, err)
		}
		pages++
		next := ""
		for k := 0; k < len(fs); {
			if !isQRP(fs[k]) {
				return fmt.Errorf("page %d frame %d is not a $QRP header", pages, k)
			}
			h := QRP.GetRootAsQRP(fs[k], 0)
			if h.KIND() != QRP.EnumValuesqrpKind["Page"] {
				return fmt.Errorf("page %d header kind %v", pages, h.KIND())
			}
			runs++
			total, next = h.TOTAL_COUNT(), string(h.NEXT_CURSOR())
			count := int(h.RECORD_COUNT())
			k++
			if k+count > len(fs) {
				return fmt.Errorf("page %d run declares %d records, %d frames remain", pages, count, len(fs)-k)
			}
			for _, payload := range fs[k : k+count] {
				if seen >= rows {
					return fmt.Errorf("more than %d records served", rows)
				}
				if string(h.SOURCE_NAME()) != laneOf(seen) || string(h.BATCH_ID()) != fmt.Sprintf("b%04d", seen/seedBatch) ||
					string(h.PROVIDER_ID()) != provider || string(h.PRODUCER_PEER_ID()) != producerPeer {
					return fmt.Errorf("record %d run header lane %s/%s/%s, want %s/%s", seen, h.PROVIDER_ID(), h.SOURCE_NAME(), h.BATCH_ID(), provider, laneOf(seen))
				}
				if want := record(seen); sha256.Sum256(payload) != sha256.Sum256(want) {
					return fmt.Errorf("record %d bytes differ from the stored record (%d vs %d bytes)", seen, len(payload), len(want))
				}
				payloadBytes += int64(len(payload))
				seen++
			}
			k += count
		}
		if next == "" {
			break
		}
		cursor = next
	}
	wall := time.Since(t0)
	fmt.Printf("full_scan rows=%d pages=%d runs=%d total_count=%d wall_s=%.2f rows_per_s=%.0f payload_mb_per_s=%.1f first_page_ms=%d slowest_continuation_ms=%d\n",
		seen, pages, runs, total, wall.Seconds(), float64(seen)/wall.Seconds(), float64(payloadBytes)/1e6/wall.Seconds(), firstPage.Milliseconds(), slowest.Milliseconds())
	if seen != rows || total != uint64(rows) {
		return fmt.Errorf("served %d records, TOTAL_COUNT %d, seeded %d", seen, total, rows)
	}
	fmt.Println("PASS full scan: every record byte-identical, in order, lanes in run headers")
	return verifyAgainstRecordRoute(ctx, base)
}

// verifyAgainstRecordRoute reads one record back through the raw record
// route by its store CID and compares it with the scan's bytes.
func verifyAgainstRecordRoute(ctx context.Context, base string) error {
	want := record(0)
	cid := storage.ComputeCID(want)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/data/records/"+url.PathEscape(schema)+"/"+url.PathEscape(cid), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
		return fmt.Errorf("records/%s answered %d with %d bytes; want the scanned %d bytes", cid, resp.StatusCode, len(got), len(want))
	}
	fmt.Printf("PASS stored bytes: records/%s equals the scanned frame\n", cid)
	return nil
}

func errorText(body []byte) string {
	fs, err := frames(body)
	if err != nil || len(fs) != 1 || !isQRP(fs[0]) {
		return fmt.Sprintf("non-$QRP body, %d bytes", len(body))
	}
	q := QRP.GetRootAsQRP(fs[0], 0)
	return fmt.Sprintf("%s: %s", q.ERROR_CODE(), q.MESSAGE())
}

func refusesJSON(ctx context.Context, base string) error {
	status, _, body, err := post(ctx, base+"/api/v1/data/scan", "application/json", []byte(`{"schema":"WXF.fbs","limit":10}`))
	if err != nil {
		return err
	}
	fs, ferr := frames(body)
	if status != http.StatusUnsupportedMediaType || ferr != nil || len(fs) != 1 || !isQRP(fs[0]) ||
		QRP.GetRootAsQRP(fs[0], 0).KIND() != QRP.EnumValuesqrpKind["Error"] {
		return fmt.Errorf("JSON scan answered %d (%d bytes), want 415 and one $QRP Error frame", status, len(body))
	}
	fmt.Printf("PASS JSON refused: 415 %s\n", errorText(body))
	status, _, _, err = post(ctx, base+"/api/v1/data/stream", "application/json", []byte(`{"schema":"WXF.fbs","records":[]}`))
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return fmt.Errorf("deleted stream route answered %d", status)
	}
	fmt.Printf("PASS stream route gone: %d\n", status)
	return nil
}

func indexPage(ctx context.Context, base string) error {
	// A bounded read answers 503 + a $QRP Busy frame until its first page is
	// cached; a client retries after RETRY_AFTER_MS.
	var status int
	var fs [][]byte
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/data/index?schema=WXF&source_name="+lanes[0]+"&batch_id=b0000&limit=5", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		status = resp.StatusCode
		var ferr error
		if fs, ferr = frames(body); ferr != nil || len(fs) != 1 || !isQRP(fs[0]) {
			return fmt.Errorf("index answered %d with %d bytes, want one $QRP frame", status, len(body))
		}
		busy := QRP.GetRootAsQRP(fs[0], 0)
		if status != http.StatusServiceUnavailable || busy.STATUS() != QRP.EnumValuesqrpStatus["Busy"] || attempt >= 120 {
			break
		}
		fmt.Printf("index busy: %s; retry after %d ms\n", busy.ERROR_CODE(), busy.RETRY_AFTER_MS())
		time.Sleep(time.Duration(busy.RETRY_AFTER_MS()) * time.Millisecond)
	}
	if status != http.StatusOK {
		return fmt.Errorf("index answered %d (%s)", status, QRP.GetRootAsQRP(fs[0], 0).MESSAGE())
	}
	q := QRP.GetRootAsQRP(fs[0], 0)
	if q.KIND() != QRP.EnumValuesqrpKind["Page"] || q.COLUMNSLength() != 3 || q.ROWSLength() != 5 || q.TOTAL_COUNT() != seedBatch {
		return fmt.Errorf("index page kind=%v columns=%d rows=%d total=%d, want Page/3/5/%d", q.KIND(), q.COLUMNSLength(), q.ROWSLength(), q.TOTAL_COUNT(), seedBatch)
	}
	seeded := map[string]bool{}
	for i := 0; i < seedBatch; i++ {
		seeded[storage.ComputeCID(record(i))] = true
	}
	var row QRP.QRPRow
	var cell QRP.QRPValue
	for j := 0; j < q.ROWSLength(); j++ {
		q.ROWS(&row, j)
		row.CELLS(&cell, 0)
		if !seeded[string(cell.TEXT_VALUE())] {
			return fmt.Errorf("index row %d cid %s is not a seeded record of the lane", j, cell.TEXT_VALUE())
		}
	}
	fmt.Printf("PASS index: one $QRP page, %d rows of %d, cids seeded\n", q.ROWSLength(), q.TOTAL_COUNT())
	return nil
}
