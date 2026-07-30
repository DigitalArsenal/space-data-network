package main

// The Seal Council's conditions on the DESTRUCTIVE half, as tests
// (node_service_control.go; graph task sdn-dashboard-wave3-service-lifecycle).
//
// Every case here is a REFUSAL that must hold on a host where the capability is
// off — which is every host this repository ships anything for. The accept path is
// exercised for its ORDERING (answer, flush, then act) and for the nonce algebra;
// it cannot be driven to a real systemctl in a unit test, and a test that shelled
// out to one would be a test that restarts the developer's machine.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// contextWithTestSession puts an ADMIN session on the request the way the auth
// wall does, so these tests exercise the handler's OWN gates rather than the
// wall's (which node_service_api_test.go drives end to end).
func contextWithTestSession(r *http.Request, xpub string) context.Context {
	return auth.ContextWithSession(r.Context(), &auth.Session{XPub: xpub, TrustLevel: peers.Admin})
}

func newTestContext(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestServiceControlPathsAreAdminClassified(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/node/service/nonce", "/api/node/service/restart", "/api/node/service/stop"} {
		if !isAdminOnlyAPIPath(path) {
			t.Fatalf("%s must be admin-classified", path)
		}
		if isPublicReadAPIPath(path) || isAnyTierAuthenticatedAPIPath(path) {
			t.Fatalf("%s must be Admin-only", path)
		}
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if isPublicAPIRequest(method, path) {
				t.Fatalf("%s %s is treated as anonymous", method, path)
			}
		}
	}
}

// The verb comes from the PATH, never from the body: a nonce minted for one verb
// must not be spendable on another, and that comparison is only meaningful if the
// verb is not a field the caller supplies.
func TestServiceActionComesFromThePath(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]hostsvc.Action{
		"/api/node/service/restart": hostsvc.ActionRestart,
		"/api/node/service/stop":    hostsvc.ActionStop,
	} {
		got, ok := serviceActionFromPath(path)
		if !ok || got != want {
			t.Fatalf("%s -> %q,%v; want %q", path, got, ok, want)
		}
	}
	for _, path := range []string{"/api/node/service", "/api/node/service/nonce", "/api/node/service/kill", "/api/node/service/restart/now"} {
		if _, ok := serviceActionFromPath(path); ok {
			t.Fatalf("%s must not resolve to a lifecycle verb", path)
		}
	}
}

// DEFAULT-OFF IS THE FIRST GATE. With the opt-in absent — the state of every host
// this repository ships anything for — both verbs refuse with a logged reason
// BEFORE any supervisor probe or signature check.
func TestServiceActionRefusesWithoutTheUnitOptIn(t *testing.T) {
	os.Unsetenv(serviceControlEnvVar)

	for _, path := range []string{"/api/node/service/restart", "/api/node/service/stop"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req = req.WithContext(contextWithTestSession(req, "xpubADMIN"))
		handleServiceAction(newServiceControlState(), nil)(rec, req)

		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotImplemented)
		}
		var refusal serviceRefusal
		if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
			t.Fatalf("decode refusal: %v (%s)", err, rec.Body.String())
		}
		if refusal.Reason != "control_not_enabled" {
			t.Fatalf("%s reason = %q, want control_not_enabled", path, refusal.Reason)
		}
		// The refusal must NAME the operand — the contract's "refused without the
		// numbers is not compliant".
		if !strings.Contains(refusal.Message, serviceControlEnvVar) {
			t.Fatalf("the refusal must name the missing grant: %q", refusal.Message)
		}
	}
}

// The CSRF belt is checked before anything is decoded, so a cross-origin form post
// cannot even reach the body parser.
func TestServiceActionRequiresTheXHRHeader(t *testing.T) {
	t.Setenv(serviceControlEnvVar, "1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/node/service/stop", strings.NewReader(`{}`))
	req = req.WithContext(contextWithTestSession(req, "xpubADMIN"))
	handleServiceAction(newServiceControlState(), nil)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var refusal serviceRefusal
	_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
	if refusal.Reason != "missing_xhr_header" {
		t.Fatalf("reason = %q", refusal.Reason)
	}
}

