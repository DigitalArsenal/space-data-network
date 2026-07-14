package appmanifest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// embeddedBoardPath is the checked-in status-board artifact (this package's
// embedded/ dir) that the App 2 record is derived from. The test reads it
// directly rather than checking in a second copy — that is the drift gate.
const embeddedBoardPath = "embedded/supplemental_omm_board.html"

func readEmbeddedBoard(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(embeddedBoardPath))
	if err != nil {
		t.Fatalf("read embedded status board artifact: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("embedded status board artifact is empty")
	}
	return b
}

// TestSupplementalOMMEmbedMatchesDisk proves the go:embedded bytes are exactly
// the checked-in file (so NewSupplementalOMMApp built from the embed and the
// drift gate built from disk are building from the same source of truth).
func TestSupplementalOMMEmbedMatchesDisk(t *testing.T) {
	disk := readEmbeddedBoard(t)
	embed := SupplementalOMMBoardHTML()
	if !bytes.Equal(disk, embed) {
		t.Fatalf("go:embedded board (%d bytes) does not byte-equal the checked-in file (%d bytes)", len(embed), len(disk))
	}
}

// TestSupplementalOMMRecordDriftGate is the HARD acceptance test: the App 2
// record's decoded CONTENT MUST byte-equal the checked-in status board. If the
// record and the serving copy ever diverge, this fails.
func TestSupplementalOMMRecordDriftGate(t *testing.T) {
	html := readEmbeddedBoard(t)

	app, err := NewSupplementalOMMApp(html)
	if err != nil {
		t.Fatalf("NewSupplementalOMMApp() error = %v", err)
	}

	// Validate + Resolve must be green (referential integrity across
	// module/data/source refs is enforced inside Validate).
	if err := app.Validate(); err != nil {
		t.Fatalf("record Validate() error = %v", err)
	}
	resolution, err := app.Resolve()
	if err != nil {
		t.Fatalf("record Resolve() error = %v", err)
	}
	if resolution.EntryPage == nil {
		t.Fatalf("Resolve() produced no EntryPage for the supplemental-omm record")
	}
	if got := resolution.EntryPage.ID; got != SupplementalOMMPageID {
		t.Fatalf("EntryPage.ID = %q, want %q", got, SupplementalOMMPageID)
	}

	if app.ID != SupplementalOMMAppID {
		t.Fatalf("app id = %q, want %q", app.ID, SupplementalOMMAppID)
	}
	if len(app.Modules) != 10 {
		t.Fatalf("supplemental-omm app must reference 10 modules (8 adapters + fit-pipeline + catalog-synthesis), got %d", len(app.Modules))
	}
	if len(app.Data) != 4 {
		t.Fatalf("supplemental-omm app must have 4 data refs, got %d", len(app.Data))
	}
	if len(app.Sources) != 17 {
		t.Fatalf("supplemental-omm app must have 17 source refs, got %d", len(app.Sources))
	}
	if len(app.Pages) != 1 {
		t.Fatalf("supplemental-omm app must have exactly one UI page, got %d", len(app.Pages))
	}

	// Every module carries a non-empty pluginId + isomorphic wasm ContentHash.
	for i, m := range app.Modules {
		if strings.TrimSpace(m.PluginID) == "" {
			t.Fatalf("modules[%d] (%s): empty pluginId", i, m.ID)
		}
		if len(m.ContentHash) != 64 {
			t.Fatalf("modules[%d] (%s): ContentHash %q is not a 64-hex sha256", i, m.ID, m.ContentHash)
		}
	}

	// Referential integrity: every DataRef.ModuleID that is set must resolve to
	// a declared module (Validate guarantees this, assert it explicitly for the
	// wired producer bindings).
	byID := resolution.ModuleByID
	for _, d := range app.Data {
		if d.ModuleID == "" {
			continue
		}
		if _, ok := byID[d.ModuleID]; !ok {
			t.Fatalf("data ref %q references unknown moduleId %q", d.ID, d.ModuleID)
		}
	}

	page := app.Pages[0]
	if page.Encoding != EncodingBase64Gzip {
		t.Fatalf("page encoding = %q, want %q", page.Encoding, EncodingBase64Gzip)
	}
	if page.MediaType != "text/html" {
		t.Fatalf("page mediaType = %q, want text/html", page.MediaType)
	}
	if !page.Entry {
		t.Fatalf("page.Entry = false, want true")
	}

	// THE DRIFT GATE: decode(CONTENT) must byte-equal the checked-in board.
	decoded, err := page.DecodedContent()
	if err != nil {
		t.Fatalf("DecodedContent() error = %v", err)
	}
	if !bytes.Equal(decoded, html) {
		t.Fatalf("DRIFT: decoded record CONTENT (%d bytes) does not byte-equal the checked-in status board (%d bytes)", len(decoded), len(html))
	}

	// ContentSHA256 must be the sha of the DECODED bytes.
	if got, want := page.ContentSHA256, sha256Hex(html); got != want {
		t.Fatalf("ContentSHA256 = %s, want %s", got, want)
	}

	t.Logf("supplemental-omm record: html %d bytes, CONTENT (base64+gzip) %d bytes, $APP %d bytes; modules=%d data=%d sources=%d",
		len(html), len(page.Content), mustToAPPLen(t, app), len(app.Modules), len(app.Data), len(app.Sources))
}

