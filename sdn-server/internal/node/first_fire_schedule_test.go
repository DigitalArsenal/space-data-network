package node

import (
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
)

// host-02 beta.75: the sigmf flow's timer is weekly (604800000 ms), but a
// restart re-pulled IQEngine's 31 MB index because first-fire only asked the
// 3 h debounce: "last retrieved 5h14m0s ago (debounce 3h0m0s)".
const (
	sigmfAppID    = "com.digitalarsenal.flows.sigmf-captures-ingest"
	sigmfInterval = 7 * 24 * time.Hour
	gpAppID       = "com.digitalarsenal.flows.celestrak-gp-ingest"
	gpInterval    = 3 * time.Hour
)

func newScheduleNode(t *testing.T, firstFire bool) *Node {
	t.Helper()
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open retrieval ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	return &Node{
		sourceMetrics: ledger,
		config: &config.Config{Flows: config.FlowsConfig{
			FirstFireWhenDue: firstFire,
			Services:         []config.FlowService{{Flow: sigmfAppID}, {Flow: gpAppID}},
		}},
	}
}

func landBatch(n *Node, appID, sourceName, batchID string, age time.Duration) {
	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      appID,
		ProviderID: "space-data-network-02",
		SourceName: sourceName,
		Schema:     "IQC.fbs",
		BatchID:    batchID,
		Records:    36636,
		Inserted:   36636,
		At:         time.Now().Add(-age),
	})
}

func TestFirstFireSkipsAWeeklySourcePulledHoursBeforeTheRestart(t *testing.T) {
	n := newScheduleNode(t, true)
	landBatch(n, sigmfAppID, "IQEngine", "iqengine-a", 5*time.Hour+14*time.Minute)

	// The debounce alone says "due": this is the defect.
	if due, reason := n.flowServiceRetrievalDue(sigmfAppID); !due {
		t.Fatalf("premise: 5h14m is past the 3 h debounce, got not due (%s)", reason)
	}
	due, reason := n.flowServiceFirstFireDue(sigmfAppID, sigmfInterval)
	if due {
		t.Fatalf("a weekly source pulled 5h14m ago must not be re-pulled at boot, got due (%s)", reason)
	}
	if !strings.Contains(reason, "inside the flow's own 168h0m0s timer") {
		t.Fatalf("the skip must name the flow's own timer: %s", reason)
	}

	// Its timer is phased to the pull, not restarted from the boot.
	phase := n.flowServiceSchedulePhase(sigmfAppID, sigmfInterval)
	want := sigmfInterval - (5*time.Hour + 14*time.Minute)
	if diff := phase - want; diff < -time.Minute || diff > time.Minute {
		t.Fatalf("first run in %s, want %s (lastSuccess + 168h)", phase, want)
	}
}

func TestFirstFireFiresAWeeklySourceOnceItsWeekHasPassed(t *testing.T) {
	n := newScheduleNode(t, true)
	landBatch(n, sigmfAppID, "IQEngine", "iqengine-a", 8*24*time.Hour)

	if due, reason := n.flowServiceFirstFireDue(sigmfAppID, sigmfInterval); !due {
		t.Fatalf("a weekly source pulled 8 days ago is due, got: %s", reason)
	}
	if phase := n.flowServiceSchedulePhase(sigmfAppID, sigmfInterval); phase != 0 {
		t.Fatalf("a due lane fires now and keeps the plain ticker, got first run in %s", phase)
	}
}

