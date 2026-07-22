package sdnservices_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ipfs/kubo/sdn/channels"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
	"github.com/ipfs/kubo/sdn/testsupport"
)

const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`

func ommSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

func buildOMM(t *testing.T, norad uint32, name string) []byte {
	t.Helper()
	sized := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch("2026-05-10T00:00:00Z").
		WithEpochTimestamp(float64(time.Now().Unix())).
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
	return sized[4:] // strip the 4-byte size prefix -> single FlatBuffer
}

func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return base + "/sdn-flatsqlrt-test-aot"
}

func newNode(ctx context.Context, t *testing.T) (host.Host, *pubsub.PubSub) {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	ps, err := pubsub.NewGossipSub(ctx, h, pubsub.WithMessageIdFn(channels.MessageIDFn))
	if err != nil {
		t.Fatalf("NewGossipSub: %v", err)
	}
	return h, ps
}

// capEnvelope is the {"ok":..,"result":..,"error":..} shape every cap hostcall
// response uses (see capsjson.go).
type capEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, raw []byte) capEnvelope {
	t.Helper()
	var env capEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode cap envelope %q: %v", string(raw), err)
	}
	return env
}

// TestSDNRuntimeStackWired is the Phase-6 acceptance test. It builds the SDN
// services through the SAME BuildServices the sdnruntime plugin calls — over an
// in-memory blockstore/datastore and a real two-host go-libp2p-pubsub — then
// proves, end to end through the wired services:
//
//	(A) BuildServices wires sdnstore + channels + a module capability registry
//	    + NodeContext together;
//	(B) the storage + pubsub caps are registered in the registry AND are
//	    operator-policy-gated (fail closed, keyed by module content hash):
//	    provisioning them without an approval is refused, with one is admitted;
//	(C) storing a record THROUGH THE STORAGE CAPABILITY (a real bridge.Dispatch
//	    hostcall) lands it in sdnstore, readable back by (source, type);
//	(D) that same store fans the record out on its (source, standard) channel to
//	    a second gossipsub host (the Phase-4 fan-out, now driven by the wired
//	    store's OnStore hook);
//	(E) a real WASM module loads through the runtime wired with this registry
//	    (guarded on the licensing fixture being present).
func TestSDNRuntimeStackWired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const (
		source   = "provider-a"
		std      = "OMM"
		modHash  = "00aa11bb22cc33dd44ee55ff66aa77bb88cc99dd00ee11ff22aa33bb44cc55dd" // stand-in module content hash
		fallback = "sdn-node"
	)

	// Real two-host gossipsub (node A stores + fans out; node B subscribes).
	hostA, psA := newNode(ctx, t)
	hostB, psB := newNode(ctx, t)
	if err := hostB.Connect(ctx, peer.AddrInfo{ID: hostA.ID(), Addrs: hostA.Addrs()}); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}

	// Operator capability policy: initially empty (default deny).
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}

	// In-memory durable stores for node A.
	mds := dssync.MutexWrap(ds.NewMapDatastore())

	// ── (A) BuildServices — the exact wiring the plugin runs ────────────────
	svc, err := sdnservices.BuildServices(sdnservices.Deps{
		Blockstore:     blockstore.NewBlockstore(mds),
		Datastore:      mds,
		PubSub:         psA,
		Schemas:        ommSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
		Policy:         policy,
		PeerID:         hostA.ID().String(),
		FallbackSource: fallback,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer svc.Close()

	if svc.Store == nil || svc.Channels == nil || svc.CapReg == nil || svc.NodeCtx == nil {
		t.Fatalf("BuildServices left a service nil: store=%v channels=%v capReg=%v nodeCtx=%v",
			svc.Store != nil, svc.Channels != nil, svc.CapReg != nil, svc.NodeCtx != nil)
	}
	if svc.NodeCtx.CapabilityPolicy != policy {
		t.Fatalf("NodeCtx did not carry the operator policy")
	}

	// The storage_* and pubsub factories are registered in the wired registry.
	for _, cap := range []string{"storage_write", "storage_query", "pubsub"} {
		if _, ok := svc.CapReg.Lookup(cap); !ok {
			t.Fatalf("capability %q was not registered in the wired registry", cap)
		}
	}

	// ── (B) Policy gating: fail closed, then admit on approval ──────────────
	// Negative: provisioning storage_write with NO recorded approval is refused.
	denyBridge := modulert.NewHostBridge(svc.NodeCtx, nil)
	err = modulert.ProvisionBridge(denyBridge, svc.CapReg, []string{"storage_write"}, nil,
		modulert.ProvisionIdentity{ContentHash: modHash, PluginID: "test-mod", Policy: policy})
	if err == nil {
		t.Fatalf("expected ProvisionBridge to REFUSE storage_write with no operator approval (fail closed)")
	}

	// Record operator approvals for this module hash, then provision succeeds.
	for _, cap := range []string{"storage_write", "storage_query", "pubsub"} {
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: modHash,
			Capability: cap,
			PluginID:   "test-mod",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s): %v", cap, err)
		}
	}
	bridge := modulert.NewHostBridge(svc.NodeCtx, nil)
	if err := modulert.ProvisionBridge(bridge, svc.CapReg, []string{"storage_write", "storage_query", "pubsub"}, nil,
		modulert.ProvisionIdentity{ContentHash: modHash, PluginID: "test-mod", Policy: policy}); err != nil {
		t.Fatalf("ProvisionBridge after approval: %v", err)
	}
	if !bridge.HasCapability("storage_write") || !bridge.HasCapability("storage_query") {
		t.Fatalf("approved bridge missing expected grants")
	}

	// Per-op least-privilege: a bridge granted ONLY storage_query cannot write.
	readOnly := modulert.NewHostBridge(svc.NodeCtx, nil)
	if _, err := policy.Approve(modulert.CapabilityApproval{ModuleHash: "beadfeed" + modHash[8:], Capability: "storage_query", ApprovedBy: "test"}); err != nil {
		t.Fatalf("approve read-only: %v", err)
	}
	if err := modulert.ProvisionBridge(readOnly, svc.CapReg, []string{"storage_query"}, nil,
		modulert.ProvisionIdentity{ContentHash: "beadfeed" + modHash[8:], Policy: policy}); err != nil {
		t.Fatalf("ProvisionBridge read-only: %v", err)
	}
	rec0 := buildOMM(t, 40000, "PERM-CHECK")
	writeReq0, _ := json.Marshal(map[string]string{"source": source, "type": std, "data": base64.StdEncoding.EncodeToString(rec0)})
	if env := decodeEnvelope(t, readOnly.Dispatch("storage.write", writeReq0)); env.OK {
		t.Fatalf("storage.write must be refused for a storage_query-only bridge (least privilege)")
	}

	// ── (C) Store THROUGH THE STORAGE CAPABILITY, read back by (source, type) ─
	// Node B subscribes to the (source, OMM) channel and to an isolation
	// channel that must stay silent, BEFORE the write, so the fan-out (D) is
	// observed on the same store call.
	chB := channels.New(psB)
	subB, err := chB.Subscribe(std, source)
	if err != nil {
		t.Fatalf("subscribe B (%s, %s): %v", source, std, err)
	}
	defer subB.Cancel()
	subIsolation, err := chB.Subscribe(std, "other-provider")
	if err != nil {
		t.Fatalf("subscribe B isolation: %v", err)
	}
	defer subIsolation.Cancel()

	// Node A joins the same channel so a real GRAFTed mesh forms; drain-discard.
	subA, err := svc.Channels.Subscribe(std, source)
	if err != nil {
		t.Fatalf("subscribe A: %v", err)
	}
	defer subA.Cancel()
	go func() {
		for {
			if _, err := subA.Next(ctx); err != nil {
				return
			}
		}
	}()

	wireTopic, _ := channels.WireTopic(source, std)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if len(psA.ListPeers(wireTopic)) >= 1 && len(psB.ListPeers(wireTopic)) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Write records via the storage capability (real hostcall Dispatch). Retry
	// with a FRESH record per attempt to absorb gossipsub mesh warmup (a
	// byte-identical re-store is idempotent and would not re-fire fan-out).
	var stored []byte
	var got *pubsub.Message
	var lastCID string
	for attempt := 0; attempt < 10 && got == nil; attempt++ {
		rec := buildOMM(t, uint32(25544+attempt), fmt.Sprintf("ISS-%d", attempt))
		writeReq, _ := json.Marshal(map[string]string{
			"source": source, "type": std, "data": base64.StdEncoding.EncodeToString(rec),
		})
		env := decodeEnvelope(t, bridge.Dispatch("storage.write", writeReq))
		if !env.OK {
			t.Fatalf("storage.write cap failed (attempt %d): %s", attempt, env.Error.Message)
		}
		var wr struct {
			CID string `json:"cid"`
		}
		if err := json.Unmarshal(env.Result, &wr); err != nil || wr.CID == "" {
			t.Fatalf("storage.write result missing cid: %s", string(env.Result))
		}
		lastCID = wr.CID

		recvCtx, recvCancel := context.WithTimeout(ctx, 3*time.Second)
		msg, err := subB.Next(recvCtx)
		recvCancel()
		if err == nil {
			stored = rec
			got = msg
		}
	}

	// Read back THROUGH THE STORAGE CAPABILITY by (source, type).
	readReq, _ := json.Marshal(map[string]string{"source": source, "type": std})
	readEnv := decodeEnvelope(t, bridge.Dispatch("storage.read", readReq))
	if !readEnv.OK {
		t.Fatalf("storage.read cap failed: %s", readEnv.Error.Message)
	}
	var rd struct {
		Records []string `json:"records"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(readEnv.Result, &rd); err != nil {
		t.Fatalf("decode storage.read result: %v", err)
	}
	if rd.Count == 0 {
		t.Fatalf("storage.read returned no records after writing through the cap")
	}
	// The record we last wrote is recoverable by (source, type).
	foundStored := false
	for _, b64 := range rd.Records {
		if raw, err := base64.StdEncoding.DecodeString(b64); err == nil && stored != nil && bytes.Equal(raw, stored) {
			foundStored = true
		}
	}
	if stored != nil && !foundStored {
		t.Fatalf("the record written through the cap was not read back by (source, type)")
	}

	// Cross-check the durable store directly (same records the cap wrote).
	directRecs, err := svc.Store.ReadBySourceType(ctx, source, std)
	if err != nil {
		t.Fatalf("Store.ReadBySourceType: %v", err)
	}
	if len(directRecs) != rd.Count {
		t.Fatalf("cap read count %d != direct store read count %d", rd.Count, len(directRecs))
	}

	// ── (D) Fan-out proof ───────────────────────────────────────────────────
	if got == nil {
		t.Fatalf("node B never received a fanned-out record on the (%s, %s) channel", source, std)
	}
	if !bytes.Equal(got.Data, stored) {
		t.Fatalf("fanned-out bytes (%d) != stored record (%d)", len(got.Data), len(stored))
	}
	wantCID, err := channels.CIDOf(stored)
	if err != nil {
		t.Fatalf("CIDOf: %v", err)
	}
	if got.ID != wantCID.String() {
		t.Fatalf("gossipsub message id %q != record CID %q", got.ID, wantCID.String())
	}
	// The cap-reported CID for the received record equals the content id the
	// channel keys on (one content-addressing across store, cap and channel).
	if lastCID != wantCID.String() {
		t.Fatalf("storage.write cid %q != record CID %q", lastCID, wantCID.String())
	}

	// Isolation: a different source's channel received nothing.
	isoCtx, isoCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer isoCancel()
	if msg, err := subIsolation.Next(isoCtx); err == nil {
		t.Fatalf("isolation broken: other-provider channel received %d bytes", len(msg.Data))
	}

	// ── (E) A real WASM module loads through the wired runtime ───────────────
	loadRealModuleThroughRuntime(t, svc)
}

