package flowrt

// wasmthreads.go — a minimal WASM binary scanner that reports whether a composed
// runtime artifact declares the isomorphic wasi-threads contract: a SHARED,
// IMPORTED linear memory + a `wasi.thread-spawn` import + a `wasi_thread_start`
// export, and ZERO Emscripten JS thread hooks. It is the same contract the SDK's
// pthreadArtifactGuard enforces on module artifacts (space-data-module-sdk
// 212e474/e6e7dff) — here it (1) GATES the bake (C1: a wasi-threads flow whose
// linked artifact misses any of these FAILS the bake, never ships un-threadable),
// and (2) GATES the load (NewFlowRuntime enables WithWASIThreads only for an
// artifact that actually declares the contract, so single-thread flows load
// unchanged).
//
// It reads only the import, export, and (imported) memory sections — enough to
// decide, cheaply, without a full decoder.

type wasmThreadFeatures struct {
	SharedImportedMemory bool
	ThreadSpawnImport    bool
	ThreadStartExport    bool
	EmscriptenThreadHook bool
}

// isIsomorphicPthreads reports the full positive contract with no emscripten hooks.
func (f wasmThreadFeatures) isIsomorphicPthreads() bool {
	return f.SharedImportedMemory && f.ThreadSpawnImport && f.ThreadStartExport && !f.EmscriptenThreadHook
}

const (
	wasmSecImport     = 0x02
	wasmSecExport     = 0x07
	wasmExtKindFunc   = 0x00
	wasmExtKindTable  = 0x01
	wasmExtKindMem    = 0x02
	wasmExtKindGlobal = 0x03
)

// scanWasmThreadFeatures parses the import/export sections of a wasm module.
// A malformed or non-wasm input yields the zero value (no features), which the
// callers treat as "not a wasi-threads artifact" (fail-safe).
func scanWasmThreadFeatures(wasm []byte) wasmThreadFeatures {
	var f wasmThreadFeatures
	if len(wasm) < 8 || wasm[0] != 0x00 || wasm[1] != 'a' || wasm[2] != 's' || wasm[3] != 'm' {
		return f
	}
	p := 8
	for p < len(wasm) {
		if p >= len(wasm) {
			break
		}
		secID := wasm[p]
		p++
		size, n := uleb(wasm, p)
		if n <= 0 {
			break
		}
		p += n
		if p+int(size) > len(wasm) {
			break
		}
		body := wasm[p : p+int(size)]
		p += int(size)
		switch secID {
		case wasmSecImport:
			scanImports(body, &f)
		case wasmSecExport:
			scanExports(body, &f)
		}
	}
	return f
}

func scanImports(b []byte, f *wasmThreadFeatures) {
	q := 0
	count, n := uleb(b, q)
	if n <= 0 {
		return
	}
	q += n
	for i := uint64(0); i < count && q < len(b); i++ {
		mod, nq, ok := readName(b, q)
		if !ok {
			return
		}
		q = nq
		name, nq2, ok := readName(b, q)
		if !ok {
			return
		}
		q = nq2
		if q >= len(b) {
			return
		}
		kind := b[q]
		q++
		switch kind {
		case wasmExtKindFunc:
			_, m := uleb(b, q)
			q += m
			if mod == "wasi" && name == "thread-spawn" {
				f.ThreadSpawnImport = true
			}
			// Emscripten browser-only pthread hooks — the SDK guard rejects these.
			if mod == "env" && (name == "__pthread_create_js" || name == "pthread_create" || name == "_emscripten_thread_mailbox_await" || name == "__emscripten_init_main_thread_js") {
				f.EmscriptenThreadHook = true
			}
		case wasmExtKindTable:
			// elemtype(1) + limits
			if q < len(b) {
				q++
			}
			q = skipLimits(b, q)
		case wasmExtKindMem:
			// limits: flags byte; bit0=has-max, bit1=shared
			if q < len(b) {
				flags := b[q]
				q++
				if flags&0x02 != 0 && mod == "env" && name == "memory" {
					f.SharedImportedMemory = true
				}
				_, m := uleb(b, q)
				q += m // min
				if flags&0x01 != 0 {
					_, m2 := uleb(b, q)
					q += m2 // max
				}
			}
		case wasmExtKindGlobal:
			// valtype(1) + mutability(1)
			q += 2
		default:
			return
		}
	}
}

func scanExports(b []byte, f *wasmThreadFeatures) {
	q := 0
	count, n := uleb(b, q)
	if n <= 0 {
		return
	}
	q += n
	for i := uint64(0); i < count && q < len(b); i++ {
		name, nq, ok := readName(b, q)
		if !ok {
			return
		}
		q = nq
		if q >= len(b) {
			return
		}
		q++ // kind
		_, m := uleb(b, q)
		q += m // index
		if name == "wasi_thread_start" {
			f.ThreadStartExport = true
		}
	}
}

func skipLimits(b []byte, q int) int {
	if q >= len(b) {
		return q
	}
	flags := b[q]
	q++
	_, m := uleb(b, q)
	q += m
	if flags&0x01 != 0 {
		_, m2 := uleb(b, q)
		q += m2
	}
	return q
}

func readName(b []byte, q int) (string, int, bool) {
	l, n := uleb(b, q)
	if n <= 0 {
		return "", q, false
	}
	q += n
	if q+int(l) > len(b) {
		return "", q, false
	}
	return string(b[q : q+int(l)]), q + int(l), true
}

// uleb decodes an unsigned LEB128 at b[p], returning the value and bytes read
// (0 on error).
func uleb(b []byte, p int) (uint64, int) {
	var result uint64
	var shift uint
	n := 0
	for {
		if p+n >= len(b) || n >= 10 {
			return 0, 0
		}
		c := b[p+n]
		result |= uint64(c&0x7f) << shift
		n++
		if c&0x80 == 0 {
			break
		}
		shift += 7
	}
	return result, n
}
