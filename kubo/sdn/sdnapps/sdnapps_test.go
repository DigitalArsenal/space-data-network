package sdnapps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/sdnapps"
)

// fakeStore is an in-memory ManifestStore that dedups by content, mirroring
// sdnstore.StoreManifest's idempotency contract without pulling in the FlatSQL
// engine. It records every (source, type) pair and every stored CID.
type fakeStore struct {
	mu    sync.Mutex
	byCID map[string][]byte
	pairs map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{byCID: map[string][]byte{}, pairs: map[string]bool{}}
}

func (f *fakeStore) StoreManifest(_ context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	h, err := mh.Sum(fb, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	c := cid.NewCidV1(cid.Raw, h)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byCID[c.String()] = fb
	f.pairs[source+"|"+sdsType] = true
	return c, nil
}

func TestManifestsValidate(t *testing.T) {
	ms, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d manifests, want 3", len(ms))
	}
	ids := map[string]bool{}
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: Validate: %v", m.ID, err)
		}
		ids[m.ID] = true
		// Each app is pure-UI: exactly one inline entry page, no member modules.
		if len(m.Modules) != 0 {
			t.Errorf("%s: expected zero modules, got %d", m.ID, len(m.Modules))
		}
		if len(m.Pages) != 1 || !m.Pages[0].Entry || !m.Pages[0].IsInline() {
			t.Errorf("%s: expected one inline entry page, got %+v", m.ID, m.Pages)
		}
		if len(m.Dataflow) != 1 {
			t.Errorf("%s: expected one dataflow entry, got %d", m.ID, len(m.Dataflow))
		}
	}
	if !ids["supplemental-omm"] || !ids["conjunction"] || !ids["flow-editor"] {
		t.Fatalf("missing expected app ids, got %v", ids)
	}
}

// TestRecordsRoundTrip proves each app survives the published $APP FlatBuffer
// round-trip (ToAPP -> bytes -> FromAPP): the inline UI page content and the
// declared dataflow contract come back field-for-field, and the record
// re-validates (ContentSHA256 over the decoded page still matches).
func TestRecordsRoundTrip(t *testing.T) {
	recs, err := sdnapps.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	want := map[string]struct {
		schema, transport, locatorHas string
		direction                     appmanifest.FlowDirection
	}{
		"supplemental-omm": {"OMM", "gateway_route", "/sdn/v1/runs", appmanifest.FlowDirectionToPage},
		"conjunction":      {"CDM", "gateway_route", "type=CDM", appmanifest.FlowDirectionToPage},
		"flow-editor":      {"PLG", "gateway_route", "/api/v1/flows/bake", appmanifest.FlowDirectionFromPage},
	}
	for id, exp := range want {
		buf, ok := recs[id]
		if !ok {
			t.Fatalf("missing record for %q", id)
		}
		m, err := appmanifest.FromAPP(buf)
		if err != nil {
			t.Fatalf("%s: FromAPP: %v", id, err)
		}
		if m.ID != id {
			t.Errorf("%s: round-tripped ID = %q", id, m.ID)
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: round-tripped Validate: %v", id, err)
		}
		// Inline entry page survives with intact, verifiable content.
		if len(m.Pages) != 1 {
			t.Fatalf("%s: pages = %d, want 1", id, len(m.Pages))
		}
		p := m.Pages[0]
		decoded, err := p.DecodedContent()
		if err != nil {
			t.Fatalf("%s: DecodedContent: %v", id, err)
		}
		if !strings.Contains(string(decoded), "/sdn/v1/") {
			t.Errorf("%s: entry page does not reference the node's /sdn/v1/ API", id)
		}
		sum := sha256.Sum256(decoded)
		if got := hex.EncodeToString(sum[:]); got != p.ContentSHA256 {
			t.Errorf("%s: ContentSHA256 mismatch: page declares %s, decoded hashes to %s", id, p.ContentSHA256, got)
		}
		// Dataflow contract survives field-for-field.
		if len(m.Dataflow) != 1 {
			t.Fatalf("%s: dataflow = %d, want 1", id, len(m.Dataflow))
		}
		f := m.Dataflow[0]
		if f.Direction != exp.direction {
			t.Errorf("%s: dataflow direction = %q, want %q", id, f.Direction, exp.direction)
		}
		if f.SDSSchema != exp.schema {
			t.Errorf("%s: dataflow sdsSchema = %q, want %q", id, f.SDSSchema, exp.schema)
		}
		if string(f.Transport) != exp.transport {
			t.Errorf("%s: dataflow transport = %q, want %q", id, f.Transport, exp.transport)
		}
		if !strings.Contains(f.Locator, exp.locatorHas) {
			t.Errorf("%s: dataflow locator = %q, want it to contain %q", id, f.Locator, exp.locatorHas)
		}
	}
}

