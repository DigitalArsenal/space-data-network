// qrp-paging-order-e2e proves the declared order and paging of the $QRP data
// routes under concurrent ingest. It seeds one 50,000-record OMM lane, boots a
// node built from this checkout, keeps publishing new records into the same
// lane while it pages, and checks:
//
//   - POST /api/v1/data/query in 20,000-record pages, by NEXT_CURSOR and by
//     PAGE: every pre-existing record exactly once, in store sequence order,
//     byte-identical to POST /api/v1/data/scan; the cursor walk is exactly
//     its first page's snapshot (TOTAL_COUNT), appended records stay out;
//
//   - GET /api/v1/data/index likewise (rows in the same order, each once);
//
//   - SORT_FIELD and foreign cursors are refused with a $QRP Error 400.
//
//     qrp-paging-order-e2e --binary <node>
package main

import (
	"bytes"
	"context"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	schema      = "OMM.fbs"
	streamType  = "application/vnd.sdn.flatbuffers.stream"
	provider    = "e2e-provider"
	lane        = "qrp-order-lane"
	peerID      = "qrp-paging-order-e2e"
	seedBatch   = 4096
	pageLimit   = 20000
	appendBatch = 100
	firstAppend = 1_000_000 // appended NORADs start here; seeded ones are 1..rows
)

var epoch0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func omm(norad int) []byte {
	at := epoch0.Add(time.Duration(norad%86400) * time.Second)
	return sds.NewOMMBuilder().
		WithNoradCatID(uint32(norad)).
		WithObjectName(fmt.Sprintf("QRP-ORDER-%d", norad)).
		WithEpoch(at.Format(time.RFC3339)).
		WithEpochTimestamp(float64(at.Unix())).
		WithMeanMotion(15.5).
		Build()
}

// noradOf reads a stored OMM frame's NORAD (bare or size-prefixed).
func noradOf(data []byte) int64 {
	if len(data) >= 8 && string(data[4:8]) == "$OMM" {
		return int64(OMM.GetRootAsOMM(data, 0).NORAD_CAT_ID())
	}
	return int64(OMM.GetSizePrefixedRootAsOMM(data, 0).NORAD_CAT_ID())
}

var failures []string

func check(name string, err error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", name, err)
		failures = append(failures, name)
		return
	}
	fmt.Printf("PASS %s\n", name)
}

func main() {
	binaryPath := flag.String("binary", "", "node binary to boot")
	rows := flag.Int("rows", 50_000, "records to seed in the lane")
	flag.Parse()
	if *binaryPath == "" {
		fmt.Fprintln(os.Stderr, "--binary is required")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *binaryPath, *rows); err != nil {
		fmt.Printf("FAIL %v\n", err)
		os.Exit(1)
	}
	if len(failures) > 0 {
		fmt.Printf("FAIL %d check(s): %s\n", len(failures), strings.Join(failures, ", "))
		os.Exit(1)
	}
	fmt.Println("PASS qrp-paging-order-e2e")
}

