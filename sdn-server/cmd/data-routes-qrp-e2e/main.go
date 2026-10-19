// data-routes-qrp-e2e boots a node built from this checkout on a few seeded
// OMM records and proves the /api/v1/data routes answer $QRP FlatBuffers:
// POST /data/query (paging, lane filter, TIME_PROFILE, TOTAL_COUNT, verbatim
// record frames), JSON refused, the projection pages (health, summary,
// datastores, local-replica-stats, sources), the admin SQL sandbox, deleted
// routes gone, the auth wall's $QRP refusal on data routes, and a records/
// miss.
//
//	data-routes-qrp-e2e --binary <node>
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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	schema     = "OMM.fbs"
	streamType = "application/vnd.sdn.flatbuffers.stream"
	provider   = "e2e-provider"
	laneA      = "lane-a"
	laneB      = "lane-b"
	peerID     = "data-routes-qrp-e2e"
)

var day0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type seeded struct {
	norad uint32
	day   int
	lane  string
	data  []byte
}

// fixture: 25544 on days 0..2 and 43013 on days 0..1 in lane A, 99999 on
// day 1 in lane B.
func fixture() []seeded {
	var out []seeded
	add := func(norad uint32, day int, lane string) {
		epoch := day0.Add(time.Duration(day) * 24 * time.Hour)
		data := sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectName(fmt.Sprintf("OBJ-%d-D%d", norad, day)).
			WithEpoch(epoch.Format(time.RFC3339)).
			WithEpochTimestamp(float64(epoch.Unix())).
			WithMeanMotion(15.5).
			Build()
		out = append(out, seeded{norad, day, lane, data})
	}
	add(25544, 0, laneA)
	add(25544, 1, laneA)
	add(25544, 2, laneA)
	add(43013, 0, laneA)
	add(43013, 1, laneA)
	add(99999, 1, laneB)
	return out
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Printf("DATA-ROUTES-QRP-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DATA-ROUTES-QRP-E2E PASS")
}

var failures []string

func check(name string, err error) {
	if err != nil {
		failures = append(failures, name)
		fmt.Printf("FAIL %s: %v\n", name, err)
		return
	}
	fmt.Printf("PASS %s\n", name)
}

