package flowrt

// od_supplemental_flow.go — the authored $PLG for the supplemental-OMM OD flow.
//
// This is the ONE composed wasi-threads flow the node bakes + runs for the full
// catalog: five in-wasm provider fetch/parse nodes each emit operator ephemeris
// ($OEM) on their typed "oem" port; the threaded OD node fits each with its
// std::thread work-stealing pool; and the in-wasm FlatSQL store node persists ONLY
// the results ($OMM/$OCM/$OBD), appending a wrapper FlatBuffer per record to the
// store engine's arena via the flatsql.ingest_record trampoline (opaque bytes; the
// host never decodes a record). $OEM is held in memory for the fit and NEVER wired
// to the store — the in-memory-only invariant is structural here (no edge carries
// $OEM to the store node).
//
// ALL nodes are linked-direct wasi-threads guest-links (threadModel="wasi-threads"),
// so the bake takes the wasi-threads path (shared-memory reactor + WithWASIThreads
// + AOT-at-load) and the dual-path gate never mixes ABIs. The Go host contributes
// ONLY capability primitives — the http connector (providers fetch) and the fs
// connector (opaque store snapshots); it never fetches, batches, caps, derives a
// record, or stores in Go.
//
// Node/port IDs are the SHIPPED guest-link identities:
//   providers  com.orbpro.{spacex-starlink,glonass,intelsat,cpf,iss}-source .emit -> port "oem"
//   od         orbit-determination .fit  : in "oem"  -> out "omm","ocm","obd"
//   store      com.digitalarsenal.hostcap.flatsql-store .store : in "records"
//
// OneWeb is excluded (LTEF metadata-only — no state vectors, not fittable). GPS +
// CelesTrak-SupGP are NOT sources. No per-object cap: each provider pulls its full
// constellation (module-owned), the OD node fits every object.

// ODSupplementalStorePluginID is the in-wasm FlatSQL store node id. It is a
// wasi-threads guest-link that co-links into the composed reactor; it persists
// $OMM/$OCM/$OBD by arena ingest (flatsql.ingest_record) — never the repudiated
// Go storage sink.
const (
	ODSupplementalStorePluginID = "com.digitalarsenal.hostcap.flatsql-store"
	ODSupplementalStoreMethod   = "store"
	ODSupplementalStorePort     = "records"
	ODSupplementalODPluginID    = "orbit-determination"
	// ODSupplementalStoreProvenancePort receives the OD node's CID-keyed
	// provenance sidecar ([u32 cid_len][cid][u32 provider_len][provider][u32
	// source_name_len][source_name]); the store maps cid -> provider/source_name.
	ODSupplementalStoreProvenancePort = "provenance"
	// ODSupplementalStoreTriggerPort receives the host fire timestamp
	// ([u64le unix_ms]) -> the store's pulled_at column. Host trigger metadata =
	// a capability read (the host reads its own clock); NOT orchestration.
	ODSupplementalStoreTriggerPort = "trigger"
	// ODSupplementalODProvenancePort is the OD node's provenance OUTPUT port.
	ODSupplementalODProvenancePort = "provenance"
)

// odSupplementalProviders lists the five fittable ephemeris providers, in a
// stable UI order. Each emits $OEM on port "oem" via method "emit".
var odSupplementalProviders = []struct{ node, plugin, odPort string }{
	{"n-starlink", "com.orbpro.spacex-starlink-source", "oem_starlink"},
	{"n-glonass", "com.orbpro.glonass-source", "oem_glonass"},
	{"n-intelsat", "com.orbpro.intelsat-source", "oem_intelsat"},
	{"n-cpf", "com.orbpro.cpf-source", "oem_cpf"},
	{"n-iss", "com.orbpro.iss-source", "oem_iss"},
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
		// provider.emit.oem -> od.fit.oem_<provider> (per-provider port so the OD
		// node derives the provider token per-edge for the provenance sidecar).
		edges = append(edges, FlowEdgeSpec{
			EdgeID: "e-" + p.node, FromNodeID: p.node, FromPortID: "oem",
			ToNodeID: "n-od", ToPortID: p.odPort,
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
	// od.fit.provenance -> store.provenance (CID-keyed sidecar -> provider/
	// source_name columns). REQUIRED for per-provider attribution.
	edges = append(edges, FlowEdgeSpec{
		EdgeID: "e-store-provenance", FromNodeID: "n-od", FromPortID: ODSupplementalODProvenancePort,
		ToNodeID: "n-store", ToPortID: ODSupplementalStoreProvenancePort,
	})
	// Host fire timestamp -> store.trigger ([u64le unix_ms] -> pulled_at). The
	// host-cron trigger delivers the fire time as a capability read.
	binds = append(binds, FlowTriggerBindingSpec{
		TriggerID: "t0", TargetNodeID: "n-store", TargetPortID: ODSupplementalStoreTriggerPort,
	})

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
