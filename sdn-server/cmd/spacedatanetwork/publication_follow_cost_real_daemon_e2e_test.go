package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
)

// slowOperation is the engine's line for a read past SDN_FLATSQL_SLOW_QUERY_MS.
var slowOperation = regexp.MustCompile(`FlatSQL slow operation: op=(\w+) .* rows_examined=(\d+)`)

// readsOfAtLeast are the engine reads logged in text that examined at
// least n rows.
func readsOfAtLeast(text string, n int64) []string {
	var out []string
	for _, m := range slowOperation.FindAllStringSubmatch(text, -1) {
		if rows, _ := strconv.ParseInt(m[2], 10, 64); rows >= n {
			out = append(out, m[0])
		}
	}
	return out
}

// TestPublicationFollowStatementCostRealDaemons: a lane's statement costs
// the producer its counters, not its records. The producer holds a lane of
// 30,000 records in ten batches (a lane of several batches, as a keep-all
// catalog lane is) and logs every engine read with the rows it examined
// (SDN_FLATSQL_SLOW_QUERY_MS=1). A statement that counted the lane or read
// its sequence head through a HEAD examined every one of its rows, twice;
// here no engine read during the statement examines as many rows as the
// lane holds, and the statement states the lane's 30,000 records and the
// current batch's 3,000.
func TestPublicationFollowStatementCostRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "cost-producer", "", "", "keep-all")
	a.env = append(a.env, "SDN_FLATSQL_SLOW_QUERY_MS=1")
	a.start(t)
	b := newPublicationDaemon(t, exe, "cost-receiver", a.address, a.id, "keep-all")
	b.start(t)
	const batches, perBatch = 10, 3_000
	for i := 0; i < batches; i++ {
		a.ingestOMM(t, "cost", "c"+strconv.Itoa(i), 400_000+i*perBatch, perBatch)
	}
	before := len(readFileString(a.logPath))
	asked := time.Now()
	plan := b.plan(t, a, "cost")
	took := time.Since(asked)
	if plan.StatementError != "" || plan.BatchID != "c9" || plan.BatchRecords != perBatch || plan.LaneRecords != batches*perBatch {
		t.Fatalf("statement: batch %q %d records, lane %d records, error %q; want c9, %d, %d", plan.BatchID, plan.BatchRecords, plan.LaneRecords,
			plan.StatementError, perBatch, batches*perBatch)
	}
	during := readFileString(a.logPath)[before:]
	t.Logf("statement in %s; producer engine reads logged: %d", took.Round(time.Millisecond), len(slowOperation.FindAllString(during, -1)))
	if lane := readsOfAtLeast(during, batches*perBatch); len(lane) > 0 {
		t.Fatalf("the statement read the lane's records: %d engine reads examined %d rows or more:\n%s", len(lane), batches*perBatch, strings.Join(lane, "\n"))
	}
}