// TestBuildServicesStorageOnly locks the nil-PubSub branch the plugin relies on
// when kubo Pubsub.Enabled=false: BuildServices must succeed storage-only (no
// channels, no pubsub capability, no fan-out hook) rather than crash, and the
// storage capability must still be registered and gated.
func TestBuildServicesStorageOnly(t *testing.T) {
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	svc, err := sdnservices.BuildServices(sdnservices.Deps{
		Blockstore:     blockstore.NewBlockstore(mds),
		Datastore:      mds,
		PubSub:         nil, // Pubsub disabled -> storage-only
		Schemas:        ommSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
		Policy:         policy,
	})
	if err != nil {
		t.Fatalf("BuildServices (storage-only) must not fail: %v", err)
	}
	defer svc.Close()

	if svc.Channels != nil {
		t.Fatalf("storage-only mode must leave Channels nil")
	}
	if _, ok := svc.CapReg.Lookup("pubsub"); ok {
		t.Fatalf("pubsub capability must NOT be registered when PubSub is nil")
	}
	if _, ok := svc.CapReg.Lookup("storage_write"); !ok {
		t.Fatalf("storage_write must still be registered in storage-only mode")
	}

	// Storage still works (no fan-out hook to fire).
	if _, err := svc.Store.Store(context.Background(), "src", "OMM", buildOMM(t, 12345, "SO")); err != nil {
		t.Fatalf("Store in storage-only mode: %v", err)
	}
}

