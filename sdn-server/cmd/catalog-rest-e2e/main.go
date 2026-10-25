// catalog-rest-e2e boots a node built from this checkout on a seeded catalog
// (SGP4, SGP4XP and SDN-AP element sets, the SDN-AP ones with COVARIANCE) and
// proves GET /api/v1/data/catalog/current: both formats round-trip, the
// theory selector, the entity tag and 304, and the headers a shared cache
// needs, all read anonymously (a proxy header refuses the loopback console).
//
//	catalog-rest-e2e --binary <node>
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	schema     = "OMM.fbs"
	path       = "/api/v1/data/catalog/current"
	streamType = "application/vnd.sdn.flatbuffers.stream"
	provider   = "catalog-rest-e2e"
	peerID     = "catalog-rest-e2e"
)

type seeded struct {
	norad  uint32
	theory string
	lane   string
	epoch  time.Time
	cov    []float64
	data   []byte
	want   bool // in the current catalog
}

// buildOMM writes one element set of a theory; cov is the 21-value covariance
// of an SDN-AP set.
func buildOMM(norad uint32, theory string, epoch time.Time, cov []float64) []byte {
	b := flatbuffers.NewBuilder(1024)
	name := b.CreateString(fmt.Sprintf("OBJ-%d", norad))
	id := b.CreateString("2026-001A")
	ep := b.CreateString(epoch.Format("2006-01-02T15:04:05.000000"))
	center := b.CreateString("EARTH")
	orig := b.CreateString("catalog-rest-e2e")
	var cv flatbuffers.UOffsetT
	if len(cov) > 0 {
		OMM.OMMStartCOVARIANCEVector(b, len(cov))
		for i := len(cov) - 1; i >= 0; i-- {
			b.PrependFloat64(cov[i])
		}
		cv = b.EndVector(len(cov))
	}
	OMM.OMMStart(b)
	OMM.OMMAddOBJECT_NAME(b, name)
	OMM.OMMAddOBJECT_ID(b, id)
	OMM.OMMAddNORAD_CAT_ID(b, norad)
	OMM.OMMAddEPOCH(b, ep)
	OMM.OMMAddMEAN_MOTION(b, 15.5)
	OMM.OMMAddECCENTRICITY(b, 0.0001)
	OMM.OMMAddINCLINATION(b, 51.6)
	OMM.OMMAddCENTER_NAME(b, center)
	OMM.OMMAddORIGINATOR(b, orig)
	OMM.OMMAddMEAN_ELEMENT_THEORY(b, OMM.EnumValuesmeanElementSource[theory])
	if cv != 0 {
		OMM.OMMAddCOVARIANCE(b, cv)
	}
	OMM.FinishSizePrefixedOMMBuffer(b, OMM.OMMEnd(b))
	// The bare record, as the publish route stores it.
	return append([]byte(nil), b.FinishedBytes()[4:]...)
}

func covariance(seed float64) []float64 {
	out := make([]float64, 21)
	for i := range out {
		out[i] = seed*float64(i+1) + 0.125
	}
	return out
}

