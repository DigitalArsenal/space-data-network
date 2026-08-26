package flowrt

// THE GUARD LIVES WHERE THE BYTES LEAVE, SO THE TEST LIVES THERE TOO.
//
// storage.QuerySandboxedJSON sanitizes the bodies the ENGINE assembles, and
// internal/storage proves that. It cannot prove anything about the OTHER JSON
// producer on the same endpoint: the full-record presentation, where the host
// hands raw record frames to a wasm encoder and streams whatever comes back.
// A store-level test passes happily while /api/v1/query answers with a body
// no strict JSON reader accepts — that is exactly what happened.
//
// So these tests mount the REAL production public-query bundle over a REAL
// store, feed it records a hostile peer could publish, and assert on the
// BYTES ON THE WIRE.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/httpabi"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// hostileStringBytes is a string a peer can legally publish in a FlatBuffers
// record: nothing on the SDS write path requires a `string` field to be valid
// UTF-8. 0xFF/0xFE are never valid, 0x80 is a lone continuation byte, and
// 0xC3 followed by '(' is a truncated two-byte sequence.
var hostileStringBytes = []byte{0xff, 0xfe, 0x80, 'A', 0xc3, '('}

// TestPublicQueryJSONWireIsAlwaysAJSONText drives the production
// /api/v1/query mount and holds every JSON-labelled answer to RFC 8259: it is
// UTF-8, and it is a value (never zero bytes).
func TestPublicQueryJSONWireIsAlwaysAJSONText(t *testing.T) {
	dist := publicQueryFlowDist(t)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	tags := storage.SourceTags{ProviderID: "hostile-peer", SourceName: "hostile-feed", BatchID: "b1"}

	omm := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName(string(hostileStringBytes)).
		WithObjectID("2026-001A").
		WithEpoch("2026-06-01T00:00:00Z").
		WithEpochTimestamp(1783300000).
		WithMeanMotion(15.1).
		WithEccentricity(0.001).
		WithInclination(51.6).
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", omm[4:], "peer-hostile", nil, tags); err != nil {
		t.Fatalf("store OMM: %v", err)
	}

	// A GENERICALLY ROUTED standard (no decorations, no bespoke JSON
	// presentation): $GNO reaches the engine only because every embedded
	// standard is routed now, which is precisely why its json answer has to
	// be checked.
	gb := flatbuffers.NewBuilder(256)
	gnoID := gb.CreateString(string(hostileStringBytes))
	gb.StartObject(24)
	gb.PrependUOffsetTSlot(0, gnoID, 0)
	gb.FinishWithFileIdentifier(gb.EndObject(), []byte("$GNO"))
	if _, err := store.StoreWithSourceTags("GNO.fbs", gb.FinishedBytes(), "peer-hostile", nil, tags); err != nil {
		t.Fatalf("store GNO: %v", err)
	}

	queryCaps := flatsqlrt.SandboxCaps{MaxRows: 100, MaxBytes: 1 << 20, Timeout: 5 * time.Second}
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query",
		caps.NewStorageCapFactoryWithOptions(store, caps.StorageCapOptions{QueryCaps: queryCaps}))
	policy := approvedCapabilityPolicy(t, dist, "storage_query")

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/api/v1/query", Flow: dist, Pool: 1}},
		FlowMountDeps{
			CapRegistry:    reg,
			NodeCtx:        &modulert.NodeContext{CapabilityPolicy: policy},
			MaxMemoryPages: 4096,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()

	srv := httptest.NewServer(mux)
	defer srv.Close()
	url := srv.URL + "/api/v1/query"

	// The invariant EVERY json answer of this endpoint owes, whatever the
	// standard and whoever encoded it.
	assertJSONWire := func(t *testing.T, what string, resp *http.Response, body []byte) {
		t.Helper()
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			return // an honest non-JSON answer (e.g. a 502 text/plain refusal)
		}
		if !utf8.Valid(body) {
			t.Fatalf("%s: application/json body is not valid UTF-8 — no strict reader accepts it: %q", what, body)
		}
		if !json.Valid(body) {
			t.Fatalf("%s: application/json body is not a JSON text (%d bytes): %q", what, len(body), body)
		}
	}

	t.Run("full-record json never carries a record's invalid bytes", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json", `{"sql":"SELECT _data FROM OMM"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		assertJSONWire(t, "OMM full-record", resp, body)

		var rows []map[string]interface{}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("decode body: %v (%q)", err, body)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		name, _ := rows[0]["OBJECT_NAME"].(string)
		if !strings.ContainsRune(name, utf8.RuneError) {
			t.Fatalf("OBJECT_NAME = %q — the invalid run should surface as U+FFFD", name)
		}
		if norad, ok := rows[0]["NORAD_CAT_ID"].(float64); !ok || norad != 25544 {
			t.Fatalf("NORAD_CAT_ID = %v — sanitizing must not disturb any other field", rows[0]["NORAD_CAT_ID"])
		}
	})

	t.Run("the record itself is never rewritten", func(t *testing.T) {
		// The flatbuffer path is the wire format: a consumer that wants the
		// original bytes of a hostile string still gets them.
		resp, body := queryPOST(t, url, `{"sql":"SELECT _data FROM OMM"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("content-type = %q", ct)
		}
		if utf8.Valid(body) {
			t.Fatalf("the FlatBuffer stream lost the record's original bytes")
		}
	})

	t.Run("a generically routed standard never answers a silent empty 200", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json", `{"sql":"SELECT _data FROM GNO"}`, nil)
		assertJSONWire(t, "GNO full-record", resp, body)
		if resp.StatusCode == http.StatusOK && len(body) == 0 {
			t.Fatal("200 + application/json + zero bytes: an empty body is not a JSON text, " +
				"and it reads to a caller like 'this standard has no records'")
		}
		if resp.StatusCode == http.StatusOK {
			// Once the bundle grows a generic record->JSON presentation
			// (task modules-public-query-generic-record-json) this becomes
			// the live shape and must still be a JSON text.
			return
		}
		if len(body) == 0 {
			t.Fatalf("status %d with no explanation at all", resp.StatusCode)
		}
	})

	t.Run("projections and the flatbuffer path stay available for that standard", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json", `{"sql":"SELECT ID FROM GNO"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("projection status = %d body %q", resp.StatusCode, body)
		}
		assertJSONWire(t, "GNO projection", resp, body)

		fbResp, fbBody := queryPOST(t, url, `{"sql":"SELECT _data FROM GNO"}`, nil)
		if fbResp.StatusCode != http.StatusOK || len(fbBody) == 0 {
			t.Fatalf("flatbuffer status = %d (%d bytes)", fbResp.StatusCode, len(fbBody))
		}
	})

	t.Run("the surface listing is a JSON text", func(t *testing.T) {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET surface: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 1<<20)
		tmp := make([]byte, 32<<10)
		for {
			n, rerr := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		assertJSONWire(t, "surface listing", resp, buf)

		// A junk column must not be advertised like a real one. $LDM's first
		// field is a sub-table, so its only projected column is the
		// >=1-column-invariant placeholder that reads one byte of slot 0 —
		// listed under the field's own name, and now marked as what it is.
		var surface struct {
			Tables []struct {
				Name         string   `json:"name"`
				Columns      []string `json:"columns"`
				Placeholders []string `json:"placeholder_columns"`
			} `json:"tables"`
		}
		if err := json.Unmarshal(buf, &surface); err != nil {
			t.Fatalf("decode surface: %v", err)
		}
		found := false
		for _, rel := range surface.Tables {
			if rel.Name != "LDM" {
				continue
			}
			found = true
			if len(rel.Placeholders) != 1 || rel.Placeholders[0] != "SITE" {
				t.Fatalf("LDM placeholder_columns = %v, want [SITE] — SELECT SITE FROM LDM returns a one-byte junk read, not the site",
					rel.Placeholders)
			}
		}
		if !found {
			t.Fatal("the public surface does not list LDM")
		}
	})
}