// The GP lane's timer equals the debounce, so its boot behaviour is unchanged.
func TestFirstFireKeepsTheThreeHourLaneAsItWas(t *testing.T) {
	n := newScheduleNode(t, true)
	landBatch(n, gpAppID, "celestrak-gp", "gp-a", 5*time.Hour)
	if due, reason := n.flowServiceFirstFireDue(gpAppID, gpInterval); !due {
		t.Fatalf("a 3 h lane pulled 5 h ago is due, got: %s", reason)
	}

	n2 := newScheduleNode(t, true)
	landBatch(n2, gpAppID, "celestrak-gp", "gp-a", 2*time.Hour)
	if due, _ := n2.flowServiceFirstFireDue(gpAppID, gpInterval); due {
		t.Fatal("a 3 h lane pulled 2 h ago must wait")
	}
	// ...but its ticker no longer waits a whole interval from the boot.
	if phase := n2.flowServiceSchedulePhase(gpAppID, gpInterval); phase < 59*time.Minute || phase > 61*time.Minute {
		t.Fatalf("first run in %s, want ~1h (lastSuccess + 3h)", phase)
	}
}

// A lane the ledger cannot vouch for is owed a pull, whatever its timer says.
func TestFirstFireOwesAPullWhenTheLedgerCannotVouchForEverySource(t *testing.T) {
	n := newScheduleNode(t, true)
	if due, reason := n.flowServiceFirstFireDue(sigmfAppID, sigmfInterval); !due {
		t.Fatalf("a never-retrieved lane is due, got: %s", reason)
	}

	// One source fresh, one whose claim reconciliation withdrew (its records
	// are gone from the store): the lane is due.
	landBatch(n, sigmfAppID, "IQEngine", "iqengine-a", time.Hour)
	landBatch(n, sigmfAppID, "IQEngine-mirror", "mirror-a", time.Hour)
	if _, err := n.sourceMetrics.ReconcileAgainstStore(map[string]int64{
		"space-data-network-02/IQEngine": 36636,
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := n.flowServiceLastSuccess(sigmfAppID); ok {
		t.Fatal("a withdrawn source must leave the lane without a vouched last success")
	}
	if due, _, reason := n.flowServiceScheduleDue(sigmfAppID, sigmfInterval); !due {
		t.Fatalf("a lane with a withdrawn source is owed a pull, got: %s", reason)
	}
}

// A conditional fetch that answered 304 is a successful retrieval without a
// batch: it anchors the schedule. A failed attempt does not.
func TestFirstFireScheduleAnchorsOnCleanAttemptsOnly(t *testing.T) {
	n := newScheduleNode(t, true)
	landBatch(n, sigmfAppID, "IQEngine", "iqengine-a", 10*24*time.Hour)

	n.sourceMetrics.RecordAttempt(sigmfAppID)
	n.sourceMetrics.RecordAttemptOutcome(sigmfAppID, errFakeFetch{})
	if due, _, reason := n.flowServiceScheduleDue(sigmfAppID, sigmfInterval); !due {
		t.Fatalf("a failed attempt must not anchor the schedule: %s", reason)
	}

	n.sourceMetrics.RecordAttempt(sigmfAppID)
	n.sourceMetrics.RecordAttemptUnchanged(sigmfAppID)
	if due, _, reason := n.flowServiceScheduleDue(sigmfAppID, sigmfInterval); due {
		t.Fatalf("a 304 just now anchors the weekly timer, got due: %s", reason)
	}
}

// first_fire_when_due:false keeps the plain ticker as well.
func TestSchedulePhaseIsOffWithFirstFire(t *testing.T) {
	n := newScheduleNode(t, false)
	landBatch(n, sigmfAppID, "IQEngine", "iqengine-a", 2*24*time.Hour)
	if phase := n.flowServiceSchedulePhase(sigmfAppID, sigmfInterval); phase != 0 {
		t.Fatalf("first run in %s with first_fire_when_due off, want the plain ticker", phase)
	}
}

// A phase is never shorter than the boot settle delay.
func TestSchedulePhaseNeverMakesTheBootAFetch(t *testing.T) {
	n := newScheduleNode(t, true)
	landBatch(n, sigmfAppID, "IQEngine", "iqengine-a", sigmfInterval-5*time.Second)
	if phase := n.flowServiceSchedulePhase(sigmfAppID, sigmfInterval); phase != flowServiceFirstFireDelay {
		t.Fatalf("first run in %s, want the %s settle delay", phase, flowServiceFirstFireDelay)
	}
}