// TestPublicationFollowWorkRealDaemons: what a follower asks a producer to
// read stops when the follower leaves, is bounded, and is limited per peer.
// The producer is built so a lane named "slow-..." has a statement that
// reads for 10 minutes (sdn_e2e_follow_faults): a follower that gives up
// after 5 s resets the stream and the producer's read stops then, not
// after its 10 minutes; of six such statements asked at once by one peer,
// four past the two a peer may have running are refused busy at once and
// the other two are answered busy at the producer's 1-minute bound (the
// follower waits 5 minutes); after them a lane's statement is answered.
func TestPublicationFollowWorkRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, publicationFollowFaultsBinary(t), "work-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "work-receiver", a.address, a.id, "keep-all")
	b.start(t)
	a.ingestOMM(t, "work", "w1", 500_000, 10)

	planQuery := func(source string) string {
		return api.SyncPlanPath + "?" + url.Values{"schema": {"OMM"}, "provider_id": {"publication-e2e"}, "source": {source}, "from": {a.id}}.Encode()
	}
	// The follower gives up after 5 s: its request ends, the node resets
	// the stream, and the producer's read stops.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", b.base+planQuery("slow-reset"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		t.Fatalf("the statement of slow-reset answered HTTP %d before its 10 minutes", res.StatusCode)
	}
	cancel()
	reset := regexp.MustCompile(`read-chunk OMM(?:\.fbs)? publication-e2e/slow-reset from .+?: the peer reset the stream after ([0-9.]+m?s)`)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if m := reset.FindStringSubmatch(readFileString(a.logPath)); m != nil {
			after, err := time.ParseDuration(m[1])
			if err != nil || after > 20*time.Second {
				t.Fatalf("the producer's read of slow-reset stopped after %s, want within seconds of the follower's 5 s", m[1])
			}
			t.Logf("the producer's read of slow-reset stopped after %s", after)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the producer did not stop the read of slow-reset within 30 s of the follower's reset")
		}
		time.Sleep(500 * time.Millisecond)
	}

	type answer struct {
		plan api.LanePlan
		took time.Duration
	}
	answers := make([]answer, 6)
	var wg sync.WaitGroup
	began := time.Now()
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, status, err := followCostRequest(b, "GET", planQuery("slow-busy-"+strconv.Itoa(i)), "", nil)
			answers[i].took = time.Since(began)
			if err != nil || status != http.StatusOK || json.Unmarshal(raw, &answers[i].plan) != nil {
				t.Errorf("plan slow-busy-%d: HTTP %d %v %.300q", i, status, err, raw)
			}
		}(i)
	}
	wg.Wait()
	refused, bounded := 0, 0
	for i, ans := range answers {
		t.Logf("slow-busy-%d answered in %s: %q", i, ans.took.Round(time.Millisecond), ans.plan.StatementError)
		switch {
		case !strings.Contains(ans.plan.StatementError, "busy"):
			t.Errorf("slow-busy-%d: statement error %q, want busy", i, ans.plan.StatementError)
		case strings.Contains(ans.plan.StatementError, "at their limit") && ans.took < 10*time.Second:
			refused++
		case strings.Contains(ans.plan.StatementError, "within its bound") && ans.took > 50*time.Second && ans.took < 2*time.Minute:
			bounded++
		default:
			t.Errorf("slow-busy-%d answered in %s: %q", i, ans.took, ans.plan.StatementError)
		}
	}
	if refused != 4 || bounded != 2 {
		t.Fatalf("of six statements at once %d were refused at once and %d answered at the bound; want 4 and 2", refused, bounded)
	}
	if plan := b.plan(t, a, "work"); plan.StatementError != "" || plan.BatchID != "w1" || plan.BatchRecords != 10 {
		t.Fatalf("statement after the slow ones: batch %q %d records, error %q", plan.BatchID, plan.BatchRecords, plan.StatementError)
	}
}

// TestPublicationFollowBackoffRealDaemons: a follower whose producer cannot
// be reached waits longer after each failed connection, past the 30 s a
// follower before 1.1.13 never went beyond (which, with the 60 s it waited
// for a statement, asked an overloaded producer again every 90 s for as
// long as it was overloaded).
func TestPublicationFollowBackoffRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "backoff-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "backoff-receiver", a.address, a.id, "keep-all")
	b.start(t)
	a.ingestOMM(t, "backoff", "k1", 600_000, 100)
	want := a.scan(t, "OMM.fbs", "backoff")
	b.subscribeCurrent(t, a, "backoff")
	b.awaitRecords(t, "OMM.fbs", "backoff", want, 3*time.Minute)
	a.stop(t)
	stopped := time.Now()
	waits := regexp.MustCompile(`Followed lane OMM\.fbs publication-e2e/backoff from \S+: .*; reconnecting in (\S+)`)
	deadline := time.Now().Add(4 * time.Minute)
	for {
		var longest time.Duration
		var seen []string
		for _, m := range waits.FindAllStringSubmatch(readFileString(b.logPath), -1) {
			if d, err := time.ParseDuration(m[1]); err == nil {
				longest = max(longest, d)
				seen = append(seen, m[1])
			}
		}
		if longest >= 45*time.Second {
			t.Logf("waits after the producer stopped (%s ago): %s", time.Since(stopped).Round(time.Second), strings.Join(seen, ", "))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("4 minutes after the producer stopped the longest wait is %s (waits %s); want one of 45 s or more", longest, strings.Join(seen, ", "))
		}
		time.Sleep(2 * time.Second)
	}
}
