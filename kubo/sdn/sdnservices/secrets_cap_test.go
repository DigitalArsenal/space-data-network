package sdnservices

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/credstore"
	"github.com/ipfs/kubo/sdn/modulert"
)

const (
	stUser   = "operator@example.com"
	stSecret = "MODULE-PLAINTEXT-CANARY-4b21"
)

// newSecretsHandlerWithGrants wires the real secrets cap handler against a
// temp-dir keystore and a bridge granted exactly the given capabilities — the
// same shape production provisioning builds.
func newSecretsHandlerWithGrants(t *testing.T, grants ...string) (modulert.CapHandler, *credstore.Store) {
	t.Helper()
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(credstore.IDSpaceTrack, stUser, stSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put(credstore.IDEDCCPF, "edc@example.com", "edc-secret-canary"); err != nil {
		t.Fatalf("Put edc: %v", err)
	}
	h := NewSecretsCapFactory(store)(nil, modulert.NewHostBridge(nil, grants))
	return h, store
}

func callCap(t *testing.T, h modulert.CapHandler, op, payload string) map[string]any {
	t.Helper()
	raw, err := h(op, []byte(payload))
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", op, err)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s response: %v", op, err)
	}
	// Nothing that failed may still carry the plaintext.
	if ok, _ := resp["ok"].(bool); !ok && strings.Contains(string(raw), stSecret) {
		t.Fatalf("SECURITY: a FAILED %s response leaked the secret: %s", op, raw)
	}
	return resp
}

// HARD REQUIREMENT: a module WITHOUT the capability cannot obtain the secret.
func TestModuleWithoutCapabilityCannotGetSecret(t *testing.T) {
	// A module granted plenty of other privileges — but not secrets:spacetrack.
	h, _ := newSecretsHandlerWithGrants(t, "http", "storage_query", "wallet_sign", "p2p_read")

	for _, op := range []string{"secrets.get", "secrets.status"} {
		resp := callCap(t, h, op, `{"id":"spacetrack"}`)
		if ok, _ := resp["ok"].(bool); ok {
			t.Fatalf("SECURITY: %s succeeded without the secrets:spacetrack grant", op)
		}
	}

	// And the raw bytes never contain the credential.
	raw, _ := h("secrets.get", []byte(`{"id":"spacetrack"}`))
	if strings.Contains(string(raw), stSecret) {
		t.Fatal("SECURITY: an ungranted module received the plaintext secret")
	}
	if strings.Contains(string(raw), stUser) {
		t.Fatal("SECURITY: an ungranted module received the username")
	}
}

// A module with NO grants at all is likewise denied (nil-bridge / empty-grant
// fail-closed path).
func TestModuleWithNoGrantsCannotGetSecret(t *testing.T) {
	h, _ := newSecretsHandlerWithGrants(t)
	resp := callCap(t, h, "secrets.get", `{"id":"spacetrack"}`)
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatal("SECURITY: a module with no capability grants obtained the secret")
	}
}

// A nil bridge must fail closed rather than panic or pass.
func TestNilBridgeFailsClosed(t *testing.T) {
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(credstore.IDSpaceTrack, stUser, stSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	h := NewSecretsCapFactory(store)(nil, nil)

	raw, err := h("secrets.get", []byte(`{"id":"spacetrack"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if strings.Contains(string(raw), stSecret) {
		t.Fatal("SECURITY: a nil bridge yielded the secret")
	}
}

// HARD REQUIREMENT (per-lane): a module approved for spacetrack must NOT be able
// to read the EDC credential. This is the storage_query/storage_write split
// applied to credentials.
func TestGrantForOneLaneDoesNotGrantAnother(t *testing.T) {
	h, _ := newSecretsHandlerWithGrants(t, CapabilityForID(credstore.IDSpaceTrack))

	// Its own lane: allowed.
	resp := callCap(t, h, "secrets.get", `{"id":"spacetrack"}`)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("the approved lane was denied: %v", resp)
	}

	// A different lane: denied.
	raw, _ := h("secrets.get", []byte(`{"id":"edc_cpf"}`))
	var other map[string]any
	if err := json.Unmarshal(raw, &other); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok, _ := other["ok"].(bool); ok {
		t.Fatal("SECURITY: a secrets:spacetrack grant also unlocked the edc_cpf credential")
	}
	if strings.Contains(string(raw), "edc-secret-canary") {
		t.Fatal("SECURITY: the EDC credential leaked to a spacetrack-only module")
	}
}

// The approved module does get exactly what it needs — otherwise the denial
// tests above would be vacuous.
func TestApprovedModuleReceivesCredential(t *testing.T) {
	h, _ := newSecretsHandlerWithGrants(t, CapabilityForID(credstore.IDSpaceTrack))

	resp := callCap(t, h, "secrets.get", `{"id":"spacetrack"}`)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("approved module denied: %v", resp)
	}
	result, _ := resp["result"].(map[string]any)
	if result["username"] != stUser {
		t.Errorf("username = %v, want %v", result["username"], stUser)
	}
	if result["secret"] != stSecret {
		t.Error("the approved module did not receive the real secret")
	}
}

// secrets.status must not disclose the secret even to an approved module.
func TestStatusDoesNotDiscloseSecret(t *testing.T) {
	h, _ := newSecretsHandlerWithGrants(t, CapabilityForID(credstore.IDSpaceTrack))

	raw, err := h("secrets.status", []byte(`{"id":"spacetrack"}`))
	if err != nil {
		t.Fatalf("secrets.status: %v", err)
	}
	if strings.Contains(string(raw), stSecret) {
		t.Fatal("SECURITY: secrets.status leaked the plaintext secret")
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("status denied for an approved module: %v", resp)
	}
	result, _ := resp["result"].(map[string]any)
	if configured, _ := result["configured"].(bool); !configured {
		t.Error("want configured=true")
	}
}

// There must be no operation that enumerates or exports the whole keystore.
func TestNoEnumerationOrExportOperation(t *testing.T) {
	h, _ := newSecretsHandlerWithGrants(t, CapabilityForID(credstore.IDSpaceTrack), CapabilityForID(credstore.IDEDCCPF))

	for _, op := range []string{"secrets.list", "secrets.export", "secrets.all", "secrets.dump", "secrets.reveal"} {
		raw, err := h(op, []byte(`{}`))
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		var resp map[string]any
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode %s: %v", op, err)
		}
		if ok, _ := resp["ok"].(bool); ok {
			t.Fatalf("SECURITY: enumeration/export operation %q exists", op)
		}
		if strings.Contains(string(raw), stSecret) {
			t.Fatalf("SECURITY: %s leaked the secret", op)
		}
	}
}

// A missing store must fail closed, not panic.
func TestNilStoreFailsClosed(t *testing.T) {
	h := NewSecretsCapFactory(nil)(nil, modulert.NewHostBridge(nil, []string{CapabilityForID(credstore.IDSpaceTrack)}))

	raw, err := h("secrets.get", []byte(`{"id":"spacetrack"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatal("a nil credential store must fail closed")
	}
}

func TestMalformedAndEmptyRequests(t *testing.T) {
	h, _ := newSecretsHandlerWithGrants(t, CapabilityForID(credstore.IDSpaceTrack))

	for name, payload := range map[string]string{
		"not json": `{`,
		"no id":    `{}`,
		"blank id": `{"id":"   "}`,
	} {
		raw, err := h("secrets.get", []byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var resp map[string]any
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if ok, _ := resp["ok"].(bool); ok {
			t.Errorf("%s: expected failure", name)
		}
	}
}
