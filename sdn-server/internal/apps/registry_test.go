package apps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

const samplePage = "<!doctype html><title>face</title><body>face</body>"

func buildTestRecord(t *testing.T, id, name, version string) []byte {
	t.Helper()
	record, err := BuildInlinePageRecord(
		AppIdentity{ID: id, Name: name, Version: version, Description: "test app"},
		[]InlinePage{{ID: "main", Title: name, HTML: []byte(samplePage), Entry: true}},
	)
	if err != nil {
		t.Fatalf("BuildInlinePageRecord(%s): %v", id, err)
	}
	return record
}

// An installed entry's record fields come from the DECODED $APP bytes — never
// from what the caller happened to pass alongside them. That is what "the
// record is the truth" means in practice.
func TestInstallRecordDecodesIdentityFromTheRecord(t *testing.T) {
	r := New("/api/v1/apps/records/")
	record := buildTestRecord(t, "sdn-dashboard", "SDN Node Dashboard", "1.0.4")

	entry, err := r.InstallRecord(RuntimeServer, "/", record)
	if err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	if entry.ID != "sdn-dashboard" || entry.Name != "SDN Node Dashboard" || entry.Version != "1.0.4" {
		t.Fatalf("identity = %q/%q/%q, want sdn-dashboard/SDN Node Dashboard/1.0.4",
			entry.ID, entry.Name, entry.Version)
	}
	if entry.State != StateInstalled {
		t.Fatalf("state = %q, want %q", entry.State, StateInstalled)
	}
	if entry.RecordURL != "/api/v1/apps/records/sdn-dashboard" {
		t.Fatalf("record_url = %q", entry.RecordURL)
	}
	if entry.RecordBytes != len(record) {
		t.Fatalf("record_bytes = %d, want %d", entry.RecordBytes, len(record))
	}
	if len(entry.UI) != 1 || !entry.UI[0].Entry {
		t.Fatalf("UI = %+v, want exactly one ENTRY page", entry.UI)
	}
	want := sha256.Sum256([]byte(samplePage))
	if entry.UI[0].ContentSHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("CONTENT_SHA256 = %q, want %q", entry.UI[0].ContentSHA256, hex.EncodeToString(want[:]))
	}
	if entry.UI[0].Encoding != "UTF8" {
		t.Fatalf("ENCODING = %q, want UTF8", entry.UI[0].Encoding)
	}
	if got, ok := r.Record("sdn-dashboard"); !ok || len(got) != len(record) {
		t.Fatalf("Record() = %d bytes, ok=%v", len(got), ok)
	}
}

// The one-app-per-class case resolves without any declaration: a node with a
// dashboard and nothing else has an unambiguous server default.
func TestDefaultResolvesTheSoleAppOfItsClass(t *testing.T) {
	r := New("")
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "sdn-dashboard", "Dashboard", "1")); err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	entry, ok := r.Default(RuntimeServer)
	if !ok || entry.ID != "sdn-dashboard" || !entry.Default {
		t.Fatalf("server default = %+v ok=%v", entry, ok)
	}
	if _, ok := r.Default(RuntimeBrowser); ok {
		t.Fatal("browser default resolved with no browser app registered")
	}
}

// Two candidates and no declaration is reported as NO default. The registry
// must never pick for the operator: an app opening by accident is worse than
// an honest "nothing is configured".
func TestAmbiguousDefaultIsReportedAbsent(t *testing.T) {
	r := New("")
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "one", "One", "1")); err != nil {
		t.Fatalf("InstallRecord one: %v", err)
	}
	if _, err := r.InstallRecord(RuntimeServer, "/two", buildTestRecord(t, "two", "Two", "1")); err != nil {
		t.Fatalf("InstallRecord two: %v", err)
	}
	if entry, ok := r.Default(RuntimeServer); ok {
		t.Fatalf("ambiguous default resolved to %q", entry.ID)
	}
	if err := r.SetDefault(RuntimeServer, "two"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	entry, ok := r.Default(RuntimeServer)
	if !ok || entry.ID != "two" {
		t.Fatalf("declared default = %+v ok=%v", entry, ok)
	}
}

// A default naming an app the node does not have, or an app of the wrong
// runtime class, is an ERROR — the operator sees the mistake instead of a
// client silently opening something else.
func TestSetDefaultRejectsUnknownAndWrongClass(t *testing.T) {
	r := New("")
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "sdn-dashboard", "Dashboard", "1")); err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	if err := r.SetDefault(RuntimeServer, "nope"); err == nil {
		t.Fatal("SetDefault accepted an unregistered app id")
	}
	if err := r.SetDefault(RuntimeBrowser, "sdn-dashboard"); err == nil {
		t.Fatal("SetDefault accepted a server-class app as the browser default")
	}
}

