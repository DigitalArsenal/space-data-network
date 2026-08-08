package caps

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

const (
	browserSuppliedUser   = "cell-operator@example.com"
	browserSuppliedSecret = "BROWSER-SUPPLIED-CANARY-9f31"
)

// newSecretsWriteHandler wires the real handler over an EMPTY temp keystore —
// the state prod host-01 is in (credstore configured, zero lanes, no
// credentials.enc) — with the given capability grants and an audit ring.
func newSecretsWriteHandler(t *testing.T, grants ...string) (modulert.CapHandler, *credstore.Store, *ActivityRing) {
	t.Helper()
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ring := NewActivityRing(ActivityRingCapacity)
	h := NewSecretsCapFactoryWithAudit(store, ring)(nil, modulert.NewHostBridge(nil, grants))
	return h, store, ring
}

// ⛔ THE CONDITION HEPHAESTUS ATTACHED TO THE SEAL COUNCIL CONCUR (2026-08-08):
// a write must NOT ride the existing read grant. capability_policy.json is an
// append-only operator ledger; rows approving secrets:spacetrack were signed
// off 2026-07-18 and 2026-07-28 for READING. If a binary upgrade let those rows
// write, every one of them would silently gain the power to overwrite operator
// credentials — retroactive escalation with no operator act.
func TestSecretsWriteDoesNotRideTheReadGrant(t *testing.T) {
	t.Parallel()

	h, store, _ := newSecretsWriteHandler(t, "secrets:spacetrack", "http", "storage_query")

	for _, call := range []struct{ op, payload string }{
		{"secrets.put", `{"id":"spacetrack","username":"` + browserSuppliedUser + `","secret":"` + browserSuppliedSecret + `"}`},
		{"secrets.clear", `{"id":"spacetrack"}`},
	} {
		raw, err := h(call.op, []byte(call.payload))
		if err != nil {
			t.Fatalf("%s returned a Go error: %v", call.op, err)
		}
		if strings.Contains(string(raw), `"ok":true`) {
			t.Fatalf("SECURITY: %s succeeded on the READ grant alone: %s", call.op, raw)
		}
		if !strings.Contains(string(raw), "secrets:spacetrack:write") {
			t.Fatalf("%s refusal should name the write capability it needs: %s", call.op, raw)
		}
	}

	// Nothing reached the keystore.
	if status, err := store.Status("spacetrack"); err != nil {
		t.Fatalf("Status: %v", err)
	} else if status.Configured {
		t.Fatal("SECURITY: a read-only module wrote a credential")
	}
}

// A module with no secrets grant at all writes nothing.
func TestSecretsWriteRefusedWithoutAnyGrant(t *testing.T) {
	t.Parallel()

	h, store, _ := newSecretsWriteHandler(t, "http", "wallet_sign")
	raw, err := h("secrets.put", []byte(`{"id":"acme","username":"u","secret":"`+browserSuppliedSecret+`"}`))
	if err != nil {
		t.Fatalf("secrets.put returned a Go error: %v", err)
	}
	if strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("SECURITY: secrets.put succeeded with no grant: %s", raw)
	}
	if status, _ := store.Status("acme"); status.Configured {
		t.Fatal("SECURITY: an ungranted module wrote a credential")
	}
}

// The write grant is PER LANE, exactly like the read grant.
func TestSecretsWriteIsPerLane(t *testing.T) {
	t.Parallel()

	h, store, _ := newSecretsWriteHandler(t, "secrets:cellular:write")

	raw, err := h("secrets.put", []byte(`{"id":"spacetrack","username":"u","secret":"`+browserSuppliedSecret+`"}`))
	if err != nil {
		t.Fatalf("secrets.put returned a Go error: %v", err)
	}
	if strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("SECURITY: a module approved for secrets:cellular:write wrote the spacetrack lane: %s", raw)
	}
	if status, _ := store.Status("spacetrack"); status.Configured {
		t.Fatal("SECURITY: cross-lane write landed")
	}
}

