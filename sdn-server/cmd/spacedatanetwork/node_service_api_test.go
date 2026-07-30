package main

// Locks for GET /api/node/service (node_service_api.go) and for the Seal
// Council's conditions on it (2026-07-30, recorded on graph task
// sdn-dashboard-wave3-service-lifecycle):
//
//   - Admin-classified, never anonymous, prefix-classified so nothing can appear
//     underneath it unclassified;
//   - GET/HEAD only — the READ half carries no verb;
//   - fail-closed: no control without BOTH a proven supervisor and the unit-level
//     opt-in;
//   - the opt-in is default-off everywhere this repository ships anything.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// TestNodeServicePathIsAdminClassifiedAndPrefixCovered is the authority lock. The
// prefix matters as much as the path: the Council's condition is that a lifecycle
// verb can never land on a sub-path that the Admin classifier does not already
// cover, so the classifier is asserted for paths that do not exist yet.
func TestNodeServicePathIsAdminClassifiedAndPrefixCovered(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/node/service",
		"/api/node/service/restart",
		"/api/node/service/stop",
		"/api/node/service/anything-a-future-agent-adds",
	} {
		if !isAdminOnlyAPIPath(path) {
			t.Fatalf("%s must be admin-classified", path)
		}
		if isPublicReadAPIPath(path) {
			t.Fatalf("%s must not be on the anonymous read surface", path)
		}
		if isAnyTierAuthenticatedAPIPath(path) {
			t.Fatalf("%s must not be readable at any authenticated tier — it is Admin", path)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if isPublicAPIRequest(method, path) {
				t.Fatalf("%s %s is treated as anonymous", method, path)
			}
		}
	}
}

// TestNodeServiceRefusesAnonymousAndBelowAdmin drives the real auth wall.
func TestNodeServiceRefusesAnonymousAndBelowAdmin(t *testing.T) {
	t.Parallel()

	const path = "/api/node/service"

	newMux := func() (http.Handler, *int) {
		calls := 0
		mux := http.NewServeMux()
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
		})
		return mux, &calls
	}

	t.Run("anonymous is refused", func(t *testing.T) {
		t.Parallel()
		handler, _ := newAdminSession(t, peers.Admin)
		mux, calls := newMux()
		rec := httptest.NewRecorder()
		serveAdminMuxRequest(rec, httptest.NewRequest(http.MethodGet, path, nil), mux, true, false, handler, isPublicAPIRequest)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if *calls != 0 {
			t.Fatalf("anonymous GET reached the service handler (%d calls)", *calls)
		}
	})

	for _, trust := range []peers.TrustLevel{peers.Unknown, peers.Marginal, peers.Standard, peers.Trusted} {
		trust := trust
		t.Run("below admin is refused/"+trust.String(), func(t *testing.T) {
			t.Parallel()
			handler, token := newAdminSession(t, trust)
			mux, calls := newMux()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
			rec := httptest.NewRecorder()
			serveAdminMuxRequest(rec, req, mux, true, false, handler, isPublicAPIRequest)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s GET status = %d, want %d", trust, rec.Code, http.StatusForbidden)
			}
			if *calls != 0 {
				t.Fatalf("%s GET reached the service handler (%d calls)", trust, *calls)
			}
		})
	}
}

// TestHandleNodeServiceIsReadOnly: the READ half answers GET and HEAD and refuses
// every mutating verb. This is the file the destructive half is NOT in.
func TestHandleNodeServiceIsReadOnly(t *testing.T) {
	t.Parallel()

	handler := handleNodeService()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/node/service", strings.NewReader("{}"))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		handler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}

	// A host with no systemd (this test machine, a container, a laptop) answers
	// 200 with "nothing to control" — a successful answer, not an error.
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/api/node/service", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body serviceStateJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if body.CanRestart || body.CanStop {
		t.Fatalf("a test process must never report a controllable supervisor: %+v", body)
	}
}

// TestServiceStateForIsFailClosed is the whole authorization rule of the
// destructive half, in one table: control requires a PROVEN supervisor AND the
// unit-level opt-in. Three of the four rows must produce no control, and the UI
// renders a button only on can_restart/can_stop.
func TestServiceStateForIsFailClosed(t *testing.T) {
	t.Parallel()

	detected := hostsvc.State{
		Supervisor:    "systemd",
		Unit:          "space-data-network-module-delivery.service",
		ActiveState:   "active",
		SubState:      "running",
		Autostart:     "enabled",
		RestartPolicy: "always",
		Detected:      true,
	}

	for _, tc := range []struct {
		name       string
		state      hostsvc.State
		optIn      bool
		wantAllows bool
	}{
		{"no supervisor, no opt-in", hostsvc.State{}, false, false},
		{"no supervisor, opt-in granted", hostsvc.State{}, true, false},
		{"supervisor proven, no opt-in", detected, false, false},
		{"supervisor proven and opt-in granted", detected, true, true},
	} {
		got := serviceStateFor(tc.state, tc.optIn)
		if got.CanRestart != tc.wantAllows || got.CanStop != tc.wantAllows {
			t.Fatalf("%s: can_restart=%v can_stop=%v, want %v", tc.name, got.CanRestart, got.CanStop, tc.wantAllows)
		}
		if got.ControlEnabled != tc.optIn {
			t.Fatalf("%s: control_enabled must report the opt-in verbatim", tc.name)
		}
	}
}

