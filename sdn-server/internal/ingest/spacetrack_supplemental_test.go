package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	OEMFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	fxDirs    = "testdata/spacetrack/publicfiles-dirs.json"
	fxListing = "testdata/spacetrack/publicfiles-loadpublicdata.json"
	fxOEMXML  = "testdata/spacetrack/oem-iss-trimmed.xml"
	fxZip     = "testdata/spacetrack/publicfiles-iss15day.zip"
	fxGP      = "testdata/spacetrack/gp-current-sample.json"
)

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// -------------------------------------------------------------------------
// CCSDS time parsing (day-of-year is the NASA-JSC form)
// -------------------------------------------------------------------------

func TestParseCCSDSTimeDayOfYear(t *testing.T) {
	// 2026 day-194 == 2026-07-13 (non-leap: Jan..Jun = 181 days, +13).
	got, err := parseCCSDSTime("2026-194T12:00:00.000Z")
	if err != nil {
		t.Fatalf("parseCCSDSTime DOY: %v", err)
	}
	want := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("DOY epoch = %s, want %s", got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}

	// Fractional seconds preserved.
	frac, err := parseCCSDSTime("2026-194T14:40:15.008Z")
	if err != nil {
		t.Fatalf("parseCCSDSTime frac: %v", err)
	}
	if frac.Nanosecond() != 8_000_000 {
		t.Fatalf("fractional nanos = %d, want 8000000", frac.Nanosecond())
	}

	// Calendar form still works through the shared calendar parser.
	cal, err := parseCCSDSTime("2026-07-13T12:00:00Z")
	if err != nil {
		t.Fatalf("parseCCSDSTime calendar: %v", err)
	}
	if !cal.Equal(want) {
		t.Fatalf("calendar epoch = %s, want %s", cal, want)
	}

	if _, err := parseCCSDSTime("2026-400T12:00:00Z"); err == nil {
		t.Fatal("expected error for day-of-year 400")
	}
}

// -------------------------------------------------------------------------
// OEM XML parsing
// -------------------------------------------------------------------------

func TestParseOEMXMLFromISSFixture(t *testing.T) {
	docs, err := parseOEMXML(mustRead(t, fxOEMXML))
	if err != nil {
		t.Fatalf("parseOEMXML: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d oem docs, want 1", len(docs))
	}
	doc := docs[0]
	if doc.Version != "1.0" {
		t.Fatalf("version = %q, want 1.0", doc.Version)
	}
	if doc.Header.Originator != "JSC" {
		t.Fatalf("originator = %q, want JSC", doc.Header.Originator)
	}
	if len(doc.Body.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(doc.Body.Segments))
	}
	seg := doc.Body.Segments[0]
	// Frame + time system preserved AS DECLARED.
	if seg.Metadata.RefFrame != "EME2000" {
		t.Fatalf("REF_FRAME = %q, want EME2000", seg.Metadata.RefFrame)
	}
	if seg.Metadata.TimeSystem != "UTC" {
		t.Fatalf("TIME_SYSTEM = %q, want UTC", seg.Metadata.TimeSystem)
	}
	// NASA-JSC internal designators preserved (not fabricated).
	if seg.Metadata.ObjectName != "8000" || seg.Metadata.ObjectID != "01" {
		t.Fatalf("OBJECT_NAME/ID = %q/%q, want 8000/01", seg.Metadata.ObjectName, seg.Metadata.ObjectID)
	}
	if len(seg.Data.StateVectors) != 5 {
		t.Fatalf("state vectors = %d, want 5", len(seg.Data.StateVectors))
	}
	sv := seg.Data.StateVectors[0]
	if sv.Epoch != "2026-194T12:00:00.000Z" {
		t.Fatalf("first epoch = %q", sv.Epoch)
	}
	if sv.X != -4024.53611737582 {
		t.Fatalf("first X = %v", sv.X)
	}
	if sv.ZDot != 4.12530324917983 {
		t.Fatalf("first Z_DOT = %v", sv.ZDot)
	}
}

// -------------------------------------------------------------------------
// OEM FlatBuffer construction round-trip
// -------------------------------------------------------------------------

