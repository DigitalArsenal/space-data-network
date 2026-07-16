// Package sdnbackup implements the SDN flow/module backup + external-storage
// adapter capability described in coordination/tasks/sdn-backup-storage-spec.md.
//
// The model is "everything is a content-addressed blob": a node already stores
// its modules (WASM bytes in the blockstore), its flows (a runtime.wasm +
// flow.json + artifact.json triple on disk) and its SDS records (FlatBuffer
// bytes in the blockstore) as durable, self-verifying units. This package fans
// those units out to one or more STORAGE ADAPTERS and can restore them again.
//
// # The three pieces
//
//  1. Adapter (adapter.go) — the uniform, content-addressed blob interface
//     every provider implements: Describe, Put, Get, Has, List, Delete. The
//     spec's $PIV adapter methods map one-to-one onto this Go interface; a real
//     provider is a WASM module implementing them, but the reference adapters
//     here (LocalAdapter, GitHubAdapter) implement the same interface in Go so
//     the whole backup -> verify -> restore round-trip is provable without a
//     WASM toolchain in the loop.
//
//  2. BackupSource (source.go) — the node-side read surface: enumerate the
//     node's backup units (modules, flows, records, on-disk config files) and
//     fetch their bytes. This is exactly what the repurposed, previously-inert
//     storage_adapter capability exposes to a WASM backup flow
//     (storage.adapter.list_units / storage.adapter.get_unit); see
//     sdn/sdnservices/storage_cap.go.
//
//  3. Runner (runner.go) — the BACKUP flow and its inverse RESTORE. Backup
//     enumerates units, skips content hashes an adapter already has
//     (incremental), fans the misses out to N primary + M secondary adapters,
//     verifies a sampled subset by re-fetch + sha256==hash, and emits a receipt.
//     Restore fetches each unit by hash with multi-provider failover, verifies,
//     and re-stages it by kind (StoreModuleBytes / FlowStore.Install /
//     sdnstore.Store) behind a fail-closed capability precheck.
//
// # Reuse-first (per the spec)
//
//   - The blob envelope crossing an adapter boundary is an $MBL
//     (ModuleBundleEntry, TRANSPORT role) with the unit's sha256 in the entry —
//     the same per-entry-hashed wrapper appmanifest.ToMBL already uses (mbl.go).
//   - A flow's on-disk triple is wrapped as a three-entry $MBL and content-hashed
//     at backup time (the one non-content-addressed substrate).
//   - The run receipt is an $MBL: one attestation entry per unit (entry_id =
//     content hash) plus one AUXILIARY JSON entry standing in for the $PNM
//     per-landing pointers and $REC run envelope the spec names — those two SDS
//     types are not present in the vendored go lib, so the spec's own sanctioned
//     "$REC-wrapped JSON AUXILIARY $MBL entry" fallback is used and the receipt
//     is stored via sdnstore.StoreManifest(node, "BKR", ...).
//   - Provider credentials are fetched only through the capability-gated secrets
//     lane (SecretsGetter), never carried in config or on the wire.
//
// No new SDS record type is minted.
package sdnbackup
