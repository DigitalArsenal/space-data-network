package caps

// Loop B1 — least-privilege gate for the shared storage_* hostcall handler
// (caps/storage.go). storage_query, storage_write, storage_adapter and
// storage_ingest all route through ONE handler under the "storage" hostcall
// prefix (capPrefixFromName in module.go). Before this fix, storage.write
// and storage.delete performed NO per-operation HasCapability check, so a
// module approved ONLY for storage_query (nominally read-only) could reach
// storage.write/storage.delete and mutate or delete the SDS store through
// the exact same shared handler. These tests pin the fix: every op in the
// switch now independently re-checks its own capability grant against the
// calling bridge — storage_query does NOT imply write, storage_write does
// NOT imply read, matching the "fail closed" default-deny posture the rest
// of loop B1 established (capability_policy.go sensitiveCapabilities).

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// newStorageHandlerWithGrants wires the real storage cap handler against a
// seeded FlatSQL store (newCapTestStore, shared with flatsql_test.go) behind
// a bridge granted exactly the given capabilities — the same shape
// production provisioning builds (module.go instantiateWASM → NewHostBridge
// with the module's approved capability set).
func newStorageHandlerWithGrants(t *testing.T, grants ...string) modulert.CapHandler {
	t.Helper()
	store := newCapTestStore(t)
	return NewStorageCapFactory(store)(nil, modulert.NewHostBridge(nil, grants))
}

// storageOpOK reports whether a cap handler call succeeded (envelope
// "ok":true) and returns the decoded envelope for further assertions.
func storageOpOK(t *testing.T, handler modulert.CapHandler, op string, payload map[string]interface{}) (bool, map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := handler(op, body)
	if err != nil {
		t.Fatalf("%s handler returned Go error: %v", op, err)
	}
	envelope, _ := decodeCapResponse(t, resp)
	ok, _ := envelope["ok"].(bool)
	return ok, envelope
}

func writeTestOMMPayload(t *testing.T, norad uint32) map[string]interface{} {
	t.Helper()
	raw := buildIngestTestOMM(t, norad, 1700000000)
	return map[string]interface{}{
		"schema": "OMM.fbs",
		"data":   base64.StdEncoding.EncodeToString(raw),
	}
}

// TestStorageQueryOnlyGrantDeniesWriteAndDelete is the core regression test
// for the finding: a module granted ONLY storage_query must not be able to
// reach storage.write or storage.delete through the shared "storage"
// handler, even though storage.query itself keeps working.
func TestStorageQueryOnlyGrantDeniesWriteAndDelete(t *testing.T) {
	handler := newStorageHandlerWithGrants(t, "storage_query")

	// storage.query: allowed — this is exactly what storage_query grants.
	if ok, envelope := storageOpOK(t, handler, "storage.query", map[string]interface{}{
		"schema": "OMM.fbs",
		"limit":  10,
	}); !ok {
		t.Fatalf("storage.query denied for a storage_query grant: %v", envelope)
	}

	// storage.write: DENIED with a capability error, not a data-layer error.
	ok, envelope := storageOpOK(t, handler, "storage.write", writeTestOMMPayload(t, 9101))
	if ok {
		t.Fatal("storage.write SUCCEEDED for a module granted only storage_query — least-privilege regression")
	}
	errMsg, _ := envelope["error"].(map[string]interface{})["message"].(string)
	if errMsg == "" || !containsStorageCapError(errMsg, "storage_write") {
		t.Fatalf("storage.write denial did not name the missing storage_write grant: %v", envelope)
	}

	// storage.delete: DENIED the same way. Use a syntactically valid cid so
	// a false-negative (op reaching the store layer) would fail on the
	// store's own error, not on missing input — proving the capability gate
	// is what stops it, not incidental validation.
	ok, envelope = storageOpOK(t, handler, "storage.delete", map[string]interface{}{
		"schema": "OMM.fbs",
		"cid":    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	})
	if ok {
		t.Fatal("storage.delete SUCCEEDED for a module granted only storage_query — least-privilege regression")
	}
	errMsg, _ = envelope["error"].(map[string]interface{})["message"].(string)
	if errMsg == "" || !containsStorageCapError(errMsg, "storage_write") {
		t.Fatalf("storage.delete denial did not name the missing storage_write grant: %v", envelope)
	}

	// storage.flatsql_query_stream / storage.flatsql_epoch_stream: also
	// read-tier — must keep working under storage_query alone.
	if ok, envelope := storageOpOK(t, handler, "storage.flatsql_query_stream", map[string]interface{}{
		"sql": `SELECT _data FROM "OMM@celestrak-gp"`,
	}); !ok {
		t.Fatalf("storage.flatsql_query_stream denied for a storage_query grant: %v", envelope)
	}
}