func run(ctx context.Context) error {
	binary := flag.String("binary", "", "node binary to boot")
	flag.Parse()
	if *binary == "" {
		return errors.New("--binary is required")
	}
	root, err := os.MkdirTemp("", "data-routes-qrp-e2e-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	recs := fixture()
	if err := seed(root, recs); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	node, err := boot(ctx, *binary, root)
	if err != nil {
		return err
	}
	defer node.stop()
	c := client{base: node.base}

	check("a query: every record verbatim, TOTAL_COUNT", c.queryAll(ctx, recs))
	check("a query: PAGE/LIMIT paging", c.queryPaging(ctx, recs))
	check("a query: SOURCE_NAME lane filter", c.queryLane(ctx, recs))
	check("a query: TIME_PROFILE Latest/Nearest", c.queryProfiles(ctx, recs))
	check("b query: JSON body refused", c.queryJSON(ctx))
	check("c health page", c.health(ctx))
	check("c summary page", c.summary(ctx, recs))
	check("c datastores page", c.plainPage(ctx, "/api/v1/data/datastores", "key", "updated_at"))
	check("c local-replica-stats page", c.plainPage(ctx, "/api/v1/data/local-replica-stats", "schema_name", "local_rows", "last_synced_at"))
	check("c sources page", c.sources(ctx, recs))
	check("d sandbox query", c.sandbox(ctx, recs))
	check("e deleted routes 404", c.deleted(ctx))
	check("f auth wall", c.authWall(ctx))
	check("g records/ miss", c.recordMiss(ctx))
	if len(failures) > 0 {
		return fmt.Errorf("%d check(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func seed(root string, recs []seeded) error {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	store, err := storage.NewFlatSQLStore(root, validator)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		tags := storage.SourceTags{ProviderID: provider, SourceName: rec.lane, BatchID: "b-" + rec.lane, ProducerPeerID: peerID}
		if _, err := store.StoreWithSourceTags(schema, rec.data, peerID, nil, tags); err != nil {
			_ = store.Close()
			return err
		}
	}
	fmt.Printf("seed rows=%d format4=%t\n", len(recs), store.Format4())
	return store.Close()
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

// boot starts the node isolated with the auth wall on and the loopback
// console admitted as root: a plain loopback call is the operator, a call
// carrying a proxy header is anonymous.
func boot(ctx context.Context, binary, root string) (*node, error) {
	home, err := os.MkdirTemp("", "data-routes-qrp-e2e-home-")
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
	// Kubo's RPC API and gateway on free loopback ports: the defaults (5002,
	// 8080) may belong to another node on this machine.
	kuboAPI, err := freePort()
	if err != nil {
		return nil, err
	}
	kuboGateway, err := freePort()
	if err != nil {
		return nil, err
	}
	config := filepath.Join(home, "config.yaml")
	yaml := strings.Join([]string{
		"mode: full",
		"network:", "  listen:", "    - /ip4/127.0.0.1/tcp/0", "  bootstrap: []", "  edge_relays: []", "  enable_relay: false",
		"storage:", fmt.Sprintf("  path: %q", root), "  max_size: 1GB",
		"peers:", "  strict_mode: false", "  enable_dht: false", "  enable_mdns: false",
		"admin:", "  enabled: true", fmt.Sprintf("  listen_addr: \"127.0.0.1:%d\"", port), "  require_auth: true", "  tls_mode: disabled",
		"  local_console: true", "  remote_requires_sealed: false",
		fmt.Sprintf("  ipfs_api_url: \"http://127.0.0.1:%d\"", kuboAPI), fmt.Sprintf("  ipfs_gateway_url: \"http://127.0.0.1:%d\"", kuboGateway),
		// No flow mounts: the native data routes alone own /api/v1/data/.
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

// ---------------------------------------------------------------------------
// wire

type client struct{ base string }

type answer struct {
	status int
	hdr    http.Header
	body   []byte
}

func (c client) do(ctx context.Context, method, path, contentType string, body []byte, anonymous bool) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return answer{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if anonymous {
		// A proxy hop refuses the loopback console session.
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
	}
	resp, err := http.DefaultClient.Do(req)
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

// oneQRP requires exactly one $QRP frame of kind under the stream type.
func oneQRP(a answer, kind string) (*QRP.QRP, error) {
	if ct := a.hdr.Get("Content-Type"); ct != streamType {
		return nil, fmt.Errorf("HTTP %d Content-Type %q, want %s (%d bytes)", a.status, ct, streamType, len(a.body))
	}
	fs, err := frames(a.body)
	if err != nil || len(fs) != 1 || !isQRP(fs[0]) {
		return nil, fmt.Errorf("HTTP %d body is %d frame(s) (%v), want one $QRP", a.status, len(fs), err)
	}
	q := QRP.GetRootAsQRP(fs[0], 0)
	if got := QRP.EnumNamesqrpKind[q.KIND()]; got != kind {
		return nil, fmt.Errorf("HTTP %d $QRP KIND %s (%s: %s), want %s", a.status, got, q.ERROR_CODE(), q.MESSAGE(), kind)
	}
	return q, nil
}

type filter struct{ field, op, value string }

type request struct {
	schema, sourceName, sql string
	page, limit             uint32
	profile                 string
	profileAt               int64
	filters                 []filter
	params                  []int64
}

func (r request) frame() []byte {
	b := flatbuffers.NewBuilder(256)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	schemaName, sourceName, sql := str(r.schema), str(r.sourceName), str(r.sql)
	var filters, params flatbuffers.UOffsetT
	if len(r.filters) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(r.filters))
		for i, f := range r.filters {
			field, value := b.CreateString(f.field), b.CreateString(f.value)
			QRP.QRPFilterStart(b)
			QRP.QRPFilterAddFIELD(b, field)
			QRP.QRPFilterAddOP(b, QRP.EnumValuesqrpFilterOp[f.op])
			QRP.QRPFilterAddVALUE(b, value)
			offs[i] = QRP.QRPFilterEnd(b)
		}
		QRP.QRPStartFILTERSVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		filters = b.EndVector(len(offs))
	}
	if len(r.params) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(r.params))
		for i, p := range r.params {
			QRP.QRPValueStart(b)
			QRP.QRPValueAddKIND(b, QRP.EnumValuesqrpValueKind["Int"])
			QRP.QRPValueAddINT_VALUE(b, p)
			offs[i] = QRP.QRPValueEnd(b)
		}
		QRP.QRPStartPARAMETERSVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		params = b.EndVector(len(offs))
	}
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, QRP.EnumValuesqrpKind["Request"])
	if schemaName != 0 {
		QRP.QRPAddSCHEMA_NAME(b, schemaName)
	}
	if sourceName != 0 {
		QRP.QRPAddSOURCE_NAME(b, sourceName)
	}
	if sql != 0 {
		QRP.QRPAddSQL(b, sql)
	}
	QRP.QRPAddPAGE(b, r.page)
	QRP.QRPAddLIMIT(b, r.limit)
	if r.profile != "" {
		QRP.QRPAddTIME_PROFILE(b, QRP.EnumValuesqrpTimeProfile[r.profile])
		QRP.QRPAddPROFILE_EPOCH_MS(b, r.profileAt)
	}
	if filters != 0 {
		QRP.QRPAddFILTERS(b, filters)
	}
	if params != 0 {
		QRP.QRPAddPARAMETERS(b, params)
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
	a, err := c.do(ctx, http.MethodPost, "/api/v1/data/query", streamType, r.frame(), false)
	if err != nil {
		return nil, err
	}
	if a.status != http.StatusOK {
		if q, qerr := oneQRP(a, "Error"); qerr == nil {
			return nil, fmt.Errorf("HTTP %d %s: %s", a.status, q.ERROR_CODE(), q.MESSAGE())
		}
		return nil, fmt.Errorf("HTTP %d (%d bytes)", a.status, len(a.body))
	}
	if ct := a.hdr.Get("Content-Type"); ct != streamType {
		return nil, fmt.Errorf("Content-Type %q", ct)
	}
	fs, err := frames(a.body)
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

// sameSet compares record byte sets.
func sameSet(got [][]byte, want []seeded) error {
	if len(got) != len(want) {
		return fmt.Errorf("%d records, want %d", len(got), len(want))
	}
	keys := func(n int, at func(int) []byte) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string(at(i))
		}
		sort.Strings(out)
		return out
	}
	g := keys(len(got), func(i int) []byte { return got[i] })
	w := keys(len(want), func(i int) []byte { return want[i].data })
	for i := range g {
		if g[i] != w[i] {
			return errors.New("record bytes differ from the published records")
		}
	}
	return nil
}

func pick(recs []seeded, keep func(seeded) bool) []seeded {
	var out []seeded
	for _, r := range recs {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// checks

func (c client) queryAll(ctx context.Context, recs []seeded) error {
	runs, err := c.query(ctx, request{schema: schema, limit: 100})
	if err != nil {
		return err
	}
	for _, r := range runs {
		h := r.header
		if h.TOTAL_COUNT() != uint64(len(recs)) || string(h.SCHEMA_NAME()) != schema || string(h.FILE_IDENTIFIER()) != "$OMM" ||
			string(h.PROVIDER_ID()) != provider || string(h.PRODUCER_PEER_ID()) != peerID || h.PAGE() != 1 || h.LIMIT() != 100 {
			return fmt.Errorf("run header total=%d schema=%s id=%s provider=%s producer=%s page=%d limit=%d",
				h.TOTAL_COUNT(), h.SCHEMA_NAME(), h.FILE_IDENTIFIER(), h.PROVIDER_ID(), h.PRODUCER_PEER_ID(), h.PAGE(), h.LIMIT())
		}
		lane := string(h.SOURCE_NAME())
		for _, rec := range r.records {
			found := false
			for _, s := range recs {
				if bytes.Equal(s.data, rec) && s.lane == lane {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("a record of run lane %q is not a published record of that lane", lane)
			}
		}
	}
	fmt.Printf("     %d runs, TOTAL_COUNT %d\n", len(runs), runs[0].header.TOTAL_COUNT())
	return sameSet(allRecords(runs), recs)
}

func (c client) queryPaging(ctx context.Context, recs []seeded) error {
	var got [][]byte
	for page := uint32(1); page <= 4; page++ {
		runs, err := c.query(ctx, request{schema: schema, page: page, limit: 2})
		if err != nil {
			return err
		}
		rs := allRecords(runs)
		want := min(2, max(0, len(recs)-int(page-1)*2))
		if len(rs) != want || runs[0].header.PAGE() != page || runs[0].header.TOTAL_COUNT() != uint64(len(recs)) {
			return fmt.Errorf("page %d: %d records (want %d), PAGE %d, TOTAL_COUNT %d", page, len(rs), want, runs[0].header.PAGE(), runs[0].header.TOTAL_COUNT())
		}
		got = append(got, rs...)
	}
	return sameSet(got, recs)
}

func (c client) queryLane(ctx context.Context, recs []seeded) error {
	runs, err := c.query(ctx, request{schema: schema, sourceName: laneB, limit: 100})
	if err != nil {
		return err
	}
	for _, r := range runs {
		if string(r.header.SOURCE_NAME()) != laneB {
			return fmt.Errorf("run lane %q, want %q", r.header.SOURCE_NAME(), laneB)
		}
	}
	want := pick(recs, func(s seeded) bool { return s.lane == laneB })
	if runs[0].header.TOTAL_COUNT() != uint64(len(want)) {
		return fmt.Errorf("TOTAL_COUNT %d, want %d", runs[0].header.TOTAL_COUNT(), len(want))
	}
	return sameSet(allRecords(runs), want)
}

func (c client) queryProfiles(ctx context.Context, recs []seeded) error {
	dayMs := func(d int, extra time.Duration) int64 {
		return day0.Add(time.Duration(d)*24*time.Hour + extra).UnixMilli()
	}
	cases := []struct {
		name string
		req  request
		want []seeded
	}{
		{"Latest NORAD 25544", request{schema: schema, limit: 10, profile: "Latest", profileAt: dayMs(2, time.Hour), filters: []filter{{"NORAD_CAT_ID", "Eq", "25544"}}},
			pick(recs, func(s seeded) bool { return s.norad == 25544 && s.day == 2 })},
		{"Latest as of day 1, every entity", request{schema: schema, limit: 10, profile: "Latest", profileAt: dayMs(1, time.Hour)},
			pick(recs, func(s seeded) bool { return s.day == 1 })},
		{"Nearest NORAD 25544 at day 0 +2 h", request{schema: schema, limit: 10, profile: "Nearest", profileAt: dayMs(0, 2*time.Hour), filters: []filter{{"NORAD_CAT_ID", "Eq", "25544"}}},
			pick(recs, func(s seeded) bool { return s.norad == 25544 && s.day == 0 })},
	}
	for _, tc := range cases {
		runs, err := c.query(ctx, tc.req)
		if err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
		if err := sameSet(allRecords(runs), tc.want); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
		if runs[0].header.TOTAL_COUNT() != uint64(len(tc.want)) {
			return fmt.Errorf("%s: TOTAL_COUNT %d, want %d", tc.name, runs[0].header.TOTAL_COUNT(), len(tc.want))
		}
		fmt.Printf("     %s: %d record(s)\n", tc.name, len(tc.want))
	}
	return nil
}

func (c client) queryJSON(ctx context.Context) error {
	a, err := c.do(ctx, http.MethodPost, "/api/v1/data/query", "application/json", []byte(`{"schema":"OMM.fbs","limit":10}`), false)
	if err != nil {
		return err
	}
	q, err := oneQRP(a, "Error")
	if err != nil {
		return err
	}
	if a.status != http.StatusUnsupportedMediaType {
		return fmt.Errorf("HTTP %d, want 415", a.status)
	}
	fmt.Printf("     415 %s\n", q.ERROR_CODE())
	return nil
}

// page GETs one projection page and indexes its columns.
func (c client) page(ctx context.Context, path string) (*QRP.QRP, map[string]int, error) {
	a, err := c.do(ctx, http.MethodGet, path, "", nil, false)
	if err != nil {
		return nil, nil, err
	}
	if a.status != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d", a.status)
	}
	q, err := oneQRP(a, "Page")
	if err != nil {
		return nil, nil, err
	}
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

func needColumns(idx map[string]int, names ...string) error {
	for _, n := range names {
		if _, ok := idx[n]; !ok {
			return fmt.Errorf("no %s column", n)
		}
	}
	return nil
}

func (c client) health(ctx context.Context) error {
	q, idx, err := c.page(ctx, "/api/v1/data/health")
	if err != nil {
		return err
	}
	if err := needColumns(idx, "component", "status"); err != nil {
		return err
	}
	if QRP.EnumNamesqrpStatus[q.STATUS()] != "Ok" || q.GENERATED_AT_MS() == 0 || q.ROWSLength() != 1 ||
		string(cell(q, 0, idx["status"]).TEXT_VALUE()) != "ok" {
		return fmt.Errorf("STATUS %s rows %d", QRP.EnumNamesqrpStatus[q.STATUS()], q.ROWSLength())
	}
	return nil
}

func laneCounts(recs []seeded) map[string]int64 {
	out := map[string]int64{}
	for _, r := range recs {
		out[r.lane]++
	}
	return out
}

func (c client) summary(ctx context.Context, recs []seeded) error {
	q, idx, err := c.page(ctx, "/api/v1/data/summary")
	if err != nil {
		return err
	}
	if err := needColumns(idx, "schema_name", "source_name", "count", "total_bytes"); err != nil {
		return err
	}
	want := laneCounts(recs)
	got := map[string]int64{}
	var schemaTotal int64 = -1
	for r := 0; r < q.ROWSLength(); r++ {
		if string(cell(q, r, idx["schema_name"]).TEXT_VALUE()) != schema {
			continue
		}
		lane := string(cell(q, r, idx["source_name"]).TEXT_VALUE())
		n := cell(q, r, idx["count"]).INT_VALUE()
		if lane == "" {
			schemaTotal = n
			continue
		}
		got[lane] += n
	}
	if got[laneA] != want[laneA] || got[laneB] != want[laneB] || schemaTotal != int64(len(recs)) {
		return fmt.Errorf("OMM lanes %v total %d, want %v total %d", got, schemaTotal, want, len(recs))
	}
	fmt.Printf("     OMM %s=%d %s=%d total=%d\n", laneA, got[laneA], laneB, got[laneB], schemaTotal)
	return nil
}

func (c client) plainPage(ctx context.Context, path string, columns ...string) error {
	q, idx, err := c.page(ctx, path)
	if err != nil {
		return err
	}
	if err := needColumns(idx, columns...); err != nil {
		return err
	}
	fmt.Printf("     %d columns, %d rows\n", q.COLUMNSLength(), q.ROWSLength())
	return nil
}

func (c client) sources(ctx context.Context, recs []seeded) error {
	q, idx, err := c.page(ctx, "/api/v1/data/sources")
	if err != nil {
		return err
	}
	if err := needColumns(idx, "schema", "source_name", "count", "organization_name", "organization_state", "organization_self"); err != nil {
		return err
	}
	if q.AS_OF_MS() == 0 {
		return errors.New("no AS_OF_MS")
	}
	got := map[string]int64{}
	for r := 0; r < q.ROWSLength(); r++ {
		if string(cell(q, r, idx["schema"]).TEXT_VALUE()) == schema {
			got[string(cell(q, r, idx["source_name"]).TEXT_VALUE())] += cell(q, r, idx["count"]).INT_VALUE()
		}
	}
	want := laneCounts(recs)
	if got[laneA] != want[laneA] || got[laneB] != want[laneB] {
		return fmt.Errorf("OMM lanes %v, want %v", got, want)
	}
	return nil
}

func (c client) sandbox(ctx context.Context, recs []seeded) error {
	req := request{sql: `SELECT NORAD_CAT_ID, OBJECT_NAME, _data FROM OMM WHERE NORAD_CAT_ID = ?1 ORDER BY OBJECT_NAME`, params: []int64{25544}}
	a, err := c.do(ctx, http.MethodPost, "/api/v1/query", streamType, req.frame(), false)
	if err != nil {
		return err
	}
	if a.status == http.StatusNotFound {
		fmt.Println("     /api/v1/query not registered in this configuration")
		return nil
	}
	q, err := oneQRP(a, "Page")
	if err != nil {
		return err
	}
	var col QRP.QRPColumn
	types := []string{}
	for i := 0; i < q.COLUMNSLength(); i++ {
		q.COLUMNS(&col, i)
		types = append(types, string(col.NAME())+":"+string(col.TYPE()))
	}
	want := pick(recs, func(s seeded) bool { return s.norad == 25544 })
	if q.ROWSLength() != len(want) || len(types) != 3 || types[0] != "NORAD_CAT_ID:int64" || types[2] != "_data:bytes" {
		return fmt.Errorf("columns %v rows %d, want NORAD_CAT_ID:int64 ... _data:bytes and %d rows", types, q.ROWSLength(), len(want))
	}
	var blobs [][]byte
	for r := 0; r < q.ROWSLength(); r++ {
		v := cell(q, r, 2)
		if QRP.EnumNamesqrpValueKind[v.KIND()] != "Bytes" {
			return fmt.Errorf("row %d _data KIND %s", r, QRP.EnumNamesqrpValueKind[v.KIND()])
		}
		if cell(q, r, 0).INT_VALUE() != 25544 {
			return fmt.Errorf("row %d NORAD %d", r, cell(q, r, 0).INT_VALUE())
		}
		blobs = append(blobs, append([]byte(nil), v.BYTES_VALUEBytes()...))
	}
	// The engine's _data is the record root: the published bytes less their
	// u32 size prefix.
	roots := make([]seeded, len(want))
	for i, w := range want {
		roots[i] = seeded{data: w.data[4:]}
	}
	if err := sameSet(blobs, roots); err != nil {
		return fmt.Errorf("_data cells: %w", err)
	}
	fmt.Printf("     columns %v, %d rows, _data blobs are the published record roots\n", types, q.ROWSLength())
	return nil
}

func (c client) deleted(ctx context.Context) error {
	for _, path := range []string{"/api/v1/data/epoch?schema=OMM.fbs", "/api/v1/data/table?schema=OMM", "/api/v1/data/table/sources?schema=OMM", "/api/v1/data/chart?schema=OMM"} {
		a, err := c.do(ctx, http.MethodGet, path, "", nil, false)
		if err != nil {
			return err
		}
		if a.status != http.StatusNotFound {
			return fmt.Errorf("%s answered %d %s %q, want 404", path, a.status, a.hdr.Get("Content-Type"), string(a.body[:min(len(a.body), 200)]))
		}
	}
	return nil
}

func (c client) authWall(ctx context.Context) error {
	q := request{schema: schema, limit: 1}
	for _, probe := range []struct{ method, path, ct string }{
		{http.MethodPost, "/api/v1/data/query", streamType},
		{http.MethodGet, "/api/v1/data/summary", ""},
	} {
		var body []byte
		if probe.method == http.MethodPost {
			body = q.frame()
		}
		a, err := c.do(ctx, probe.method, probe.path, probe.ct, body, true)
		if err != nil {
			return err
		}
		e, err := oneQRP(a, "Error")
		if err != nil {
			return fmt.Errorf("%s: %w", probe.path, err)
		}
		if a.status != http.StatusUnauthorized || string(e.ERROR_CODE()) != "unauthorized" || e.HTTP_STATUS() != 401 {
			return fmt.Errorf("%s: HTTP %d code %q HTTP_STATUS %d", probe.path, a.status, e.ERROR_CODE(), e.HTTP_STATUS())
		}
		fmt.Printf("     %s %s: 401 $QRP Error %s\n", probe.method, probe.path, e.ERROR_CODE())
	}
	a, err := c.do(ctx, http.MethodGet, "/api/v1/admin/peers", "", nil, true)
	if err != nil {
		return err
	}
	if a.status != http.StatusUnauthorized || !strings.HasPrefix(a.hdr.Get("Content-Type"), "application/json") {
		return fmt.Errorf("non-data /api/v1/admin/peers: HTTP %d %s, want 401 application/json", a.status, a.hdr.Get("Content-Type"))
	}
	fmt.Printf("     GET /api/v1/admin/peers: 401 %s (unchanged)\n", a.hdr.Get("Content-Type"))
	return nil
}

func (c client) recordMiss(ctx context.Context) error {
	a, err := c.do(ctx, http.MethodGet, "/api/v1/data/records/OMM.fbs/bafkreimissingrecord", "", nil, false)
	if err != nil {
		return err
	}
	e, err := oneQRP(a, "Error")
	if err != nil {
		return err
	}
	if a.status != http.StatusNotFound {
		return fmt.Errorf("HTTP %d, want 404", a.status)
	}
	fmt.Printf("     404 $QRP Error %s\n", e.ERROR_CODE())
	return nil
}
