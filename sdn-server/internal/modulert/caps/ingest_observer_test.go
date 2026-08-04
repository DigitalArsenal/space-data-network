package caps

import "testing"

// The ingest signal has exactly one LEDGER (the node's operational metrics)
// and any number of other host connectors that legitimately need it — the
// dataset auto-publisher (sdn-rfb-publish-to-consumer-node) is the first. A
// second consumer must never have to displace the ledger to be told that a
// batch landed.

func TestIngestObserversFanOutWithoutDisplacingTheLedger(t *testing.T) {
	t.Cleanup(func() { SetIngestObserver(nil) })

	var ledger, tapA, tapB []string
	SetIngestObserver(func(obs IngestObservation) { ledger = append(ledger, obs.BatchID) })
	removeA := AddIngestObserver(func(obs IngestObservation) { tapA = append(tapA, obs.BatchID) })
	removeB := AddIngestObserver(func(obs IngestObservation) { tapB = append(tapB, obs.BatchID) })

	observeIngest(IngestObservation{Schema: "RFB.fbs", BatchID: "b1"})

	if len(ledger) != 1 || len(tapA) != 1 || len(tapB) != 1 {
		t.Fatalf("fan-out failed: ledger=%v tapA=%v tapB=%v", ledger, tapA, tapB)
	}

	// Removing one tap leaves the others intact.
	removeA()
	observeIngest(IngestObservation{Schema: "RFB.fbs", BatchID: "b2"})
	if len(tapA) != 1 {
		t.Fatalf("removed tap still fired: %v", tapA)
	}
	if len(tapB) != 2 || len(ledger) != 2 {
		t.Fatalf("remaining observers stopped firing: ledger=%v tapB=%v", ledger, tapB)
	}

	// Replacing the ledger must not silence the taps — the failure this
	// registry exists to prevent.
	SetIngestObserver(func(obs IngestObservation) { ledger = append(ledger, "replaced:"+obs.BatchID) })
	observeIngest(IngestObservation{Schema: "RFB.fbs", BatchID: "b3"})
	if len(tapB) != 3 {
		t.Fatalf("tap was silenced by SetIngestObserver: %v", tapB)
	}

	removeB()
	SetIngestObserver(nil)
	observeIngest(IngestObservation{Schema: "RFB.fbs", BatchID: "b4"})
	if len(ledger) != 3 || len(tapB) != 3 {
		t.Fatalf("observers fired after removal: ledger=%v tapB=%v", ledger, tapB)
	}
}

func TestAddIngestObserverToleratesNil(t *testing.T) {
	remove := AddIngestObserver(nil)
	if remove == nil {
		t.Fatal("AddIngestObserver(nil) must still return a remover")
	}
	remove()
	observeIngest(IngestObservation{Schema: "RFB.fbs", BatchID: "b0"})
}