func run(ctx context.Context, binaryPath string, rows int) error {
	root, err := os.MkdirTemp("", "qrp-paging-order-e2e-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	t0 := time.Now()
	if err := seed(root, rows); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	fmt.Printf("PASS seed rows=%d in %.1f s\n", rows, time.Since(t0).Seconds())
	n, err := boot(ctx, binaryPath, root)
	if err != nil {
		return err
	}
	defer n.stop()
	c := client{base: n.base}

	pre, err := c.scanAll(ctx)
	if err != nil {
		return fmt.Errorf("scan before ingest: %w", err)
	}
	if len(pre) != rows {
		return fmt.Errorf("scan before ingest: %d records, want %d", len(pre), rows)
	}
	fmt.Printf("PASS scan before ingest: %d records\n", len(pre))

	ing := &ingest{c: c}
	ing.start(ctx)
	var walks walkResults
	t1 := time.Now()
	walks.queryCursor, walks.queryCursorTotal, err = c.queryWalk(ctx, ing, true)
	check("query cursor walk under ingest", err)
	walks.queryPage, _, err = c.queryWalk(ctx, ing, false)
	check("query PAGE walk under ingest", err)
	walks.indexCursor, walks.indexCursorTotal, err = c.indexWalk(ctx, ing, true)
	check("index cursor walk under ingest", err)
	walks.indexPage, _, err = c.indexWalk(ctx, ing, false)
	check("index page walk under ingest", err)
	walkWall := time.Since(t1)
	appended, ingErr := ing.stop()
	check("ingest appended during the walks", func() error {
		if ingErr != nil {
			return ingErr
		}
		if appended == 0 {
			return errors.New("no record was appended")
		}
		fmt.Printf("     %d records appended while paging (%.1f s)\n", appended, walkWall.Seconds())
		return nil
	}())

	final, err := c.scanAll(ctx)
	if err != nil {
		return fmt.Errorf("scan after ingest: %w", err)
	}
	fmt.Printf("     final scan: %d records\n", len(final))
	check("pre-existing records are the final scan's prefix", prefixOf(pre, final, rows))
	check("query cursor walk = its snapshot, exactly once, seq order, byte-identical", func() error {
		if int64(len(walks.queryCursor)) != walks.queryCursorTotal {
			return fmt.Errorf("walk served %d records, snapshot TOTAL_COUNT %d", len(walks.queryCursor), walks.queryCursorTotal)
		}
		if len(walks.queryCursor) >= len(final) {
			return fmt.Errorf("walk served %d records of %d: appended records leaked into the snapshot", len(walks.queryCursor), len(final))
		}
		return prefixOf(walks.queryCursor, final, rows)
	}())
	check("query PAGE walk = every pre-existing record once, seq order, byte-identical", prefixOf(walks.queryPage, final, rows))
	norads := make([]int64, len(final))
	for i, rec := range final {
		norads[i] = noradOf(rec)
	}
	check("index cursor walk = its snapshot, exactly once, seq order", func() error {
		if int64(len(walks.indexCursor)) != walks.indexCursorTotal {
			return fmt.Errorf("walk served %d rows, snapshot TOTAL_COUNT %d", len(walks.indexCursor), walks.indexCursorTotal)
		}
		return rowsPrefixOf(walks.indexCursor, norads, rows)
	}())
	check("index page walk = every pre-existing record once, seq order", rowsPrefixOf(walks.indexPage, norads, rows))
	check("refusals", c.refusals(ctx))
	return nil
}

type walkResults struct {
	queryCursor, queryPage [][]byte
	queryCursorTotal       int64
	indexCursor, indexPage []indexRow
	indexCursorTotal       int64
}

// prefixOf: got is want[:len(got)] byte for byte, covering at least the
// first need records.
func prefixOf(got, want [][]byte, need int) error {
	if len(got) < need {
		return fmt.Errorf("%d records, want at least %d", len(got), need)
	}
	if len(got) > len(want) {
		return fmt.Errorf("%d records, the store holds %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			return fmt.Errorf("record %d differs from the scan's (NORAD %d, scan NORAD %d)", i, noradOf(got[i]), noradOf(want[i]))
		}
	}
	return nil
}

func rowsPrefixOf(got []indexRow, norads []int64, need int) error {
	if len(got) < need {
		return fmt.Errorf("%d rows, want at least %d", len(got), need)
	}
	if len(got) > len(norads) {
		return fmt.Errorf("%d rows, the store holds %d", len(got), len(norads))
	}
	seen := make(map[string]bool, len(got))
	for i, r := range got {
		if seen[r.cid] {
			return fmt.Errorf("row %d: CID %s served twice", i, r.cid)
		}
		seen[r.cid] = true
		if r.norad != norads[i] {
			return fmt.Errorf("row %d: NORAD %d, the scan's record %d is NORAD %d", i, r.norad, i, norads[i])
		}
	}
	return nil
}

// seed stores rows records in the lane, seedBatch per batch, offline.
func seed(root string, rows int) error {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	store, err := storage.NewFlatSQLStore(root, validator)
	if err != nil {
		return err
	}
	for first, b := 1, 0; first <= rows; first, b = first+seedBatch, b+1 {
		batch := make([][]byte, 0, seedBatch)
		for norad := first; norad < first+seedBatch && norad <= rows; norad++ {
			batch = append(batch, omm(norad))
		}
		tags := storage.SourceTags{ProviderID: provider, SourceName: lane, BatchID: fmt.Sprintf("b%04d", b), ProducerPeerID: peerID}
		if _, err := store.StoreBatchWithSourceTags(schema, batch, peerID, nil, tags); err != nil {
			_ = store.Close()
			return err
		}
	}
	fmt.Printf("     store format4=%t\n", store.Format4())
	return store.Close()
}

// ---------------------------------------------------------------------------
// concurrent ingest

// ingest publishes appendBatch new records into the lane, back to back,
// through the node's batch publish route until stopped.
type ingest struct {
	c        client
	appended atomic.Int64
	stopCh   chan struct{}
	done     sync.WaitGroup
	err      error
}

func (g *ingest) start(ctx context.Context) {
	g.stopCh = make(chan struct{})
	g.done.Add(1)
	go func() {
		defer g.done.Done()
		next := firstAppend
		for b := 0; ; b++ {
			select {
			case <-g.stopCh:
				return
			case <-ctx.Done():
				return
			default:
			}
			var body bytes.Buffer
			for i := 0; i < appendBatch; i++ {
				rec := omm(next + i)
				var prefix [4]byte
				binary.LittleEndian.PutUint32(prefix[:], uint32(len(rec)))
				body.Write(prefix[:])
				body.Write(rec)
			}
			path := "/api/v1/data/publish/batch/" + schema + "?" + url.Values{
				"provider_id": {provider}, "source_name": {lane}, "batch_id": {fmt.Sprintf("append-%05d", b)}}.Encode()
			a, err := g.c.do(ctx, http.MethodPost, path, "application/octet-stream", body.Bytes())
			if err != nil || a.status/100 != 2 {
				if ctx.Err() == nil {
					g.err = fmt.Errorf("publish batch %d: HTTP %d %v %s", b, a.status, err, clip(a.body))
				}
				return
			}
			next += appendBatch
			g.appended.Add(appendBatch)
		}
	}()
}

// waitProgress returns once at least one more batch landed since mark.
func (g *ingest) waitProgress(mark int64) error {
	deadline := time.Now().Add(2 * time.Minute)
	for g.appended.Load() <= mark {
		if g.err != nil {
			return g.err
		}
		if time.Now().After(deadline) {
			return errors.New("ingest made no progress in 2 min")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (g *ingest) stop() (int64, error) {
	close(g.stopCh)
	g.done.Wait()
	return g.appended.Load(), g.err
}

// ---------------------------------------------------------------------------
// walks

// queryWalk pages /data/query at pageLimit, by NEXT_CURSOR or by PAGE,
// waiting for an ingest batch to land before every page.
func (c client) queryWalk(ctx context.Context, ing *ingest, byCursor bool) ([][]byte, int64, error) {
	var out [][]byte
	var total int64
	cursor := ""
	for page := uint32(1); ; page++ {
		if err := ing.waitProgress(ing.appended.Load()); err != nil {
			return nil, 0, err
		}
		r := request{page: page, limit: pageLimit}
		if byCursor {
			r.page, r.cursor = 1, cursor
		}
		t := time.Now()
		runs, err := c.query(ctx, r)
		if err != nil {
			return nil, 0, fmt.Errorf("page %d: %w", page, err)
		}
		h := runs[0].header
		got := allRecords(runs)
		fmt.Printf("     query page %d: %d records TOTAL_COUNT %d in %.2f s\n", page, len(got), h.TOTAL_COUNT(), time.Since(t).Seconds())
		out = append(out, got...)
		if byCursor {
			if page == 1 {
				total = int64(h.TOTAL_COUNT())
			} else if int64(h.TOTAL_COUNT()) != total {
				return nil, 0, fmt.Errorf("page %d TOTAL_COUNT %d, the snapshot's is %d", page, h.TOTAL_COUNT(), total)
			}
			if len(h.CURSOR()) == 0 {
				return nil, 0, fmt.Errorf("page %d carries no CURSOR", page)
			}
			cursor = string(h.NEXT_CURSOR())
			if cursor == "" {
				return out, total, nil
			}
		} else if len(got) < pageLimit {
			return out, 0, nil
		}
		if page > 50 {
			return nil, 0, errors.New("walk did not end within 50 pages")
		}
	}
}

type indexRow struct {
	cid   string
	norad int64
}

// indexWalk pages /data/index at pageLimit, by cursor or by page.
func (c client) indexWalk(ctx context.Context, ing *ingest, byCursor bool) ([]indexRow, int64, error) {
	var out []indexRow
	var total int64
	cursor := ""
	for page := 1; ; page++ {
		if err := ing.waitProgress(ing.appended.Load()); err != nil {
			return nil, 0, err
		}
		v := url.Values{"schema": {"OMM"}, "provider_id": {provider}, "source_name": {lane}, "limit": {strconv.Itoa(pageLimit)}}
		if byCursor {
			if cursor != "" {
				v.Set("cursor", cursor)
			}
		} else {
			v.Set("page", strconv.Itoa(page))
		}
		t := time.Now()
		q, idx, err := c.page(ctx, "/api/v1/data/index?"+v.Encode())
		if err != nil {
			return nil, 0, fmt.Errorf("page %d: %w", page, err)
		}
		cidCol, okC := idx["cid"]
		noradCol, okN := idx["norad"]
		if !okC || !okN {
			return nil, 0, errors.New("index page lacks the cid or norad column")
		}
		for i := 0; i < q.ROWSLength(); i++ {
			cid := cell(q, i, cidCol)
			norad := cell(q, i, noradCol)
			if QRP.EnumNamesqrpValueKind[norad.KIND()] != "Int" {
				return nil, 0, fmt.Errorf("page %d row %d: norad is %s", page, i, QRP.EnumNamesqrpValueKind[norad.KIND()])
			}
			out = append(out, indexRow{cid: string(cid.TEXT_VALUE()), norad: norad.INT_VALUE()})
		}
		fmt.Printf("     index page %d: %d rows TOTAL_COUNT %d in %.2f s\n", page, q.ROWSLength(), q.TOTAL_COUNT(), time.Since(t).Seconds())
		if byCursor {
			if page == 1 {
				total = int64(q.TOTAL_COUNT())
			} else if int64(q.TOTAL_COUNT()) != total {
				return nil, 0, fmt.Errorf("page %d TOTAL_COUNT %d, the snapshot's is %d", page, q.TOTAL_COUNT(), total)
			}
			cursor = string(q.NEXT_CURSOR())
			if cursor == "" {
				return out, total, nil
			}
		} else if q.ROWSLength() < pageLimit {
			return out, 0, nil
		}
		if page > 50 {
			return nil, 0, errors.New("walk did not end within 50 pages")
		}
	}
}

// scanAll reads the lane through POST /api/v1/data/scan (the reference
// store order), following its NEXT_CURSOR.
func (c client) scanAll(ctx context.Context) ([][]byte, error) {
	var out [][]byte
	cursor := ""
	for {
		a, err := c.do(ctx, http.MethodPost, "/api/v1/data/scan", streamType, request{limit: 50000, cursor: cursor}.frame())
		if err != nil {
			return nil, err
		}
		if a.status != http.StatusOK {
			return nil, fmt.Errorf("scan HTTP %d %s", a.status, clip(a.body))
		}
		runs, err := parseRuns(a.body)
		if err != nil {
			return nil, err
		}
		out = append(out, allRecords(runs)...)
		cursor = a.hdr.Get("X-SDN-Next-Cursor")
		if cursor == "" {
			return out, nil
		}
	}
}

func (c client) refusals(ctx context.Context) error {
	cases := []struct {
		name string
		r    request
	}{
		{"SORT_FIELD", request{page: 1, limit: 10, sortField: "EPOCH"}},
		{"SORT_DESC", request{page: 1, limit: 10, sortDesc: true}},
		{"foreign cursor", request{page: 1, limit: 10, cursor: "bm90LWEtY3Vyc29y"}},
	}
	for _, tc := range cases {
		a, err := c.do(ctx, http.MethodPost, "/api/v1/data/query", streamType, tc.r.frame())
		if err != nil {
			return err
		}
		if err := wantError(a); err != nil {
			return fmt.Errorf("query %s: %w", tc.name, err)
		}
	}
	// An index cursor of a different lane.
	q, _, err := c.page(ctx, "/api/v1/data/index?schema=OMM&source_name=other-lane&limit=1")
	if err != nil {
		return err
	}
	_ = q
	for _, cur := range []string{"garbage", "bm90LWEtY3Vyc29y"} {
		a, err := c.do(ctx, http.MethodGet, "/api/v1/data/index?"+url.Values{"schema": {"OMM"}, "source_name": {lane}, "cursor": {cur}}.Encode(), "", nil)
		if err != nil {
			return err
		}
		if err := wantError(a); err != nil {
			return fmt.Errorf("index cursor %q: %w", cur, err)
		}
	}
	return nil
}

func wantError(a answer) error {
	if a.status != http.StatusBadRequest {
		return fmt.Errorf("HTTP %d, want 400", a.status)
	}
	fs, err := frames(a.body)
	if err != nil || len(fs) != 1 || !isQRP(fs[0]) {
		return fmt.Errorf("body is not one $QRP frame (%v)", err)
	}
	if k := QRP.EnumNamesqrpKind[QRP.GetRootAsQRP(fs[0], 0).KIND()]; k != "Error" {
		return fmt.Errorf("$QRP KIND %s, want Error", k)
	}
	return nil
}

// ---------------------------------------------------------------------------
// node

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

// boot starts the node isolated with the auth wall on and the loopback
// console admitted as the operator (publishing enabled).
func boot(ctx context.Context, binaryPath, root string) (*node, error) {
	home, err := os.MkdirTemp("", "qrp-paging-order-e2e-home-")
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
	ports := make([]int, 3)
	for i := range ports {
		if ports[i], err = freePort(); err != nil {
			return nil, err
		}
	}
	port, kuboAPI, kuboGateway := ports[0], ports[1], ports[2]
	config := filepath.Join(home, "config.yaml")
	yaml := strings.Join([]string{
		"mode: full",
		"network:", "  listen:", "    - /ip4/127.0.0.1/tcp/0", "  bootstrap: []", "  edge_relays: []", "  enable_relay: false",
		"storage:", fmt.Sprintf("  path: %q", root), "  max_size: 4GB",
		"peers:", "  strict_mode: false", "  enable_dht: false", "  enable_mdns: false",
		"admin:", "  enabled: true", fmt.Sprintf("  listen_addr: \"127.0.0.1:%d\"", port), "  require_auth: true", "  tls_mode: disabled",
		"  local_console: true", "  remote_requires_sealed: false",
		fmt.Sprintf("  ipfs_api_url: \"http://127.0.0.1:%d\"", kuboAPI), fmt.Sprintf("  ipfs_gateway_url: \"http://127.0.0.1:%d\"", kuboGateway),
		"publishing:", "  enabled: true",
		"flows:", "  mounts: []",
		"setup:", fmt.Sprintf("  data_path: %q", root), "",
	}, "\n")
	if err := os.WriteFile(config, []byte(yaml), 0o644); err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(home, "daemon.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binaryPath, "daemon", "--allow-multi-daemon", "--config", config)
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
		n.cmd = nil
	}
	if os.Getenv("QRP_ORDER_E2E_KEEP") == "" {
		_ = os.RemoveAll(n.home)
	} else {
		fmt.Printf("     kept %s\n", n.home)
	}
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func clip(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// wire

type client struct{ base string }

type answer struct {
	status int
	hdr    http.Header
	body   []byte
}

var httpClient = &http.Client{Timeout: 5 * time.Minute}

func (c client) do(ctx context.Context, method, path, contentType string, body []byte) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return answer{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, data}, err
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

// request is one $QRP{KIND=Request} on the lane.
type request struct {
	page, limit uint32
	cursor      string
	sortField   string
	sortDesc    bool
}

func (r request) frame() []byte {
	b := flatbuffers.NewBuilder(256)
	schemaName, providerID, sourceName := b.CreateString(schema), b.CreateString(provider), b.CreateString(lane)
	var cursor, sortField flatbuffers.UOffsetT
	if r.cursor != "" {
		cursor = b.CreateString(r.cursor)
	}
	if r.sortField != "" {
		sortField = b.CreateString(r.sortField)
	}
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, QRP.EnumValuesqrpKind["Request"])
	QRP.QRPAddSCHEMA_NAME(b, schemaName)
	QRP.QRPAddPROVIDER_ID(b, providerID)
	QRP.QRPAddSOURCE_NAME(b, sourceName)
	QRP.QRPAddPAGE(b, r.page)
	QRP.QRPAddLIMIT(b, r.limit)
	if cursor != 0 {
		QRP.QRPAddCURSOR(b, cursor)
	}
	if sortField != 0 {
		QRP.QRPAddSORT_FIELD(b, sortField)
	}
	if r.sortDesc {
		QRP.QRPAddSORT_DESC(b, true)
	}
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}

type laneRun struct {
	header  *QRP.QRP
	records [][]byte
}

// query posts one request and parses the answer runs.
func (c client) query(ctx context.Context, r request) ([]laneRun, error) {
	a, err := c.do(ctx, http.MethodPost, "/api/v1/data/query", streamType, r.frame())
	if err != nil {
		return nil, err
	}
	if a.status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", a.status, clip(a.body))
	}
	return parseRuns(a.body)
}

// parseRuns splits [$QRP page header][RECORD_COUNT record frames] runs.
func parseRuns(body []byte) ([]laneRun, error) {
	fs, err := frames(body)
	if err != nil {
		return nil, err
	}
	var runs []laneRun
	for k := 0; k < len(fs); {
		if !isQRP(fs[k]) {
			return nil, fmt.Errorf("frame %d is not a $QRP header", k)
		}
		h := QRP.GetRootAsQRP(fs[k], 0)
		if QRP.EnumNamesqrpKind[h.KIND()] != "Page" {
			return nil, fmt.Errorf("frame %d header KIND %s", k, QRP.EnumNamesqrpKind[h.KIND()])
		}
		n := int(h.RECORD_COUNT())
		k++
		if k+n > len(fs) {
			return nil, fmt.Errorf("run declares %d records, %d frames remain", n, len(fs)-k)
		}
		runs = append(runs, laneRun{h, fs[k : k+n]})
		k += n
	}
	if len(runs) == 0 {
		return nil, errors.New("no run header")
	}
	return runs, nil
}

func allRecords(runs []laneRun) [][]byte {
	var out [][]byte
	for _, r := range runs {
		out = append(out, r.records...)
	}
	return out
}

// page GETs one $QRP table page and its column index.
func (c client) page(ctx context.Context, path string) (*QRP.QRP, map[string]int, error) {
	a, err := c.do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, nil, err
	}
	if a.status != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d %s", a.status, clip(a.body))
	}
	fs, err := frames(a.body)
	if err != nil || len(fs) != 1 || !isQRP(fs[0]) {
		return nil, nil, fmt.Errorf("body is not one $QRP frame (%v)", err)
	}
	q := QRP.GetRootAsQRP(fs[0], 0)
	idx := map[string]int{}
	var col QRP.QRPColumn
	for i := 0; i < q.COLUMNSLength(); i++ {
		q.COLUMNS(&col, i)
		idx[string(col.NAME())] = i
	}
	return q, idx, nil
}

func cell(q *QRP.QRP, r, c int) *QRP.QRPValue {
	var row QRP.QRPRow
	var v QRP.QRPValue
	q.ROWS(&row, r)
	row.CELLS(&v, c)
	return &v
}
