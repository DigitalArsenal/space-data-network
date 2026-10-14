# typed-hostcall-guest

`guest.wasm` and `guest-no-query.wasm` are modules built by the space-data-module-sdk compiler from `guest.cpp`. They
call the node through `typedHostcall.hpp`: SDS `$PIV` hostcalls carrying HostOps aligned records plus raw and list
frames (typed contracts 1.0). They read their config through `sdm_piv::get_config()`, which reads the ModuleConfig
FlatBuffer on raw `config`. `../../typed_hostcall_e2e_test.go` runs them under the node's WasmEdge runtime.
`guest-no-query.wasm` is the same code without the `storage_query` capability, so `t_denied` is refused.

Rebuild after changing `guest.cpp` or after the SDK's `HostOps.fbs` / `typedHostcall.hpp` change. The current build is
from module-sdk 0.9.2 (`plugin.getConfig` answers one ModuleConfig on raw `config`; read through `sdm_piv::get_config()`),
which follows 0.9.1 (tagged SQL values on raw `params` / `cells`, `provenance_attributes`, wider identity fields):

```sh
node build.mjs <space-data-module-sdk checkout>
```

The SDK's `node_modules` must be installed, because the compiler runs emception from there.
