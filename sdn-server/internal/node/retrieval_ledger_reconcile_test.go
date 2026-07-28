package node

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
)

func newLedgerOnlyNode(t *testing.T) *Node {
	t.Helper()
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open retrieval ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	return &Node{sourceMetrics: ledger}
}

// The whole point of withdrawing an unsupported claim: the debounce gate must
// then let the node fetch data it genuinely does not have.
func TestInvalidatedLedgerRowMakesRetrievalDue(t *testing.T) {
	n := newLedgerOnlyNode(t)
	const appID = "com.digitalarsenal.flows.celestrak-satcat-ingest"

	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      appID,
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		SourceURL:  "https://celestrak.org/pub/satcat.csv",
		Schema:     "CAT.fbs",
		BatchID:    "batch-satcat",
		Records:    70122,
		Inserted:   68127,
		At:         time.Now().Add(-10 * time.Minute),
	})

	// A ten-minute-old success is inside the 3 h window: correctly NOT due.
	if due, reason := n.flowServiceRetrievalDue(appID); due {
		t.Fatalf("a fresh, corroborated pull should gate the lane shut, got due (%s)", reason)
	}

	// Now the store proves it holds nothing for that source.
	invalidated, err := n.sourceMetrics.ReconcileAgainstStore(map[string]int64{})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(invalidated) != 1 {
		t.Fatalf("invalidated %d row(s), want 1", len(invalidated))
	}

	due, reason := n.flowServiceRetrievalDue(appID)
	if !due {
		t.Fatalf("an invalidated source should read as never retrieved and be due, got: %s", reason)
	}
	if reason == "" {
		t.Fatal("the gate should say WHY it opened")
	}
}

// ...but only once the publisher's attempt window has also passed. Invalidation
// withdraws a SUCCESS claim; it does not erase the fact that this node asked.
func TestInvalidationDoesNotBypassTheAttemptWindow(t *testing.T) {
	n := newLedgerOnlyNode(t)
	const appID = "com.digitalarsenal.flows.celestrak-satcat-ingest"

	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      appID,
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		SourceURL:  "https://celestrak.org/pub/satcat.csv",
		Schema:     "CAT.fbs",
		BatchID:    "batch-satcat",
		Records:    70122,
		Inserted:   68127,
		At:         time.Now().Add(-10 * time.Minute),
	})
	// The node just asked — this is the restart-storm guard.
	n.sourceMetrics.RecordAttempt(appID)

	if _, err := n.sourceMetrics.ReconcileAgainstStore(map[string]int64{}); err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}

	due, reason := n.flowServiceRetrievalDue(appID)
	if due {
		t.Fatal("invalidation opened the lane despite a just-made attempt; a restart loop would now hammer the publisher")
	}
	if reason == "" {
		t.Fatal("the gate should say why it stayed shut")
	}
}

// The dangerous direction, at the gate: a ledger the store corroborates must
// keep gating, so reconciliation can never turn into an unearned re-fetch.
func TestCorroboratedLedgerKeepsGating(t *testing.T) {
	n := newLedgerOnlyNode(t)
	const appID = "com.digitalarsenal.flows.celestrak-satcat-ingest"

	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      appID,
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		SourceURL:  "https://celestrak.org/pub/satcat.csv",
		Schema:     "CAT.fbs",
		BatchID:    "batch-satcat",
		Records:    70122,
		Inserted:   68127,
		At:         time.Now().Add(-10 * time.Minute),
	})

	invalidated, err := n.sourceMetrics.ReconcileAgainstStore(map[string]int64{
		"space-data-network-02/celestrak-satcat-csv": 68127,
	})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(invalidated) != 0 {
		t.Fatalf("a corroborated ledger was invalidated: %+v", invalidated)
	}
	if due, reason := n.flowServiceRetrievalDue(appID); due {
		t.Fatalf("a corroborated lane opened after reconciliation (%s)", reason)
	}
}

// A node with no store cannot produce evidence, and no evidence must never be
// read as evidence of absence.
func TestReconcileRetrievalLedgerIsANoOpWithoutAStore(t *testing.T) {
	n := newLedgerOnlyNode(t)
	const appID = "com.digitalarsenal.flows.celestrak-satcat-ingest"
	n.sourceMetrics.RecordIngest(sourcemetrics.Ingest{
		AppID:      appID,
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-satcat-csv",
		Schema:     "CAT.fbs",
		BatchID:    "batch-satcat",
		Records:    70122,
		Inserted:   68127,
		At:         time.Now().Add(-10 * time.Minute),
	})

	n.reconcileRetrievalLedger(false)

	sources, err := n.sourceMetrics.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	for _, src := range sources {
		if src.Invalidated {
			t.Fatalf("a storeless node invalidated %s on no evidence", src.SourceID)
		}
	}
}
