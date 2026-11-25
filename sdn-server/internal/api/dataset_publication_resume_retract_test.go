package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestInterruptedPublicationMarkDroppedWhenLaneRetracted: a publication mark
// left before the lane was retracted is dropped on the next start with zero
// retry attempts and no publication_failed alert (stack issue 1085).
func TestInterruptedPublicationMarkDroppedWhenLaneRetracted(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := crypto.UnmarshalEd25519PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(t.TempDir(), "publications")
	alerts := ops.NewRegistry()
	svc := NewConcreteDatasetPublicationService(store, nil, priv, pid.String(), "", "", outputDir)
	svc.SetAlerts(alerts)

	req := DatasetPublicationRequest{Schema: "OMM.fbs", ProviderID: "synthetic", SourceName: "retracted-lane", BatchID: "b1"}
	started := time.Now().UTC().Add(-time.Minute)
	if !svc.runs.mark(req, started) {
		t.Fatal("failed to write interrupted publication mark")
	}
	markFile := svc.runs.markPath(req)
	if _, err := os.Stat(markFile); err != nil {
		t.Fatalf("mark file missing before retract: %v", err)
	}

	// Simulate the lane being retracted while the node was down or restarting.
	lane := storage.DatasetPublicationLane{SchemaName: "OMM.fbs", ProviderID: "synthetic", SourceName: "retracted-lane"}
	if err := store.MarkDatasetPublicationLaneRetracted(lane, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	restarted := NewConcreteDatasetPublicationService(store, nil, priv, pid.String(), "", "", outputDir)
	restarted.SetAlerts(alerts)
	if n := restarted.ReadInterruptedPublications(); n != 1 {
		t.Fatalf("ReadInterruptedPublications() = %d, want 1", n)
	}
	restarted.ResumeInterruptedPublications(context.Background())

	if _, err := os.Stat(markFile); !os.IsNotExist(err) {
		t.Fatalf("mark file for retracted lane was not removed: err=%v", err)
	}
	if active := alerts.Active(); len(active) != 0 {
		t.Fatalf("retracted lane mark raised alerts: %+v", active)
	}
}
