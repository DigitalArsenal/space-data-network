package appmanifest

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func inlineGzipPage(t *testing.T, id string, raw []byte, entry bool) UIPage {
	t.Helper()
	content, err := EncodingBase64Gzip.encodeContent(raw)
	if err != nil {
		t.Fatalf("encode inline page %q: %v", id, err)
	}
	return UIPage{
		ID:            id,
		Title:         "Page " + id,
		Content:       content,
		Encoding:      EncodingBase64Gzip,
		MediaType:     "text/html",
		ContentSHA256: sha256Hex(raw),
		Entry:         entry,
	}
}

func TestEncodingRoundTrip(t *testing.T) {
	raw := []byte("<!doctype html><title>hi</title><body>conjunction \x00\x01\xff bytes</body>")
	for _, enc := range []UIContentEncoding{EncodingUTF8, EncodingBase64, EncodingBase64Gzip, "" /* normalizes to utf8 */} {
		s, err := enc.encodeContent(raw)
		if err != nil {
			t.Fatalf("encode(%q) error = %v", enc, err)
		}
		got, err := enc.decodeContent(s)
		if err != nil {
			t.Fatalf("decode(%q) error = %v", enc, err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("encoding %q did not round-trip", enc)
		}
	}

	// Brotli is a recognized enum value but has no vendored codec: encode and
	// decode both error rather than silently guessing.
	if _, err := EncodingBase64Brotli.encodeContent(raw); err == nil {
		t.Fatalf("base64_brotli encode: want error, got nil")
	}
	if _, err := EncodingBase64Brotli.decodeContent("abc"); err == nil {
		t.Fatalf("base64_brotli decode: want error, got nil")
	}
}

func TestPureUIAppValidWithoutModules(t *testing.T) {
	raw := []byte("<html>pure ui</html>")
	m := &AppManifest{
		ID:      "io.example.pureui",
		Name:    "Pure UI",
		Version: "0.1.0",
		Pages:   []UIPage{inlineGzipPage(t, "home", raw, true)},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("pure-UI app (zero modules, one inline page) should validate, got %v", err)
	}
}

func TestUIPageValidationRules(t *testing.T) {
	raw := []byte("<html>x</html>")
	base := func() *AppManifest {
		return &AppManifest{
			ID:      "io.example.app",
			Name:    "App",
			Version: "1.0.0",
			Modules: []ModuleRef{{ID: "ui", PluginID: "ui-plugin"}},
			Pages:   []UIPage{inlineGzipPage(t, "home", raw, true)},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*AppManifest)
		wantErr string
	}{
		{
			name: "inline and module-served both set",
			mutate: func(m *AppManifest) {
				m.Pages[0].ModuleID = "ui"
				m.Pages[0].URL = "/x"
			},
			wantErr: "exactly one of inline content or moduleId+url",
		},
		{
			name: "neither inline nor module-served",
			mutate: func(m *AppManifest) {
				m.Pages[0].Content = ""
				m.Pages[0].ContentSHA256 = ""
			},
			wantErr: "exactly one of inline content or moduleId+url",
		},
		{
			name: "inline missing sha",
			mutate: func(m *AppManifest) {
				m.Pages[0].ContentSHA256 = ""
			},
			wantErr: "contentSha256 is required",
		},
		{
			name: "inline wrong sha",
			mutate: func(m *AppManifest) {
				m.Pages[0].ContentSHA256 = strings.Repeat("a", 64)
			},
			wantErr: "contentSha256 mismatch",
		},
		{
			name: "module-served missing url",
			mutate: func(m *AppManifest) {
				m.Pages[0] = UIPage{ID: "home", ModuleID: "ui", Entry: true}
			},
			wantErr: "url is required for a module-served page",
		},
		{
			name: "module-served dangling moduleId",
			mutate: func(m *AppManifest) {
				m.Pages[0] = UIPage{ID: "home", ModuleID: "nope", URL: "/x", Entry: true}
			},
			wantErr: "does not match any modules",
		},
		{
			name: "duplicate page id",
			mutate: func(m *AppManifest) {
				p := inlineGzipPage(t, "home", raw, false)
				m.Pages = append(m.Pages, p)
			},
			wantErr: "duplicate page id",
		},
		{
			name: "no entry page",
			mutate: func(m *AppManifest) {
				m.Pages[0].Entry = false
			},
			wantErr: "exactly one UI page must be marked entry",
		},
		{
			name: "two entry pages",
			mutate: func(m *AppManifest) {
				m.Pages = append(m.Pages, inlineGzipPage(t, "second", raw, true))
			},
			wantErr: "exactly one UI page must be marked entry",
		},
		{
			name: "unknown encoding",
			mutate: func(m *AppManifest) {
				m.Pages[0].Encoding = "rot13"
			},
			wantErr: "unknown content encoding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := base()
			tt.mutate(m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestPagesBasedAPPRoundTrip exercises the $APP lane over a manifest that uses
// modules, data, sources, an inline entry page AND a module-served page — the
// full field surface — and asserts a struct-equal identity round-trip.
func TestPagesBasedAPPRoundTrip(t *testing.T) {
	raw := []byte("<html>full surface</html>")
	m := &AppManifest{
		ID:          "io.example.full",
		Name:        "Full Surface",
		Version:     "2.3.1",
		Description: "exercises every $APP field",
		CreatedAt:   "2026-07-13T00:00:00.000Z",
		UpdatedAt:   "2026-07-13T01:02:03.004Z",
		Modules: []ModuleRef{
			{ID: "core", PluginID: "core-plugin", ContentHash: "deadbeef", Version: "1.0.0", Role: "primary", Description: "core"},
		},
		Data: []DataRef{
			{ID: "omm-in", SDSType: "OMM", Direction: DataDirectionConsumes, Description: "catalog"},
			{ID: "cdm-out", SDSType: "CDM", Direction: DataDirectionProduces, ModuleID: "core", Description: "results"},
		},
		Sources: []SourceRef{
			{ID: "self-src", Kind: SourceKindModule, Ref: "core", Description: "module source"},
			{ID: "ext-src", Kind: SourceKindExternalAPI, Ref: "/api/v1/peers", Description: "peers surface"},
			{ID: "ds-src", Kind: SourceKindDataset, Ref: "celestrak:active", Description: "dataset"},
		},
		Pages: []UIPage{
			inlineGzipPage(t, "home", raw, true),
			{ID: "detail", Title: "Detail", ModuleID: "core", URL: "/detail", Color: "#123456"},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("base manifest Validate() error = %v", err)
	}

	buf, err := m.ToAPP()
	if err != nil {
		t.Fatalf("ToAPP() error = %v", err)
	}
	back, err := FromAPP(buf)
	if err != nil {
		t.Fatalf("FromAPP() error = %v", err)
	}
	if !reflect.DeepEqual(m, back) {
		t.Fatalf("$APP round-trip changed the manifest:\n got  = %#v\n want = %#v", back, m)
	}
}

func TestFromAPPRejectsGarbage(t *testing.T) {
	for _, buf := range [][]byte{nil, {}, []byte("not a flatbuffer at all")} {
		if _, err := FromAPP(buf); err == nil {
			t.Fatalf("FromAPP(%q): want error, got nil", buf)
		}
	}
	// A valid-length but structurally bogus buffer must error, not panic.
	if _, err := FromAPP(bytes.Repeat([]byte{0x04, 0x00, 0x00, 0x00}, 8)); err == nil {
		t.Fatalf("FromAPP(bogus): want error, got nil")
	}
}