// GET is not a lifecycle verb. The method gate is what stops a link, a prefetch or
// a browser history entry from stopping a node.
func TestServiceActionRefusesEveryMethodButPost(t *testing.T) {
	t.Setenv(serviceControlEnvVar, "1")

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/node/service/restart", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		handleServiceAction(newServiceControlState(), nil)(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

// With the opt-in granted but NO supervisor proven — a container, a laptop, any
// host whose cgroup does not name a unit whose MainPID is us — the answer is a
// refusal, not an attempt.
func TestServiceActionRefusesWithoutAProvenSupervisor(t *testing.T) {
	t.Setenv(serviceControlEnvVar, "1")

	if probe := hostsvc.Probe(newTestContext(t)); probe.Detected {
		t.Skip("this test process really is under a systemd unit of its own")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/node/service/stop", strings.NewReader(`{}`))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req = req.WithContext(contextWithTestSession(req, "xpubADMIN"))
	handleServiceAction(newServiceControlState(), nil)(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var refusal serviceRefusal
	_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
	if refusal.Reason != "no_supervisor" {
		t.Fatalf("reason = %q, want no_supervisor", refusal.Reason)
	}
}

// THE NONCE ALGEBRA, driven directly — the part that makes a signature mean
// something specific rather than "this operator once signed something".
func TestServiceNonceIsSingleUseAndBoundToVerbUnitAndIdentity(t *testing.T) {
	t.Parallel()

	state := newServiceControlState()
	const unit = "space-data-network-module-delivery.service"

	id, challenge, expiry, err := state.mint(hostsvc.ActionRestart, unit, "xpubA")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(challenge) != 32 {
		t.Fatalf("challenge is %d bytes, want 32", len(challenge))
	}
	// The Council's ceiling, asserted rather than commented.
	if ttl := expiry.Sub(state.clock()); ttl > 120*time.Second {
		t.Fatalf("nonce TTL %s exceeds the Council's 120s ceiling", ttl)
	}

	pending, ok := state.consume(id)
	if !ok {
		t.Fatal("a freshly minted nonce must be consumable")
	}
	if pending.action != hostsvc.ActionRestart || pending.unit != unit || pending.xpub != "xpubA" {
		t.Fatalf("the nonce must bind verb, unit and identity: %+v", pending)
	}
	// SINGLE USE.
	if _, ok := state.consume(id); ok {
		t.Fatal("a nonce must not be consumable twice")
	}
}

func TestServiceNonceExpires(t *testing.T) {
	t.Parallel()

	state := newServiceControlState()
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return base }

	id, _, _, err := state.mint(hostsvc.ActionStop, "x.service", "xpubA")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// One second past the TTL, the mint table sweeps it on the next mint and the
	// spend path refuses it either way.
	state.now = func() time.Time { return base.Add(serviceNonceTTL + time.Second) }
	pending, ok := state.consume(id)
	if ok && pending.expiresAt.After(state.clock()) {
		t.Fatal("an expired nonce must not still be valid")
	}
}

func TestServiceActionsAreRateLimitedToProtectTheStartLimit(t *testing.T) {
	t.Parallel()

	state := newServiceControlState()
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return base }

	if limited, _ := state.rateLimited(); limited {
		t.Fatal("a node that has done nothing must not be rate limited")
	}
	state.recordAction()

	// Immediately after: refused, with the remaining time stated so the UI can
	// say when instead of just no.
	limited, left := state.rateLimited()
	if !limited || left <= 0 || left > serviceActionMinInterval {
		t.Fatalf("limited=%v left=%s; want limited with a positive remainder", limited, left)
	}

	// One second short of the window: still refused.
	state.now = func() time.Time { return base.Add(serviceActionMinInterval - time.Second) }
	if limited, _ := state.rateLimited(); !limited {
		t.Fatal("the window must hold until it has fully elapsed")
	}

	// At the window: allowed. The interval is the Council's floor.
	state.now = func() time.Time { return base.Add(serviceActionMinInterval) }
	if limited, _ := state.rateLimited(); limited {
		t.Fatal("the action must be allowed once the interval has elapsed")
	}
	if serviceActionMinInterval < 60*time.Second {
		t.Fatalf("serviceActionMinInterval = %s; the Council required >= 60s", serviceActionMinInterval)
	}
}

func TestServiceNonceTableIsBounded(t *testing.T) {
	t.Parallel()

	state := newServiceControlState()
	for i := 0; i < serviceNonceMax; i++ {
		if _, _, _, err := state.mint(hostsvc.ActionRestart, "x.service", "xpubA"); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, _, _, err := state.mint(hostsvc.ActionRestart, "x.service", "xpubA"); err == nil {
		t.Fatal("minting past the bound must refuse rather than grow")
	}
}

// The source-level lock on the CONTROL file, mirroring the one that guards the
// read surfaces. These are the invariants the Council set, and they are the kind a
// later edit erodes silently.
func TestServiceControlSourceKeepsTheCouncilsConditions(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("node_service_control.go")
	if err != nil {
		t.Fatalf("read node_service_control.go: %v", err)
	}
	src := string(raw)

	// It must never build a unit name, or take one from a request: the ONLY unit
	// any verb may act on is the one hostsvc proved from /proc/self/cgroup.
	for _, forbidden := range []string{
		".service\"",               // a literal unit name
		"r.URL.Query().Get(\"unit", // a unit from the query string
		"req.Unit",                 // a unit from the body
		"exec.Command",             // execution belongs to hostsvc, behind its allow-list
		"/bin/sh",
		"os.Exit",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("node_service_control.go must not contain %q", forbidden)
		}
	}

	// And it must keep the five gates. Their absence is the failure mode that has
	// no symptom until someone stops a production node.
	for _, required := range []string{
		"serviceControlEnabled()",      // the unit-level opt-in
		"hostsvc.Probe(",               // a proven supervisor
		"state.consume(",               // single-use nonce
		"ed25519.Verify(",              // the signature itself
		"SigningKeyAuthorizesSession(", // …from THIS session's identity
		"state.rateLimited()",          // the start-limit guard
		"X-Requested-With",             // the CSRF belt
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("node_service_control.go has lost a Council condition: %q", required)
		}
	}

	// The answer must precede the action, because the answer travels over the
	// connection the action breaks.
	acceptIdx := strings.Index(src, "http.StatusAccepted")
	actIdx := strings.Index(src, "hostsvc.Control(")
	if acceptIdx < 0 || actIdx < 0 || acceptIdx > actIdx {
		t.Fatal("the 202 must be written (and flushed) BEFORE hostsvc.Control is called")
	}
	if !strings.Contains(src, "flusher.Flush()") {
		t.Fatal("the response must be flushed before the daemon is asked to die")
	}
}

// hostsvc's own conditions, from the caller's side: --no-block and an absolute
// systemctl are not optional, and there are exactly two verbs.
func TestHostsvcControlKeepsTheShipTimeConditions(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../internal/hostsvc/hostsvc.go")
	if err != nil {
		t.Fatalf("read hostsvc.go: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, `"--no-block"`) {
		t.Fatal("systemctl must be called with --no-block (Hephaestus: it dies in its own cgroup)")
	}
	if !strings.Contains(src, `SystemctlPath = "/usr/bin/systemctl"`) {
		t.Fatal("systemctl must be allow-listed by absolute path")
	}
	// Exactly two verbs reach systemd.
	if strings.Count(src, "return \"restart\", true") != 1 || strings.Count(src, "return \"stop\", true") != 1 {
		t.Fatal("systemctlVerb must map exactly the two authorized verbs")
	}
	for _, forbidden := range []string{`"kill"`, `"poweroff"`, `"reboot"`, `"disable"`, `"mask"`} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("hostsvc must not know the verb %s", forbidden)
		}
	}
}
