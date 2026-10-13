# typed-hostcall-guest

`guest.wasm` is a module built by the space-data-module-sdk compiler from `guest.cpp`. It calls the node through
`typedHostcall.hpp`: SDS `$PIV` hostcalls carrying HostOps aligned records plus raw and list frames (typed contracts
1.0). `../../typed_hostcall_e2e_test.go` runs it under the node's WasmEdge runtime.

Rebuild after changing `guest.cpp` or after the SDK's `HostOps.fbs` / `typedHostcall.hpp` change. This needs
module-sdk with `sdk-typed-js-hosts`, whose compiler stages the hostcall headers:

```sh
node build.mjs <space-data-module-sdk checkout>
```

The SDK's `node_modules` must be installed, because the compiler runs emception from there.
