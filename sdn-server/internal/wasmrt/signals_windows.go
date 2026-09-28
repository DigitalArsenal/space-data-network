//go:build windows

package wasmrt

// EnsureGoSignalHandling is a no-op on Windows: WasmEdge traps AOT faults
// there with a vectored exception handler, which leaves Go's own handling in
// place (signals.go describes the POSIX problem).
func EnsureGoSignalHandling() error { return nil }
