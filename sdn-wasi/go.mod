module github.com/spacedatanetwork/sdn-wasi

go 1.22

// WASI-compatible module - minimal dependencies
// No CGO, no network-dependent packages

require github.com/tetratelabs/wazero v1.7.0