// TestStorageWriteGrantAllowsWriteButNotQuery pins the other half of the
// tiering: storage_write allows storage.write/storage.delete, and — per the
// task's least-privilege intent ("a write-only approval doesn't silently
// grant reads either") — does NOT implicitly grant storage.query or the
// flatsql read-stream ops. No existing test or shipped flow manifest relies
// on storage_write implying read (verified against the compiled
// data-retrieval/public-query/celestrak-ingest flow manifests, which each
// request exactly one of storage_query or storage_ingest, never relying on
// storage_write for reads), so this is a safe, strictly-more-restrictive
// default.
func TestStorageWriteGrantAllowsWriteButNotQuery(t *testing.T) {
	handler := newStorageHandlerWithGrants(t, "storage_write")

	// storage.write: allowed.
	ok, envelope := storageOpOK(t, handler, "storage.write", writeTestOMMPayload(t, 9102))
	if !ok {
		t.Fatalf("storage.write denied for a storage_write grant: %v", envelope)
	}
	cid, _ := envelope["result"].(map[string]interface{})["cid"].(string)
	if cid == "" {
		t.Fatalf("storage.write did not return a cid: %v", envelope)
	}

	// storage.delete: allowed (write/delete share the storage_write tier —
	// there is no distinct storage_delete manifest capability).
	if ok, envelope := storageOpOK(t, handler, "storage.delete", map[string]interface{}{
		"schema": "OMM.fbs",
		"cid":    cid,
	}); !ok {
		t.Fatalf("storage.delete denied for a storage_write grant: %v", envelope)
	}

	// storage.query: DENIED — storage_write does not imply read.
	if ok, envelope := storageOpOK(t, handler, "storage.query", map[string]interface{}{
		"schema": "OMM.fbs",
		"limit":  10,
	}); ok {
		t.Fatal("storage.query SUCCEEDED for a module granted only storage_write — write must not imply read")
	} else if errMsg, _ := envelope["error"].(map[string]interface{})["message"].(string); !containsStorageCapError(errMsg, "storage_query") {
		t.Fatalf("storage.query denial did not name the missing storage_query grant: %v", envelope)
	}

	// storage.flatsql_query_stream: DENIED for the same reason.
	if ok, envelope := storageOpOK(t, handler, "storage.flatsql_query_stream", map[string]interface{}{
		"sql": `SELECT _data FROM "OMM@celestrak-gp"`,
	}); ok {
		t.Fatal("storage.flatsql_query_stream SUCCEEDED for a module granted only storage_write")
	} else if errMsg, _ := envelope["error"].(map[string]interface{})["message"].(string); !containsStorageCapError(errMsg, "storage_query") {
		t.Fatalf("storage.flatsql_query_stream denial did not name the missing storage_query grant: %v", envelope)
	}
}

// TestStorageNoGrantDeniesEverything is the baseline: a bridge with no
// storage_* grant at all (nil bridge behaves the same way via the
// `s.bridge == nil` half of every gate) is refused every op this fix
// touches.
func TestStorageNoGrantDeniesEverything(t *testing.T) {
	handler := newStorageHandlerWithGrants(t /* no grants */)

	for _, tc := range []struct {
		op      string
		payload map[string]interface{}
	}{
		{"storage.query", map[string]interface{}{"schema": "OMM.fbs", "limit": 10}},
		{"storage.write", writeTestOMMPayload(t, 9103)},
		{"storage.delete", map[string]interface{}{"schema": "OMM.fbs", "cid": "deadbeef"}},
		{"storage.flatsql_query_stream", map[string]interface{}{"sql": `SELECT _data FROM "OMM@celestrak-gp"`}},
		{"storage.flatsql_epoch_stream", map[string]interface{}{"schema": "OMM.fbs", "source": "celestrak-gp", "epoch": float64(1700000000)}},
	} {
		if ok, envelope := storageOpOK(t, handler, tc.op, tc.payload); ok {
			t.Fatalf("%s SUCCEEDED with no storage_* grant at all: %v", tc.op, envelope)
		}
	}
}

// TestStorageSandboxedAndIngestPolicyUnchanged pins that this fix leaves the
// ALREADY-gated ops (query_sandboxed, query_surface, ingest_with_source)
// behaving exactly as before: storage_query alone unlocks the sandboxed
// query surface, storage_write does NOT unlock it, and neither unlocks
// ingest_with_source (only storage_ingest does).
func TestStorageSandboxedAndIngestPolicyUnchanged(t *testing.T) {
	// query_surface: needs storage_query specifically.
	queryHandler := newStorageHandlerWithGrants(t, "storage_query")
	if ok, envelope := storageOpOK(t, queryHandler, "storage.query_surface", nil); !ok {
		t.Fatalf("storage.query_surface denied for a storage_query grant (pre-existing behavior changed): %v", envelope)
	}

	writeHandler := newStorageHandlerWithGrants(t, "storage_write")
	if ok, envelope := storageOpOK(t, writeHandler, "storage.query_surface", nil); ok {
		t.Fatal("storage.query_surface SUCCEEDED for a storage_write-only grant (pre-existing behavior changed)")
	} else if errMsg, _ := envelope["error"].(map[string]interface{})["message"].(string); !containsStorageCapError(errMsg, "storage_query") {
		t.Fatalf("storage.query_surface denial message changed shape: %v", envelope)
	}

	// ingest_with_source: neither storage_query nor storage_write unlocks
	// it — only storage_ingest does (already covered in depth by
	// TestIngestWithSourcePolicy in ingest_test.go; this just re-confirms
	// the storage_write case specifically, since that's the grant this fix
	// newly makes storage.write-capable).
	ingestPayload := map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "p",
		"source_name": "s",
		"batch_id":    "b",
		"records":     base64.StdEncoding.EncodeToString(sizePrefixedStream([][]byte{buildIngestTestOMM(t, 9104, 1700000000)})),
	}
	if ok, envelope := storageOpOK(t, writeHandler, "storage.ingest_with_source", ingestPayload); ok {
		t.Fatal("storage.ingest_with_source SUCCEEDED for a storage_write-only grant (pre-existing behavior changed)")
	} else if errMsg, _ := envelope["error"].(map[string]interface{})["message"].(string); !containsStorageCapError(errMsg, "storage_ingest") {
		t.Fatalf("storage.ingest_with_source denial message changed shape: %v", envelope)
	}
}

// containsStorageCapError is a tiny substring helper so denial assertions
// read as "names the missing grant" rather than string-matching the whole
// message verbatim (message wording is allowed to evolve; naming the
// capability is the load-bearing part).
func containsStorageCapError(msg, capName string) bool {
	return strings.Contains(msg, capName)
}
