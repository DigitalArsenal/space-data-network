package sdnapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

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
