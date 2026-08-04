package apps

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	APPfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
)

// The record this node mints is a real $APP FlatBuffer: right file
// identifier, size-prefixed, and the page bytes come back byte-identical.
// "Same bytes, two envelopes" only holds if the round trip is exact.
func TestBuildInlinePageRecordRoundTrips(t *testing.T) {
	page := []byte("<!doctype html><title>SDN</title><body>dashboard</body>")
	record, err := BuildInlinePageRecord(
		AppIdentity{ID: "sdn-dashboard", Name: "SDN Node Dashboard", Version: "1.0.4", Description: "face"},
		[]InlinePage{{ID: "dashboard", Title: "SDN Node Dashboard", HTML: page, Entry: true}},
	)
	if err != nil {
		t.Fatalf("BuildInlinePageRecord: %v", err)
	}
	if !APPfb.SizePrefixedAPPBufferHasIdentifier(record) {
		t.Fatal("record does not carry the $APP file identifier")
	}
	root := APPfb.GetSizePrefixedRootAsAPP(record, 0)
	if string(root.ID()) != "sdn-dashboard" {
		t.Fatalf("ID = %q", root.ID())
	}
	if root.UILength() != 1 {
		t.Fatalf("UILength = %d, want 1", root.UILength())
	}
	var ui APPfb.APPUIPage
	if !root.UI(&ui, 0) {
		t.Fatal("UI[0] missing")
	}
	if string(ui.CONTENT()) != string(page) {
		t.Fatal("CONTENT did not round-trip byte-identically")
	}
	if !ui.ENTRY() {
		t.Fatal("ENTRY not set")
	}
	if string(ui.MEDIA_TYPE()) != "text/html; charset=utf-8" {
		t.Fatalf("MEDIA_TYPE = %q", ui.MEDIA_TYPE())
	}
	sum := sha256.Sum256(page)
	if string(ui.CONTENT_SHA256()) != hex.EncodeToString(sum[:]) {
		t.Fatalf("CONTENT_SHA256 = %q", ui.CONTENT_SHA256())
	}
}

// CONTENT_SHA256 exists so a launcher can VERIFY what it decoded. It is
// therefore always computed from the bytes and never accepted from a caller —
// this test pins that by changing the page and demanding the hash follow.
func TestContentHashTracksTheBytes(t *testing.T) {
	first, err := BuildInlinePageRecord(
		AppIdentity{ID: "app"},
		[]InlinePage{{ID: "p", HTML: []byte("a"), Entry: true}})
	if err != nil {
		t.Fatalf("build first: %v", err)
	}
	second, err := BuildInlinePageRecord(
		AppIdentity{ID: "app"},
		[]InlinePage{{ID: "p", HTML: []byte("b"), Entry: true}})
	if err != nil {
		t.Fatalf("build second: %v", err)
	}
	a, err := DecodeAPP(first)
	if err != nil {
		t.Fatalf("decode first: %v", err)
	}
	b, err := DecodeAPP(second)
	if err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if a.UI[0].ContentSHA256 == b.UI[0].ContentSHA256 {
		t.Fatal("different page bytes produced the same CONTENT_SHA256")
	}
}

// Exactly one ENTRY page, per the schema. Zero or two is a malformed app and
// is refused at build time rather than shipped to a launcher that must guess.
func TestBuildRejectsWrongEntryPageCount(t *testing.T) {
	for name, pages := range map[string][]InlinePage{
		"none": {{ID: "a", HTML: []byte("x")}},
		"two":  {{ID: "a", HTML: []byte("x"), Entry: true}, {ID: "b", HTML: []byte("y"), Entry: true}},
	} {
		if _, err := BuildInlinePageRecord(AppIdentity{ID: "app"}, pages); err == nil {
			t.Fatalf("%s ENTRY pages accepted", name)
		}
	}
}

func TestBuildRejectsEmptyInput(t *testing.T) {
	if _, err := BuildInlinePageRecord(AppIdentity{}, []InlinePage{{ID: "a", HTML: []byte("x"), Entry: true}}); err == nil {
		t.Fatal("record with no ID accepted")
	}
	if _, err := BuildInlinePageRecord(AppIdentity{ID: "app"}, nil); err == nil {
		t.Fatal("record with no pages accepted")
	}
	if _, err := BuildInlinePageRecord(AppIdentity{ID: "app"}, []InlinePage{{ID: "a", Entry: true}}); err == nil {
		t.Fatal("page with no content accepted")
	}
}

// A decoder that reads whatever it is handed is how a wrong-typed buffer
// becomes a plausible-looking app listing. The file identifier is checked.
func TestDecodeRejectsNonAPPBuffers(t *testing.T) {
	if _, err := DecodeAPP(nil); err == nil {
		t.Fatal("nil buffer decoded")
	}
	if _, err := DecodeAPP([]byte("not a flatbuffer")); err == nil {
		t.Fatal("garbage decoded as an $APP record")
	}
	record, err := BuildInlinePageRecord(AppIdentity{ID: "app"},
		[]InlinePage{{ID: "p", HTML: []byte("x"), Entry: true}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	corrupted := append([]byte(nil), record...)
	// The file identifier sits at bytes 8..12 of a size-prefixed buffer.
	copy(corrupted[8:12], []byte("$XXX"))
	if _, err := DecodeAPP(corrupted); err == nil || !strings.Contains(err.Error(), "file identifier") {
		t.Fatalf("corrupted identifier decoded, err = %v", err)
	}
}
