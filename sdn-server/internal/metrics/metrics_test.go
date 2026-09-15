package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerServesSDNInstruments(t *testing.T) {
	SetPeerCountFunc(func() int { return 7 })
	SetStorageRecordCountFunc(func() int64 { return 42 })
	PubsubPublished("OMM.fbs")
	PubsubReceived("OMM.fbs")
	APIRequest("core", "2xx")
	IngestRecords("udl-elset", 3)

	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"sdn_connected_peers 7",
		"sdn_storage_records_total 42",
		`sdn_pubsub_messages_published_total{schema="OMM.fbs"} 1`,
		`sdn_pubsub_messages_received_total{schema="OMM.fbs"} 1`,
		`sdn_api_requests_total{route="core",status="2xx"} 1`,
		`sdn_ingest_records_total{source="udl-elset"} 3`,
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q\n%s", want, body[:min(len(body), 2000)])
		}
	}
}

// The alert gauge is the third surface of the OPS ALERT lane (after /health
// and /api/v1/status/alerts): one series per kind and severity, read live so a
// recovered lane's series disappears instead of going stale at 1.
func TestMetricsHandlerServesActiveAlertGauge(t *testing.T) {
	counts := []AlertCount{
		{Kind: "lane_failing", Severity: "error", Count: 2},
		{Kind: "publication_rejected", Severity: "error", Count: 1},
	}
	SetAlertCountsFunc(func() []AlertCount { return counts })
	t.Cleanup(func() { SetAlertCountsFunc(nil) })

	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`sdn_alerts_active{kind="lane_failing",severity="error"} 2`,
		`sdn_alerts_active{kind="publication_rejected",severity="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q\n%s", want, body[:min(len(body), 2000)])
		}
	}

	counts = nil
	recorder = httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(recorder.Body.String(), "sdn_alerts_active{") {
		t.Fatal("a cleared alert left a stale sdn_alerts_active series behind")
	}
}