// ==========================================================================
// The guard itself, exercised frame by frame
// ==========================================================================

func htrFrame(t *testing.T, status uint16, contentType string, body []byte) FrameData {
	t.Helper()
	var headers []httpabi.Header
	if contentType != "" {
		headers = []httpabi.Header{{Name: "content-type", Value: contentType}}
	}
	return htrFrameHeaders(t, status, headers, body)
}

// htrFrameHeaders builds an egress frame with an arbitrary header block — the
// shape a flow that declares its own Content-Length or Vary produces.
func htrFrameHeaders(t *testing.T, status uint16, headers []httpabi.Header, body []byte) FrameData {
	t.Helper()
	return FrameData{PortID: "response", Bytes: httpabi.EncodeResponse(&httpabi.Response{
		Status:  status,
		Headers: headers,
		Body:    body,
	})}
}

// runPipe drives htrPipe exactly as ServeHTTP does: one emit per egress
// invocation, then finish().
func runPipe(t *testing.T, method string, frames ...FrameData) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	pipe := &htrPipe{w: rec, method: method}
	for _, frame := range frames {
		if _, err := pipe.emit(t.Context(), &InvocationArgs{Frames: []FrameData{frame}}); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	pipe.finish()
	if pipe.err != nil {
		t.Fatalf("pipe error: %v", pipe.err)
	}
	return rec
}

