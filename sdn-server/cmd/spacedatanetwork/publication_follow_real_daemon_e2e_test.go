package main

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestPublicationFollowRealDaemons follows a lane between two real daemons
// with nothing published: the receiver subscribes to the producer's lane
// (a $DSS Subscribe naming PROVIDER_PEER_ID), the producer's stored records
// arrive over the followed read-chunk stream, records stored later arrive
// within seconds, and a receiver stopped while the producer stores more
// resumes at its saved cursor on restart: every record once, byte for byte.
// The receiver does not trust the producer, so no other path imports.
func TestPublicationFollowRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "follow-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "follow-receiver", a.address, "", "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)

	a.ingestOMMCIDs(t, "follow", "first", 60_000, 10_000)
	want := a.scan(t, "OMM.fbs", "follow")
	if len(want) != 10_000 {
		t.Fatalf("producer holds %d records, want 10000", len(want))
	}
	b.request(t, "POST", "/api/v1/sync", publicationStreamType, api.EncodeDSSFollow("OMM", "publication-e2e", "follow", a.id), http.StatusAccepted)
	started := time.Now()
	b.awaitRecords(t, "OMM.fbs", "follow", want, 3*time.Minute)
	t.Logf("10,000 stored records followed in %s", time.Since(started).Round(time.Millisecond))

	// Live: records stored now arrive within seconds.
	for round := 0; round < 3; round++ {
		a.ingestOMMCIDs(t, "follow", "live", 70_000+round*500, 500)
		want = a.scan(t, "OMM.fbs", "follow")
		started = time.Now()
		b.awaitRecordsFast(t, "OMM.fbs", "follow", want, 30*time.Second)
		took := time.Since(started)
		t.Logf("live round %d: 500 records on the receiver %s after the producer's ingest returned", round, took.Round(time.Millisecond))
		if took > 15*time.Second {
			t.Fatalf("live records took %s", took)
		}
	}

	// The subscription is a $DSS record on disk naming the producer.
	follows := findFile(t, b.root, "follows.dss")
	raw, err := os.ReadFile(follows)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil || len(frames) != 1 || !DSS.SizePrefixedDSSBufferHasIdentifier(frames[0]) {
		t.Fatalf("follows.dss holds %d frames (%v), want one $DSS record", len(frames), err)
	}
	sub := DSS.GetSizePrefixedRootAsDSS(frames[0], 0)
	if string(sub.PROVIDER_PEER_ID()) != a.id || string(sub.SOURCE_NAME()) != "follow" || !sub.SUBSCRIBED() || len(sub.CURSOR())+len(sub.NEXT_CURSOR()) == 0 {
		t.Fatalf("follows.dss record: producer %q source %q subscribed %v cursor %d bytes", sub.PROVIDER_PEER_ID(), sub.SOURCE_NAME(), sub.SUBSCRIBED(), len(sub.CURSOR())+len(sub.NEXT_CURSOR()))
	}

	// Restart: the receiver resumes where it stopped; nothing is lost or
	// stored twice.
	b.stop(t)
	a.ingestOMMCIDs(t, "follow", "offline", 80_000, 5_000)
	want = a.scan(t, "OMM.fbs", "follow")
	b.start(t)
	b.awaitRecords(t, "OMM.fbs", "follow", want, 3*time.Minute)
	if log := readFileString(b.logPath); !strings.Contains(log, "Followed lane OMM.fbs publication-e2e/follow from "+a.id+": stream open") {
		t.Fatalf("receiver log has no followed stream from %s", a.id)
	}
	t.Logf("restart: %d records, each once, byte for byte", len(want))
}

// awaitRecordsFast is awaitRecords polling every 100 ms.
func (d *publicationDaemon) awaitRecordsFast(t *testing.T, schema, source string, want map[string]int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if reflect.DeepEqual(d.scan(t, schema, source), want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.requireRecords(t, schema, source, want)
}

func findFile(t *testing.T, root, name string) string {
	t.Helper()
	found := ""
	_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no %s under %s", name, root)
	}
	return found
}