// tblStr reads a byte-vector field (vtable offset vt) as a string.
func tblStr(tab *flatbuffers.Table, vt flatbuffers.VOffsetT) string {
	o := tab.Offset(vt)
	if o == 0 {
		return ""
	}
	return string(tab.ByteVector(flatbuffers.UOffsetT(o) + tab.Pos))
}

// decodeOEMBlock0 navigates the first ephemeris data block using low-level
// flatbuffers.Table access (the generated ephemerisDataBlock/Line accessors
// take unexported-typed params). Vtable offsets: block CENTER_NAME=8,
// REFERENCE_FRAME=10, TIME_SYSTEM=16, EPHEMERIS_DATA_LINES=36; RFM NAME=10;
// line EPOCH=4, X=6.
func decodeOEMBlock0(t *testing.T, buf []byte) (center, frame string, timeSys int8, lineCount int, firstEpoch string, firstX float64) {
	t.Helper()
	oem := OEMFB.GetSizePrefixedRootAsOEM(buf, 0)
	tab := oem.Table()
	bOff := tab.Offset(12) // EPHEMERIS_DATA_BLOCK
	if bOff == 0 {
		t.Fatal("OEM buffer has no ephemeris data block vector")
	}
	blkPos := tab.Indirect(tab.Vector(flatbuffers.UOffsetT(bOff)))
	blk := flatbuffers.Table{Bytes: tab.Bytes, Pos: blkPos}

	center = tblStr(&blk, 8)
	if o := blk.Offset(16); o != 0 {
		timeSys = blk.GetInt8(flatbuffers.UOffsetT(o) + blk.Pos)
	}
	if o := blk.Offset(10); o != 0 {
		rfmPos := blk.Indirect(flatbuffers.UOffsetT(o) + blk.Pos)
		rfm := flatbuffers.Table{Bytes: tab.Bytes, Pos: rfmPos}
		frame = tblStr(&rfm, 10)
	}
	if o := blk.Offset(36); o != 0 {
		lineCount = blk.VectorLen(flatbuffers.UOffsetT(o))
		linePos := blk.Indirect(blk.Vector(flatbuffers.UOffsetT(o)))
		line := flatbuffers.Table{Bytes: tab.Bytes, Pos: linePos}
		firstEpoch = tblStr(&line, 4)
		if xo := line.Offset(6); xo != 0 {
			firstX = line.GetFloat64(flatbuffers.UOffsetT(xo) + line.Pos)
		}
	}
	return
}

func TestBuildOEMRecordRoundTrip(t *testing.T) {
	docs, err := parseOEMXML(mustRead(t, fxOEMXML))
	if err != nil {
		t.Fatalf("parseOEMXML: %v", err)
	}
	buf, warnings := buildOEMRecord(docs[0])
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if !OEMFB.SizePrefixedOEMBufferHasIdentifier(buf) {
		t.Fatal("OEM buffer missing $OEM size-prefixed identifier")
	}

	oem := OEMFB.GetSizePrefixedRootAsOEM(buf, 0)
	if got := string(oem.ORIGINATOR()); got != "JSC" {
		t.Fatalf("ORIGINATOR = %q, want JSC", got)
	}
	if got := oem.CCSDS_OEM_VERS(); got != 1.0 {
		t.Fatalf("CCSDS_OEM_VERS = %v, want 1.0", got)
	}
	if got := string(oem.CREATION_DATE()); got != "2026-07-13T14:40:15.008Z" {
		t.Fatalf("CREATION_DATE = %q, want normalized ISO", got)
	}
	if got := oem.EPHEMERIS_DATA_BLOCKLength(); got != 1 {
		t.Fatalf("block count = %d, want 1", got)
	}

	center, frame, timeSys, lineCount, firstEpoch, firstX := decodeOEMBlock0(t, buf)
	if center != "EARTH" {
		t.Fatalf("CENTER_NAME = %q, want EARTH", center)
	}
	if frame != "EME2000" {
		t.Fatalf("REFERENCE_FRAME NAME = %q, want EME2000 (as-declared)", frame)
	}
	wantUTC := int8(OEMFB.EnumValuestimingStandard["UTC"])
	if timeSys != wantUTC {
		t.Fatalf("TIME_SYSTEM = %d, want UTC(%d)", timeSys, wantUTC)
	}
	if lineCount != 5 {
		t.Fatalf("ephemeris data lines = %d, want 5", lineCount)
	}
	if firstEpoch != "2026-07-13T12:00:00Z" {
		t.Fatalf("first line EPOCH = %q, want normalized ISO", firstEpoch)
	}
	if firstX != -4024.53611737582 {
		t.Fatalf("first line X = %v", firstX)
	}
}

