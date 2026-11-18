package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestPublicationFingerprintRealDaemons publishes a lane of 150,000 element
// sets: more than one 20,000-row read of the lane and more than two 65,536-CID
// sorted runs of its fingerprint. The fingerprint the producer records is the
// definition's (the SHA-256 of the lane's header and its sorted text CIDs, one
// a line), computed here from the records as sent; the series is three
// 50,000-record windows read by CID from the plan that read made; and the
// receiver holds the published records byte for byte.
func TestPublicationFingerprintRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "fingerprint-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "fingerprint-receiver", a.address, a.id, "keep-all")
	b.start(t)
	b.follow(t, a, "OMM", "fingerprint", "")
	time.Sleep(3 * time.Second)

	const n, per = 150_000, 5_000
	var cids []string
	for offset := 0; offset < n; offset += per {
		cids = append(cids, a.ingestOMMCIDs(t, "fingerprint", "first", 30_000+offset, per)...)
	}
	slices.Sort(cids)
	if len(slices.Compact(slices.Clone(cids))) != n {
		t.Fatalf("%d distinct CIDs, want %d", len(slices.Compact(slices.Clone(cids))), n)
	}
	h := sha256.New()
	fmt.Fprintf(h, "sdn-dataset-publication-set-v1\x00%s\x00%s\x00%s\x00%s\n", "OMM.fbs", "publication-e2e", "fingerprint", "first")
	for _, c := range cids {
		h.Write([]byte(c))
		h.Write([]byte{'\n'})
	}
	want := fmt.Sprintf("%x over %d records", h.Sum(nil), n)

	records := a.scan(t, "OMM.fbs", "fingerprint")
	pub := a.publish(t, "OMM.fbs", "fingerprint", "first")
	if pub.RecordCount != n {
		t.Fatalf("published %d records, want %d", pub.RecordCount, n)
	}
	got := publicationFingerprintLog(t, a, "OMM.fbs publication-e2e/fingerprint/first: ")
	if !strings.HasPrefix(got, want+" in ") {
		t.Fatalf("producer fingerprint %q, want %q", got, want)
	}
	t.Logf("producer fingerprint %s", got)
	// The series' windows are the CID window's by offset (50,000 records
	// each), read by CID from the plan the fingerprint read made.
	log := readFileString(a.logPath)
	for offset := 0; offset < n; offset += 50_000 {
		window := fmt.Sprintf("Dataset publication window OMM.fbs publication-e2e/fingerprint/first offset=%d limit=50000: 50000 records read by CID from the plan", offset)
		if !strings.Contains(log, window) {
			t.Fatalf("no %q line in %s", window, a.logPath)
		}
	}
	b.awaitRecords(t, "OMM.fbs", "fingerprint", records, 6*time.Minute)

	// The sort's runs and plan are written under the store's directory and
	// removed when the publication's read ends; what a stopped process left
	// there is removed when the store opens.
	sortDir := filepath.Join(a.store, "publication-sort")
	left, err := os.ReadDir(sortDir)
	if err != nil {
		t.Fatalf("the sort did not write under the store directory: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("%d sort files left under %s after the publication", len(left), sortDir)
	}
	a.stop(t)
	stale := filepath.Join(sortDir, "sdn-publication-fingerprint-stale")
	if err := os.WriteFile(stale, []byte("bafy 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.start(t)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a sort file a stopped process left is still there after the store opened (%v)", err)
	}
}

// ingestOMMCIDs ingests n element sets and returns their CIDs, computed from
// the records as sent and checked against the node's answer.
func (d *publicationDaemon) ingestOMMCIDs(t *testing.T, source, batch string, offset, n int) []string {
	t.Helper()
	frames := make([][]byte, n)
	for i := range frames {
		frames[i] = baselineOMM(offset + i)
	}
	return d.ingestOMMFrames(t, source, batch, offset, frames)
}

// ingestOMMFrames ingests size-prefixed element sets and returns their CIDs,
// computed from the records as sent and checked against the node's answer.
func (d *publicationDaemon) ingestOMMFrames(t *testing.T, source, batch string, offset int, frames [][]byte) []string {
	t.Helper()
	var body bytes.Buffer
	n := len(frames)
	cids := make([]string, 0, n)
	for i, frame := range frames {
		if len(frame) < 4 || int(binary.LittleEndian.Uint32(frame)) != len(frame)-4 {
			t.Fatalf("record %d is not one size-prefixed frame", offset+i)
		}
		cids = append(cids, storage.ComputeCID(frame[4:]))
		body.Write(frame)
	}
	query := url.Values{"provider_id": {"publication-e2e"}, "source_name": {source}, "batch_id": {batch}}
	raw := d.request(t, "POST", "/api/v1/data/publish/batch/OMM.fbs?"+query.Encode(), publicationStreamType, body.Bytes(), http.StatusCreated)
	var response struct {
		Results []struct {
			CID   string `json:"cid"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != n {
		t.Fatalf("ingest returned %d results, want %d", len(response.Results), n)
	}
	for i, r := range response.Results {
		if r.Error != "" || r.CID != cids[i] {
			t.Fatalf("record %d: node answered CID %q error %q, sent %s", offset+i, r.CID, r.Error, cids[i])
		}
	}
	return cids
}

// publicationFingerprintLog is the text after prefix on the daemon's
// "publication fingerprint" log line.
func publicationFingerprintLog(t *testing.T, d *publicationDaemon, prefix string) string {
	t.Helper()
	raw, err := os.ReadFile(d.logPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := "publication fingerprint " + prefix
	got := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			got = line[i+len(marker):]
		}
	}
	if got == "" {
		t.Fatalf("no %q line in %s", marker, d.logPath)
	}
	return got
}
