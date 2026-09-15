package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

func alertProbe(t *testing.T, h http.HandlerFunc, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// A healthy node says so, and the probe's HTTP status never changes: /health is
// a liveness check, and a failing CelesTrak lane is not a reason for a load
// balancer to take the node out of rotation.
func TestHealthReportsOKWithNoAlerts(t *testing.T) {
	registry := ops.NewRegistry()
	code, body := alertProbe(t, HealthHandler(registry), "/health")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body != `{"status":"ok","alerts":{"error":0,"warning":0}}` {
		t.Fatalf("body = %s", body)
	}
}

// With an error alert active the body says degraded and counts it, while the
// anonymous route still discloses no subject and no error text.
func TestHealthReportsDegradedWithAlertCounts(t *testing.T) {
	registry := ops.NewRegistry()
	registry.Raise(ops.KindPublicationRejected, "12D3KooWhost01", ops.SeverityError,
		`SIGNATURE_TYPE "Ed25519" does not match the provider's Secp256k1 key`)
	registry.Raise(ops.KindLaneFailing, "celestrak-satcat-ingest", ops.SeverityWarning, "parser returned HTTP 400")

	code, body := alertProbe(t, HealthHandler(registry), "/health")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the HTTP status is unchanged by degradation)", code)
	}
	if body != `{"status":"degraded","alerts":{"error":1,"warning":1}}` {
		t.Fatalf("body = %s", body)
	}
	for _, leak := range []string{"12D3KooWhost01", "celestrak-satcat-ingest", "Ed25519"} {
		if strings.Contains(body, leak) {
			t.Fatalf("anonymous /health leaked %q: %s", leak, body)
		}
	}
}

// The operator route carries the detail the anonymous one withholds.
func TestAlertsStatusListsActiveAlerts(t *testing.T) {
	registry := ops.NewRegistry()
	registry.Raise(ops.KindPublicationRejected, "12D3KooWhost01", ops.SeverityError,
		`SIGNATURE_TYPE "Ed25519" does not match the provider's Secp256k1 key`)
	registry.Raise(ops.KindLaneFailing, "celestrak-satcat-ingest", ops.SeverityWarning, "parser returned HTTP 400")
	registry.Raise(ops.KindLaneFailing, "celestrak-satcat-ingest", ops.SeverityWarning, "parser returned HTTP 400")

	code, body := alertProbe(t, AlertsHandler(registry), AlertsStatusPath)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var payload struct {
		Alerts []ops.Alert `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(payload.Alerts) != 2 {
		t.Fatalf("alerts = %+v, want 2", payload.Alerts)
	}
	// Errors sort first.
	if payload.Alerts[0].Kind != ops.KindPublicationRejected || payload.Alerts[0].Severity != ops.SeverityError {
		t.Fatalf("alerts[0] = %+v, want the publication_rejected error first", payload.Alerts[0])
	}
	if payload.Alerts[0].Subject != "12D3KooWhost01" {
		t.Errorf("subject = %q, want the producer peer id", payload.Alerts[0].Subject)
	}
	if !strings.Contains(payload.Alerts[0].LastError, "Secp256k1") {
		t.Errorf("last_error = %q, want the verification error text", payload.Alerts[0].LastError)
	}
	lane := payload.Alerts[1]
	if lane.Kind != ops.KindLaneFailing || lane.Subject != "celestrak-satcat-ingest" {
		t.Fatalf("alerts[1] = %+v, want the lane_failing warning", lane)
	}
	if lane.Count != 2 {
		t.Errorf("count = %d, want 2", lane.Count)
	}
	if lane.Since.IsZero() || lane.LastAt.IsZero() {
		t.Errorf("alert timestamps are zero: %+v", lane)
	}
}

// An empty registry answers with an empty list, never a JSON null.
func TestAlertsStatusIsEmptyListWhenQuiet(t *testing.T) {
	code, body := alertProbe(t, AlertsHandler(ops.NewRegistry()), AlertsStatusPath)
	if code != http.StatusOK || body != `{"alerts":[]}` {
		t.Fatalf("status=%d body=%s", code, body)
	}
}

// Both routes tolerate a node that never built a registry.
func TestHealthAndAlertsTolerateNoRegistry(t *testing.T) {
	if code, body := alertProbe(t, HealthHandler(nil), "/health"); code != http.StatusOK ||
		body != `{"status":"ok","alerts":{"error":0,"warning":0}}` {
		t.Fatalf("health with nil registry: status=%d body=%s", code, body)
	}
	if code, body := alertProbe(t, AlertsHandler(nil), AlertsStatusPath); code != http.StatusOK ||
		body != `{"alerts":[]}` {
		t.Fatalf("alerts with nil registry: status=%d body=%s", code, body)
	}
}

// Both routes are reads.
func TestHealthAndAlertsRejectWrites(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"/health":        HealthHandler(ops.NewRegistry()),
		AlertsStatusPath: AlertsHandler(ops.NewRegistry()),
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, name, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", name, rec.Code)
		}
	}
}
