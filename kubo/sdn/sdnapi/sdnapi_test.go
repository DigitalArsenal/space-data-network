package sdnapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
)

const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`

func ommSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

func buildOMM(t *testing.T, norad uint32, name string) []byte {
	t.Helper()
	sized := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch("2026-05-10T00:00:00Z").
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
	return sized[4:] // strip size prefix -> single FlatBuffer
}

func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return base + "/sdn-flatsqlrt-test-aot"
}

// newTestStore opens an in-memory sdnstore seeded with two OMM records under a
// single (source, type) pair.
func newTestStore(t *testing.T) *sdnstore.Store {
	t.Helper()
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)
	st, err := sdnstore.Open(sdnstore.Config{
		Blockstore:     bs,
		Datastore:      mds,
		Schemas:        ommSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	for _, r := range [][]byte{buildOMM(t, 1001, "SAT-A1"), buildOMM(t, 1002, "SAT-A2")} {
		if _, err := st.Store(t.Context(), "celestrak-gp", "OMM", r); err != nil {
			t.Fatalf("store record: %v", err)
		}
	}
	return st
}

func testDeps(st *sdnstore.Store) sdnapi.Deps {
	return sdnapi.Deps{
		Node: func() sdnapi.NodeInfo {
			return sdnapi.NodeInfo{
				PeerID:        "12D3KooWTestPeerID",
				FlagNamespace: "space-data-network/discovery/advertisement-flag/spacedatanetwork/1.0.0",
				PubSubEnabled: true,
			}
		},
		Store:     func() *sdnstore.Store { return st },
		IPFSPeers: func() []string { return []string{"12D3KooWSwarmPeer"} },
		SDNPeers:  func() []string { return []string{"12D3KooWSdnPeer"} },
	}
}

func get(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Header()
}

func TestDataSources(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, hdr := get(t, h, "/sdn/v1/data/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := hdr.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	var got []sdnstore.CatalogEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d catalog entries, want 1: %+v", len(got), got)
	}
	if got[0].Source != "celestrak-gp" || got[0].Type != "OMM" {
		t.Errorf("catalog entry = %+v, want {celestrak-gp OMM}", got[0])
	}
}

func TestData(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/data?source=celestrak-gp&type=OMM")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Source   string `json:"source"`
		Type     string `json:"type"`
		Total    int    `json:"total"`
		Returned int    `json:"returned"`
		Limit    int    `json:"limit"`
		Records  []struct {
			CID    string `json:"cid"`
			Size   int    `json:"size"`
			FileID string `json:"file_id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.Source != "celestrak-gp" || got.Type != "OMM" {
		t.Errorf("source/type = %s/%s", got.Source, got.Type)
	}
	if got.Total != 2 || got.Returned != 2 || len(got.Records) != 2 {
		t.Fatalf("total=%d returned=%d records=%d, want 2/2/2", got.Total, got.Returned, len(got.Records))
	}
	if got.Limit != sdnapi.DefaultDataLimit {
		t.Errorf("limit = %d, want default %d", got.Limit, sdnapi.DefaultDataLimit)
	}
	for i, r := range got.Records {
		if r.CID == "" {
			t.Errorf("record %d has empty cid", i)
		}
		if r.Size <= 0 {
			t.Errorf("record %d size = %d", i, r.Size)
		}
		if r.FileID != "$OMM" {
			t.Errorf("record %d file_id = %q, want $OMM", i, r.FileID)
		}
	}
}