// TestSelfContained asserts every app's inline UI page makes NO external-origin
// request: no http(s):// URL of any kind appears in the page bytes. The whole
// app is served from the node loopback; a foreign origin would break the
// self-contained contract.
func TestSelfContained(t *testing.T) {
	foreign := regexp.MustCompile(`https?://`)
	ms, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	for _, m := range ms {
		content := m.Pages[0].Content
		if foreign.MatchString(content) {
			loc := foreign.FindStringIndex(content)
			t.Errorf("%s: entry page contains an external-origin URL near %q", m.ID,
				content[loc[0]:min(loc[0]+60, len(content))])
		}
		if !strings.Contains(content, "/sdn/v1/") {
			t.Errorf("%s: entry page does not use the node's /sdn/v1/ API", m.ID)
		}
	}
}

// TestOMMBoardWiring asserts the Supplemental-OMM board's entry page is wired to
// the node's REAL run + module-config API (not the old thin record listing) and
// self-hosts its two font families same-origin, with no external-origin URL. It
// pins the owner-directed run-log/drill-down contract (2026-07-19 reconfiguration):
// a single paginated RUN LOG (no standalone "latest run" box — deleted outright),
// a synthesized "ongoing" row for a currently-executing run, and a row-click
// drill-down that REPLACES the log with a single-row run-stats table plus a
// paginated, searchable per-object table with RMS + CelesTrak/Space-Track parity
// and TLE/OMM/CDM element downloads, and a BACK TO RUN LOG control (no arrow
// glyphs anywhere), alongside the provider checkbox + cron controls that persist
// through the module config.
func TestOMMBoardWiring(t *testing.T) {
	ms, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	var board string
	for _, m := range ms {
		if m.ID == "supplemental-omm" {
			if len(m.Pages) == 1 {
				board = m.Pages[0].Content
			}
		}
	}
	if board == "" {
		t.Fatal("supplemental-omm app or its entry page is missing")
	}

	// Real run API: list + live run, run detail, searchable per-object rows, and
	// the per-object VCM-format element downloads.
	for _, needle := range []string{
		"/sdn/v1/runs",            // run list + live run
		"/objects",                // searchable per-object rows
		"?search=",                // NORAD search
		"/download?format=",       // element download route
		`"tle"`, `"omm"`, `"cdm"`, // TLE / OMM / CDM (VCM) formats
		"current_avg_rms", // live run: current average RMS
		"ephemeris_files", // ephemeris files processed
		"celestrak_rms",   // parity vs CelesTrak
		"spacetrack_rms",  // parity vs Space-Track OMM
		"beats_celestrak", // beats flag
		"omm_cid",         // produced OMM record CID
	} {
		if !strings.Contains(board, needle) {
			t.Errorf("board entry page does not wire %q", needle)
		}
	}

	// Owner-directed run-log/drill-down contract: the main element is a
	// paginated run log (no standalone "latest run" box — see the negative
	// assertions below), a synthesized "ongoing" status for a live run, and a
	// row-click drill-down with a BACK control (no arrow glyph).
	for _, needle := range []string{
		"RUN LOG",           // the main element's panel kicker
		"ongoing",           // synthesized live-run status token
		"ONGOING",           // its rendered chip label
		"RUN DETAIL",        // the drill-down panel kicker
		"BACK TO RUN LOG",   // the back control (plain text, no arrow glyph)
		"SEARCH OBJECTS",    // the drill-down's plaintext search bar label
		"runlog-pagination", // run-log pagination container (5/page)
		"PREV", "NEXT",      // pagination controls are words, never arrows
	} {
		if !strings.Contains(board, needle) {
			t.Errorf("board entry page does not wire %q", needle)
		}
	}

	// The old standalone "latest run" / "current run" snapshot box is DELETED
	// outright (owner rule: no tombstones or hidden remnants) — these ids only
	// ever existed on that removed panel.
	for _, needle := range []string{"current-body", "current-kicker", "current-status", "live-remaining"} {
		if strings.Contains(board, needle) {
			t.Errorf("board still carries a remnant of the deleted 'latest run' box: %q", needle)
		}
	}

	// No directional arrow glyphs anywhere on the page (owner hard rule).
	for _, glyph := range []string{"→", "←", "▶", "◀", "›", "‹", "»", "«", "➡", "⬅"} {
		if strings.Contains(board, glyph) {
			t.Errorf("board contains a directional arrow glyph %q", glyph)
		}
	}

	// Provider selection + cron persist through the module config PUT.
	for _, needle := range []string{
		"/sdn/v1/modules/supplemental-omm/config",
		"enabled_providers",
		"interval_ms",
	} {
		if !strings.Contains(board, needle) {
			t.Errorf("board entry page does not wire provider/cron config %q", needle)
		}
	}

	// Every owner-listed provider has a checkbox row.
	for _, p := range []string{"spacex-starlink", "iss", "gps", "glonass", "cpf", "intelsat", "oneweb"} {
		if !strings.Contains(board, p) {
			t.Errorf("board is missing provider %q", p)
		}
	}

	// Honest empty state (a fresh node has no runs) rather than fake rows.
	if !strings.Contains(board, "NO RUNS YET") {
		t.Error("board does not carry the honest 'NO RUNS YET' empty state")
	}

	// Fonts are self-hosted same-origin under /fonts/*.woff2 — never fetched
	// off-host (that is what keeps the whole page same-origin).
	if !strings.Contains(board, ".woff2") || !strings.Contains(board, "/fonts/") {
		t.Error("board does not self-host its web fonts at the same-origin /fonts/ path")
	}
	for _, ff := range []string{"chakra-400.woff2", "chakra-600.woff2", "chakra-700.woff2", "plex-400.woff2"} {
		if !strings.Contains(board, "/fonts/"+ff) {
			t.Errorf("board does not self-host %q under /fonts/", ff)
		}
	}
	// No external origin of any kind (defense in depth alongside TestSelfContained).
	if strings.Contains(board, "http://") || strings.Contains(board, "https://") {
		t.Error("board contains an external-origin URL")
	}
}

