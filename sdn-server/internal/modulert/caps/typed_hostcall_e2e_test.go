package caps

// Typed contracts 1.0, end to end: a C++ module built by the space-data-module-sdk compiler
// (testdata/typed-hostcall-guest, see its README) runs under the node's WasmEdge runtime and calls the node through
// typedHostcall.hpp - SDS $PIV hostcalls carrying HostOps aligned records and raw / list frames, no JSON. The
// capabilities are the node's own: a real FlatSQL store, the crypto handler, the p2p handler over a stubbed network
// view, the operator capability policy.

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// typedP2POptions is the stubbed network view the p2p ops read: this node, the provider (connected, with an $EPM) and
// an unconnected peer; the provider's signed dataset publications, whose FILE_IDs are 72 bytes (the 0.9.0 bound was
// 16); the newest OMM batch pinned and materialized.
func typedP2POptions(t *testing.T) (P2PCapOptions, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	batch := "batch-" + strings.Repeat("7", 33)
	fileID := "sdn-OMM-full:OMM.fbs:" + batch + ":part-000001"
	if len(fileID) != 72 {
		t.Fatalf("FILE_ID is %d bytes, want 72", len(fileID))
	}
	newest := buildSignedTestPNM(t, priv, fileID, "bafy-omm-newest", "2026-07-06T06:00:00Z")
	older := buildSignedTestPNM(t, priv, "sdn-OMM-full:OMM.fbs:batch-old:part-000001", "bafy-omm-old", "2026-07-06T03:00:00Z")
	cat := buildSignedTestPNM(t, priv, "sdn-CAT-full:CAT.fbs:batch-cat-"+strings.Repeat("c", 45)+":part-000001", "bafy-cat", "2026-07-06T05:00:00Z")
	pnms := []P2PPNMRecord{{PeerID: testProviderID, Data: newest}, {PeerID: testProviderID, Data: cat}, {PeerID: testProviderID, Data: older}}
	providerEPM, selfEPM := buildTestEPM(t, "provider"), buildTestEPM(t, "self-node")
	batchStream := latestStreamFixtureBytes()
	return P2PCapOptions{
		SelfID:           testSelfID,
		SelfAgentVersion: "spacedatanetwork/test",
		SelfAddrs:        func() []string { return []string{"/ip4/10.0.0.1/tcp/4001", "/ip6/::1/tcp/4001"} },
		SelfEPM:          func() []byte { return selfEPM },
		Peers: func() []P2PPeerInfo {
			return []P2PPeerInfo{
				{ID: testProviderID, Addrs: []string{"/ip4/167.172.219.213/tcp/4001"}, Connected: true, AgentVersion: "spacedatanetwork/1.0.4"},
				{ID: "16Uiu2HAmZZunconnected", Connected: false},
			}
		},
		PeerEPM: func(peerID string) []byte {
			if peerID == testProviderID {
				return providerEPM
			}
			return nil
		},
		RecentPNMs: func(int) []P2PPNMRecord { return pnms },
		PublisherKeys: func(peerID string) []P2PPublisherKey {
			if peerID == testProviderID {
				return []P2PPublisherKey{{PublicKey: publisherKeyBytes(t, pub), Source: "epm-directory"}}
			}
			return nil
		},
		SchemaForStandard: func(standard string) string {
			if standard == "OMM" {
				return "OMM.fbs"
			}
			return ""
		},
		PinnedDataset: func(peerID, schemaName string) bool { return peerID == testProviderID && schemaName == "OMM.fbs" },
		LatestDatasetBatch: func(schemaName, batchID string, includeBytes bool) (*P2PDatasetBatch, bool) {
			if schemaName != "OMM.fbs" || batchID != batch {
				return nil, false
			}
			b := &P2PDatasetBatch{ProviderID: "space-data-network-02", SourceName: "provider-gp", BatchID: batchID, RecordCount: 2, Parts: 1,
				PublishedAt: "2026-07-06T06:00:00Z", FNV1a64: 0xdeadbeefcafef00d}
			if includeBytes {
				b.Bytes = batchStream
			}
			return b, true
		},
	}, fileID
}

type typedGuest struct {
	t   *testing.T
	mod interface {
		Execute(string, ...interface{}) ([]interface{}, error)
		ReadMemory(uint32, uint32) ([]byte, error)
		Allocate([]byte) (uint32, error)
	}
}

