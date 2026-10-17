# Retrieval guest fixture

`retrieval-flow.wasm` is the unchanged committed
`flows/celestrak-ingest/dist/gp/runtime.wasm` from the public modules repository
at `7d59ae2171b5b57fd3b3587ff012de75b8aabb0c` (Git blob
`bb86ee5ac9c56f3ad011bd6ca7b61fca46668828`, 227,784 bytes). `flow.json` is the
unmodified topology from the same directory and revision (Git blob
`d92ae7652828aeaf57611b79366472f0deb5a858`).

Reproduce with `git show <revision>:flows/celestrak-ingest/dist/gp/runtime.wasm`
in that repository. The E2E gives the guest a loopback URL and grants only HTTP
and a storage trap. Stalls and HTTP 304 exercise its real timer, HTTP hostcall,
error frame and unchanged frame without retrieving public data or ingesting.