// TestFlowEditorWiring asserts the Flow Editor's entry page wires the node's
// palette + bake API, self-hosts its fonts same-origin, and carries no external
// origin. It pins the Phase-1 contract: the palette source (GET
// /api/v1/flows/palette) and the deploy target (POST /api/v1/flows/bake).
func TestFlowEditorWiring(t *testing.T) {
	ms, err := sdnapps.Manifests()
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	var page string
	for _, m := range ms {
		if m.ID == "flow-editor" && len(m.Pages) == 1 {
			page = m.Pages[0].Content
		}
	}
	if page == "" {
		t.Fatal("flow-editor app or its entry page is missing")
	}

	// Palette source + bake target (the Phase-1 compose/deploy contract).
	for _, needle := range []string{
		"/api/v1/flows/palette", // local node-type catalog (palette source)
		"/api/v1/flows/bake",    // deploy target (compose + link + run)
		"moduleRefs",            // bake payload carries the used modules
		"triggerBindings",       // full graph: triggers bound to node ports
		"bakeMillis",            // result renders the node's bake wall time
	} {
		if !strings.Contains(page, needle) {
			t.Errorf("flow editor page does not wire %q", needle)
		}
	}

	// Uses the node's read-only API for status (keeps the page same-origin).
	if !strings.Contains(page, "/sdn/v1/") {
		t.Error("flow editor page does not use the node's /sdn/v1/ API")
	}

	// Fonts self-hosted same-origin under /fonts/*.woff2 — never fetched off-host.
	if !strings.Contains(page, ".woff2") || !strings.Contains(page, "/fonts/") {
		t.Error("flow editor page does not self-host its web fonts at /fonts/")
	}
	// No external origin of any kind (defense in depth alongside TestSelfContained).
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("flow editor page contains an external-origin URL")
	}
}

// TestSeedIdempotent stores every app via Seed, then seeds again: the second
// pass writes no new distinct records (content-addressed dedup), and both apps
// land under the single (Source, "APP") pair.
func TestSeedIdempotent(t *testing.T) {
	fs := newFakeStore()
	n, err := sdnapps.Seed(context.Background(), fs)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if n != 3 {
		t.Fatalf("first Seed stored %d, want 3", n)
	}
	if len(fs.byCID) != 3 {
		t.Fatalf("distinct records after first Seed = %d, want 3", len(fs.byCID))
	}
	if _, err := sdnapps.Seed(context.Background(), fs); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(fs.byCID) != 3 {
		t.Fatalf("distinct records after re-seed = %d, want 3 (idempotent)", len(fs.byCID))
	}
	if !fs.pairs[sdnapps.Source+"|"+sdnapps.SDSType] {
		t.Fatalf("apps were not stored under (%q, %q)", sdnapps.Source, sdnapps.SDSType)
	}
	if len(fs.pairs) != 1 {
		t.Fatalf("apps spread across %d (source,type) pairs, want 1", len(fs.pairs))
	}
}
