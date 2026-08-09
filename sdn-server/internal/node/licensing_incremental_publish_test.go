package node

import (
	"context"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// A module publish must not disturb a delivery already in flight for a
// DIFFERENT module.
//
// The defect this guards (graph/tasks/sdn-delivery-first-attempt-bar-remaining-modes.md):
// the libp2p module-publish handler answered every successful publish by
// re-running bootstrapLicensingModule, which begins with
// `server_configure_runtime`. In the key server that message is a session
// reset — clear_pending_challenges(), clear_pending_grants(),
// clear_publications(), and a fresh ephemeral keypair
// (key_server.cpp:1930-1940) — after which the host re-provisioned the whole
// catalog over dozens of serial guest invocations. Any browser mid-handshake
// during that window lost its module on the FIRST attempt, to one of three
// error strings that look like three unrelated defects:
//
//	"requested module publication was not found"   (its module was cleared)
//	"invalid licensing grant identifier"           (its challenge/grant was cleared)
//	"... Read aborted"                             (the mutex was held by the rebuild)
//
// The assertion below is deliberately indirect, because it is the one a
// regression cannot slip past: module A is published, then EXCLUDED from a
// scoped publish of module B. If anything in that path reconfigures the
// runtime, A is wiped and — being out of scope — never re-added, so A's
// challenge comes back as an Error frame instead of a Response.
func TestIncrementalPublishLeavesOtherModulesAndTheSessionIntact(t *testing.T) {
	t.Parallel()

	const (
		moduleA = "com.orbpro.sgp4"
		moduleB = "com.orbpro.rf-fspl"
	)

	reg := writeTestPluginRegistryWithGrantPolicy(
		t,
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyOpen},
		license.PluginCatalogEntry{
			ID:                moduleA,
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "sgp4.wasm.enc",
			KeyPath:           "sgp4.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
		license.PluginCatalogEntry{
			ID:                moduleB,
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "rf-fspl.wasm.enc",
			KeyPath:           "rf-fspl.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
	)

	mod := newLicensingTestModule(t)
	defer func() {
		if err := mod.Close(); err != nil {
			t.Fatalf("Close() failed: %v", err)
		}
	}()

	if err := bootstrapLicensingModule(mod, reg); err != nil {
		t.Fatalf("bootstrapLicensingModule() failed: %v", err)
	}

	challengeable := func(t *testing.T, requestID, moduleID string) bool {
		t.Helper()
		response, err := mod.InvokeMethod(
			context.Background(),
			"server_handle_message",
			buildChallengeRequestFrame(
				requestID,
				moduleID,
				"1.0.0",
				"requester.orbpro.test",
				"localhost",
				"provider.orbpro.test",
			),
		)
		if err != nil {
			t.Fatalf("InvokeMethod(server_handle_message, %s) failed: %v", moduleID, err)
		}
		if !flatbuffers.BufferHasIdentifier(response, "$LCH") {
			t.Fatalf("expected an $LCH frame for %s, got %q", moduleID, response)
		}
		messageType, _ := decodeChallengeHeader(t, response)
		// 1 = Response (challenge issued), 2 = Error (module_not_found et al).
		return messageType == 1
	}

	if !challengeable(t, "req-a-before", moduleA) {
		t.Fatalf("precondition failed: %s is not challengeable after bootstrap", moduleA)
	}

	// Mark A so an unscoped republish is detectable: publishCatalogAssets
	// overwrites the runtime status of every module it publishes.
	const sentinel = "sentinel: this module must not be republished"
	if err := reg.SetRuntimeStatus(moduleA, "stopped", sentinel); err != nil {
		t.Fatalf("SetRuntimeStatus(%s) failed: %v", moduleA, err)
	}

	// The incremental path: exactly what the libp2p publish handler now calls.
	if err := publishCatalogAssets(mod, reg, []string{moduleB}); err != nil {
		t.Fatalf("publishCatalogAssets(%s) failed: %v", moduleB, err)
	}

	// 1. The scope held: A was not touched by a publish for B.
	if _, message, ok := reg.RuntimeStatus(moduleA); !ok || message != sentinel {
		t.Fatalf("publishing %s republished %s (runtime message = %q, want the sentinel) — the publish is not scoped", moduleB, moduleA, message)
	}

	// 2. B really was published.
	if _, message, ok := reg.RuntimeStatus(moduleB); !ok || message == "" || message == sentinel {
		t.Fatalf("publishing %s did not update its runtime status (message = %q)", moduleB, message)
	}

	// 3. THE LOAD-BEARING ONE. A is still published inside the key server. A
	// runtime reconfiguration on this path would have cleared it, and the
	// scope would have kept it from being re-added, so this is the assertion
	// that fails the moment the wipe comes back.
	if !challengeable(t, "req-a-after", moduleA) {
		t.Fatalf("publishing %s dropped the publication for %s: its challenge now answers with an Error frame "+
			"(this is the 'requested module publication was not found' the gallery saw)", moduleB, moduleA)
	}

	// 4. And B is challengeable too, so the incremental publish is a real
	// publish and not merely a no-op that trivially satisfies 1-3.
	if !challengeable(t, "req-b-after", moduleB) {
		t.Fatalf("%s is not challengeable after an incremental publish — the scoped publish provisioned no key", moduleB)
	}
}

// An incremental publish for a module the admit point REFUSES must stay
// refused. Scoping decides which assets are considered; it never widens what
// may be published.
func TestIncrementalPublishStillHonoursTheAdmitPoint(t *testing.T) {
	t.Parallel()

	const refused = "com.orbpro.hpop"

	reg := writeTestPluginRegistryWithGrantPolicy(
		t,
		// Default-deny with an empty allowlist: the fail-closed admit point.
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyAllowlist},
		license.PluginCatalogEntry{
			ID:                refused,
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "hpop.wasm.enc",
			KeyPath:           "hpop.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
	)

	plan := planCatalogPublication(reg, map[string]struct{}{refused: {}})
	if len(plan.Admitted) != 0 {
		t.Fatalf("scoped plan admitted %d asset(s); a scoped ruling must not relax the admit point", len(plan.Admitted))
	}
	if len(plan.Refused) != 1 || plan.Refused[0] != refused {
		t.Fatalf("scoped plan refused = %v, want [%s]", plan.Refused, refused)
	}
}
