//go:build !linux

package wasmrt

import "sync/atomic"

// osThreadID is a portable fallback for non-Linux dev builds: it hands out a
// unique monotonic id per call. The production runtime and its proof run on
// Linux (WasmEdge under sdn-kubo), where the build-tagged Linux variant returns
// the real kernel thread id (unix.Gettid).
var osThreadIDCounter int64

func osThreadID() int64 { return atomic.AddInt64(&osThreadIDCounter, 1) }
