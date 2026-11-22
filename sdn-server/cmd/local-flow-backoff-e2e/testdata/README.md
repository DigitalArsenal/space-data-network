# Catalog-builder guest fixture

`catalog-builder-flow.wasm` is the unchanged committed
`flows/catalog-builder/dist/flow/runtime/runtime.wasm` from the modules
repository (DigitalArsenal/space-data-network-modules) at
`05c3df5c7aa1efed2ffe9a2afb4d076e6fade28a` (Git blob
`81b3d7d3ee21be5e1a9e07f9c2bc92b891e9352c`, 4,293,811 bytes, SHA-256
`cb4e7b15ea893883ad6112acd2a874dc5ab52bc6c5639a50e7479c8248cf2364`).
`catalog-builder-flow.json` is the unmodified
`flows/catalog-builder/dist/flow/runtime/flow.json` from the same revision
(Git blob `03dc2461cd2d2c77fd869b75e807ed3de37d0ae7`).

That revision is the one the catalog builder's eight partition flows run on
the builder node. Reproduce with `git show <revision>:<path>` in that
repository. The E2E grants the guest only clock, storage_query and
storage_ingest over an empty FlatSQL store in a temporary folder; it reads no
catalog data.
