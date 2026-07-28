package caps

// The host must never refuse work invisibly.
//
// A cap error is a RETURN VALUE to wasm, not a host event. When the ingest cap
// refused every batch on host-02 for being under its 5 GiB free-disk floor, the
// host logged nothing, the flow swallowed the error and reported `ok`, and
// three separate investigations read "fetch 200, store empty, no error
// anywhere" and concluded the flow had silently trapped. The guardrail itself
// was already correct and already tested; what was missing was any way to SEE
// it fire.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func refusalMessage(t *testing.T, resp []byte) string {
	t.Helper()
	meta := decodeCapMeta(t, resp)
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("expected a refusal, got success: %s", string(resp))
	}
	errObj, ok := meta["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("refusal carries no error object: %s", string(resp))
	}
	msg, _ := errObj["message"].(string)
	if msg == "" {
		t.Fatalf("refusal carries no message: %s", string(resp))
	}
	return msg
}

// The disk-floor refusal has to tell an operator what was dropped and what the
// floor was, in units they can act on. "ingest requires at least 5368709120
// free bytes" alone does not say whose data just vanished.
func TestDiskFloorRefusalNamesWhatItDropped(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: int64(1) << 62})
	payload := map[string]interface{}{
		"schema":      "CAT.fbs",
		"provider_id": "space-data-network-02",
		"source_name": "celestrak-satcat-csv",
		"batch_id":    "batch-disk-floor",
		"records": base64.StdEncoding.EncodeToString(
			sizePrefixedStream([][]byte{buildIngestTestOMM(t, 4242, 1700000000)})),
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	msg := refusalMessage(t, resp)

	// The batch is still refused before any write — the guardrail's own job.
	if count, err := store.Count("CAT.fbs"); err != nil || count != 0 {
		t.Fatalf("guardrail-refused ingest wrote records: count=%d err=%v", count, err)
	}

	// And the message now identifies the victim and the policy.
	for _, want := range []string{
		"space-data-network-02", // whose data
		"celestrak-satcat-csv",  // which lane
		"CAT.fbs",               // which schema
		"GiB floor",             // the policy, in operator units
		"GiB free",              // and what the box actually had
		"dropped",               // and that this is a loss, not a retry
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("disk-floor refusal does not mention %q: %s", want, msg)
		}
	}
}

// The wasm-side contract must not change: a logged refusal is byte-identical to
// the refusal that was already being returned, or every module that inspects
// cap errors breaks.
func TestLoggedRefusalKeepsTheWireShape(t *testing.T) {
	const msg = "requires the storage_ingest capability grant"
	silent := errCapJSON(msg)
	logged := refuseCapJSON("storage.ingest_with_source", msg)
	if string(silent) != string(logged) {
		t.Fatalf("refuseCapJSON changed the wire shape:\n silent: %s\n logged: %s", silent, logged)
	}
}

// A capability the module does not hold is a HOST decision and must be visible
// too — fail-closed is only trustworthy if you can tell it fired.
func TestMissingGrantIsStillRefused(t *testing.T) {
	handler, _ := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1}, "storage_write")
	payload := map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "p",
		"source_name": "s",
		"batch_id":    "b",
		"records": base64.StdEncoding.EncodeToString(
			sizePrefixedStream([][]byte{buildIngestTestOMM(t, 1, 1700000000)})),
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if msg := refusalMessage(t, resp); !strings.Contains(msg, "storage_ingest capability grant") {
		t.Fatalf("unexpected refusal message: %s", msg)
	}
}

// A module's OWN mistake stays quiet on the host: the module is told, and the
// module is the right audience. Logging those would bury the host's decisions
// in a module's debugging noise.
func TestModuleInputErrorsAreNotHostRefusals(t *testing.T) {
	handler, _ := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1})
	payload := map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "", // the module failed to attribute its own batch
		"source_name": "s",
		"batch_id":    "b",
		"records": base64.StdEncoding.EncodeToString(
			sizePrefixedStream([][]byte{buildIngestTestOMM(t, 1, 1700000000)})),
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if msg := refusalMessage(t, resp); !strings.Contains(msg, "provenance attribution") {
		t.Fatalf("unexpected refusal message: %s", msg)
	}
}
