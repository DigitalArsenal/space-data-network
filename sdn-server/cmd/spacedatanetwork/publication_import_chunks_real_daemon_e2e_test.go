package main

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// TestPublicationImportChunksRealDaemons publishes shards that a format-5
// receiver imports in several engine writes: 10,000 element sets (more than
// two 4,096-record chunks) and 24 ephemerides of about 1 MiB each (more than
// two 8 MiB chunks). The receiver's records are byte-identical to the
// producer's, compared record by record through both nodes' APIs.
func TestPublicationImportChunksRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "chunks-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "chunks-receiver", a.address, a.id, "keep-all")
	b.start(t)
	b.follow(t, a, "OMM", "chunks", "")
	// OEM is off the public read surface and the producer grants the
	// receiver no private reads: it does not state the lane, so the
	// subscription is forced, and the lane imports through its signed
	// publications (the chunked shard import).
	b.follow(t, a, "OEM", "chunks", "start=beginning&force=1")
	time.Sleep(3 * time.Second)

	const elementSets = 10_000
	for offset := 0; offset < elementSets; offset += 2_000 {
		a.ingestOMM(t, "chunks", "first", 20_000+offset, 2_000)
	}
	const ephemerides = 24
	for i := 0; i < ephemerides; i += 3 {
		var body bytes.Buffer
		for j := i; j < i+3; j++ {
			body.Write(baselineOEM(j))
		}
		a.ingest(t, "OEM.fbs", "chunks", "first", body.Bytes(), 3)
	}

	for _, lane := range []struct {
		schema string
		count  int
	}{{"OMM.fbs", elementSets}, {"OEM.fbs", ephemerides}} {
		want := a.scan(t, lane.schema, "chunks")
		if len(want) != lane.count {
			t.Fatalf("producer holds %d %s records, want %d", len(want), lane.schema, lane.count)
		}
		pub := a.publish(t, lane.schema, "chunks", "first")
		if pub.RecordCount != lane.count {
			t.Fatalf("published %d %s records, want %d", pub.RecordCount, lane.schema, lane.count)
		}
		started := time.Now()
		b.awaitRecords(t, lane.schema, "chunks", want, 6*time.Minute)
		t.Logf("receiver holds the %d %s records byte for byte %.1f s after the publish response", lane.count, lane.schema, time.Since(started).Seconds())
	}
}
