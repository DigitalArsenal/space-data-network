// dataset-retract-e2e boots a node built from this checkout on a fresh data
// directory with its own Kubo, seeds two sensor-network lanes of two providers
// (SIT.fbs sites, as a published network is), publishes both, and proves one
// loopback call withdraws one of them and nothing else:
//
//	a. both lanes publish; the source list, the channel list and a data query
//	   show both, and Kubo pins the first lane's CIDs;
//	b. the retract DRY RUN names the lane's series, its record count and every
//	   pinned CID, and changes nothing (same lists, same pins);
//	c. the retract APPLY unpins every CID the dry run named, removes the
//	   lane's records, and marks the lane retracted;
//	d. the source list, the channel list and a data query no longer show the
//	   lane;
//	e. the other provider's lane is untouched, field by field: its source row,
//	   its channel row, its records byte for byte, its pins;
//	f. the node refuses to publish the retracted lane again, and a second
//	   apply finishes with nothing left to remove;
//	g. the route is loopback only and refuses a request that names no lane.
//
//	dataset-retract-e2e --binary <node>
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
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
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/SIT"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	schema    = "SIT.fbs"
	providerA = "sdn-ssa-e2e-alpha"
	providerB = "sdn-ssa-e2e-beta"
	laneAName = "e2e-sites-alpha"
	laneBName = "e2e-sites-beta"
	// The records carry the tags a module's ingest gives them (the producer
	// is the provider when none is stated).
	batch = "e2e-batch-1"
)

var failures []string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Printf("DATASET-RETRACT-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DATASET-RETRACT-E2E PASS")
}

func check(name string, err error) {
	if err != nil {
		failures = append(failures, name)
		fmt.Printf("FAIL %s: %v\n", name, err)
		return
	}
	fmt.Printf("PASS %s\n", name)
}

type lane struct {
	provider, source string
	sites            []string
}

var (
	laneA = lane{providerA, laneAName, []string{"Alpha One", "Alpha Two", "Alpha Three"}}
	laneB = lane{providerB, laneBName, []string{"Beta One", "Beta Two"}}
)

func (l lane) channelID() string { return l.provider + "-SIT" }

func site(name string, i int) []byte {
	b := flatbuffers.NewBuilder(256)
	id := b.CreateString(fmt.Sprintf("%s-%d", name, i))
	n := b.CreateString(name)
	network := b.CreateString("e2e-network")
	SIT.SITStart(b)
	SIT.SITAddID(b, id)
	SIT.SITAddNAME(b, n)
	SIT.SITAddNETWORK(b, network)
	SIT.SITAddLATITUDE(b, 10+float32(i))
	SIT.SITAddLONGITUDE(b, 20+float32(i))
	SIT.SITAddALTITUDE(b, 100)
	SIT.FinishSITBuffer(b, SIT.SITEnd(b))
	return b.FinishedBytes()
}

func (l lane) records() [][]byte {
	out := make([][]byte, len(l.sites))
	for i, name := range l.sites {
		out[i] = site(name, i)
	}
	return out
}