func TestJSONWireGuardPreservesRunesSplitAcrossFrames(t *testing.T) {
	// "héllo ☃" split so that BOTH multi-byte runes straddle a frame
	// boundary — the shape a streaming encoder produces and the shape a
	// naive per-frame utf8.Valid check would corrupt into U+FFFD.
	whole := []byte(`["héllo ☃"]`)
	cuts := []int{4, 5, 10, 11, 12}
	frames := make([]FrameData, 0, len(cuts)+1)
	prev := 0
	for _, cut := range cuts {
		frames = append(frames, htrFrame(t, 200, "application/json", whole[prev:cut]))
		prev = cut
	}
	frames = append(frames, htrFrame(t, 200, "application/json", whole[prev:]))

	rec := runPipe(t, http.MethodPost, frames...)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != string(whole) {
		t.Fatalf("body = %q, want %q — a split rune must be carried, not replaced", got, whole)
	}
}

func TestJSONWireGuardReplacesInvalidRunsAndRefusesEmptyBodies(t *testing.T) {
	t.Run("invalid bytes become U+FFFD", func(t *testing.T) {
		body := append(append([]byte(`["`), hostileStringBytes...), []byte(`"]`)...)
		rec := runPipe(t, http.MethodPost, htrFrame(t, 200, "application/json", body))
		if !utf8.Valid(rec.Body.Bytes()) || !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})

	t.Run("an empty json 200 is refused, not sent", func(t *testing.T) {
		rec := runPipe(t, http.MethodPost, htrFrame(t, 200, "application/json", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
			t.Fatalf("the refusal inherited the flow's content type %q", ct)
		}
	})

	t.Run("the refusal keeps headers the host set, drops the ones the flow set", func(t *testing.T) {
		// An outer middleware's headers (CORS, security) are on the same
		// writer: refusing the flow's response must not strip them.
		rec := httptest.NewRecorder()
		rec.Header().Set("Access-Control-Allow-Origin", "*")
		pipe := &htrPipe{w: rec, method: http.MethodPost}
		if _, err := pipe.emit(t.Context(), &InvocationArgs{
			Frames: []FrameData{htrFrame(t, 200, "application/json", nil)},
		}); err != nil {
			t.Fatalf("emit: %v", err)
		}
		pipe.finish()
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("the host's own header was dropped: %q", got)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
			t.Fatalf("the flow's content type survived the refusal: %q", ct)
		}
	})

	t.Run("a body-less status may declare json and stay empty", func(t *testing.T) {
		for _, status := range []uint16{http.StatusNoContent, http.StatusNotModified} {
			rec := runPipe(t, http.MethodPost, htrFrame(t, status, "application/json", nil))
			if rec.Code != int(status) {
				t.Fatalf("status %d answered %d", status, rec.Code)
			}
		}
	})

	t.Run("HEAD may declare json and stay empty", func(t *testing.T) {
		rec := runPipe(t, http.MethodHead, htrFrame(t, 200, "application/json", nil))
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Fatalf("HEAD answered %d with %d body bytes", rec.Code, rec.Body.Len())
		}
	})

	t.Run("non-json bodies stream verbatim", func(t *testing.T) {
		raw := append([]byte{0x0c, 0x00, 0x00, 0x00}, hostileStringBytes...)
		rec := runPipe(t, http.MethodPost,
			htrFrame(t, 200, "application/vnd.sdn.flatbuffers.stream", raw))
		if rec.Body.String() != string(raw) {
			t.Fatalf("body = %q, want the bytes verbatim", rec.Body.String())
		}
	})

	t.Run("an empty non-json 200 is the flow's business", func(t *testing.T) {
		rec := runPipe(t, http.MethodPost, htrFrame(t, 200, "text/plain", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the guard only speaks for JSON", rec.Code)
		}
	})
}

