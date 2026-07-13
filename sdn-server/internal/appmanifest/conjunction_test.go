package appmanifest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// embeddedConjunctionPath is the serving artifact (C1/C2/C3 build output) that
// is the single source of truth the conjunction APP record is derived from.
// The test reads it directly (relative to this package dir) rather than
// checking in a second copy — that is the whole point of the drift gate.
const embeddedConjunctionPath = "../../cmd/spacedatanetwork/embedded/conjunction_app.html"

func readEmbeddedConjunction(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(embeddedConjunctionPath))
	if err != nil {
		t.Fatalf("read embedded conjunction artifact: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("embedded conjunction artifact is empty")
	}
	return b
}

// TestConjunctionRecordDriftGate is the HARD acceptance test: the APP record's
// decoded CONTENT MUST byte-equal the embedded serving artifact. The record is
// the source of truth for the app definition; the embed is the serving copy;
// if they ever diverge this test fails.
func TestConjunctionRecordDriftGate(t *testing.T) {
	html := readEmbeddedConjunction(t)

	app, err := NewConjunctionApp(html)
	if err != nil {
		t.Fatalf("NewConjunctionApp() error = %v", err)
	}

	// Validate + Resolve must be green on the record.
	if err := app.Validate(); err != nil {
		t.Fatalf("record Validate() error = %v", err)
	}
	resolution, err := app.Resolve()
	if err != nil {
		t.Fatalf("record Resolve() error = %v", err)
	}
	if resolution.EntryPage == nil {
		t.Fatalf("Resolve() produced no EntryPage for the conjunction record")
	}
	if got := resolution.EntryPage.ID; got != ConjunctionPageID {
		t.Fatalf("EntryPage.ID = %q, want %q", got, ConjunctionPageID)
	}

	if len(app.Modules) != 0 {
		t.Fatalf("conjunction app must have zero modules (pure-UI v1), got %d", len(app.Modules))
	}
	if len(app.Pages) != 1 {
		t.Fatalf("conjunction app must have exactly one UI page, got %d", len(app.Pages))
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

	// THE DRIFT GATE: decode(CONTENT) must byte-equal the embed.
	decoded, err := page.DecodedContent()
	if err != nil {
		t.Fatalf("DecodedContent() error = %v", err)
	}
	if !bytes.Equal(decoded, html) {
		t.Fatalf("DRIFT: decoded record CONTENT (%d bytes) does not byte-equal the embedded artifact (%d bytes) — the APP record and the serving copy have diverged", len(decoded), len(html))
	}

	// ContentSHA256 must be the sha of the DECODED bytes.
	if got, want := page.ContentSHA256, sha256Hex(html); got != want {
		t.Fatalf("ContentSHA256 = %s, want %s", got, want)
	}

	// Report honest sizes.
	t.Logf("conjunction record: html %d bytes, CONTENT (base64+gzip) %d bytes, $APP %d bytes",
		len(html), len(page.Content), mustToAPPLen(t, app))
}

func mustToAPPLen(t *testing.T, a *AppManifest) int {
	t.Helper()
	buf, err := a.ToAPP()
	if err != nil {
		t.Fatalf("ToAPP() error = %v", err)
	}
	return len(buf)
}

// TestConjunctionAPPRoundTrip proves the $APP FlatBuffer round-trip preserves
// everything for the real record: bytes (decoded CONTENT), sha, and the
// encoding enum, plus full struct equality.
func TestConjunctionAPPRoundTrip(t *testing.T) {
	html := readEmbeddedConjunction(t)
	app, err := NewConjunctionApp(html)
	if err != nil {
		t.Fatalf("NewConjunctionApp() error = %v", err)
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
		t.Fatalf("$APP round-trip changed the manifest:\n got  ID=%s pages=%d data=%d sources=%d\n want ID=%s pages=%d data=%d sources=%d",
			back.ID, len(back.Pages), len(back.Data), len(back.Sources),
			app.ID, len(app.Pages), len(app.Data), len(app.Sources))
	}

	// Explicit invariants beyond DeepEqual: decoded bytes, sha, enum.
	page := back.Pages[0]
	if page.Encoding != EncodingBase64Gzip {
		t.Fatalf("round-tripped encoding = %q, want base64_gzip (enum drifted)", page.Encoding)
	}
	decoded, err := page.DecodedContent()
	if err != nil {
		t.Fatalf("round-tripped DecodedContent() error = %v", err)
	}
	if !bytes.Equal(decoded, html) {
		t.Fatalf("round-tripped decoded CONTENT does not byte-equal the embed")
	}
	if page.ContentSHA256 != sha256Hex(html) {
		t.Fatalf("round-tripped ContentSHA256 drifted")
	}
}

// TestConjunctionTamperedContentFails proves a tampered CONTENT (decodes to
// different bytes than ContentSHA256 claims) is rejected by Validate.
func TestConjunctionTamperedContentFails(t *testing.T) {
	html := readEmbeddedConjunction(t)
	app, err := NewConjunctionApp(html)
	if err != nil {
		t.Fatalf("NewConjunctionApp() error = %v", err)
	}

	// Re-encode a mutated body but keep the ORIGINAL sha: decode now yields the
	// mutated bytes while the declared hash still covers the pristine embed.
	tamperedHTML := append([]byte(nil), html...)
	tamperedHTML[len(tamperedHTML)/2] ^= 0xFF
	tamperedContent, err := EncodingBase64Gzip.encodeContent(tamperedHTML)
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

// TestConjunctionWrongShaFails proves a wrong declared ContentSHA256 is
// rejected even when CONTENT is the pristine artifact.
func TestConjunctionWrongShaFails(t *testing.T) {
	html := readEmbeddedConjunction(t)
	app, err := NewConjunctionApp(html)
	if err != nil {
		t.Fatalf("NewConjunctionApp() error = %v", err)
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

// TestConjunctionRecordJSONRoundTrip proves the record also survives the
// existing JSON canonical lane (back-compat) unchanged.
func TestConjunctionRecordJSONRoundTrip(t *testing.T) {
	html := readEmbeddedConjunction(t)
	app, err := NewConjunctionApp(html)
	if err != nil {
		t.Fatalf("NewConjunctionApp() error = %v", err)
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
		t.Fatalf("JSON round-trip changed the conjunction record")
	}
}