// run calls one t_* export and reads its report line.
func (g typedGuest) run(name string, args ...interface{}) string {
	g.t.Helper()
	if _, err := g.mod.Execute(name, args...); err != nil {
		g.t.Fatalf("%s: %v", name, err)
	}
	ptr, _ := g.mod.Execute("guest_out_ptr")
	n, _ := g.mod.Execute("guest_out_len")
	out, err := g.mod.ReadMemory(uint32(ptr[0].(int32)), uint32(n[0].(int32)))
	if err != nil {
		g.t.Fatalf("%s: read report: %v", name, err)
	}
	return string(out)
}

func TestTypedHostcallsFromAnSDKModule(t *testing.T) {
	dir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	rawRoot := filepath.Join(dir, "raw")

	registry := modulert.NewCapabilityRegistry()
	storageFactory := NewStorageCapFactoryWithOptions(store, StorageCapOptions{FallbackProducer: "typed-test", MinFreeDiskBytes: 1, RawRoot: rawRoot})
	for _, c := range []string{"storage_query", "storage_write", "storage_adapter", "storage_ingest"} {
		registry.RegisterBridgeAware(c, storageFactory)
	}
	for _, c := range []string{"crypto_hash", "crypto_sign", "crypto_verify", "crypto_encrypt", "crypto_decrypt", "crypto_key_agreement", "crypto_kdf"} {
		registry.Register(c, NewCryptoCapFactory())
	}
	p2pOptions, fileID := typedP2POptions(t)
	registry.RegisterBridgeAware("p2p_read", NewP2PCapFactory(p2pOptions))
	policy, err := modulert.NewCapabilityPolicyStore(filepath.Join(dir, "capability-policy.json"))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	registry.Register("http", NewHTTPCapFactory())
	nodeCtx := &modulert.NodeContext{
		PeerID:           "12D3KooWTypedHostcallTest",
		CapabilityPolicy: policy,
		Config: map[string]interface{}{
			"nwp":        map[string]interface{}{"max_lead": float64(72), "model": "gfs"},
			"targets":    []interface{}{map[string]interface{}{"provider_id": "noaa-ncep"}, map[string]interface{}{"provider_id": "dwd"}},
			"widths":     []interface{}{float64(1), 2.5},
			"names":      []string{"a", "b"},
			"dotted.key": true,
			"unset":      nil,
		},
	}
	load := func(file string, approve ...string) typedGuest {
		t.Helper()
		wasm, err := os.ReadFile(filepath.Join("testdata", "typed-hostcall-guest", file))
		if err != nil {
			t.Fatalf("read guest: %v", err)
		}
		for _, c := range approve {
			if _, err := policy.Approve(modulert.CapabilityApproval{ModuleHash: modulert.ContentHashHex(wasm), Capability: c}); err != nil {
				t.Fatalf("approve %s: %v", c, err)
			}
		}
		module, err := modulert.NewModule(wasm, registry, nodeCtx)
		if err != nil {
			t.Fatalf("NewModule(%s): %v", file, err)
		}
		t.Cleanup(func() { module.Close() })
		return typedGuest{t: t, mod: module.Mod()}
	}
	guest := load("guest.wasm", "storage_ingest", "storage_write", "storage_query", "http")
	run, mod := guest.run, guest.mod

	clock := strings.Fields(run("t_clock"))
	ms, _ := strconv.ParseInt(clock[1], 10, 64)
	if clock[0] != "1" || time.Since(time.UnixMilli(ms)).Abs() > time.Minute {
		t.Fatalf("clock.now: %v", clock)
	}
	if got := run("t_random"); got != "1 16 0 too-large" {
		t.Fatalf("random.bytes: %q (16 bytes, and 9000 refused too-large)", got)
	}
	// plugin.getConfig: one ModuleConfig (MCFG) on raw "config" - nested tables, lists, a key with a dot; nil left out
	if got := run("t_config"); got != "1 5 72 noaa-ncep 2 2.5 b 1 2 gfs" {
		t.Fatalf("plugin.getConfig: %q", got)
	}
	// a 1,000,000-number config (a terrain module's index): one 8 MB ModuleConfig; the module's heap grows by about
	// that, not by an aligned record (~536 bytes) per value
	{
		const n = 1_000_000
		index := make([]interface{}, n)
		for i := range index {
			index[i] = float64(i)
		}
		wasm, err := os.ReadFile(filepath.Join("testdata", "typed-hostcall-guest", "guest.wasm"))
		if err != nil {
			t.Fatalf("read guest: %v", err)
		}
		big, err := modulert.NewModule(wasm, registry, &modulert.NodeContext{PeerID: nodeCtx.PeerID, CapabilityPolicy: policy, Config: map[string]interface{}{"name": "terrain", "index": index}})
		if err != nil {
			t.Fatalf("NewModule(big config): %v", err)
		}
		t.Cleanup(func() { big.Close() })
		fields := strings.Fields(typedGuest{t: t, mod: big.Mod()}.run("t_big_config"))
		if len(fields) != 5 || fields[0] != "1" || fields[1] != strconv.Itoa(n) || fields[2] != strconv.Itoa(n*(n-1)/2) {
			t.Fatalf("plugin.getConfig (1,000,000 numbers): %v", fields)
		}
		payload, _ := strconv.Atoi(fields[3])
		growth, _ := strconv.Atoi(fields[4])
		if payload < n*8 || payload > n*8+4096 || growth < payload || growth > payload+payload/4+(1<<20) {
			t.Fatalf("plugin.getConfig (1,000,000 numbers): a %d-byte ModuleConfig grew the heap %d bytes", payload, growth)
		}
		t.Logf("plugin.getConfig: a %d-byte ModuleConfig grew the module's heap %d bytes", payload, growth)
	}
	if got := run("t_ops"); got != "1 1 1" {
		t.Fatalf("host.listOperations lists storage.expire and storage.query: %q", got)
	}

	// storage: ingest a record stream (raw "records"), expire it by arrival, and be refused a query
	records := [][]byte{buildIngestTestOMM(t, 25544, 1791900000), buildIngestTestOMM(t, 43013, 1791900060)}
	stream := sizePrefixedStream(records)
	ptr, err := mod.Allocate(stream)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	ingest := run("t_ingest", int32(ptr), int32(len(stream)))
	if !strings.HasPrefix(ingest, "1 2 OMM.fbs ") || !strings.HasSuffix(ingest, "|") {
		t.Fatalf("storage.ingest_with_source: %q", ingest)
	}
	// list "provenance_attributes" lands in the stored provenance document as "attributes", in order, beside the
	// "provenance" record's fields (a 200-byte ETAG and a 100-byte PARSER: the widened bounds)
	provenanceFile := strings.TrimSuffix(strings.Fields(ingest)[3], "|")
	body, err := os.ReadFile(provenanceFile)
	if err != nil || !strings.HasPrefix(provenanceFile, rawRoot) {
		t.Fatalf("provenance file %q: %v", provenanceFile, err)
	}
	var provenance struct {
		Attributes []struct{ Name, Value string } `json:"attributes"`
		SourceURL  string                         `json:"source_url"`
		HTTPStatus int                            `json:"http_status"`
		ETag       string                         `json:"etag"`
		Parser     string                         `json:"parser"`
		Count      int                            `json:"normalized_count"`
	}
	if err := json.Unmarshal(body, &provenance); err != nil {
		t.Fatalf("provenance document %s: %v", body, err)
	}
	attributes, _ := json.Marshal(provenance.Attributes)
	if string(attributes) != `[{"Name":"method","Value":"SGP4 mean elements"},{"Name":"units","Value":"rev/day"},{"Name":"date_range","Value":"2026-10-13/2026-10-14"}]` ||
		provenance.SourceURL != "https://example.test/omm.csv" || provenance.HTTPStatus != 200 || len(provenance.ETag) != 200 || len(provenance.Parser) != 100 || provenance.Count != 2 {
		t.Fatalf("provenance document: %s", body)
	}
	if license := storedLicense(t, store); len(license) != 128 {
		t.Fatalf("stored LICENSE is %d bytes (%q), want the 128 the module sent", len(license), license)
	}

	// SQL values in FlatSQL's tagged encoding: a null, bytes, a 300-byte string and an i64 beyond 2^53 reach the real
	// store whole (the WHERE clause holds only then); a malformed run is refused invalid-params
	if got := run("t_sql"); !strings.HasPrefix(got, "1 1 ") || !strings.HasSuffix(got, " 0 invalid-params|") {
		t.Fatalf("storage.flatsql_query_stream with raw params: %q", got)
	}
	// rows answer: list "columns", raw "cells" row-major (integer -> i64, other number -> f64, string, null)
	if got := run("t_rows"); got != "1 1 2 5 NORAD_CAT_ID,OBJECT_NAME,empty,half,word|"+
		"i25544 sING-SAT-25544 null f0.5 styped i43013 sING-SAT-43013 null f0.5 styped|" {
		t.Fatalf("storage.query_sandboxed rows: %q", got)
	}
	if got := run("t_expire"); !strings.HasPrefix(got, "1 2 0") {
		t.Fatalf("storage.expire (MAX_DELETES 1000000): %q", got)
	}
	if left, err := store.QueryByIndexedFields("OMM.fbs", "", nil, "", 10); err != nil || len(left) != 0 {
		t.Fatalf("records left after expire: %d (%v)", len(left), err)
	}
	// a module that did not declare storage_query is refused, typed
	if got := load("guest-no-query.wasm", "storage_ingest", "storage_write", "http").run("t_denied"); got != "0 not-authorized" {
		t.Fatalf("storage.query without storage_query: %q", got)
	}

	// crypto: sha256, ed25519 sign / verify, AES-GCM round trip and a refused short nonce
	if got := run("t_sha"); got != "1 ba7816bf 32" {
		t.Fatalf("crypto.hash: %q", got)
	}
	if got := run("t_sign"); got != "1 32 32 64 1 0" {
		t.Fatalf("crypto.generate_key/sign/verify: %q", got)
	}
	if got := run("t_gcm"); got != "1 21 1 hello 0 invalid-params" {
		t.Fatalf("crypto.encrypt/decrypt: %q", got)
	}

	// http: a range request (raw "headers" block), binary body (raw "body"), response headers back as a block
	var gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte{0x47, 0x52, 0x49, 0xb2}) //nolint:errcheck
	}))
	defer server.Close()
	port, _ := strconv.Atoi(server.URL[strings.LastIndex(server.URL, ":")+1:])
	if got := run("t_http", int32(port)); got != "1 206 475249b2|1|" || gotRange != "bytes=0-63" {
		t.Fatalf("http.request: %q (range %q)", got, gotRange)
	}

	// p2p: the lists the generic path dropped (peer_addresses, peer_standards), the handler's keys mapped
	// (publish_timestamp -> PUBLISH_MS, signature -> SIGNATURE_HEX, publisher_key -> PUBLISHER_KEY_HEX), the frames on
	// raw "records" and a 72-byte FILE_ID
	selfShort, providerShort := testSelfID[:8], testProviderID[:8]
	epms := len(buildTestEPM(t, "self-node")) + len(buildTestEPM(t, "provider"))
	if got, want := run("t_peers"), "1 "+testSelfID+" "+strconv.Itoa(epms)+"|"+
		testSelfID+":110:spacedatanetwork/test "+testProviderID+":101:spacedatanetwork/1.0.4 16Uiu2HAmZZunconnected:00-1:|"+
		"a:"+selfShort+"=/ip4/10.0.0.1/tcp/4001 a:"+selfShort+"=/ip6/::1/tcp/4001 a:"+providerShort+"=/ip4/167.172.219.213/tcp/4001 "+
		"s:"+providerShort+"=CAT s:"+providerShort+"=OMM|"; got != want {
		t.Fatalf("p2p.peers_snapshot:\n got  %q\n want %q", got, want)
	}
	newestMs := time.Date(2026, 7, 6, 6, 0, 0, 0, time.UTC).UnixMilli()
	catMs := time.Date(2026, 7, 6, 5, 0, 0, 0, time.UTC).UnixMilli()
	pnms := run("t_pnms")
	parts := strings.Split(pnms, "|")
	if len(parts) != 5 || parts[1] != "CAT:88:bafy-cat:"+strconv.FormatInt(catMs, 10)+":0 OMM:72:bafy-omm-newest:"+strconv.FormatInt(newestMs, 10)+":1" ||
		!strings.HasPrefix(parts[0], "1 2 ") || !strings.HasPrefix(parts[2], "1 1 0 1 ") ||
		parts[3] != providerShort+":72:"+strconv.FormatInt(newestMs, 10)+":Ed25519:128:1:signature:72:epm-directory:dataset.fsql" || parts[4] != "" {
		t.Fatalf("p2p.standards_snapshot / pnm_history: %q", pnms)
	}
	batchStream := latestStreamFixtureBytes()
	if got, want := run("t_latest"), "1 1101 OMM OMM.fbs 72 "+strconv.FormatInt(newestMs, 10)+" signature space-data-network-02 provider-gp 2 "+
		strconv.Itoa(len(batchStream))+" 1 "+strconv.FormatInt(newestMs, 10)+" deadbeefcafef00d "+strconv.Itoa(len(batchStream))+"|1 0 "+
		strconv.Itoa(len(batchStream))+" 2 deadbeefcafef00d"; got != want {
		t.Fatalf("p2p.latest_dataset (inline, then DELIVER Ref):\n got  %q\n want %q (FILE_ID %s)", got, want, fileID)
	}
}

// storedLicense reads the licence terms recorded for the batch t_ingest stored.
func storedLicense(t *testing.T, store *storage.FlatSQLStore) string {
	t.Helper()
	license, ok, err := store.SourceBatchLicenseFor("OMM.fbs", "typed-test", "typed-src", "typed-batch-1")
	if err != nil || !ok {
		t.Fatalf("batch licence: %v (found %v)", err, ok)
	}
	return license.License
}
