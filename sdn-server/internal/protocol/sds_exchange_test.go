package protocol

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestHandlePubSubMessageStoresPNMAnnouncementsFromDatasetSchemaTopic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-protocol-pnm-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	pnmBytes := buildProtocolTestPNM(t, "bafymanifest", "DPM")
	handler := NewSDSExchangeHandler(store, validator)
	handler.SetPushAuthorizer(AllowAnyPush) // these tests exercise store mechanics, not trust policy
	if err := handler.HandlePubSubMessage("OMM.fbs", pnmBytes, peer.ID("12D3KooWDatasetPublisher")); err != nil {
		t.Fatalf("HandlePubSubMessage failed: %v", err)
	}

	pnmRecords, err := store.QueryAll("PNM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll PNM failed: %v", err)
	}
	if len(pnmRecords) != 1 || !reflect.DeepEqual(pnmRecords[0], pnmBytes) {
		t.Fatalf("PNM records = %d, first matches original: %v", len(pnmRecords), len(pnmRecords) == 1 && reflect.DeepEqual(pnmRecords[0], pnmBytes))
	}

	ommRecords, err := store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM failed: %v", err)
	}
	if len(ommRecords) != 0 {
		t.Fatalf("PNM announcement should not be stored as OMM data, got %d OMM records", len(ommRecords))
	}
}

func TestHandlePubSubMessageCallsPNMHandlerForDatasetSchemaTopic(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	pnmBytes := buildProtocolTestPNM(t, "bafymanifest", "dataset:OMM.fbs:batch")
	handler := NewSDSExchangeHandler(store, validator)
	handler.SetPushAuthorizer(AllowAnyPush) // these tests exercise store mechanics, not trust policy
	called := false
	fromPeer := peer.ID("12D3KooWDatasetPublisher")
	handler.SetPubSubPNMHandler(func(ctx context.Context, schema string, data []byte, from peer.ID) error {
		called = true
		if schema != "OMM.fbs" {
			t.Fatalf("handler schema = %q, want OMM.fbs", schema)
		}
		if !reflect.DeepEqual(data, pnmBytes) {
			t.Fatal("handler data does not match PNM bytes")
		}
		if from != fromPeer {
			t.Fatalf("handler peer = %s", from)
		}
		return nil
	})
	if err := handler.HandlePubSubMessage("OMM.fbs", pnmBytes, fromPeer); err != nil {
		t.Fatalf("HandlePubSubMessage failed: %v", err)
	}
	if !called {
		t.Fatal("PNM handler was not called")
	}
}

// TestHandlePubSubMessageSDSRecordNilStoreDoesNotPanic reproduces the prod
// host-01 (sdn.spaceaware.io) edge-mode crash: an edge node has no local store
// (h.store == nil) yet still subscribes to every schema pubsub topic, so an
// incoming valid SDS record message drives Store() on a nil store and SIGSEGVs.
// The guard must skip the store write, count the drop, and return nil (so the
// caller does not per-message warn) instead of panicking.
func TestHandlePubSubMessageSDSRecordNilStoreDoesNotPanic(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	// nil store models an edge-mode node (config mode: edge, no FlatSQL store).
	handler := NewSDSExchangeHandler(nil, validator)
	handler.SetPushAuthorizer(AllowAnyPush) // these tests exercise store mechanics, not trust policy

	ommBytes := buildFixtureOMM(t, 25544, "ISS (ZARYA)")
	from := peer.ID("12D3KooWEdgePublisher")

	if err := handler.HandlePubSubMessage("OMM.fbs", ommBytes, from); err != nil {
		t.Fatalf("edge-mode nil store must skip silently, got err: %v", err)
	}
	if got := handler.DroppedNoStore(); got != 1 {
		t.Fatalf("DroppedNoStore = %d, want 1", got)
	}

	// A second record increments the drop counter again (no panic, still nil).
	if err := handler.HandlePubSubMessage("OMM.fbs", ommBytes, from); err != nil {
		t.Fatalf("second edge-mode record must skip silently, got err: %v", err)
	}
	if got := handler.DroppedNoStore(); got != 2 {
		t.Fatalf("DroppedNoStore after two records = %d, want 2", got)
	}
}

// TestHandlePubSubMessagePNMNilStoreDoesNotPanic covers the other pubsub store
// boundary: a PNM announcement reaching an edge node must also skip the store
// (and the downstream materialize handler) rather than panic.
func TestHandlePubSubMessagePNMNilStoreDoesNotPanic(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	handler := NewSDSExchangeHandler(nil, validator)
	handler.SetPushAuthorizer(AllowAnyPush) // these tests exercise store mechanics, not trust policy
	pnmHandlerCalled := false
	handler.SetPubSubPNMHandler(func(ctx context.Context, schema string, data []byte, from peer.ID) error {
		pnmHandlerCalled = true
		return nil
	})

	pnmBytes := buildProtocolTestPNM(t, "bafymanifest", "DPM")
	if err := handler.HandlePubSubMessage("OMM.fbs", pnmBytes, peer.ID("12D3KooWEdgePublisher")); err != nil {
		t.Fatalf("edge-mode nil store must skip PNM silently, got err: %v", err)
	}
	if got := handler.DroppedNoStore(); got != 1 {
		t.Fatalf("DroppedNoStore = %d, want 1", got)
	}
	if pnmHandlerCalled {
		t.Fatal("PNM materialize handler must not run on an edge node with no store")
	}
}