// A write grant is not a read grant. Holding secrets:<lane>:write must not let
// a module read the value back — otherwise "write-only" is a fiction.
func TestSecretsWriteGrantDoesNotConferRead(t *testing.T) {
	t.Parallel()

	h, _, _ := newSecretsWriteHandler(t, "secrets:cellular:write")
	if raw, err := h("secrets.put", []byte(`{"id":"cellular","username":"`+browserSuppliedUser+`","secret":"`+browserSuppliedSecret+`"}`)); err != nil {
		t.Fatalf("secrets.put: %v", err)
	} else if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("secrets.put should have succeeded with its own write grant: %s", raw)
	}

	raw, err := h("secrets.get", []byte(`{"id":"cellular"}`))
	if err != nil {
		t.Fatalf("secrets.get: %v", err)
	}
	if strings.Contains(string(raw), browserSuppliedSecret) {
		t.Fatalf("SECURITY: a write-only grant read the value back: %s", raw)
	}
	if strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("SECURITY: secrets.get succeeded on a write-only grant: %s", raw)
	}
}

// The round trip the cellular module needs: store a browser-supplied
// credential, prove it is at rest ENCRYPTED, prove a fresh store over the same
// directory still opens it (survives restart), then clear it.
func TestSecretsWriteRoundTripSurvivesRestartAndClears(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := credstore.NewStore(dir, "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ring := NewActivityRing(ActivityRingCapacity)
	h := NewSecretsCapFactoryWithAudit(store, ring)(nil, modulert.NewHostBridge(nil, []string{"secrets:cellular:write", "secrets:cellular"}))

	raw, err := h("secrets.put", []byte(`{"id":"cellular","username":"`+browserSuppliedUser+`","secret":"`+browserSuppliedSecret+`"}`))
	if err != nil {
		t.Fatalf("secrets.put: %v", err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("secrets.put failed: %s", raw)
	}
	// THE RESPONSE NEVER ECHOES THE VALUE.
	if strings.Contains(string(raw), browserSuppliedSecret) || strings.Contains(string(raw), browserSuppliedUser) {
		t.Fatalf("SECURITY: secrets.put echoed the credential: %s", raw)
	}

	// RESTART: a brand-new Store over the same directory reads it back.
	reopened, err := credstore.NewStore(dir, "test-root-key-password")
	if err != nil {
		t.Fatalf("reopen NewStore: %v", err)
	}
	cred, err := reopened.Reveal("cellular")
	if err != nil {
		t.Fatalf("credential did not survive a restart: %v", err)
	}
	if cred.Secret.Reveal() != browserSuppliedSecret || cred.Username != browserSuppliedUser {
		t.Fatal("credential round-tripped to the wrong value")
	}

	// AT REST IT IS ENCRYPTED: the canary must not appear anywhere on disk.
	assertNoPlaintextOnDisk(t, dir, browserSuppliedSecret)

	// CLEAR works, through the same write grant.
	if raw, err := h("secrets.clear", []byte(`{"id":"cellular"}`)); err != nil {
		t.Fatalf("secrets.clear: %v", err)
	} else if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("secrets.clear failed: %s", raw)
	}
	if status, _ := store.Status("cellular"); status.Configured {
		t.Fatal("secrets.clear left the lane configured")
	}

	// AUDIT: set and clear are both recorded, and neither event carries a value.
	events := ring.Snapshot(0)
	kinds := 0
	for _, event := range events {
		if event.Kind != "secrets_write" {
			continue
		}
		kinds++
		if strings.Contains(event.Detail, browserSuppliedSecret) || strings.Contains(event.Detail, browserSuppliedUser) {
			t.Fatalf("SECURITY: an audit event carried credential material: %q", event.Detail)
		}
		if !strings.Contains(event.Detail, "cellular") {
			t.Fatalf("audit event does not name the lane: %q", event.Detail)
		}
	}
	if kinds != 2 {
		t.Fatalf("recorded %d secrets_write audit events, want 2 (put + clear)", kinds)
	}
}

// A lane id that could not round-trip through a capability name is refused at
// the store, so no caller can mint an unapprovable lane.
func TestSecretsWriteRefusesAnInvalidLaneID(t *testing.T) {
	t.Parallel()

	h, _, _ := newSecretsWriteHandler(t, "secrets:BAD LANE:write")
	raw, err := h("secrets.put", []byte(`{"id":"BAD LANE","username":"u","secret":"s"}`))
	if err != nil {
		t.Fatalf("secrets.put: %v", err)
	}
	if strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("SECURITY: an invalid lane id was accepted: %s", raw)
	}
}

func assertNoPlaintextOnDisk(t *testing.T, dir, canary string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(body), canary) {
			t.Fatalf("SECURITY: credential plaintext found at rest in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}