// -------------------------------------------------------------------------
// Nested-zip extraction
// -------------------------------------------------------------------------

func TestExtractOEMXMLsNestedZipIgnoresNonOEM(t *testing.T) {
	xmls, warnings, err := extractOEMXMLs(mustRead(t, fxZip))
	if err != nil {
		t.Fatalf("extractOEMXMLs: %v", err)
	}
	if len(xmls) != 1 {
		t.Fatalf("found %d OEM XMLs, want 1", len(xmls))
	}
	if !strings.Contains(string(xmls[0]), "<REF_FRAME>EME2000</REF_FRAME>") {
		t.Fatal("extracted XML is not the expected OEM")
	}
	foundTxtWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "TrajectorySummary.txt") {
			foundTxtWarning = true
		}
	}
	if !foundTxtWarning {
		t.Fatalf("expected an 'ignored non-OEM entry' warning for the .txt, got %v", warnings)
	}
}

// -------------------------------------------------------------------------
// Listing / dirs / file-classification parsing
// -------------------------------------------------------------------------

func TestParsePublicFilesDirsAndListing(t *testing.T) {
	dirs, err := parsePublicFilesDirs(mustRead(t, fxDirs))
	if err != nil {
		t.Fatalf("parsePublicFilesDirs: %v", err)
	}
	if len(dirs) != 5 {
		t.Fatalf("dirs = %d, want 5", len(dirs))
	}
	foundKuiper, foundSpaceX := false, false
	for _, d := range dirs {
		if strings.Contains(d, "24206-kuiper") {
			foundKuiper = true
		}
		if strings.Contains(d, "552-spacex") {
			foundSpaceX = true
		}
	}
	if !foundKuiper || !foundSpaceX {
		t.Fatalf("expected kuiper+spacex dirs, got %v", dirs)
	}

	files, err := parsePublicFilesListing(mustRead(t, fxListing))
	if err != nil {
		t.Fatalf("parsePublicFilesListing: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3", len(files))
	}
	var ephemeris, readme int
	for _, f := range files {
		if isOEMEphemerisFile(f) {
			ephemeris++
		}
		if strings.EqualFold(f.Type, "ReadMe") {
			readme++
		}
	}
	if ephemeris != 2 {
		t.Fatalf("ephemeris files = %d, want 2 (15Day + 8Week)", ephemeris)
	}
	if readme != 1 {
		t.Fatalf("readme files = %d, want 1", readme)
	}
}

func TestIsOEMEphemerisFile(t *testing.T) {
	cases := []struct {
		f    publicFile
		want bool
	}{
		{publicFile{Type: "Ephemeris", Name: "x_15Day.zip"}, true},
		{publicFile{Type: "ReadMe", Name: "readme.zip"}, false},
		{publicFile{Type: "Ephemeris", Name: "notes.txt"}, false},
		{publicFile{Type: "SomeFutureType", Name: "new_provider.zip"}, true}, // new sources onboard w/o code change
		{publicFile{Type: "Ephemeris", Name: ""}, false},
	}
	for i, c := range cases {
		if got := isOEMEphemerisFile(c.f); got != c.want {
			t.Fatalf("case %d: isOEMEphemerisFile=%v, want %v", i, got, c.want)
		}
	}
}

// -------------------------------------------------------------------------
// current-gp JSON parsing
// -------------------------------------------------------------------------