// Both faces registered ⇒ each default carries a link to the other. This is
// the "link to each one in the other" half of the owner's ruling.
func TestDefaultsCrossLinkTheTwoFaces(t *testing.T) {
	r := New("/api/v1/apps/records/")
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "sdn-dashboard", "SDN Node Dashboard", "1.0.4")); err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	if _, err := r.Declare(Declaration{
		ID: "spaceaware-orbital-console", Name: "Orbital Console",
		RuntimeClass: RuntimeBrowser, URL: "https://spaceaware.io/beta/",
	}); err != nil {
		t.Fatalf("Declare: %v", err)
	}

	defaults := r.Defaults()
	server, ok := defaults[RuntimeServer]
	if !ok {
		t.Fatal("no server default")
	}
	browser, ok := defaults[RuntimeBrowser]
	if !ok {
		t.Fatal("no browser default")
	}
	if server.CrossLink == nil || server.CrossLink.AppID != "spaceaware-orbital-console" ||
		server.CrossLink.URL != "https://spaceaware.io/beta/" {
		t.Fatalf("server cross_link = %+v", server.CrossLink)
	}
	if browser.CrossLink == nil || browser.CrossLink.AppID != "sdn-dashboard" || browser.CrossLink.URL != "/" {
		t.Fatalf("browser cross_link = %+v", browser.CrossLink)
	}
	// A declared app is a pointer, never a claim about content the node
	// cannot show: no record, no record URL.
	if browser.State != StateDeclared || browser.RecordURL != "" {
		t.Fatalf("declared entry = state %q record_url %q", browser.State, browser.RecordURL)
	}
	if _, ok := r.Record("spaceaware-orbital-console"); ok {
		t.Fatal("a declared app served record bytes it does not have")
	}
}

// JSON-capitalization law: $APP record fields keep the IDL's spelling, every
// field the node synthesizes is lowercase. A rename on either side is a
// contract break, so the wire keys are asserted verbatim.
func TestEntryJSONKeysFollowTheCapitalizationLaw(t *testing.T) {
	r := New("/api/v1/apps/records/")
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "sdn-dashboard", "SDN Node Dashboard", "1.0.4")); err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	entry, _ := r.Default(RuntimeServer)
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"ID", "NAME", "VERSION", "DESCRIPTION", "UI"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("record field %q missing from %s", key, raw)
		}
	}
	for _, key := range []string{"runtime_class", "state", "default", "url", "record_url"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("synthesized field %q missing from %s", key, raw)
		}
	}
	for _, forbidden := range []string{"id", "name", "version", "Runtime_Class", "RuntimeClass"} {
		if _, ok := decoded[forbidden]; ok {
			t.Fatalf("wrong-capitalization key %q present in %s", forbidden, raw)
		}
	}
	var pages []map[string]json.RawMessage
	if err := json.Unmarshal(decoded["UI"], &pages); err != nil {
		t.Fatalf("unmarshal UI: %v", err)
	}
	for _, key := range []string{"ID", "MEDIA_TYPE", "ENCODING", "CONTENT_SHA256", "ENTRY"} {
		if _, ok := pages[0][key]; !ok {
			t.Fatalf("UI page field %q missing from %v", key, pages[0])
		}
	}
	// CONTENT is never duplicated into a listing — the record serves it.
	if _, ok := pages[0]["CONTENT"]; ok {
		t.Fatal("UI page listing carried CONTENT")
	}
}

// Re-installing an ID replaces the entry (a rebuilt dashboard supersedes the
// previous one) without disturbing the declared default.
func TestReinstallReplacesInPlace(t *testing.T) {
	r := New("")
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "sdn-dashboard", "Dashboard", "1.0.0")); err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	if err := r.SetDefault(RuntimeServer, "sdn-dashboard"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if _, err := r.InstallRecord(RuntimeServer, "/", buildTestRecord(t, "sdn-dashboard", "Dashboard", "1.0.4")); err != nil {
		t.Fatalf("re-InstallRecord: %v", err)
	}
	if got := r.IDs(); len(got) != 1 {
		t.Fatalf("IDs = %v, want one entry", got)
	}
	entry, ok := r.Default(RuntimeServer)
	if !ok || entry.Version != "1.0.4" {
		t.Fatalf("default after reinstall = %+v ok=%v", entry, ok)
	}
}

func TestParseRuntimeClassAcceptsSchemaNames(t *testing.T) {
	for raw, want := range map[string]RuntimeClass{
		"server": RuntimeServer, "SERVER": RuntimeServer, "node": RuntimeServer, "NODE": RuntimeServer,
		"browser": RuntimeBrowser, "Browser": RuntimeBrowser, "page": RuntimeBrowser, "PAGE": RuntimeBrowser,
	} {
		got, ok := ParseRuntimeClass(raw)
		if !ok || got != want {
			t.Fatalf("ParseRuntimeClass(%q) = %q,%v want %q", raw, got, ok, want)
		}
	}
	if _, ok := ParseRuntimeClass("desktop"); ok {
		t.Fatal("ParseRuntimeClass accepted an unknown class")
	}
}