func run(ctx context.Context) error {
	binary := flag.String("binary", "", "node binary to boot")
	flag.Parse()
	if *binary == "" {
		return errors.New("--binary is required")
	}
	root, err := os.MkdirTemp("", "dataset-retract-e2e-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := seed(root); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	n, err := prepare(*binary, root)
	if err != nil {
		return err
	}
	defer n.cleanup()
	if err := n.start(ctx); err != nil {
		return err
	}
	c := client{base: n.base}
	k := kubo{base: n.kubo}

	check("a lane alpha publishes", c.publish(ctx, laneA))
	check("a lane beta publishes", c.publish(ctx, laneB))
	before, err := c.state(ctx, k)
	if err != nil {
		return fmt.Errorf("state before: %w", err)
	}
	check("a both lanes are listed (sources, channels, query, pins)", before.shows(laneA, laneB))

	var dry retractReport
	check("b dry run names series, records and pinned CIDs", func() error {
		var err error
		dry, err = c.retract(ctx, laneA, false)
		if err != nil {
			return err
		}
		if dry.Applied || dry.AlreadyRetracted || len(dry.RetiredSeries) == 0 || dry.Records != int64(len(laneA.sites)) || len(dry.Unpin) < 3 {
			return fmt.Errorf("dry run answered %+v", dry)
		}
		pins, err := k.pins(ctx)
		if err != nil {
			return err
		}
		for _, p := range dry.Unpin {
			if pins[p.CID] == "" {
				return fmt.Errorf("the dry run names %s %s, which kubo does not pin", p.Role, p.CID)
			}
		}
		fmt.Printf("     dry run: %d series, %d records, %d pinned CIDs (%d bytes)\n", len(dry.RetiredSeries), dry.Records, len(dry.Unpin), dry.UnpinBytes)
		return nil
	}())
	check("b dry run changed nothing", func() error {
		after, err := c.state(ctx, k)
		if err != nil {
			return err
		}
		return before.equal(after)
	}())

	check("c apply unpins every named CID, removes the records, marks the lane", func() error {
		got, err := c.retract(ctx, laneA, true)
		if err != nil {
			return err
		}
		if !got.Applied || got.RetractedAtMs == 0 || got.RecordsRemoved != int64(len(laneA.sites)) || got.UnpinFailed != 0 || got.Unpinned != int64(len(dry.Unpin)) {
			return fmt.Errorf("apply answered %+v", got)
		}
		pins, err := k.pins(ctx)
		if err != nil {
			return err
		}
		for _, p := range dry.Unpin {
			if pins[p.CID] != "" {
				return fmt.Errorf("%s %s is still pinned (%s)", p.Role, p.CID, pins[p.CID])
			}
		}
		fmt.Printf("     applied: %d CIDs unpinned, %d records removed\n", got.Unpinned, got.RecordsRemoved)
		return nil
	}())

	after, err := c.state(ctx, k)
	if err != nil {
		return fmt.Errorf("state after: %w", err)
	}
	check("d the lane is gone from sources, channels and query", after.gone(laneA))
	check("e the other provider's lane is untouched, field by field", before.sameLane(after, laneB))

	check("f the node refuses to publish the retracted lane", func() error {
		status, body, err := c.post(ctx, "/api/v1/admin/dataset-updates/publish", map[string]any{"schema": schema, "providerId": laneA.provider, "sourceName": laneA.source}, false)
		if err != nil {
			return err
		}
		if status < 400 || !strings.Contains(string(body), "retracted") {
			return fmt.Errorf("publish of the retracted lane answered HTTP %d %s", status, clip(body))
		}
		return nil
	}())
	check("f a second apply finds nothing left", func() error {
		got, err := c.retract(ctx, laneA, true)
		if err != nil {
			return err
		}
		if !got.Applied || !got.AlreadyRetracted || got.Records != 0 || got.RecordsRemoved != 0 || len(got.Unpin) != 0 {
			return fmt.Errorf("second apply answered %+v", got)
		}
		return nil
	}())
	check("g the route is loopback only and names one lane", func() error {
		status, _, body, err := c.postQuery(ctx, "/api/v1/admin/dataset-updates/retract", url.Values{"schema": {schema}, "providerId": {laneB.provider}, "sourceName": {laneB.source}, "apply": {"true"}}, true)
		if err != nil {
			return err
		}
		// With the auth wall on, a request with a proxy header is no
		// loopback client: the wall refuses it before the handler (the
		// handler's own gate answers 403 to a non-loopback address).
		if status != http.StatusUnauthorized {
			return fmt.Errorf("a proxied request answered HTTP %d %s, want 401", status, clip(body))
		}
		status, _, body, err = c.postQuery(ctx, "/api/v1/admin/dataset-updates/retract", url.Values{"schema": {schema}, "providerId": {laneB.provider}, "apply": {"true"}}, false)
		if err != nil {
			return err
		}
		if status != http.StatusBadRequest {
			return fmt.Errorf("a request with no sourceName answered HTTP %d %s, want 400", status, clip(body))
		}
		after2, err := c.state(ctx, k)
		if err != nil {
			return err
		}
		return after.equal(after2)
	}())

	if len(failures) > 0 {
		return fmt.Errorf("%d check(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func seed(root string) error {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	store, err := storage.NewFlatSQLStore(root, validator)
	if err != nil {
		return err
	}
	for _, l := range []lane{laneA, laneB} {
		tags := storage.SourceTags{ProviderID: l.provider, SourceName: l.source, BatchID: batch}
		for _, rec := range l.records() {
			if _, err := store.StoreWithSourceTags(schema, rec, "dataset-retract-e2e", nil, tags); err != nil {
				_ = store.Close()
				return err
			}
		}
	}
	return store.Close()
}

// ---------------------------------------------------------------------------
// client

type client struct{ base string }

var httpClient = &http.Client{Timeout: 5 * time.Minute}

func (c client) post(ctx context.Context, path string, body any, proxied bool) (int, []byte, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if proxied {
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, out, err
}

func (c client) get(ctx context.Context, path string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out, err
}

func (c client) publish(ctx context.Context, l lane) error {
	status, body, err := c.post(ctx, "/api/v1/admin/dataset-updates/publish", map[string]any{"schema": schema, "providerId": l.provider, "sourceName": l.source}, false)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return fmt.Errorf("publish: HTTP %d %s", status, clip(body))
	}
	var result struct {
		RecordCount int    `json:"recordCount"`
		ManifestCID string `json:"manifestCid"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ManifestCID == "" || result.RecordCount != len(l.sites) {
		return fmt.Errorf("publish answered %s", clip(body))
	}
	return nil
}

// retractReport is the $QRP page the retract route answers, one row per fact
// (kind, key, detail, count).
type retractReport struct {
	Applied          bool
	AlreadyRetracted bool
	RetiredSeries    []string
	Records          int64
	RecordsRemoved   int64
	Unpin            []struct{ CID, Role string }
	UnpinBytes       int64
	Unpinned         int64
	UnpinFailed      int64
	// RetractedAtMs is 0 when the lane is not marked.
	RetractedAtMs int64
}

// retract calls the route: the lane in the query string, no body; a dry run
// unless apply. The answer is one $QRP Page; an error is one $QRP Error.
func (c client) retract(ctx context.Context, l lane, apply bool) (retractReport, error) {
	var out retractReport
	status, hdr, body, err := c.postQuery(ctx, "/api/v1/admin/dataset-updates/retract", url.Values{"schema": {schema}, "providerId": {l.provider}, "sourceName": {l.source}, "apply": {fmt.Sprint(apply)}}, false)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("retract: HTTP %d %s", status, clip(body))
	}
	if ct := hdr.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
		return out, fmt.Errorf("retract answered Content-Type %q, want the $QRP stream", ct)
	}
	fs, err := frames(body)
	if err != nil || len(fs) != 1 {
		return out, fmt.Errorf("retract answered %d frames %v, want one $QRP page", len(fs), err)
	}
	q := QRP.GetRootAsQRP(fs[0], 0)
	if QRP.EnumNamesqrpKind[q.KIND()] != "Page" {
		return out, fmt.Errorf("retract answered a %s frame", QRP.EnumNamesqrpKind[q.KIND()])
	}
	for r := 0; r < q.ROWSLength(); r++ {
		var row QRP.QRPRow
		q.ROWS(&row, r)
		var kind, key, detail QRP.QRPValue
		var count QRP.QRPValue
		row.CELLS(&kind, 0)
		row.CELLS(&key, 1)
		row.CELLS(&detail, 2)
		row.CELLS(&count, 3)
		k, name, n := string(kind.TEXT_VALUE()), string(key.TEXT_VALUE()), count.INT_VALUE()
		switch k {
		case "total":
			switch name {
			case "applied":
				out.Applied = n == 1
			case "already-retracted":
				out.AlreadyRetracted = n == 1
			case "records":
				out.Records = n
			case "records-removed":
				out.RecordsRemoved = n
			case "unpin-bytes":
				out.UnpinBytes = n
			case "unpinned":
				out.Unpinned = n
			case "unpin-failed":
				out.UnpinFailed = n
			case "retracted-at-ms":
				out.RetractedAtMs = n
			}
		case "series":
			out.RetiredSeries = append(out.RetiredSeries, name)
		case "pin":
			out.Unpin = append(out.Unpin, struct{ CID, Role string }{name, string(detail.TEXT_VALUE())})
		}
	}
	return out, nil
}

func (c client) postQuery(ctx context.Context, path string, query url.Values, proxied bool) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path+"?"+query.Encode(), nil)
	if err != nil {
		return 0, nil, nil, err
	}
	if proxied {
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out, err
}

// state is what the node shows of every lane: its sources, its channels, its
// records by data query, its pins.
type state struct {
	sources  map[string]int64 // source name -> records, SIT.fbs
	channels map[string]map[string]any
	records  map[string][][]byte // source name -> record bytes
	pins     map[string]string
}

func (c client) state(ctx context.Context, k kubo) (state, error) {
	s := state{sources: map[string]int64{}, channels: map[string]map[string]any{}, records: map[string][][]byte{}}
	status, hdr, body, err := c.get(ctx, "/api/v1/data/sources")
	if err != nil || status != http.StatusOK {
		return s, fmt.Errorf("sources: HTTP %d %v", status, err)
	}
	fs, err := frames(body)
	if err != nil || len(fs) != 1 {
		return s, fmt.Errorf("sources (%s): %d frames %v", hdr.Get("Content-Type"), len(fs), err)
	}
	q := QRP.GetRootAsQRP(fs[0], 0)
	idx := map[string]int{}
	var col QRP.QRPColumn
	for i := 0; i < q.COLUMNSLength(); i++ {
		q.COLUMNS(&col, i)
		idx[string(col.NAME())] = i
	}
	for r := 0; r < q.ROWSLength(); r++ {
		var row QRP.QRPRow
		q.ROWS(&row, r)
		cell := func(name string) *QRP.QRPValue {
			var v QRP.QRPValue
			row.CELLS(&v, idx[name])
			return &v
		}
		if string(cell("schema").TEXT_VALUE()) == schema {
			s.sources[string(cell("source_name").TEXT_VALUE())] += cell("count").INT_VALUE()
		}
	}
	status, _, body, err = c.get(ctx, "/api/v1/channels?standard=SIT")
	if err != nil || status != http.StatusOK {
		return s, fmt.Errorf("channels: HTTP %d %v %s", status, err, clip(body))
	}
	var list struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return s, err
	}
	for _, row := range list.Results {
		if id, _ := row["channelId"].(string); id != "" {
			s.channels[id] = scrub(row)
		}
	}
	for _, l := range []lane{laneA, laneB} {
		recs, err := c.query(ctx, l)
		if err != nil {
			return s, err
		}
		s.records[l.source] = recs
	}
	s.pins, err = k.pins(ctx)
	return s, err
}

// volatile keys: clocks and ages that move between two reads.
var volatile = regexp.MustCompile(`(?i)(At|Ms|Time|Age|Elapsed|Seconds)$`)

func scrub(row map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range row {
		if volatile.MatchString(k) {
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			v = scrub(nested)
		}
		out[k] = v
	}
	return out
}

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

// query reads a lane's records through POST /api/v1/data/query.
func (c client) query(ctx context.Context, l lane) ([][]byte, error) {
	frame := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema, SourceName: l.source, Limit: 100})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/data/query", bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query %s: HTTP %d %s", l.source, resp.StatusCode, clip(body))
	}
	fs, err := frames(body)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for k := 0; k < len(fs); {
		h := QRP.GetRootAsQRP(fs[k], 0)
		n := int(h.RECORD_COUNT())
		k++
		if k+n > len(fs) {
			return nil, fmt.Errorf("a run declares %d records, %d frames remain", n, len(fs)-k)
		}
		out = append(out, fs[k:k+n]...)
		k += n
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out, nil
}

func (s state) shows(ls ...lane) error {
	for _, l := range ls {
		if s.sources[l.source] != int64(len(l.sites)) {
			return fmt.Errorf("sources list %s with %d records, want %d", l.source, s.sources[l.source], len(l.sites))
		}
		if s.channels[l.channelID()] == nil {
			return fmt.Errorf("the channel list has no %s", l.channelID())
		}
		if len(s.records[l.source]) != len(l.sites) {
			return fmt.Errorf("a data query of %s answers %d records, want %d", l.source, len(s.records[l.source]), len(l.sites))
		}
	}
	return nil
}

func (s state) gone(l lane) error {
	if n, ok := s.sources[l.source]; ok && n != 0 {
		return fmt.Errorf("the source list still offers %s (%d records)", l.source, n)
	}
	if s.channels[l.channelID()] != nil {
		return fmt.Errorf("the channel list still offers %s", l.channelID())
	}
	if len(s.records[l.source]) != 0 {
		return fmt.Errorf("a data query of %s still answers %d records", l.source, len(s.records[l.source]))
	}
	return nil
}

func (s state) sameLane(after state, l lane) error {
	if s.sources[l.source] != after.sources[l.source] {
		return fmt.Errorf("source row of %s: %d records before, %d after", l.source, s.sources[l.source], after.sources[l.source])
	}
	if !reflect.DeepEqual(s.channels[l.channelID()], after.channels[l.channelID()]) {
		before, _ := json.Marshal(s.channels[l.channelID()])
		now, _ := json.Marshal(after.channels[l.channelID()])
		return fmt.Errorf("channel row of %s changed:\nbefore %s\nafter  %s", l.channelID(), before, now)
	}
	if len(s.records[l.source]) != len(after.records[l.source]) {
		return fmt.Errorf("records of %s: %d before, %d after", l.source, len(s.records[l.source]), len(after.records[l.source]))
	}
	for i := range s.records[l.source] {
		if !bytes.Equal(s.records[l.source][i], after.records[l.source][i]) {
			return fmt.Errorf("record %d of %s changed", i, l.source)
		}
	}
	return nil
}

func (s state) equal(after state) error {
	for _, l := range []lane{laneA, laneB} {
		if err := s.sameLane(after, l); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(s.pins, after.pins) {
		return fmt.Errorf("the pin set changed: %d before, %d after", len(s.pins), len(after.pins))
	}
	return nil
}

// ---------------------------------------------------------------------------
// node

type node struct {
	binary string
	base   string
	kubo   string
	config string
	home   string
	cmd    *exec.Cmd
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// prepare writes the node's home and config: isolated, with the auth wall on
// and the loopback console admitted as the operator.
func prepare(binary, root string) (*node, error) {
	home, err := os.MkdirTemp("", "dataset-retract-e2e-home-")
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
		"storage:", fmt.Sprintf("  path: %q", root), "  max_size: 1GB",
		"peers:", "  strict_mode: false", "  enable_dht: false", "  enable_mdns: false",
		"admin:", "  enabled: true", fmt.Sprintf("  listen_addr: \"127.0.0.1:%d\"", port), "  require_auth: true", "  tls_mode: disabled",
		"  local_console: true", "  remote_requires_sealed: false",
		fmt.Sprintf("  ipfs_api_url: \"http://127.0.0.1:%d\"", kuboAPI), fmt.Sprintf("  ipfs_gateway_url: \"http://127.0.0.1:%d\"", kuboGateway),
		"publishing:", "  enabled: true", "  ipfs_schemas:", "    - " + schema,
		"flows:", "  mounts: []",
		"setup:", fmt.Sprintf("  data_path: %q", root), "",
	}, "\n")
	if err := os.WriteFile(config, []byte(yaml), 0o644); err != nil {
		return nil, err
	}
	return &node{
		binary: binary,
		base:   fmt.Sprintf("http://127.0.0.1:%d", port),
		kubo:   fmt.Sprintf("http://127.0.0.1:%d", kuboAPI),
		config: config,
		home:   home,
	}, nil
}

// start boots the node and waits for health 200.
func (n *node) start(ctx context.Context) error {
	logFile, err := os.OpenFile(filepath.Join(n.home, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(n.binary, "daemon", "--allow-multi-daemon", "--config", n.config)
	cmd.Dir, cmd.Stdout, cmd.Stderr = n.home, logFile, logFile
	cmd.Env = append(os.Environ(), "HOME="+n.home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	n.cmd = cmd
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t0 := time.Now()
	for time.Since(t0) < 5*time.Minute {
		select {
		case err := <-exited:
			tail, _ := os.ReadFile(filepath.Join(n.home, "daemon.log"))
			n.cmd = nil
			return fmt.Errorf("node exited before health (%v): %s", err, lastLines(tail, 8))
		case <-ctx.Done():
			n.stop()
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := http.Get(n.base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Printf("PASS boot pid=%d %s (Kubo %s) after %.1f s\n", cmd.Process.Pid, n.base, n.kubo, time.Since(t0).Seconds())
				return nil
			}
		}
	}
	n.stop()
	return errors.New("no health 200 within 5 min")
}

// stop ends exactly the process group this harness started.
func (n *node) stop() {
	if n.cmd == nil || n.cmd.Process == nil {
		return
	}
	pgid := n.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	for i := 0; i < 120; i++ {
		if syscall.Kill(-pgid, 0) != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	n.cmd = nil
}

func (n *node) cleanup() {
	n.stop()
	if os.Getenv("DATASET_RETRACT_E2E_KEEP") == "" {
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

// kubo reads the node's Kubo RPC API directly.
type kubo struct{ base string }

func (k kubo) post(ctx context.Context, command string, query url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base+command+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// pins is Kubo's recursive and direct pin set: CID -> pin type.
func (k kubo) pins(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, kind := range []string{"recursive", "direct"} {
		data, status, err := k.post(ctx, "/api/v0/pin/ls", url.Values{"type": {kind}})
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("pin/ls %s: HTTP %d %s", kind, status, clip(data))
		}
		var listing struct {
			Keys map[string]struct{ Type string }
		}
		if err := json.Unmarshal(data, &listing); err != nil {
			return nil, fmt.Errorf("pin/ls %s: %w", kind, err)
		}
		for c, entry := range listing.Keys {
			out[c] = entry.Type
		}
	}
	return out, nil
}

func clip(body []byte) string {
	if len(body) > 300 {
		body = body[:300]
	}
	return strings.TrimSpace(string(body))
}
