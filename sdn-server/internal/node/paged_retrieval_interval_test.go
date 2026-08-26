package node

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
)

// The cellular lane on host-02: a 1,565,271,921 B archive paged by durable
// $IRM mark, one ranged chunk per fire. Every fire asks for DIFFERENT bytes,
// so the whole-payload 3 h debounce prices it at ~2 years.
const pagedAppID = "com.digitalarsenal.flows.cellular-network-ingest"

func newPagedLaneNode(t *testing.T, retrievalInterval string) *Node {
	t.Helper()
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open retrieval ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	return &Node{
		sourceMetrics: ledger,
		config: &config.Config{
			Flows: config.FlowsConfig{
				Services: []config.FlowService{{
					Flow:              pagedAppID,
					RetrievalInterval: retrievalInterval,
				}},
			},
		},
	}
}

// recordPagedChunk books one landed chunk the way the ingest capability does:
// a distinct batch id per byte offset, at a chosen age.
func recordPagedChunk(t *testing.T, n *Node, batchID string, age time.Duration) {
	t.Helper()
	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      pagedAppID,
		ProviderID: "mls-archive",
		SourceName: "mls-final-full-cell-export",
		SourceURL:  "https://archive.org/download/MLS_Full_Cell_Export_Final/MLS-full-cell-export-final.csv.gz",
		Schema:     "TBS.fbs",
		BatchID:    batchID,
		Records:    9817,
		Inserted:   9817,
		At:         time.Now().Add(-age),
	})
}

// WITHOUT the knob the lane is gated for three hours after a chunk lands —
// the behaviour every other lane keeps, pinned here so the default cannot
// drift while this feature exists.
func TestPagedLaneWithoutOverrideKeepsTheThreeHourWindow(t *testing.T) {
	n := newPagedLaneNode(t, "")
	recordPagedChunk(t, n, "mls-archive@262144", 90*time.Second)

	if due, reason := n.flowServiceRetrievalDue(pagedAppID); due {
		t.Fatalf("a 90s-old chunk must stay inside the default window, got due (%s)", reason)
	}
}

// WITH the knob the same 90-second-old chunk is past a 45 s window, so the
// next page may be fetched.
func TestPagedLaneWithOverrideAdmitsTheNextChunk(t *testing.T) {
	n := newPagedLaneNode(t, "45s")
	recordPagedChunk(t, n, "mls-archive@262144", 90*time.Second)

	due, reason := n.flowServiceRetrievalDue(pagedAppID)
	if !due {
		t.Fatalf("a 90s-old chunk is past a 45s window and must be due, got: %s", reason)
	}
	if reason == "" {
		t.Fatal("the gate must say WHY it opened")
	}
}

// The override is a WINDOW, not an off switch: inside it the lane is still
// gated, so a runaway timer cannot turn into a fetch storm.
func TestPagedLaneOverrideStillGatesInsideItsWindow(t *testing.T) {
	n := newPagedLaneNode(t, "45s")
	recordPagedChunk(t, n, "mls-archive@262144", 10*time.Second)

	if due, reason := n.flowServiceRetrievalDue(pagedAppID); due {
		t.Fatalf("a 10s-old chunk is inside a 45s window and must be gated, got due (%s)", reason)
	}
}

// A DIFFERENT flow must not inherit this lane's cadence.
func TestRetrievalOverrideIsScopedToItsOwnFlow(t *testing.T) {
	n := newPagedLaneNode(t, "45s")
	const otherApp = "com.digitalarsenal.flows.celestrak-gp-ingest"
	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      otherApp,
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Schema:     "OMM.fbs",
		BatchID:    "batch-gp",
		Records:    64556,
		Inserted:   59655,
		At:         time.Now().Add(-90 * time.Second),
	})

	if due, reason := n.flowServiceRetrievalDue(otherApp); due {
		t.Fatalf("the CelesTrak lane keeps the 3 h window, got due (%s)", reason)
	}
}

// A typo must not read as "no window". The lane falls back to the node
// default rather than pulling on every tick.
func TestUnparseableRetrievalIntervalFallsBackToTheDefaultWindow(t *testing.T) {
	n := newPagedLaneNode(t, "45 seconds")
	recordPagedChunk(t, n, "mls-archive@262144", 90*time.Second)

	if due, reason := n.flowServiceRetrievalDue(pagedAppID); due {
		t.Fatalf("an unparseable interval must keep the default window, got due (%s)", reason)
	}
	if _, ok, err := n.config.Flows.Services[0].EffectiveRetrievalInterval(); err == nil || ok {
		t.Fatalf("EffectiveRetrievalInterval must report the typo: ok=%v err=%v", ok, err)
	}
}

// Escalating backoff survives the override: a failing lane still doubles from
// its configured base instead of retrying on the same cadence forever.
func TestRetrievalOverrideStillBacksOffOnConsecutiveFailures(t *testing.T) {
	n := newPagedLaneNode(t, "45s")

	// Two attempts with no ingest between them = two failures on record.
	n.sourceMetrics.RecordAttempt(pagedAppID)
	n.sourceMetrics.RecordAttemptOutcome(pagedAppID, errFakeFetch{})
	n.sourceMetrics.RecordAttempt(pagedAppID)
	n.sourceMetrics.RecordAttemptOutcome(pagedAppID, errFakeFetch{})

	_, failures := n.sourceMetrics.AttemptState(pagedAppID)
	if failures < 2 {
		t.Fatalf("failures=%d, want >= 2 to exercise the backoff", failures)
	}
	base := 45 * time.Second
	widened := sourcemetrics.EffectiveDebounceHoursFrom(base.Hours(), failures)
	if widened <= base.Hours() {
		t.Fatalf("backoff must widen the configured base: %v vs %v", widened, base.Hours())
	}
	if due, reason := n.flowServiceRetrievalDue(pagedAppID); due {
		t.Fatalf("a just-failed attempt must be inside its widened window, got due (%s)", reason)
	}
}

type errFakeFetch struct{}

func (errFakeFetch) Error() string { return "fetch refused by publisher" }