// TestSupplementalOMMAPPRoundTrip proves the $APP FlatBuffer round-trip
// preserves everything for the real App 2 record: full struct equality plus the
// decoded bytes / sha / encoding enum invariants over the whole field surface
// (modules + data + sources + inline page).
func TestSupplementalOMMAPPRoundTrip(t *testing.T) {
	html := readEmbeddedBoard(t)
	app, err := NewSupplementalOMMApp(html)
	if err != nil {
		t.Fatalf("NewSupplementalOMMApp() error = %v", err)
	}

	buf, err := app.ToAPP()
	if err != nil {
		t.Fatalf("ToAPP() error = %v", err)
	}
	back, err := FromAPP(buf)
	if err != nil {
		t.Fatalf("FromAPP() error = %v", err)
	}

	if !reflect.DeepEqual(app, back) {
		t.Fatalf("$APP round-trip changed the manifest:\n got  ID=%s modules=%d data=%d sources=%d pages=%d\n want ID=%s modules=%d data=%d sources=%d pages=%d",
			back.ID, len(back.Modules), len(back.Data), len(back.Sources), len(back.Pages),
			app.ID, len(app.Modules), len(app.Data), len(app.Sources), len(app.Pages))
	}

	page := back.Pages[0]
	if page.Encoding != EncodingBase64Gzip {
		t.Fatalf("round-tripped encoding = %q, want base64_gzip (enum drifted)", page.Encoding)
	}
	decoded, err := page.DecodedContent()
	if err != nil {
		t.Fatalf("round-tripped DecodedContent() error = %v", err)
	}
	if !bytes.Equal(decoded, html) {
		t.Fatalf("round-tripped decoded CONTENT does not byte-equal the checked-in board")
	}
	if page.ContentSHA256 != sha256Hex(html) {
		t.Fatalf("round-tripped ContentSHA256 drifted")
	}

	// Spot-check that a wired producer binding survived the round-trip: the
	// fitted-omm DataRef must still point at the fit-pipeline module.
	var found bool
	for _, d := range back.Data {
		if d.ID == "fitted-omm" {
			found = true
			if d.ModuleID != "od-fit-pipeline" {
				t.Fatalf("fitted-omm.ModuleID = %q after round-trip, want od-fit-pipeline", d.ModuleID)
			}
			if d.SDSType != "OMM" || d.Direction != DataDirectionProduces {
				t.Fatalf("fitted-omm drifted: sdsType=%q direction=%q", d.SDSType, d.Direction)
			}
		}
	}
	if !found {
		t.Fatalf("fitted-omm data ref missing after round-trip")
	}
}

// TestSupplementalOMMTamperedContentFails proves a tampered CONTENT (decodes to
// different bytes than ContentSHA256 claims) is rejected by Validate.
func TestSupplementalOMMTamperedContentFails(t *testing.T) {
	html := readEmbeddedBoard(t)
	app, err := NewSupplementalOMMApp(html)
	if err != nil {
		t.Fatalf("NewSupplementalOMMApp() error = %v", err)
	}

	tampered := append([]byte(nil), html...)
	tampered[len(tampered)/2] ^= 0xFF
	tamperedContent, err := EncodingBase64Gzip.encodeContent(tampered)
	if err != nil {
		t.Fatalf("encode tampered content: %v", err)
	}
	app.Pages[0].Content = tamperedContent // sha unchanged -> mismatch

	err = app.Validate()
	if err == nil {
		t.Fatalf("Validate() on tampered CONTENT: want error, got nil")
	}
	if !strings.Contains(err.Error(), "contentSha256 mismatch") {
		t.Fatalf("Validate() error = %q, want a contentSha256 mismatch", err.Error())
	}
}

// TestSupplementalOMMWrongShaFails proves a wrong declared ContentSHA256 is
// rejected even when CONTENT is the pristine artifact.
func TestSupplementalOMMWrongShaFails(t *testing.T) {
	html := readEmbeddedBoard(t)
	app, err := NewSupplementalOMMApp(html)
	if err != nil {
		t.Fatalf("NewSupplementalOMMApp() error = %v", err)
	}

	app.Pages[0].ContentSHA256 = strings.Repeat("0", 64)

	err = app.Validate()
	if err == nil {
		t.Fatalf("Validate() on wrong sha: want error, got nil")
	}
	if !strings.Contains(err.Error(), "contentSha256 mismatch") {
		t.Fatalf("Validate() error = %q, want a contentSha256 mismatch", err.Error())
	}
}

// TestSupplementalOMMEmptyHTMLFails proves the builder rejects empty artifact
// bytes rather than emitting a degenerate record.
func TestSupplementalOMMEmptyHTMLFails(t *testing.T) {
	if _, err := NewSupplementalOMMApp(nil); err == nil {
		t.Fatalf("NewSupplementalOMMApp(nil): want error, got nil")
	}
}

// TestSupplementalOMMJSONRoundTrip proves the record survives the JSON canonical
// lane (the back-compat path) unchanged.
func TestSupplementalOMMJSONRoundTrip(t *testing.T) {
	html := readEmbeddedBoard(t)
	app, err := NewSupplementalOMMApp(html)
	if err != nil {
		t.Fatalf("NewSupplementalOMMApp() error = %v", err)
	}

	data, err := app.MarshalCanonicalJSON()
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !reflect.DeepEqual(app, parsed) {
		t.Fatalf("JSON round-trip changed the supplemental-omm record")
	}
}