func TestDataLimit(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/data?source=celestrak-gp&type=OMM&limit=1")
	var got struct {
		Total    int `json:"total"`
		Returned int `json:"returned"`
		Limit    int `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 2 || got.Returned != 1 || got.Limit != 1 {
		t.Errorf("total=%d returned=%d limit=%d, want 2/1/1", got.Total, got.Returned, got.Limit)
	}
}

func TestDataMissingParams(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/data?source=celestrak-gp")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestNode(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/node")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		PeerID           string `json:"peer_id"`
		SDNFlagNamespace string `json:"sdn_flag_namespace"`
		PubSubEnabled    bool   `json:"pubsub_enabled"`
		Storage          struct {
			Sources int  `json:"sources"`
			Records *int `json:"records"`
		} `json:"storage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.PeerID != "12D3KooWTestPeerID" {
		t.Errorf("peer_id = %q", got.PeerID)
	}
	if got.SDNFlagNamespace == "" {
		t.Error("sdn_flag_namespace is empty")
	}
	if !got.PubSubEnabled {
		t.Error("pubsub_enabled should be true")
	}
	if got.Storage.Sources != 1 {
		t.Errorf("storage.sources = %d, want 1", got.Storage.Sources)
	}
	if got.Storage.Records != nil {
		t.Errorf("storage.records should be omitted (not cheap), got %v", *got.Storage.Records)
	}
}

func TestPeers(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/peers")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		IPFS []string `json:"ipfs"`
		SDN  []string `json:"sdn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.IPFS) != 1 || got.IPFS[0] != "12D3KooWSwarmPeer" {
		t.Errorf("ipfs peers = %v", got.IPFS)
	}
	if len(got.SDN) != 1 || got.SDN[0] != "12D3KooWSdnPeer" {
		t.Errorf("sdn peers = %v", got.SDN)
	}
}

func TestChannels(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/channels")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []struct {
		Source   string `json:"source"`
		Standard string `json:"standard"`
		Topic    string `json:"topic"`
		Active   bool   `json:"active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d channels, want 1: %+v", len(got), got)
	}
	if got[0].Source != "celestrak-gp" || got[0].Standard != "OMM" {
		t.Errorf("channel = %+v", got[0])
	}
	if got[0].Topic != "/spacedatanetwork/channels/OMM/celestrak-gp" {
		t.Errorf("topic = %q", got[0].Topic)
	}
	// No Channels dep supplied -> storage-only -> known but inactive.
	if got[0].Active {
		t.Error("channel should be inactive with no pubsub fan-out")
	}
}

func TestApps_EmptyByDefault(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	rec, _ := get(t, h, "/sdn/v1/apps")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(got) != 0 {
		t.Errorf("apps should be empty, got %d", len(got))
	}
}

// storeApp installs an inline-UI $APP record into the store via the composition
// write-path (StoreManifest, no FlatSQL schema needed for type "APP"), so the
// apps list + app-UI routes have a real installed app to serve.
func storeApp(t *testing.T, st *sdnstore.Store, id, name, html string) {
	t.Helper()
	sum := sha256.Sum256([]byte(html))
	app := &appmanifest.AppManifest{
		ID:      id,
		Name:    name,
		Version: "1.0.0",
		Dataflow: []appmanifest.DataflowEntry{{
			Name:      "omm-in",
			Direction: appmanifest.FlowDirectionToPage,
			SDSSchema: "OMM",
			Transport: appmanifest.FlowTransportGatewayRoute,
			Locator:   "/sdn/v1/data?source={source}&type=OMM",
		}},
		Pages: []appmanifest.UIPage{{
			ID:            "main",
			Content:       html,
			Encoding:      appmanifest.EncodingUTF8,
			MediaType:     "text/html; charset=utf-8",
			ContentSHA256: hex.EncodeToString(sum[:]),
			Entry:         true,
		}},
	}
	buf, err := app.ToAPP()
	if err != nil {
		t.Fatalf("ToAPP: %v", err)
	}
	if _, err := st.StoreManifest(t.Context(), "sdn", "APP", buf); err != nil {
		t.Fatalf("StoreManifest: %v", err)
	}
}

