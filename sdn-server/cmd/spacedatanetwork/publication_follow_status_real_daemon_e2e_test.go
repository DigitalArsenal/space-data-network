package main

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationFollowStatusRealDaemons reads a followed lane's $DSS on both
// daemons. The receiver's reads subscribed over the followed stream and
// counts the records received, their bytes and the time spent receiving,
// verifying and storing them; the producer's counts its follower and the
// record bytes it served, exactly the bytes of the records it holds, and
// no follower once the receiver has stopped.
func TestPublicationFollowStatusRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "status-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "status-receiver", a.address, "", "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)

	const n = 5_000
	var recordBytes uint64
	frames := make([][]byte, n)
	for i := range frames {
		frames[i] = baselineOMM(90_000 + i)
		recordBytes += uint64(len(frames[i]))
	}
	a.ingestOMMFrames(t, "status", "first", 90_000, frames)
	want := a.scan(t, "OMM.fbs", "status")
	b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "status", a.id), http.StatusAccepted)
	b.awaitRecords(t, "OMM.fbs", "status", want, 3*time.Minute)

	got := b.laneStatus(t, "status")
	if !got.SUBSCRIBED() || string(got.SYNC_PROTOCOL()) != "flatsql-sync read-chunk follow" {
		t.Fatalf("receiver $DSS: subscribed %v protocol %q", got.SUBSCRIBED(), got.SYNC_PROTOCOL())
	}
	if got.SYNCED_ROWS() != n || got.DOWNLOADED_BYTES() < recordBytes || len(got.LAST_SYNCED_AT()) == 0 {
		t.Fatalf("receiver $DSS: SYNCED_ROWS %d (want %d), DOWNLOADED_BYTES %d (records %d), LAST_SYNCED_AT %q",
			got.SYNCED_ROWS(), n, got.DOWNLOADED_BYTES(), recordBytes, got.LAST_SYNCED_AT())
	}
	t.Logf("receiver $DSS: %d rows, %d bytes, transfer %d ms, verification %d ms, store %d ms, %d B/s, last %s",
		got.SYNCED_ROWS(), got.DOWNLOADED_BYTES(), got.NETWORK_TRANSFER_MS(), got.VERIFICATION_MS(), got.FLATSQL_MATERIALIZATION_MS(),
		got.DOWNLOAD_SPEED_BYTES_PER_SECOND(), got.LAST_SYNCED_AT())

	served := a.laneStatus(t, "status")
	if served.SUBSCRIBER_COUNT() != 1 || served.SERVED_BYTES() != recordBytes {
		t.Fatalf("producer $DSS: SUBSCRIBER_COUNT %d (want 1), SERVED_BYTES %d (want %d)", served.SUBSCRIBER_COUNT(), served.SERVED_BYTES(), recordBytes)
	}
	t.Logf("producer $DSS: %d follower, %d bytes served, %d B/s", served.SUBSCRIBER_COUNT(), served.SERVED_BYTES(), served.SERVE_SPEED_BYTES_PER_SECOND())

	b.stop(t)
	deadline := time.Now().Add(30 * time.Second)
	for a.laneStatus(t, "status").SUBSCRIBER_COUNT() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the producer still counts a follower 30 s after the receiver stopped")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// laneStatus is the daemon's $DSS of one publication-e2e OMM lane.
func (d *publicationDaemon) laneStatus(t *testing.T, source string) *DSS.DSS {
	t.Helper()
	raw := d.request(t, "GET", "/api/v1/sync/OMM.fbs/publication-e2e/"+source, "", nil, http.StatusOK)
	frames, err := syncwire.SplitFrames(raw)
	if err != nil || len(frames) != 1 {
		t.Fatalf("lane %s: %d $DSS frames (%v), want one", source, len(frames), err)
	}
	return DSS.GetSizePrefixedRootAsDSS(frames[0], 0)
}
