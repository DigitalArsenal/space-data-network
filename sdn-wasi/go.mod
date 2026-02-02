module github.com/spacedatanetwork/sdn-wasi

go 1.24.0

// WASI-compatible module - minimal dependencies
// No CGO, no network-dependent packages

require github.com/tetratelabs/wazero v1.11.0

require golang.org/x/sys v0.38.0 // indirect
