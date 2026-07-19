package flowrt

// od_supplemental_flow.go — the authored $PLG for the supplemental-OMM OD flow.
//
// This is the ONE composed wasi-threads flow the node bakes + runs for the full
// catalog: five in-wasm provider fetch/parse nodes each emit operator ephemeris
// ($OEM) on their typed "oem" port; the threaded OD node fits each with its
// std::thread work-stealing pool; and the store node persists ONLY the results
// ($OMM/$OCM/$OBD) via the storage.ingest_with_source host capability. $OEM is
// held in memory for the fit and NEVER wired to the store — the in-memory-only
// invariant is structural here (no edge carries $OEM to storage-ingest).
//
// ALL nodes are linked-direct wasi-threads guest-links (threadModel="wasi-threads"),
// so the bake takes the wasi-threads path (shared-memory reactor + WithWASIThreads
// + AOT-at-load) and the dual-path gate never mixes ABIs. The Go host contributes
// ONLY the http/fs/storage capability primitives; it never fetches, batches,
// caps, or stores in Go.
//
// Node/port IDs are the SHIPPED guest-link identities:
//   providers  com.orbpro.{spacex-starlink,glonass,intelsat,cpf,iss}-source .emit -> port "oem"
//   od         orbit-determination .fit  : in "oem"  -> out "omm","ocm","obd"
//   store      com.digitalarsenal.hostcap.storage-ingest .ingest : in "records"
//
// OneWeb is excluded (LTEF metadata-only — no state vectors, not fittable). GPS +
// CelesTrak-SupGP are NOT sources. No per-object cap: each provider pulls its full
// constellation (module-owned), the OD node fits every object.

// ODSupplementalStorePluginID is the store node id. The wasi-threads storage-ingest
// guest-link keeps this same id/method/ports as the emscripten one — only its
// metadata.threadModel flips to "wasi-threads" so it co-links with the flow.
const (
	ODSupplementalStorePluginID = "com.digitalarsenal.hostcap.flatsql-store"
	ODSupplementalStoreMethod   = "store"
	ODSupplementalStorePort     = "records"
	ODSupplementalODPluginID    = "orbit-determination"
)

// odSupplementalProviders lists the five fittable ephemeris providers, in a
// stable UI order. Each emits $OEM on port "oem" via method "emit".
var odSupplementalProviders = []struct{ node, plugin string }{
	{"n-starlink", "com.orbpro.spacex-starlink-source"},
	{"n-glonass", "com.orbpro.glonass-source"},
	{"n-intelsat", "com.orbpro.intelsat-source"},
	{"n-cpf", "com.orbpro.cpf-source"},
	{"n-iss", "com.orbpro.iss-source"},
}

// ODSupplementalOMMSpec builds the supplemental-OMM OD flow as a FlowSpec (baked
// via BuildFlowPLG). A single host-cron trigger fans all five providers; every
// provider's $OEM flows into the ONE threaded OD node; the OD node's three result
// records flow into the store. Laid out left-to-right for the editor.
func ODSupplementalOMMSpec() FlowSpec {
	nodes := make([]FlowNodeSpec, 0, len(odSupplementalProviders)+2)
	edges := make([]FlowEdgeSpec, 0, len(odSupplementalProviders)+3)
	binds := make([]FlowTriggerBindingSpec, 0, len(odSupplementalProviders))

	for i, p := range odSupplementalProviders {
		nodes = append(nodes, FlowNodeSpec{
			NodeID: p.node, PluginID: p.plugin, MethodID: "emit", Kind: "source",
			UIX: 40, UIY: float32(40 + i*110),
		})
		// provider.emit.oem -> od.fit.oem (all five into the OD node's oem port).
		edges = append(edges, FlowEdgeSpec{
			EdgeID: "e-" + p.node, FromNodeID: p.node, FromPortID: "oem",
			ToNodeID: "n-od", ToPortID: "oem",
		})
		// The host-cron trigger fires each provider's emit (its "config" input tick).
		binds = append(binds, FlowTriggerBindingSpec{
			TriggerID: "t0", TargetNodeID: p.node, TargetPortID: "config",
		})
	}

	nodes = append(nodes,
		FlowNodeSpec{NodeID: "n-od", PluginID: ODSupplementalODPluginID, MethodID: "fit", Kind: "transform", UIX: 400, UIY: 260},
		FlowNodeSpec{NodeID: "n-store", PluginID: ODSupplementalStorePluginID, MethodID: ODSupplementalStoreMethod, Kind: "sink", UIX: 760, UIY: 260},
	)
	// od.fit.{omm,ocm,obd} -> store.ingest.records (results only; NEVER $OEM).
	for _, port := range []string{"omm", "ocm", "obd"} {
		edges = append(edges, FlowEdgeSpec{
			EdgeID: "e-store-" + port, FromNodeID: "n-od", FromPortID: port,
			ToNodeID: "n-store", ToPortID: ODSupplementalStorePort,
		})
	}

	return FlowSpec{
		ProgramID:       "org.sdn.flows.od-supplemental-omm",
		Name:            "Supplemental-OMM Orbit Determination",
		Version:         "1.0.0",
		Description:     "Five in-wasm ephemeris providers -> threaded SGP4 OD fit -> $OMM/$OCM/$OBD store. Ephemeris in-memory only; no $OEM persisted. One composed wasi-threads flow.",
		Nodes:           nodes,
		Edges:           edges,
		Triggers:        []FlowTriggerSpec{{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}},
		TriggerBindings: binds,
	}
}

// odSupplementalOEMConsumers returns, for the assertNoOEMToStore-style invariant
// check, the set of node.port that consume any provider's $OEM. It must be EXACTLY
// the OD node's oem port — no store/persist node may consume $OEM.
func odSupplementalOEMConsumers(spec FlowSpec) []string {
	var consumers []string
	providerNodes := map[string]bool{}
	for _, p := range odSupplementalProviders {
		providerNodes[p.node] = true
	}
	for _, e := range spec.Edges {
		if providerNodes[e.FromNodeID] && e.FromPortID == "oem" {
			consumers = append(consumers, e.ToNodeID+"."+e.ToPortID)
		}
	}
	return consumers
}
