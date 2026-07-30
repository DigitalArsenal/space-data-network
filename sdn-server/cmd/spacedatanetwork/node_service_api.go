package main

// node_service_api.go — GET /api/node/service: what this node's process
// supervisor says about it.
//
// OWNER AUTHORIZATION AND ITS CONDITIONS. The owner approved a daemon-lifecycle
// capability on 2026-07-30 ("approved", graph task
// sdn-dashboard-wave3-service-lifecycle) with the Seal Council as the condition.
// The Council ruled the same day (Hermes + Hephaestus, recorded on the task):
// CHECK is separable and ships FIRST as a pure read — "a read-only health probe
// is not a lifecycle verb". RESTART and STOP are the destructive half and land
// separately, behind a fresh wallet signature over a single-use server nonce
// (Hephaestus DISSENTED from gating them on the session cookie alone: "a bearer
// credential darkening a live host"). IRIS ruled the same split for the UI: this
// endpoint ships LIVE in the wave-2 cutover so the AUTOSTART cell can be honest,
// and the RESTART / STOP buttons ship in the next one.
//
// WHAT THIS FILE IS. A connector to internal/hostsvc, which reads systemd. It
// makes no decision about the node's lifecycle, contains no verb, and — per the
// Council — cannot grow one by accident: node_service_control_test.go asserts
// this file holds no mutating route, mirroring the lock that guards
// node_runtime_api.go.
//
// WHY THE NODE NEEDS IT AT ALL, in one sentence: the dashboard's SERVICE panel
// had an AUTOSTART cell rendering a HARDCODED "ENABLED" behind a flag that was
// always false, because the only surface that knew about autostart —
// node_status_read — honestly reports `autostart_known:false` (it has no
// supervisor surface). This is that surface, so the cell can state a measurement
// or nothing at all.
//
// AUTHORITY. Admin-only, two independent locks, exactly as the other two node
// read surfaces:
//
//  1. the top-level auth wall (serveAdminMuxRequest -> isAdminOnlyAPIPath,
//     which carries the "/api/node/service" prefix — and therefore also covers
//     every future path under it), and
//  2. the method gate below — GET/HEAD only.
//
// DISCLOSURE. The unit name, its enablement and its restart policy describe the
// HOST's deployment, not published data — the same classification as
// /api/node/runtime's storage path and bandwidth totals.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
)

// serviceControlEnvVar is the OPT-IN for the destructive half, and it is
// deliberately an ENVIRONMENT variable on the unit rather than a config key.
//
// The Seal Council's condition (Hephaestus, ship-time half) is that the opt-in
// "never [appears] in a repo-shipped config or topology.json" and that the
// capability is default-off on every host. A unit-level environment variable
// satisfies both structurally: the grant lives in the systemd unit, which is
// root-owned and is Hephaestus's surface, so nothing the daemon can write to —
// and no file this repository ships — can turn it on.
// node_service_api_test.go asserts that no file under deployment/ sets it.
const serviceControlEnvVar = "SDN_SERVICE_CONTROL"

// serviceControlEnabled reports the opt-in. Anything other than exactly "1" is
// off: a capability that can darken a live host does not accept "true", "yes" or
// "TRUE" as approximate consent.
func serviceControlEnabled() bool {
	return strings.TrimSpace(os.Getenv(serviceControlEnvVar)) == "1"
}

// serviceStateJSON is the wire shape. snake_case, like the node's other host-fact
// surfaces (these are runtime facts, not SDS record fields, so the SDS
// capitalization rule does not apply).
type serviceStateJSON struct {
	// Supervisor is "systemd", or "" when none was proven. The empty string is
	// the refusal, and the UI renders no lifecycle control on it.
	Supervisor string `json:"supervisor"`
	// Unit, ActiveState, SubState, Autostart, RestartPolicy are systemd's own
	// words, verbatim (see internal/hostsvc). Omitted when unproven rather
	// than sent as empty strings that would read as "known to be blank".
	Unit          string `json:"unit,omitempty"`
	ActiveState   string `json:"active_state,omitempty"`
	SubState      string `json:"sub_state,omitempty"`
	Autostart     string `json:"autostart,omitempty"`
	RestartPolicy string `json:"restart_policy,omitempty"`
	// ControlEnabled is the unit-level opt-in, reported so an operator can see
	// WHY a control is absent: "this host has no supervisor" and "this host has
	// not been granted self-control" are different facts and both are true
	// answers to "where are my buttons".
	ControlEnabled bool `json:"control_enabled"`
	// CanRestart / CanStop are the ONLY fields the dashboard reads to decide
	// whether to render a control. Both require a proven supervisor AND the
	// opt-in: fail-closed, and the UI never renders a greyed button that
	// advertises a capability the node lacks (IRIS §5).
	CanRestart bool `json:"can_restart"`
	CanStop    bool `json:"can_stop"`
}

// serviceStateFor folds a probe plus the opt-in into the wire shape. Split out
// from the handler so the rules are testable without a supervisor.
func serviceStateFor(state hostsvc.State, controlEnabled bool) serviceStateJSON {
	out := serviceStateJSON{
		Supervisor:     state.Supervisor,
		Unit:           state.Unit,
		ActiveState:    state.ActiveState,
		SubState:       state.SubState,
		Autostart:      state.Autostart,
		RestartPolicy:  state.RestartPolicy,
		ControlEnabled: controlEnabled,
	}
	// AND, never OR: a detected supervisor without the grant is still no, and
	// the grant without a detected supervisor is still no.
	out.CanRestart = state.Detected && controlEnabled
	out.CanStop = state.Detected && controlEnabled
	return out
}

// handleNodeService serves this node's supervisor state.
//
//	GET /api/node/service -> serviceStateJSON
//
// Read-only, and the method gate is what keeps it that way. A host with no
// systemd answers 200 with `{"supervisor":"","control_enabled":false,
// "can_restart":false,"can_stop":false}` — a successful answer whose content is
// "nothing to control", not an error: the dashboard asks this on every admin load
// and a 501 on a laptop would be noise that trains operators to ignore errors.
func handleNodeService() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Unit state changes underneath us; a cached copy is a claim about the
		// past.
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(serviceStateFor(hostsvc.Probe(r.Context()), serviceControlEnabled()))
	}
}
