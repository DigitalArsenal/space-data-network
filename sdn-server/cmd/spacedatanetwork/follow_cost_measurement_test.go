package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
)

// followCostLane is one lane a follower subscribes to in the measurement.
type followCostLane struct{ schema, provider, source string }

// followCostLanes are the seven lanes a follower subscribes to from a
// producer holding the catalog node's store: two lanes of millions of
// records (one current batch of about 33,000 every three hours) and five
// lanes of one current batch each.
var followCostLanes = []followCostLane{
	{"OMM.fbs", "space-data-network-02", "celestrak-gp"},
	{"MPE.fbs", "space-data-network-02", "celestrak-gp"},
	{"SPW.fbs", "space-weather", "gfz-noaa"},
	{"SPW.fbs", "space-data-network-02", "celestrak-space-weather"},
	{"CAT.fbs", "space-data-network-02", "celestrak-satcat"},
	{"CAT.fbs", "space-data-network-02", "celestrak-satcat-csv"},
	{"RFB.fbs", "space-data-network-02", "satnogs-db"},
}

// TestFollowCostMeasurement measures what a producer holding a store of
// millions of records spends on its followers: the time and CPU of one lane
// statement per lane, what it goes on spending after the follower gives up,
// and what seven subscriptions retrying do to it over a period. It is opt-in
// because it needs a format-5 store of that size:
//
//	SDN_FOLLOW_COST_STORE   the store to copy (an APFS or reflink copy; never used in place)
//	SDN_FOLLOW_COST_REPORT  the file the measurement lines are appended to
//	SDN_FOLLOW_COST_PHASES  statement, storm or both (default "statement,storm")
//	SDN_FOLLOW_COST_STORM   how long the seven subscriptions run (default 15m)
//
// The producer runs with GOMAXPROCS=4. A binary built with the tag
// sdn_measure_pprof and SDN_MEASURE_PPROF_ADDR set also reports its
// goroutines; any other build reports them as -1.
func TestFollowCostMeasurement(t *testing.T) {
	src := os.Getenv("SDN_FOLLOW_COST_STORE")
	if src == "" {
		t.Skip("set SDN_FOLLOW_COST_STORE to a format-5 store to copy")
	}
	reportPath := os.Getenv("SDN_FOLLOW_COST_REPORT")
	if reportPath == "" {
		t.Fatal("SDN_FOLLOW_COST_REPORT names the measurement file")
	}
	report, err := os.OpenFile(reportPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	var reportMu sync.Mutex
	logf := func(format string, args ...any) {
		line := time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...)
		reportMu.Lock()
		fmt.Fprintln(report, line)
		reportMu.Unlock()
		t.Log(line)
	}
	phases := os.Getenv("SDN_FOLLOW_COST_PHASES")
	if phases == "" {
		phases = "statement,storm"
	}
	storm := 15 * time.Minute
	if v := os.Getenv("SDN_FOLLOW_COST_STORM"); v != "" {
		if storm, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}

	exe := publicationBinary(t)
	producer := newPublicationDaemon(t, exe, "cost-producer", "", "", "keep-all")
	if out, err := exec.Command("cp", "-c", "-R", src, producer.store).CombinedOutput(); err != nil {
		t.Fatalf("copy the store: %v: %s", err, out)
	}
	// The copied store is past the test config's 32 GB storage limit once
	// it grows.
	raw, err := os.ReadFile(producer.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(string(raw), "  max_size: 32GB", "  max_size: 512GB", 1)
	// The local publisher's quota (100 MB by default) bounds what grows it.
	cfg = strings.Replace(cfg, "publishing:\n  enabled: true\n", "publishing:\n  enabled: true\n  default_quota_bytes: 200000000000\n", 1)
	if err := os.WriteFile(producer.cfg, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	pprof := freeLoopbackPort(t)
	producer.env = append(producer.env, "GOMAXPROCS=4", fmt.Sprintf("SDN_MEASURE_PPROF_ADDR=127.0.0.1:%d", pprof))
	if ms := os.Getenv("SDN_FOLLOW_COST_SLOW_MS"); ms != "" {
		producer.env = append(producer.env, "SDN_FLATSQL_SLOW_QUERY_MS="+ms)
	}
	began := time.Now()
	producer.start(t)
	logf("producer ready in %s on a copy of %s, binary %s, pid %d", time.Since(began).Round(time.Second), src, exe, producer.cmd.Process.Pid)
	// SDN_FOLLOW_COST_FOLLOWER_BINARY runs the follower on another build
	// (a release before the producer's, or after it).
	followerExe := exe
	if v := os.Getenv("SDN_FOLLOW_COST_FOLLOWER_BINARY"); v != "" {
		followerExe = v
	}
	follower := newPublicationDaemon(t, followerExe, "cost-follower", producer.address, producer.id, "keep-all")
	follower.start(t)
	logf("follower ready, binary %s, pid %d", followerExe, follower.cmd.Process.Pid)

	pid := producer.cmd.Process.Pid
	sample := func() followCostSample { return followCostSampleOf(pid, pprof) }
	// The store's own background work after a start runs at a steady rate
	// for a while; the producer's work for a request is what it spends
	// above that rate. It is measured over a minute once the start settles.
	time.Sleep(2 * time.Minute)
	b0 := sample()
	time.Sleep(time.Minute)
	b1 := sample()
	baseline := float64(b1.cpu-b0.cpu) / float64(b1.at.Sub(b0.at))
	logf("producer background rate %.0f%% of one core (%s)", 100*baseline, b1)
	idle := func(budget time.Duration) (followCostSample, bool) {
		deadline := time.Now().Add(budget)
		last := sample()
		for time.Now().Before(deadline) {
			time.Sleep(5 * time.Second)
			now := sample()
			// Idle: within 30% of one core of the background rate over
			// 5 s (one engine read is a whole core).
			if float64(now.cpu-last.cpu)/float64(now.at.Sub(last.at)) < baseline+0.3 {
				return now, true
			}
			last = now
		}
		return last, false
	}
	above := func(from, to followCostSample) time.Duration {
		return max(to.cpu-from.cpu-time.Duration(baseline*float64(to.at.Sub(from.at))), 0)
	}

	if grow := os.Getenv("SDN_FOLLOW_COST_GROW"); grow != "" {
		// Grow the OMM lane by this many synthetic records in batches of
		// 33,000 (the catalog's three-hourly batch), then keep the store as
		// SDN_FOLLOW_COST_KEEP (an APFS copy) for the measurements.
		n, err := strconv.Atoi(grow)
		if err != nil || n <= 0 {
			t.Fatal("SDN_FOLLOW_COST_GROW is a record count")
		}
		keep := os.Getenv("SDN_FOLLOW_COST_KEEP")
		if keep == "" {
			t.Fatal("SDN_FOLLOW_COST_KEEP names where the grown store is kept")
		}
		const batch = 33_000
		began := time.Now()
		for b := 0; b*batch < n; b++ {
			var body bytes.Buffer
			for i := 0; i < batch && b*batch+i < n; i++ {
				body.Write(baselineOMM(50_000_000 + b*batch + i))
			}
			q := url.Values{"provider_id": {"space-data-network-02"}, "source_name": {"celestrak-gp"}, "batch_id": {fmt.Sprintf("grow-%04d", b)}}
			raw, status, err := followCostRequest(producer, "POST", "/api/v1/data/publish/batch/OMM.fbs?"+q.Encode(), publicationStreamType, body.Bytes())
			if err != nil || status != http.StatusCreated {
				t.Fatalf("grow batch %d: HTTP %d %v %.300q", b, status, err, raw)
			}
			var response struct {
				Results []struct {
					CID   string `json:"cid"`
					Error string `json:"error"`
				} `json:"results"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatalf("grow batch %d: %v", b, err)
			}
			for _, r := range response.Results {
				if r.Error != "" || r.CID == "" {
					t.Fatalf("grow batch %d: a record was refused: %s", b, r.Error)
				}
			}
			if b%20 == 19 {
				done := (b + 1) * batch
				logf("grew the OMM lane by %d records in %s (%.0f records/s)", done, time.Since(began).Round(time.Second), float64(done)/time.Since(began).Seconds())
			}
		}
		logf("grew the OMM lane by %d records in %s", n, time.Since(began).Round(time.Second))
		follower.stop(t)
		producer.stop(t)
		if out, err := exec.Command("cp", "-c", "-R", producer.store, keep).CombinedOutput(); err != nil {
			t.Fatalf("keep the grown store: %v: %s", err, out)
		}
		logf("kept the grown store as %s", keep)
		return
	}

	if strings.Contains(phases, "statement") {
		for _, lane := range followCostLanes {
			before, _ := idle(15 * time.Minute)
			q := url.Values{"schema": {lane.schema}, "provider_id": {lane.provider}, "source": {lane.source}, "from": {producer.id}, "start": {"current"}}
			asked := time.Now()
			raw, status, err := followCostRequest(follower, "GET", "/api/v1/sync/plan?"+q.Encode(), "", nil)
			answered := time.Since(asked)
			var plan api.LanePlan
			_ = json.Unmarshal(raw, &plan)
			after, ok := idle(30 * time.Minute)
			logf("statement %s %s/%s: answered in %s (HTTP %d, %v): batch %q %d records, lane %d records, error %q; producer CPU %s above its background rate, busy %s after the request (idle %v), %s",
				lane.schema, lane.provider, lane.source, answered.Round(10*time.Millisecond), status, err, plan.BatchID, plan.BatchRecords, plan.LaneRecords,
				plan.StatementError, above(before, after).Round(10*time.Millisecond), after.at.Sub(asked).Round(time.Second), ok, after)
		}
	}

	// publishLane publishes the producer's current batch of the OMM lane,
	// as its three-hourly catalog flow does, and logs how long it took and
	// the engine reads logged meanwhile.
	publishLane := func(label string) {
		q := url.Values{"schema": {"OMM.fbs"}, "provider_id": {"space-data-network-02"}, "source": {"celestrak-gp"}, "from": {producer.id}, "start": {"current"}}
		raw, _, _ := followCostRequest(follower, "GET", "/api/v1/sync/plan?"+q.Encode(), "", nil)
		var plan api.LanePlan
		_ = json.Unmarshal(raw, &plan)
		body, _ := json.Marshal(map[string]any{"schema": "OMM.fbs", "providerId": "space-data-network-02", "sourceName": "celestrak-gp",
			"batchId": plan.BatchID, "chunkSize": 50000, "fullCatalog": true})
		before := len(readFileString(producer.logPath))
		asked := time.Now()
		raw, status, err := followCostRequest(producer, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", body)
		took := time.Since(asked)
		ops := slowOperation.FindAllString(readFileString(producer.logPath)[before:], -1)
		logf("%s: publication of OMM batch %q in %s: HTTP %d %v %.300q; %d engine reads logged", label, plan.BatchID, took.Round(10*time.Millisecond),
			status, err, raw, len(ops))
		for _, op := range ops {
			logf("%s: %s", label, op)
		}
	}
	if strings.Contains(phases, "publish") {
		publishLane("publication without followers")
	}

	if strings.Contains(phases, "storm") {
		if strings.Contains(phases, "stormpublish") {
			go func() {
				time.Sleep(5 * time.Minute)
				publishLane("publication during the storm")
			}()
		}
		before := sample()
		logf("storm: seven subscriptions for %s; producer before: %s", storm, before)
		var wg sync.WaitGroup
		for _, lane := range followCostLanes {
			wg.Add(1)
			go func(lane followCostLane) {
				defer wg.Done()
				asked := time.Now()
				raw, status, err := followCostRequest(follower, "POST", "/api/v1/sync?start=current&force=1", publicationStreamType,
					api.EncodeDSSFollow(strings.TrimSuffix(lane.schema, ".fbs"), lane.provider, lane.source, producer.id))
				logf("subscribed %s %s/%s in %s: HTTP %d %v %.300q", lane.schema, lane.provider, lane.source, time.Since(asked).Round(10*time.Millisecond), status, err, raw)
			}(lane)
		}
		wg.Wait()
		end := time.Now().Add(storm)
		peak := before
		for time.Now().Before(end) {
			time.Sleep(30 * time.Second)
			s := sample()
			if s.rss > peak.rss {
				peak.rss = s.rss
			}
			if s.goroutines > peak.goroutines {
				peak.goroutines = s.goroutines
			}
			logf("storm sample: %s", s)
		}
		after := sample()
		span := after.at.Sub(before.at)
		logf("storm: producer CPU %s over %s (%.0f%% of one core; background rate %.0f%%), peak RSS %d MiB, peak goroutines %d", (after.cpu - before.cpu).Round(time.Second),
			span.Round(time.Second), 100*float64(after.cpu-before.cpu)/float64(span), 100*baseline, peak.rss>>20, peak.goroutines)
		raw, _, _ := followCostRequest(follower, "GET", "/api/v1/sync/subscriptions", "", nil)
		var subs struct {
			Subscriptions []api.LaneFollow `json:"subscriptions"`
		}
		_ = json.Unmarshal(raw, &subs)
		for _, f := range subs.Subscriptions {
			logf("storm end: %s %s/%s connected %v, received %d records, stored %d, producer batch %q (%d), last error %.300q", f.Schema, f.ProviderID,
				f.SourceName, f.Connected, f.ReceivedRecords, f.StoredRecords, f.StatedBatch, f.StatedRecords, f.Error)
		}
		text := readFileString(follower.logPath)
		for _, needle := range []string{"read the producer's statement of the lane", "stream open", "i/o deadline reached", "stream reset", "reconnecting in", "busy"} {
			logf("follower log: %d lines with %q", strings.Count(text, needle), needle)
		}
		ptext := readFileString(producer.logPath)
		for _, needle := range []string{"slow operation: op=HEAD", "slow operation: op=SCAN", "busy"} {
			logf("producer log: %d lines with %q", strings.Count(ptext, needle), needle)
		}
	}
}

// followCostSample is the producer process at one moment.
type followCostSample struct {
	at         time.Time
	cpu        time.Duration
	rss        int64
	goroutines int
	threads    int
}

func (s followCostSample) String() string {
	return fmt.Sprintf("cpu %s, rss %d MiB, goroutines %d, threads %d", s.cpu.Round(10*time.Millisecond), s.rss>>20, s.goroutines, s.threads)
}

func followCostSampleOf(pid, pprof int) followCostSample {
	s := followCostSample{at: time.Now(), goroutines: -1}
	s.cpu = psCPUTime(processField(pid, "time"))
	if kb, err := strconv.ParseInt(strings.TrimSpace(processField(pid, "rss")), 10, 64); err == nil {
		s.rss = kb << 10
	}
	if out, err := exec.Command("ps", "-M", "-p", strconv.Itoa(pid)).Output(); err == nil {
		s.threads = max(strings.Count(string(out), "\n")-1, 0)
	}
	c := http.Client{Timeout: 5 * time.Second}
	if res, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/goroutines", pprof)); err == nil {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			s.goroutines = n
		}
	}
	return s
}

// psCPUTime parses ps's cumulative CPU time ([[dd-]hh:]mm:ss.cc).
func psCPUTime(v string) time.Duration {
	v = strings.TrimSpace(v)
	var days int64
	if i := strings.Index(v, "-"); i >= 0 {
		days, _ = strconv.ParseInt(v[:i], 10, 64)
		v = v[i+1:]
	}
	parts := strings.Split(v, ":")
	var secs float64
	for _, p := range parts {
		f, _ := strconv.ParseFloat(p, 64)
		secs = secs*60 + f
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs*float64(time.Second))
}

// followCostRequest is one admin request whose failure is measured, not fatal.
func followCostRequest(d *publicationDaemon, method, path, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequest(method, d.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	return raw, res.StatusCode, err
}