// fixture: ISS in all three theories (the SDN-AP set with covariance), one
// object with an older and a nearer SGP4 set, one with SGP4 only.
func fixture(now time.Time) []seeded {
	var out []seeded
	add := func(norad uint32, theory, lane string, ago time.Duration, cov []float64, want bool) {
		epoch := now.Add(-ago)
		out = append(out, seeded{norad, theory, lane, epoch, cov, buildOMM(norad, theory, epoch, cov), want})
	}
	add(25544, "SGP4", "celestrak-gp", 2*time.Hour, nil, true)
	add(25544, "SGP4XP", "supplemental", 3*time.Hour, nil, true)
	add(25544, "SDNAP", "sdn-ap", time.Hour, covariance(1e-3), true)
	add(43013, "SGP4", "celestrak-gp", 30*time.Hour, nil, false)
	add(43013, "SGP4", "celestrak-gp", 5*time.Hour, nil, true)
	add(43013, "SDNAP", "sdn-ap", 4*time.Hour, covariance(2e-3), true)
	add(99999, "SGP4", "celestrak-gp", 26*time.Hour, nil, true)
	return out
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Printf("CATALOG-REST-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("CATALOG-REST-E2E PASS")
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
	home, err := os.MkdirTemp("", "catalog-rest-e2e-home-")
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

type answer struct {
	status int
	hdr    http.Header
	body   []byte
}

func get(ctx context.Context, base, method, query string, hdr map[string]string) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path+query, nil)
	if err != nil {
		return answer{}, err
	}
	// A proxy hop refuses the loopback console session: the call is anonymous.
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	// The Go client would add gzip and hide Content-Length; ask for the bytes.
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range hdr {
		req.Header.Set(k, v)
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

func key(norad uint32, theory string) string { return fmt.Sprintf("%d/%s", norad, theory) }

func wanted(recs []seeded, theory string) map[string]seeded {
	out := map[string]seeded{}
	for _, r := range recs {
		if r.want && (theory == "" || r.theory == theory) {
			out[key(r.norad, r.theory)] = r
		}
	}
	return out
}

var strongTag = regexp.MustCompile(`^"[0-9a-f]{64}"$`)

// headers: the strong tag, public with s-maxage, a modified time.
func sharedCacheHeaders(a answer) error {
	if !strongTag.MatchString(a.hdr.Get("ETag")) {
		return fmt.Errorf("ETag %q is not a strong sha-256 tag", a.hdr.Get("ETag"))
	}
	cc := a.hdr.Get("Cache-Control")
	m := regexp.MustCompile(`s-maxage=(\d+)`).FindStringSubmatch(cc)
	if !strings.HasPrefix(cc, "public") || m == nil || strings.Contains(cc, "private") || strings.Contains(cc, "no-store") {
		return fmt.Errorf("Cache-Control %q is not public with s-maxage", cc)
	}
	if s, _ := strconv.Atoi(m[1]); s < 60 || s > 3600 {
		return fmt.Errorf("s-maxage %d outside 60..3600 s", s)
	}
	if !strings.Contains(cc, "stale-while-revalidate=") {
		return fmt.Errorf("Cache-Control %q lacks stale-while-revalidate", cc)
	}
	if _, err := http.ParseTime(a.hdr.Get("Last-Modified")); err != nil {
		return fmt.Errorf("Last-Modified %q: %v", a.hdr.Get("Last-Modified"), err)
	}
	if a.hdr.Get("Set-Cookie") != "" {
		return errors.New("a Set-Cookie on a cacheable answer")
	}
	return nil
}

func readFB(a answer) (map[string]*OMM.OMM, error) {
	if a.status != 200 || a.hdr.Get("Content-Type") != streamType {
		return nil, fmt.Errorf("HTTP %d Content-Type %q (%d bytes), want 200 %s", a.status, a.hdr.Get("Content-Type"), len(a.body), streamType)
	}
	if n, _ := strconv.Atoi(a.hdr.Get("Content-Length")); n != len(a.body) {
		return nil, fmt.Errorf("Content-Length %q for %d bytes", a.hdr.Get("Content-Length"), len(a.body))
	}
	fs, err := frames(a.body)
	if err != nil {
		return nil, err
	}
	out := map[string]*OMM.OMM{}
	for _, f := range fs {
		if len(f) < 8 || string(f[4:8]) != "$OMM" {
			return nil, errors.New("a frame is not a $OMM")
		}
		o := OMM.GetRootAsOMM(f, 0)
		out[key(o.NORAD_CAT_ID(), o.MEAN_ELEMENT_THEORY().String())] = o
	}
	if len(out) != len(fs) {
		return nil, fmt.Errorf("%d frames hold %d distinct (object, theory) pairs", len(fs), len(out))
	}
	return out, nil
}

func checkSet(got map[string]*OMM.OMM, want map[string]seeded, a answer) error {
	if len(got) != len(want) {
		return fmt.Errorf("%d records, want %d", len(got), len(want))
	}
	for k, w := range want {
		o, ok := got[k]
		if !ok {
			return fmt.Errorf("%s missing", k)
		}
		if string(o.EPOCH()) != w.epoch.Format("2006-01-02T15:04:05.000000") {
			return fmt.Errorf("%s epoch %s, want the nearest %s", k, o.EPOCH(), w.epoch.Format("2006-01-02T15:04:05.000000"))
		}
		if o.COVARIANCELength() != len(w.cov) {
			return fmt.Errorf("%s COVARIANCE length %d, want %d", k, o.COVARIANCELength(), len(w.cov))
		}
		for i := range w.cov {
			if o.COVARIANCE(i) != w.cov[i] {
				return fmt.Errorf("%s COVARIANCE[%d] %v, want %v", k, i, o.COVARIANCE(i), w.cov[i])
			}
		}
	}
	return nil
}

func run(ctx context.Context) error {
	binary_ := flag.String("binary", "", "node binary to boot")
	flag.Parse()
	if *binary_ == "" {
		return errors.New("--binary is required")
	}
	root, err := os.MkdirTemp("", "catalog-rest-e2e-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	recs := fixture(time.Now().UTC())
	if err := seed(root, recs); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	node, err := boot(ctx, *binary_, root)
	if err != nil {
		return err
	}
	defer node.stop()
	base := node.base

	var fbTag, jsonTag string
	check("FlatBuffers: one record per object and theory, nearest epoch, SDN-AP COVARIANCE intact", func() error {
		a, err := get(ctx, base, "GET", "", nil)
		if err != nil {
			return err
		}
		got, err := readFB(a)
		if err != nil {
			return err
		}
		fbTag = a.hdr.Get("ETag")
		return checkSet(got, wanted(recs, ""), a)
	}())
	check("FlatBuffers: shared-cache headers (strong ETag, public s-maxage, Last-Modified)", func() error {
		a, err := get(ctx, base, "GET", "", nil)
		if err != nil {
			return err
		}
		return sharedCacheHeaders(a)
	}())
	check("FB-JSON: the same records, field names of the IDL, COVARIANCE as numbers", func() error {
		a, err := get(ctx, base, "GET", "?format=json", nil)
		if err != nil {
			return err
		}
		if a.status != 200 || !strings.HasPrefix(a.hdr.Get("Content-Type"), "application/json") {
			return fmt.Errorf("HTTP %d Content-Type %q", a.status, a.hdr.Get("Content-Type"))
		}
		jsonTag = a.hdr.Get("ETag")
		if err := sharedCacheHeaders(a); err != nil {
			return err
		}
		var rows []map[string]any
		if err := json.Unmarshal(a.body, &rows); err != nil {
			return err
		}
		fa, err := get(ctx, base, "GET", "", nil)
		if err != nil {
			return err
		}
		fb, err := readFB(fa)
		if err != nil {
			return err
		}
		if len(rows) != len(fb) {
			return fmt.Errorf("%d JSON rows, %d FlatBuffers records", len(rows), len(fb))
		}
		for _, row := range rows {
			norad, _ := row["NORAD_CAT_ID"].(float64)
			theory, _ := row["MEAN_ELEMENT_THEORY"].(string)
			o, ok := fb[key(uint32(norad), theory)]
			if !ok {
				return fmt.Errorf("JSON row %v/%v has no FlatBuffers record", row["NORAD_CAT_ID"], row["MEAN_ELEMENT_THEORY"])
			}
			if row["EPOCH"] != string(o.EPOCH()) || row["OBJECT_NAME"] != string(o.OBJECT_NAME()) || row["MEAN_MOTION"] != o.MEAN_MOTION() {
				return fmt.Errorf("JSON row %v/%v differs from its FlatBuffers record", norad, theory)
			}
			cov, _ := row["COVARIANCE"].([]any)
			if len(cov) != o.COVARIANCELength() {
				return fmt.Errorf("JSON %v/%v COVARIANCE has %d values, FlatBuffers %d", norad, theory, len(cov), o.COVARIANCELength())
			}
			for i, v := range cov {
				if f, _ := v.(float64); math.Float64bits(f) != math.Float64bits(o.COVARIANCE(i)) {
					return fmt.Errorf("JSON %v/%v COVARIANCE[%d] %v, FlatBuffers %v", norad, theory, i, v, o.COVARIANCE(i))
				}
			}
		}
		return nil
	}())
	check("ETag: one per format, stable across calls", func() error {
		a, _ := get(ctx, base, "GET", "", nil)
		b, _ := get(ctx, base, "GET", "?format=json", nil)
		if fbTag == "" || jsonTag == "" || fbTag == jsonTag {
			return fmt.Errorf("FlatBuffers tag %q, JSON tag %q: want two different tags", fbTag, jsonTag)
		}
		if a.hdr.Get("ETag") != fbTag || b.hdr.Get("ETag") != jsonTag {
			return errors.New("a tag changed between calls with no publication")
		}
		return nil
	}())
	for _, f := range []struct{ name, query, tag string }{{"FlatBuffers", "", fbTag}, {"FB-JSON", "?format=json", jsonTag}} {
		f := f
		check("304 on If-None-Match: "+f.name+" (empty body, the cache headers kept)", func() error {
			tag := fbTag
			if f.name == "FB-JSON" {
				tag = jsonTag
			}
			a, err := get(ctx, base, "GET", f.query, map[string]string{"If-None-Match": tag})
			if err != nil {
				return err
			}
			if a.status != 304 || len(a.body) != 0 {
				return fmt.Errorf("HTTP %d with %d bytes, want 304 and none", a.status, len(a.body))
			}
			if a.hdr.Get("ETag") != tag {
				return fmt.Errorf("304 ETag %q, want %q", a.hdr.Get("ETag"), tag)
			}
			if err := sharedCacheHeaders(a); err != nil {
				return fmt.Errorf("on the 304: %v", err)
			}
			list, err := get(ctx, base, "GET", f.query, map[string]string{"If-None-Match": `"0000", ` + tag})
			if err != nil || list.status != 304 {
				return fmt.Errorf("a tag list naming the tag: HTTP %d %v", list.status, err)
			}
			other, err := get(ctx, base, "GET", f.query, map[string]string{"If-None-Match": `"` + strings.Repeat("0", 64) + `"`})
			if err != nil || other.status != 200 || len(other.body) == 0 {
				return fmt.Errorf("a different tag: HTTP %d %v, want 200 with a body", other.status, err)
			}
			return nil
		}())
	}
	check("HEAD: headers and no body", func() error {
		a, err := get(ctx, base, "HEAD", "", nil)
		if err != nil {
			return err
		}
		if a.status != 200 || len(a.body) != 0 || a.hdr.Get("ETag") != fbTag {
			return fmt.Errorf("HTTP %d, %d bytes, ETag %q", a.status, len(a.body), a.hdr.Get("ETag"))
		}
		return nil
	}())
	for _, theory := range []string{"SGP4", "SGP4XP", "SDNAP"} {
		theory := theory
		check("theory="+theory+": only that theory, both formats", func() error {
			a, err := get(ctx, base, "GET", "?theory="+theory, nil)
			if err != nil {
				return err
			}
			got, err := readFB(a)
			if err != nil {
				return err
			}
			if err := checkSet(got, wanted(recs, theory), a); err != nil {
				return err
			}
			if a.hdr.Get("ETag") == fbTag {
				return errors.New("a theory shares the all-theories tag")
			}
			j, err := get(ctx, base, "GET", "?format=json&theory="+strings.ToLower(theory), nil)
			if err != nil {
				return err
			}
			var rows []map[string]any
			if j.status != 200 || json.Unmarshal(j.body, &rows) != nil || len(rows) != len(got) {
				return fmt.Errorf("JSON: HTTP %d, %d rows, want %d", j.status, len(rows), len(got))
			}
			return nil
		}())
	}
	check("refusals are $QRP FlatBuffers and never cached: history parameter, bad theory, bad format, POST", func() error {
		for _, c := range []struct {
			method, query string
			want          int
		}{
			{"GET", "?epoch=2026-01-01T00:00:00Z", 400}, {"GET", "?day=2026-01-01", 400}, {"GET", "?theory=DSST", 400},
			{"GET", "?format=csv", 400}, {"POST", "", 401},
		} {
			a, err := get(ctx, base, c.method, c.query, nil)
			if err != nil {
				return err
			}
			if a.status != c.want || !strings.Contains(a.hdr.Get("Cache-Control"), "no-store") || len(a.body) < 12 || string(a.body[8:12]) != "$QRP" {
				return fmt.Errorf("%s %s: HTTP %d Cache-Control %q, want %d no-store $QRP", c.method, c.query, a.status, a.hdr.Get("Cache-Control"), c.want)
			}
		}
		return nil
	}())
	if len(failures) > 0 {
		return fmt.Errorf("%d check(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}