// TestHandlePubSubMessageStoresSDSRecordWhenStorePresent is the regression
// guard: when a store IS present, a valid SDS record still stores normally and
// the drop counter stays zero (the nil-store guard must not affect this path).
func TestHandlePubSubMessageStoresSDSRecordWhenStorePresent(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	handler := NewSDSExchangeHandler(store, validator)
	handler.SetPushAuthorizer(AllowAnyPush) // these tests exercise store mechanics, not trust policy
	ommBytes := buildFixtureOMM(t, 25544, "ISS (ZARYA)")
	if err := handler.HandlePubSubMessage("OMM.fbs", ommBytes, peer.ID("12D3KooWStorePublisher")); err != nil {
		t.Fatalf("store-present HandlePubSubMessage failed: %v", err)
	}
	if got := handler.DroppedNoStore(); got != 0 {
		t.Fatalf("store-present path must not drop, DroppedNoStore = %d", got)
	}

	records, err := store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 stored OMM record, got %d", len(records))
	}
}

func buildProtocolTestPNM(t *testing.T, cid, fileID string) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}

// The push lane used to go from a rate-limit check straight to store.Store with a
// literal nil signature: any peer that could dial the node, or simply subscribe to a
// pubsub topic, could inject records — which the node then served back on its
// ANONYMOUS public data plane, unioned across every producer table. Discovery makes
// reaching a node free, because it advertises itself on the public Amino DHT.
//
// There was no test on any of the three write paths, which is how it survived.
func TestUntrustedPeerCannotWriteRecords(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-protocol-push-gate-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	pnmBytes := buildProtocolTestPNM(t, "bafyuntrusted", "DPM")
	stranger := peer.ID("12D3KooWUntrustedStranger")

	// UNWIRED IS DENY. A handler whose authorizer was never set must refuse, so
	// that a future second construction path that forgets to wire one fails
	// closed instead of silently reopening the hole.
	unwired := NewSDSExchangeHandler(store, validator)
	if err := unwired.HandlePubSubMessage("OMM.fbs", pnmBytes, stranger); err != nil {
		t.Fatalf("an unwired handler should drop the record, not error: %v", err)
	}
	if got := countStored(t, store, "PNM.fbs"); got != 0 {
		t.Fatalf("unwired handler stored %d PNM records; nil authorizer must deny", got)
	}

	// An explicit deny behaves the same way.
	denied := NewSDSExchangeHandler(store, validator)
	denied.SetPushAuthorizer(func(peer.ID) bool { return false })
	if err := denied.HandlePubSubMessage("OMM.fbs", pnmBytes, stranger); err != nil {
		t.Fatalf("a denied push should drop the record, not error: %v", err)
	}
	if got := countStored(t, store, "PNM.fbs"); got != 0 {
		t.Fatalf("denied peer stored %d PNM records", got)
	}

	// And the trusted peer it is gating FOR still gets through, or the gate would
	// be indistinguishable from the feature being broken.
	allowed := NewSDSExchangeHandler(store, validator)
	allowed.SetPushAuthorizer(func(id peer.ID) bool { return id == stranger })
	if err := allowed.HandlePubSubMessage("OMM.fbs", pnmBytes, stranger); err != nil {
		t.Fatalf("HandlePubSubMessage for a trusted peer failed: %v", err)
	}
	if got := countStored(t, store, "PNM.fbs"); got != 1 {
		t.Fatalf("trusted peer stored %d PNM records, want 1", got)
	}
}

// The authorizer must be consulted per peer, not cached or decided once.
func TestPushAuthorizerIsConsultedPerPeer(t *testing.T) {
	var asked []peer.ID
	h := &SDSExchangeHandler{}
	h.SetPushAuthorizer(func(id peer.ID) bool {
		asked = append(asked, id)
		return id == peer.ID("yes")
	})
	if h.mayPush(peer.ID("no")) {
		t.Error("mayPush allowed a peer the authorizer rejected")
	}
	if !h.mayPush(peer.ID("yes")) {
		t.Error("mayPush rejected a peer the authorizer allowed")
	}
	if len(asked) != 2 {
		t.Fatalf("authorizer consulted %d times, want 2 (once per call)", len(asked))
	}
}

func countStored(t *testing.T, store *storage.FlatSQLStore, schema string) int {
	t.Helper()
	recs, err := store.QueryAll(schema, 10)
	if err != nil {
		t.Fatalf("QueryAll %s failed: %v", schema, err)
	}
	return len(recs)
}
