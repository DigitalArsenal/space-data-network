package sourcemetrics

import (
	"errors"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// One failed retrieval is ordinary. Two in a row is a lane that is not
// recovering on its own, and must be visible on the operator probes; four is
// the state host-02's SATCAT lane sat in for 48 consecutive runs while nothing
// said so. A landed batch clears it.
func TestAttemptFailuresRaiseAndASuccessClearsTheLaneAlert(t *testing.T) {
	store := openLedger(t)
	registry := ops.NewRegistry()
	store.SetAlerts(registry)

	const appID = "celestrak-satcat-ingest"

	// One failure: nothing raised.
	store.RecordAttempt(appID)
	store.RecordAttemptOutcome(appID, errors.New("parser returned HTTP 400"))
	if got := registry.Active(); len(got) != 0 {
		t.Fatalf("one failure raised %+v; a single miss is ordinary", got)
	}

	// Two in a row: a warning naming the lane and its cause.
	store.RecordAttempt(appID)
	store.RecordAttemptOutcome(appID, errors.New("parser returned HTTP 400"))
	active := registry.Active()
	if len(active) != 1 {
		t.Fatalf("Active() = %+v, want one alert after two consecutive failures", active)
	}
	if active[0].Kind != ops.KindLaneFailing || active[0].Subject != appID {
		t.Fatalf("alert = %s/%s, want %s/%s", active[0].Kind, active[0].Subject, ops.KindLaneFailing, appID)
	}
	if active[0].Severity != ops.SeverityWarning {
		t.Errorf("Severity = %q, want %q at two failures", active[0].Severity, ops.SeverityWarning)
	}
	if active[0].LastError != "parser returned HTTP 400" {
		t.Errorf("LastError = %q, want the flow's own error text", active[0].LastError)
	}

	// Four: the lane has burned through the escalating backoff.
	store.RecordAttempt(appID)
	store.RecordAttemptOutcome(appID, errors.New("parser returned HTTP 400"))
	store.RecordAttempt(appID)
	store.RecordAttemptOutcome(appID, nil)
	active = registry.Active()
	if len(active) != 1 || active[0].Severity != ops.SeverityError {
		t.Fatalf("Active() = %+v, want a single error-severity alert at four failures", active)
	}
	if active[0].LastError != "run completed but landed no batch" {
		t.Errorf("LastError = %q, want the clean-run-no-batch reason", active[0].LastError)
	}
	if active[0].Count != 3 {
		t.Errorf("Count = %d, want 3 reported failures", active[0].Count)
	}

	// A batch lands: the lane is working, so the alert goes away.
	store.RecordIngest(Ingest{
		AppID: appID, ProviderID: "celestrak", SourceName: "satcat",
		SourceURL: "https://celestrak.org/pub/satcat.csv",
		Schema:    "CAT.fbs", BatchID: "b1", Records: 10, Inserted: 10,
	})
	if got := registry.Active(); len(got) != 0 {
		t.Fatalf("Active() = %+v, want empty after a batch landed", got)
	}
}

// A restart must not hide a lane that is still down: the streak is in
// app_attempts and the registry is rebuilt from it at boot.
func TestSeedLaneAlertsRebuildsFailingLanesFromTheLedger(t *testing.T) {
	store := openLedger(t)

	// Build the durable state with no registry attached, exactly as a daemon
	// that failed, exited, and came back would have left it.
	store.RecordAttempt("failing-lane")
	store.RecordAttemptOutcome("failing-lane", errors.New("connection refused"))
	store.RecordAttempt("failing-lane")
	store.RecordAttemptOutcome("failing-lane", errors.New("connection refused"))
	store.RecordAttempt("healthy-lane")
	store.RecordIngest(Ingest{
		AppID: "healthy-lane", ProviderID: "p", SourceName: "s",
		SourceURL: "https://example.test/s", Schema: "CAT.fbs", BatchID: "b", Records: 1, Inserted: 1,
	})

	registry := ops.NewRegistry()
	store.SetAlerts(registry)
	if seeded := store.SeedLaneAlerts(); seeded != 1 {
		t.Fatalf("SeedLaneAlerts() = %d, want 1", seeded)
	}
	active := registry.Active()
	if len(active) != 1 || active[0].Subject != "failing-lane" {
		t.Fatalf("Active() = %+v, want only the failing lane", active)
	}
	if active[0].LastError != "connection refused" {
		t.Errorf("LastError = %q, want the persisted failure reason", active[0].LastError)
	}
}

// A ledger with no registry attached must behave exactly as it did before.
func TestAttemptRecordingToleratesNoAlertRegistry(t *testing.T) {
	store := openLedger(t)
	store.RecordAttempt("app")
	store.RecordAttemptOutcome("app", errors.New("boom"))
	store.RecordAttempt("app")
	store.RecordAttemptOutcome("app", errors.New("boom"))
	if _, failures := store.AttemptState("app"); failures != 2 {
		t.Fatalf("consecutive failures = %d, want 2", failures)
	}
	if seeded := store.SeedLaneAlerts(); seeded != 0 {
		t.Fatalf("SeedLaneAlerts() with no registry = %d, want 0", seeded)
	}
}
