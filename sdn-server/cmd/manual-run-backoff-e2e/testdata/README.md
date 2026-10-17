# Retention guest fixture

`retention-flow.wasm` is the unchanged committed
`flows/record-retention/dist/runtime.wasm` from the public modules repository
at `38ddecf0796952ab73012cde7673f1d5c84dc047` (Git blob
`f951172c80a0873f45e17bfeb59256d1e2a9b97f`, 102,816 bytes). `retention-flow.json`
is the unmodified `flows/record-retention/dist/flow.json` from the same revision
(Git blob `13a8e377908164d7edf583d5cb2a7ac4aadc69d3`).

Reproduce with `git show <revision>:<path>` in that repository. The guest
declares only `storage_write`; the E2E answers its `storage.expire` hostcall
from a stub and stores nothing. The CelesTrak GP guest is read from
`cmd/cron-robustness-e2e/testdata`.
