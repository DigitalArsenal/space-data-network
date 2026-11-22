package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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
