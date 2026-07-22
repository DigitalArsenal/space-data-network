// Package modulert is the SDN WASM module runtime, ported onto the kubo base
// as Phase 2b of the kubo rebase.
//
// # Port status (Phase 2b checkpoint: prove one module loads and invokes)
//
// Ported and building/green here:
//   - the 9 runtime source files (capability_policy, capability_policy_api,
//     capability_provision, hostbridge, invoke_codec, manifest, module,
//     publication, publication_signature),
//   - its dependencies rehomed under kubo/sdn: wasmrt (WasmEdge leaf, ported
//     earlier), plugins (TRIMMED to the runtime types/interfaces only — the
//     Manager runtime was left behind, see sdn/plugins/manager.go), and
//     license (TRIMMED to DefaultPluginRoot + the ModulePublish signing
//     surface, see sdn/license/publish_protocol.go),
//   - the spacedatastandards PIV/PLG/MBL FlatBuffer bindings, vendored under
//     sdn/third_party/spacedatastandards-go (local go.mod replace).
//
// # DEFERRED — caps (not yet ported; not required for the invoke checkpoint)
//
// The capability host services under sdn-server/internal/modulert/caps are NOT
// ported yet. They are test-only from modulert's perspective (no non-test
// runtime file imports caps), so a cap-free module load + invoke works without
// them. Port them in a later phase, when the services they bind to are rebased:
//
//   - caps/p2p.go       — needs the node's libp2p channels/streams wiring
//   - caps/secrets.go   — needs the credential store (credstore)
//   - caps/storage.go   — needs flatsqlrt / ingest / storage
//   - caps/crypto.go, http.go, ipfs.go, keyslot.go, nodeactivity.go,
//     nodestatus.go, protocol.go, pubsub.go — self-contained-ish; port when
//     the first cap-using module integration test is brought over.
package modulert