func TestParseSpaceTrackJSONRecordsGP(t *testing.T) {
	rows, err := parseSpaceTrackJSONRecords(mustRead(t, fxGP))
	if err != nil {
		t.Fatalf("parseSpaceTrackJSONRecords: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Schema-exact keys resolve through getValue.
	if got := getValue(rows[0], "NORAD_CAT_ID"); got != "5" {
		t.Fatalf("NORAD_CAT_ID = %q, want 5", got)
	}
	if got := getValue(rows[0], "OBJECT_NAME"); got != "VANGUARD 1" {
		t.Fatalf("OBJECT_NAME = %q", got)
	}
	if got := getValue(rows[0], "ORIGINATOR"); got != "18 SPCS" {
		t.Fatalf("ORIGINATOR = %q, want '18 SPCS'", got)
	}
}

// -------------------------------------------------------------------------
// Rate limiter
// -------------------------------------------------------------------------

func TestRateLimiterReserveEnforcesGapAndWindow(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	l := newRateLimiter(2*time.Second, 3, 100)

	// First request: no delay.
	if d, at := l.reserve(base); d != 0 {
		t.Fatalf("first reserve delay = %s, want 0", d)
	} else {
		l.commit(at)
	}
	// Second request 0.5s later: must wait out the 2s min gap.
	now := base.Add(500 * time.Millisecond)
	d, at := l.reserve(now)
	if d != 1500*time.Millisecond {
		t.Fatalf("min-gap delay = %s, want 1.5s", d)
	}
	l.commit(at)
	// Third at the gap boundary, then a fourth must wait for the per-minute
	// window (3/min): the 4th can only proceed 60s after the 1st.
	l.commit(base.Add(4 * time.Second)) // 3rd
	d4, _ := l.reserve(base.Add(5 * time.Second))
	// window holds [0s, 4s, ...]; with perMinN=3 the 4th waits until the
	// oldest (t=0) ages out at t=60s.
	if d4 < 54*time.Second {
		t.Fatalf("per-minute delay = %s, want >= ~55s", d4)
	}
}

func TestRateLimiterWaitCancels(t *testing.T) {
	l := newRateLimiter(time.Hour, 25, 250)
	l.commit(l.now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("expected context cancellation error from Wait")
	}
}

// -------------------------------------------------------------------------
// End-to-end lane wiring against a mock Space-Track server
// -------------------------------------------------------------------------

type mockSpaceTrack struct {
	server        *httptest.Server
	mu            sync.Mutex
	logins        int
	downloadNames []string
	gpHits        int
}

func newMockSpaceTrack(t *testing.T, listing []byte) *mockSpaceTrack {
	t.Helper()
	m := &mockSpaceTrack{}
	zipBytes := mustRead(t, fxZip)
	gpBytes := mustRead(t, fxGP)
	dirsBytes := mustRead(t, fxDirs)

	mux := http.NewServeMux()
	mux.HandleFunc("/ajaxauth/login", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.logins++
		m.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "session"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`""`))
	})
	mux.HandleFunc("/publicfiles/query/class/dirs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(dirsBytes)
	})
	mux.HandleFunc("/publicfiles/query/class/loadpublicdata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(listing)
	})
	mux.HandleFunc("/publicfiles/query/class/download", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, `{"error":"Parameter [ name ] expected but not provided"}`, http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.downloadNames = append(m.downloadNames, name)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	})
	mux.HandleFunc("/gp.json", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.gpHits++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gpBytes)
	})
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func newSupplementalRunner(t *testing.T, m *mockSpaceTrack) *Runner {
	t.Helper()
	dir := t.TempDir()
	runner, err := NewRunner(Config{
		StoragePath:                  filepath.Join(dir, "store"),
		RawPath:                      filepath.Join(dir, "raw"),
		SpaceTrackEnabled:            true,
		SpaceTrackPublicFilesEnabled: true,
		SpaceTrackCurrentGPEnabled:   true,
		SpaceTrackIdentity:           "tester",
		SpaceTrackPassword:           "secret",
		SpaceTrackLoginURL:           m.server.URL + "/ajaxauth/login",
		SpaceTrackPublicFilesBaseURL: m.server.URL + "/publicfiles/query/class",
		SpaceTrackCurrentGPQueryURL:  m.server.URL + "/gp.json",
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	// Zero-gap limiter so the test does not sleep.
	runner.stLimiter = newRateLimiter(0, spaceTrackPerMinuteCap, spaceTrackPerHourCap)
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

func TestSyncSpaceTrackSupplementalEndToEnd(t *testing.T) {
	// Listing with one ReadMe (skipped) + one ephemeris (ingested).
	listing := []byte(`[
	  {"source":"NASA-JSC","type":"ReadMe","date":"2024-11-05 19:03:59","size":"829 Bytes","link":"NASAJSC_ReadMe_23643_ReadMe_2024-11-05UTC19:03:52_01.zip","name":"NASAJSC_ReadMe_23643_ReadMe_2024-11-05UTC19:03:52_01.zip"},
	  {"source":"NASA-JSC","type":"Ephemeris","date":"2026-07-13 14:46:26","size":"383.22 KB","link":"NASAJSC_Ephemeris_23644_15Day_2026-07-13UTC14:46:19_01.zip","name":"NASAJSC_Ephemeris_23644_15Day_2026-07-13UTC14:46:19_01.zip"}
	]`)
	m := newMockSpaceTrack(t, listing)
	runner := newSupplementalRunner(t, m)

	if err := runner.syncSpaceTrackSupplemental(context.Background()); err != nil {
		t.Fatalf("syncSpaceTrackSupplemental: %v", err)
	}

	// publicfiles: exactly one OEM stored, and the ReadMe was never downloaded.
	oems, err := runner.store.QueryAll("OEM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OEM: %v", err)
	}
	if len(oems) != 1 {
		t.Fatalf("stored OEM = %d, want 1", len(oems))
	}
	if _, frame, _, lines, _, _ := decodeOEMBlock0(t, oems[0]); frame != "EME2000" || lines != 5 {
		t.Fatalf("stored OEM frame=%q lines=%d, want EME2000/5", frame, lines)
	}
	m.mu.Lock()
	if len(m.downloadNames) != 1 || !strings.Contains(m.downloadNames[0], "15Day") {
		m.mu.Unlock()
		t.Fatalf("download names = %v, want only the 15Day ephemeris", m.downloadNames)
	}
	m.mu.Unlock()

	// current-gp: 3 OMM + 3 MPE stored.
	omms, err := runner.store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM: %v", err)
	}
	if len(omms) != 3 {
		t.Fatalf("stored OMM = %d, want 3", len(omms))
	}
	mpes, err := runner.store.QueryAll("MPE.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll MPE: %v", err)
	}
	if len(mpes) != 3 {
		t.Fatalf("stored MPE = %d, want 3", len(mpes))
	}

	// Second cycle: the ephemeris is deduped by name+date -> no new download,
	// and gp records dedupe by content -> still exactly one OEM / three OMM.
	if err := runner.syncSpaceTrackSupplemental(context.Background()); err != nil {
		t.Fatalf("second syncSpaceTrackSupplemental: %v", err)
	}
	m.mu.Lock()
	downloads := len(m.downloadNames)
	m.mu.Unlock()
	if downloads != 1 {
		t.Fatalf("downloads after 2 cycles = %d, want 1 (dedup by name+date)", downloads)
	}
	oems2, _ := runner.store.QueryAll("OEM.fbs", 10)
	if len(oems2) != 1 {
		t.Fatalf("OEM after 2 cycles = %d, want 1", len(oems2))
	}
}

func TestSyncSpaceTrackSupplementalSkipsWhenDisabledOrNoCreds(t *testing.T) {
	m := newMockSpaceTrack(t, mustRead(t, fxListing))

	// Master switch off.
	r1 := newSupplementalRunner(t, m)
	r1.cfg.SpaceTrackEnabled = false
	if err := r1.syncSpaceTrackSupplemental(context.Background()); err != nil {
		t.Fatalf("disabled sync should be a no-op, got %v", err)
	}

	// Enabled but no credentials.
	r2 := newSupplementalRunner(t, m)
	r2.cfg.SpaceTrackIdentity = ""
	if err := r2.syncSpaceTrackSupplemental(context.Background()); err != nil {
		t.Fatalf("no-cred sync should skip, got %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.logins != 0 {
		t.Fatalf("expected 0 logins when disabled/uncredentialed, got %d", m.logins)
	}
}