// loadRealModuleThroughRuntime proves the runtime the plugin builds actually
// loads a real WASM module wired with the services' capability registry +
// NodeContext. Skipped when the licensing fixture is absent.
func loadRealModuleThroughRuntime(t *testing.T, svc *sdnservices.Services) {
	t.Helper()
	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read licensing wasm: %v", err)
	}

	// The licensing manifest declares sensitive caps that default-deny at load;
	// approve them for its hash in the SAME operator policy the services carry,
	// so the runtime's fail-closed gate admits it.
	policy := svc.NodeCtx.CapabilityPolicy
	moduleHash := modulert.ContentHashHex(wasmBytes)
	for _, cap := range []string{"ipfs", "protocol_dial", "wallet_sign", "protocol_handle", "crypto_sign", "crypto_verify"} {
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: moduleHash, Capability: cap, PluginID: "licensing", ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("approve %s for licensing module: %v", cap, err)
		}
	}

	mod, err := svc.LoadModule(wasmBytes)
	if err != nil {
		t.Fatalf("LoadModule(real licensing wasm) through wired runtime: %v", err)
	}
	defer func() { _ = mod.Close() }()

	if mod.Manifest() == nil {
		t.Fatalf("loaded module has no parsed manifest")
	}
	if mod.ContentHash() != moduleHash {
		t.Fatalf("module content hash %q != %q", mod.ContentHash(), moduleHash)
	}
}