// GET /sdn/v1/apps lists an installed $APP record with its decoded id/name/
// version and page count.
func TestAppsList_Installed(t *testing.T) {
	st := newTestStore(t)
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", "<!doctype html><title>omm</title><body>fetch /sdn/v1/data</body>")
	h := sdnapi.NewHandler(testDeps(st))

	rec, _ := get(t, h, "/sdn/v1/apps")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Source  string `json:"source"`
		CID     string `json:"cid"`
		Pages   int    `json:"pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("apps = %d, want 1: %+v", len(got), got)
	}
	if got[0].ID != "supplemental-omm" || got[0].Name != "Supplemental OMM" || got[0].Version != "1.0.0" {
		t.Errorf("app summary = %+v", got[0])
	}
	if got[0].Source != "sdn" || got[0].CID == "" || got[0].Pages != 1 {
		t.Errorf("app source/cid/pages = %q/%q/%d", got[0].Source, got[0].CID, got[0].Pages)
	}
}

// GET /sdn/v1/apps/<id> serves the app's inline entry page straight from the
// $APP record, as text/html, and the body is the exact page bytes.
func TestAppUI_ServesInlinePage(t *testing.T) {
	st := newTestStore(t)
	html := "<!doctype html><title>omm board</title><body><script>fetch('/sdn/v1/data/sources')</script></body>"
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", html)
	h := sdnapi.NewHandler(testDeps(st))

	rec, hdr := get(t, h, "/sdn/v1/apps/supplemental-omm")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := hdr.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if body != html {
		t.Errorf("served body does not match stored inline page; got %d bytes", len(body))
	}
	if !strings.Contains(body, "/sdn/v1/data") {
		t.Errorf("served app UI does not reference /sdn/v1/data")
	}
}

// Re-seeding an app with CHANGED content (e.g. shipping a binary with an
// edited board.html — StoreManifest is content-addressed, so different bytes
// land as a NEW block under the same (source, "APP") pair rather than
// overwriting the old one) must serve the NEWEST content, never a stale
// version an earlier boot already stored. Regression test for the bug where
// appUI walked ReadBySourceType's oldest-first order and returned on the
// FIRST id match — i.e. the OLDEST content — forever hiding every content
// update to an already-seeded app.
func TestAppUI_ReseededContentServesNewest(t *testing.T) {
	st := newTestStore(t)
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", "<!doctype html><title>v1</title><body>old board</body>")
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", "<!doctype html><title>v2</title><body>NEW board content</body>")
	h := sdnapi.NewHandler(testDeps(st))

	rec, _ := get(t, h, "/sdn/v1/apps/supplemental-omm")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "NEW board content") {
		t.Errorf("appUI served stale content after re-seed; got %d bytes: %.120s", len(body), body)
	}
	if strings.Contains(body, "old board") {
		t.Errorf("appUI served the OLD re-seeded content instead of the newest")
	}
}

// GET /sdn/v1/apps must not list the SAME app id twice just because it was
// re-seeded with different content across boots — the listing shows one tile
// per installed app, carrying the newest version's summary.
func TestAppsList_ReseededContentDedupes(t *testing.T) {
	st := newTestStore(t)
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", "<!doctype html><body>v1</body>")
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", "<!doctype html><body>v2 (newer)</body>")
	h := sdnapi.NewHandler(testDeps(st))

	rec, _ := get(t, h, "/sdn/v1/apps")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID  string `json:"id"`
		CID string `json:"cid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("apps = %d, want 1 (deduped): %+v", len(got), got)
	}
	if got[0].ID != "supplemental-omm" {
		t.Errorf("app id = %q", got[0].ID)
	}
}

// An unknown app id is a plain 404.
func TestAppUI_UnknownIs404(t *testing.T) {
	st := newTestStore(t)
	storeApp(t, st, "supplemental-omm", "Supplemental OMM", "<!doctype html><body>/sdn/v1/data</body>")
	h := sdnapi.NewHandler(testDeps(st))
	rec, _ := get(t, h, "/sdn/v1/apps/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown app status = %d, want 404", rec.Code)
	}
}

