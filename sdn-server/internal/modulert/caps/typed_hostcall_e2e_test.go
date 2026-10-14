package caps

// Typed contracts 1.0, end to end: a C++ module built by the space-data-module-sdk compiler
// (testdata/typed-hostcall-guest, see its README) runs under the node's WasmEdge runtime and calls the node through
// typedHostcall.hpp - SDS $PIV hostcalls carrying HostOps aligned records and raw / list frames, no JSON. The
// capabilities are the node's own: a real FlatSQL store, the crypto handler, the operator capability policy.

import (
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

func TestTypedHostcallsFromAnSDKModule(t *testing.T) {
	wasm, err := os.ReadFile(filepath.Join("testdata", "typed-hostcall-guest", "guest.wasm"))
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
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

	registry := modulert.NewCapabilityRegistry()
	storageFactory := NewStorageCapFactoryWithOptions(store, StorageCapOptions{FallbackProducer: "typed-test", MinFreeDiskBytes: 1})
	for _, c := range []string{"storage_query", "storage_write", "storage_adapter", "storage_ingest"} {
		registry.RegisterBridgeAware(c, storageFactory)
	}
	for _, c := range []string{"crypto_hash", "crypto_sign", "crypto_verify", "crypto_encrypt", "crypto_decrypt", "crypto_key_agreement", "crypto_kdf"} {
		registry.Register(c, NewCryptoCapFactory())
	}
	policy, err := modulert.NewCapabilityPolicyStore(filepath.Join(dir, "capability-policy.json"))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	registry.Register("http", NewHTTPCapFactory())
	for _, c := range []string{"storage_ingest", "storage_write", "http"} {
		if _, err := policy.Approve(modulert.CapabilityApproval{ModuleHash: modulert.ContentHashHex(wasm), Capability: c}); err != nil {
			t.Fatalf("approve %s: %v", c, err)
		}
	}
	nodeCtx := &modulert.NodeContext{
		PeerID:           "12D3KooWTypedHostcallTest",
		CapabilityPolicy: policy,
		Config: map[string]interface{}{
			"nwp":     map[string]interface{}{"max_lead": float64(72)},
			"targets": []interface{}{map[string]interface{}{"provider_id": "noaa-ncep"}},
		},
	}
	module, err := modulert.NewModule(wasm, registry, nodeCtx)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	t.Cleanup(func() { module.Close() })
	mod := module.Mod()

	run := func(name string, args ...interface{}) string {
		t.Helper()
		if _, err := mod.Execute(name, args...); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ptr, _ := mod.Execute("guest_out_ptr")
		n, _ := mod.Execute("guest_out_len")
		out, err := mod.ReadMemory(uint32(ptr[0].(int32)), uint32(n[0].(int32)))
		if err != nil {
			t.Fatalf("%s: read report: %v", name, err)
		}
		return string(out)
	}

	clock := strings.Fields(run("t_clock"))
	ms, _ := strconv.ParseInt(clock[1], 10, 64)
	if clock[0] != "1" || time.Since(time.UnixMilli(ms)).Abs() > time.Minute {
		t.Fatalf("clock.now: %v", clock)
	}
	if got := run("t_random"); got != "1 16 0 too-large" {
		t.Fatalf("random.bytes: %q (16 bytes, and 9000 refused too-large)", got)
	}
	if got := run("t_config"); !strings.HasPrefix(got, "1 2 72 noaa-ncep") {
		t.Fatalf("plugin.getConfig: %q", got)
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
	if got := run("t_ingest", int32(ptr), int32(len(stream))); !strings.HasPrefix(got, "1 2 OMM.fbs") {
		t.Fatalf("storage.ingest_with_source: %q", got)
	}
	if got := run("t_expire"); !strings.HasPrefix(got, "1 2 0") {
		t.Fatalf("storage.expire (MAX_DELETES 1000000): %q", got)
	}
	if left, err := store.QueryByIndexedFields("OMM.fbs", "", nil, "", 10); err != nil || len(left) != 0 {
		t.Fatalf("records left after expire: %d (%v)", len(left), err)
	}
	if got := run("t_denied"); got != "0 not-authorized" {
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
}
