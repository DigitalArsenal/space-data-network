// Package format2 is SDN's side of the FlatSQL partition store (store format
// 2; stack design docs/architecture/flatsql-partition-store.md, task T6).
//
// The engine is the published flatsql-ps-threads.wasm (flatsqlrt/psartifact.go)
// running as separate WasmEdge instances on the T5 substrate
// (flatsqlrt/psinstance.go, PSABIEngine):
//
//   - one WRITER instance: N writer threads own the (producer, SDS type)
//     partitions; Go enqueues ring entries in its shared memory and waits for
//     durable acks (writer.go);
//   - an INTERACTIVE and a BULK reader instance: lane threads answer SQL over
//     committed files only, through a mailbox in their memory (reader.go);
//     results are RB1 row blocks (rb1.go) or raw frame streams;
//   - until T8, the legacy control instance (the existing flatsqlrt engine)
//     holds the control tables.
//
// Go never calls a guest export on a hot path: rings, mailboxes, acks and
// doorbells are words in shared memory. Control calls (init, registration,
// stop) run on the instance under an explicit budget.
//
// Everything here is behind SDN_STORE_FORMAT=2 (store.go). Format 1 stays the
// default; nothing in this package runs for a node that did not select it.
package format2