// A GET-only surface: non-GET is 405.
func TestMethodNotAllowed(t *testing.T) {
	h := sdnapi.NewHandler(testDeps(newTestStore(t)))
	req := httptest.NewRequest(http.MethodPost, "/sdn/v1/node", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// The handler must not panic when the runtime services are absent (listener
// started before the store exists); storage endpoints return empty.
func TestNilStore(t *testing.T) {
	h := sdnapi.NewHandler(sdnapi.Deps{
		Node: func() sdnapi.NodeInfo { return sdnapi.NodeInfo{PeerID: "p"} },
	})
	for _, path := range []string{"/sdn/v1/node", "/sdn/v1/peers", "/sdn/v1/data/sources", "/sdn/v1/channels", "/sdn/v1/apps"} {
		rec, _ := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

// depsWithBlockstore is testDeps plus a live block store backing the module
// endpoint, so GET /sdn/v1/module?hash= can resolve stored module bytes.
func depsWithBlockstore(st *sdnstore.Store, bs blockstore.Blockstore) sdnapi.Deps {
	d := testDeps(st)
	d.Blockstore = func() appmanifest.ModuleBlockstore { return bs }
	return d
}

// GET /sdn/v1/module?hash= is the PAGE-side module fetch: it returns the exact
// WASM bytes a CONTENT_HASH addresses, as application/wasm, with the served
// body hashing back to the requested CONTENT_HASH (the isomorphic contract's
// source-of-truth: the page loads these very bytes under the node's ABI).
func TestModuleEndpoint(t *testing.T) {
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	// A stand-in "module artifact": StoreModuleBytes derives the CONTENT_HASH
	// (sha-256 hex) exactly as an $APP's APPModuleRef would advertise it.
	wasm := []byte("\x00asm\x01\x00\x00\x00 not-a-real-module but content-addressed")
	hash, _, err := appmanifest.StoreModuleBytes(t.Context(), bs, wasm)
	if err != nil {
		t.Fatalf("StoreModuleBytes: %v", err)
	}

	h := sdnapi.NewHandler(depsWithBlockstore(newTestStore(t), bs))

	// Present hash → 200 application/wasm, byte-identical body, digest matches.
	rec, hdr := get(t, h, "/sdn/v1/module?hash="+hash)
	if rec.Code != http.StatusOK {
		t.Fatalf("module status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := hdr.Get("Content-Type"); ct != "application/wasm" {
		t.Errorf("content-type = %q, want application/wasm", ct)
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, wasm) {
		t.Errorf("served body (%d bytes) != stored module bytes (%d bytes)", len(got), len(wasm))
	}
	sum := sha256.Sum256(rec.Body.Bytes())
	if hex.EncodeToString(sum[:]) != hash {
		t.Errorf("served body does not hash back to the requested CONTENT_HASH")
	}

	// Well-formed but absent hash → 404 (not present in the block store).
	absent := hex.EncodeToString(sha256.New().Sum(nil)) // 64 hex chars, never stored
	if rec, _ := get(t, h, "/sdn/v1/module?hash="+absent); rec.Code != http.StatusNotFound {
		t.Errorf("absent hash status = %d, want 404", rec.Code)
	}

	// Missing hash param → 400.
	if rec, _ := get(t, h, "/sdn/v1/module"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing hash status = %d, want 400", rec.Code)
	}

	// Malformed hash (wrong length / non-hex) → 400.
	for _, bad := range []string{"deadbeef", strings.Repeat("z", 64)} {
		if rec, _ := get(t, h, "/sdn/v1/module?hash="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("malformed hash %q status = %d, want 400", bad, rec.Code)
		}
	}

	// No block store wired → 503.
	nbs := sdnapi.NewHandler(testDeps(newTestStore(t)))
	if rec, _ := get(t, nbs, "/sdn/v1/module?hash="+hash); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no-blockstore status = %d, want 503", rec.Code)
	}
}