// TestServiceStateReportsSystemdWordsVerbatim: autostart is systemd's own
// UnitFileState string and is never flattened to a boolean. "static" and
// "indirect" are neither enabled nor disabled, and a boolean would have to lie
// about one of them — which is precisely the defect (a hardcoded ENABLED) this
// endpoint exists to remove.
func TestServiceStateReportsSystemdWordsVerbatim(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"enabled", "disabled", "static", "indirect", "masked", "enabled-runtime"} {
		got := serviceStateFor(hostsvc.State{Supervisor: "systemd", Autostart: word, Detected: true}, false)
		if got.Autostart != word {
			t.Fatalf("autostart = %q, want %q verbatim", got.Autostart, word)
		}
	}

	// An unproven supervisor omits the field entirely rather than sending "" —
	// the dashboard's rule is that an absent fact is an absent cell.
	encoded, err := json.Marshal(serviceStateFor(hostsvc.State{}, false))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"autostart", "unit", "active_state", "sub_state", "restart_policy"} {
		if strings.Contains(string(encoded), `"`+key+`"`) {
			t.Fatalf("an unproven supervisor must omit %q, got %s", key, encoded)
		}
	}
	// …but the three fields the UI branches on are ALWAYS present, so a missing
	// key can never be read as permission.
	for _, key := range []string{"supervisor", "control_enabled", "can_restart", "can_stop"} {
		if !strings.Contains(string(encoded), `"`+key+`"`) {
			t.Fatalf("%q must always be present, got %s", key, encoded)
		}
	}
}

// TestServiceControlOptInIsExactlyOne: "1" and nothing else. A capability that
// can darken a live host does not accept approximate consent.
func TestServiceControlOptInIsExactlyOne(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true},
		{" 1 ", true},
		{"", false},
		{"0", false},
		{"true", false},
		{"TRUE", false},
		{"yes", false},
		{"on", false},
		{"2", false},
	} {
		t.Setenv(serviceControlEnvVar, tc.value)
		if got := serviceControlEnabled(); got != tc.want {
			t.Fatalf("%s=%q -> %v, want %v", serviceControlEnvVar, tc.value, got, tc.want)
		}
	}
}

// TestServiceControlIsDefaultOffEverythingThisRepoShips is the Seal Council's
// ship-time condition as a test (Hephaestus, 2026-07-30): "+opt-in key never in a
// repo-shipped config or topology.json" and "assert default-off across
// deployment/**".
//
// Enabling the destructive half is a deliberate act on ONE host, performed by
// whoever owns that host's unit. If this test ever fails, a capability that can
// stop a production node has been turned on for everyone by a file.
func TestServiceControlIsDefaultOffEverythingThisRepoShips(t *testing.T) {
	t.Parallel()

	// cmd/spacedatanetwork -> sdn-server -> repo root.
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	for _, dir := range []string{"deployment", filepath.Join("sdn-server", "deployment")} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable entry cannot enable anything
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			body := string(raw)
			if !strings.Contains(body, serviceControlEnvVar) {
				return nil
			}
			// The name may APPEAR — documentation of how to enable it on one
			// host is exactly where it belongs — but never as an assignment
			// that would take effect.
			for _, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.Contains(trimmed, serviceControlEnvVar) {
					continue
				}
				if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(trimmed, serviceControlEnvVar+"=1") {
					rel, _ := filepath.Rel(root, path)
					t.Fatalf("%s enables the destructive lifecycle half: %q\n"+
						"The Seal Council's condition is default-off in everything this repo ships; "+
						"enabling it is a per-host act on that host's unit.", rel, trimmed)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
}

// TestNodeServiceReadSurfaceCarriesNoLifecycleVerb is the source-level lock on
// the READ half — the sibling of node_runtime_api_test.go's lock, and the reason
// that one did not have to be deleted to make room for this work (the owner's
// explicit condition: revised as part of the change, "not deleted around").
//
// The read surface stays a read surface: the destructive half lives in its own
// file, with its own nonce/signature admit point, and cannot leak back into this
// one.
func TestNodeServiceReadSurfaceCarriesNoLifecycleVerb(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("node_service_api.go")
	if err != nil {
		t.Fatalf("read node_service_api.go: %v", err)
	}
	src := string(raw)
	for _, forbidden := range []string{
		"os.Exit", "syscall.Kill", "exec.Command", "exec.CommandContext",
		"hostsvc.Restart", "hostsvc.Stop", "http.MethodPost",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("node_service_api.go is the READ half; found %q", forbidden)
		}
	}
	// It must also never resolve a unit name itself: the ONLY unit any part of
	// this surface may act on is the one hostsvc proved from /proc/self/cgroup.
	if strings.Contains(src, ".service\"") {
		t.Fatalf("node_service_api.go must not name a unit — hostsvc resolves it from the running process")
	}
}