func TestJSONContentTypeDetection(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"APPLICATION/JSON", true},
		{"text/json", true},
		{"application/problem+json", true},
		{"application/vnd.sdn.flatbuffers.stream", false},
		{"text/plain; charset=utf-8", false},
		{"application/octet-stream", false},
	}
	for _, tc := range cases {
		got := declaresJSONBody([]httpabi.Header{{Name: "Content-Type", Value: tc.value}})
		if got != tc.want {
			t.Fatalf("declaresJSONBody(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
	if declaresJSONBody(nil) {
		t.Fatal("a response with no content type declares no JSON body")
	}
}

// TestJSONWireGuardDropsAFlowDeclaredContentLength drives the guard through a
// REAL net/http server, because the defect it closes only exists there: the
// guard can GROW the body (one invalid byte becomes a three-byte U+FFFD), and
// net/http truncates every write past a Content-Length the response already
// declared. A flow that computed its length before the rewrite would therefore
// have its body cut at an arbitrary offset — malformed JSON, possibly with a
// U+FFFD sliced in half, which is the very invalid UTF-8 the guard exists to
// prevent. The recorder used by the other tests does not enforce lengths, so
// this one takes the socket.
func TestJSONWireGuardDropsAFlowDeclaredContentLength(t *testing.T) {
	body := []byte("[\"A\xffB\"]") // 8 bytes; 10 after replacement
	frame := htrFrameHeaders(t, 200, []httpabi.Header{
		{Name: "content-type", Value: "application/json"},
		{Name: "content-length", Value: "8"},
	}, body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pipe := &htrPipe{w: w, method: r.Method}
		if _, err := pipe.emit(r.Context(), &InvocationArgs{Frames: []FrameData{frame}}); err != nil {
			t.Errorf("emit: %v", err)
			return
		}
		pipe.finish()
		if pipe.err != nil {
			t.Errorf("pipe error: %v", pipe.err)
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v (got %q)", err, got)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(got) <= len(body) {
		t.Fatalf("client got %d bytes %q, want the whole %d-byte sanitized body — a stale Content-Length truncated it",
			len(got), got, len(body)+2)
	}
	if !utf8.Valid(got) || !json.Valid(got) {
		t.Fatalf("client got %q, which is not a JSON text", got)
	}
	if cl := resp.Header.Get("Content-Length"); cl == "8" {
		t.Fatalf("the flow's pre-sanitization Content-Length %q reached the wire", cl)
	}
}

// TestRefusalRestoresHeaderValuesTheHostSet covers the case a delete-by-name
// drop silently broke: a header BOTH an outer middleware and the flow set. The
// refusal must take back exactly the flow's values.
func TestRefusalRestoresHeaderValuesTheHostSet(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Add("Vary", "Origin")
	rec.Header().Set("Access-Control-Allow-Origin", "*")

	pipe := &htrPipe{w: rec, method: http.MethodGet}
	frame := htrFrameHeaders(t, 200, []httpabi.Header{
		{Name: "content-type", Value: "application/json"},
		{Name: "vary", Value: "Accept"},
		{Name: "etag", Value: `"deadbeef"`},
	}, nil)
	if _, err := pipe.emit(t.Context(), &InvocationArgs{Frames: []FrameData{frame}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	pipe.finish()

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Origin" {
		t.Fatalf("Vary = %v, want [Origin] — the flow's value must go, the middleware's must stay", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("the host's own header was dropped: %q", got)
	}
	if got := rec.Header().Get("Etag"); got != "" {
		t.Fatalf("the flow's ETag survived the refusal: %q", got)
	}
}

// TestBodyLessStatusesMayDeclareJSON pins the exemption list to the HTTP spec
// the guard cites: 205 Reset Content is REQUIRED to have no content (RFC 9110
// §15.3.6), so refusing one would be the guard inventing a defect.
func TestBodyLessStatusesMayDeclareJSON(t *testing.T) {
	for _, status := range []uint16{
		http.StatusContinue,
		http.StatusNoContent,
		http.StatusResetContent,
		http.StatusNotModified,
	} {
		rec := runPipe(t, http.MethodGet, htrFrame(t, status, "application/json", nil))
		if rec.Code != int(status) {
			t.Fatalf("status %d answered %d — a body-less status may declare a media type and send nothing", status, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("status %d sent %d body bytes", status, rec.Body.Len())
		}
	}
}
