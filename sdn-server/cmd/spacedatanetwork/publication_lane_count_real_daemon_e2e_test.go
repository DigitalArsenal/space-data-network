package main

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationLaneCountRealDaemons checks the record count a scan reports
// (TOTAL_COUNT of its first page) on a real daemon against the records sent.
// A lane with one tag instance is counted from the lane counters; a source
// whose records sit in two batches (the same 1,000 records in both) has two
// instances meeting the filter and is counted by reading the lane. Both
// answers are the distinct records sent.
func TestPublicationLaneCountRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "count-producer", "", "", "keep-all")
	a.start(t)

	const n, per = 150_000, 5_000
	var again [][]byte
	for offset := 0; offset < n; offset += per {
		frames := make([][]byte, per)
		for i := range frames {
			frames[i] = baselineOMM(40_000 + offset + i)
		}
		if offset == 0 {
			again = frames[:1_000]
		}
		a.ingestOMMFrames(t, "count", "first", 40_000+offset, frames)
	}
	// The same 1,000 records (the same bytes) in a second batch.
	a.ingestOMMFrames(t, "count", "second", 40_000, again)
	a.ingestOMMCIDs(t, "count-other", "first", 400_000, per)

	for _, c := range []struct {
		source, batch string
		want          int64
	}{
		{"count", "first", n},
		{"count", "second", 1_000},
		{"count", "", n},
		{"count-other", "", per},
		{"count-other", "first", per},
	} {
		started := time.Now()
		got := a.scanTotalCount(t, "OMM.fbs", c.source, c.batch)
		took := time.Since(started)
		if got != c.want {
			t.Fatalf("lane %s/%s: TOTAL_COUNT %d, want %d", c.source, c.batch, got, c.want)
		}
		t.Logf("lane %s/%s: TOTAL_COUNT %d in %s", c.source, c.batch, got, took.Round(time.Millisecond))
	}
}

// scanTotalCount is TOTAL_COUNT of a scan's first page of one lane.
func (d *publicationDaemon) scanTotalCount(t *testing.T, schema, source, batch string) int64 {
	t.Helper()
	request := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema, ProviderID: "publication-e2e", SourceName: source, BatchID: batch, Limit: 1})
	raw := d.request(t, "POST", "/api/v1/data/scan", publicationStreamType, request, http.StatusOK)
	frames, err := syncwire.SplitFrames(raw)
	if err != nil || len(frames) == 0 {
		t.Fatalf("invalid scan reply: %v", err)
	}
	q, err := syncwire.ParseQRP(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	return int64(q.TOTAL_COUNT())
}
