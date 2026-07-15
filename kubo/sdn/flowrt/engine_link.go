package flowrt

// Engine-link surface for flow mounts — the MINIMAL port of
// sdn-server/internal/flowrt/engine_link.go needed by the kubo timer-served
// flow path (cronmount.go).
//
// WHAT IS PORTED: the mechanical detection of an engine-LINKED artifact
// (wasmImportsModule) plus the EngineLinkCapability constant loadFlowInstance
// excludes from bridge provisioning. This is enough for cronmount to admit
// bridge-mode flows and REJECT linked ones with a clear error.
//
// WHAT IS DEFERRED (and why): the full loop-C.7 direct-linkage machinery — the
// flatsql_link memory-crossing shim, PrewarmLinkShimAOT, EngineLinkProvider,
// the SdnEngineRefEntry body-ref harvest and epoch/poison handling — is NOT
// ported here. Every flow this foundation targets (the CelesTrak ingest flows
// and the supplemental-OMM OD flow) is bridge-mode by design ("new flows
// default to bridge"): they read/write records through the policy-mediated
// storage.ingest_with_source / storage.* cap ops, which land records in
// sdnstore by (source, 3-letter type) — NOT through direct engine linkage. The
// low-level engine-ref harvest core already lives at
// kubo/sdn/flatsqlrt/engine_link.go for when a linked flow is actually needed;
// bringing the flow-level mount machinery over is a separate, larger port.

// EngineLinkCapability is the manifest capability a flow compiler stamps onto
// engine-linked artifacts. It is satisfied by a mount's engine link, not by a
// hostcall handler, so capability provisioning excludes it. Retained here so
// loadFlowInstance can recognize and (for now) reject it cleanly.
const EngineLinkCapability = "storage_engine_link"

// engineImportModule is the wasm import-module name a linked flow artifact
// imports from the live store engine. A bundle importing it NEEDS the engine
// instance to instantiate at all; cronmount rejects such bundles (bridge-mode
// only). Mirrors flatsqlrt.EngineImportModule without importing flatsqlrt.
const engineImportModule = "flatsql"

// wasmImportsModule reports whether portable wasm bytes import anything from
// the given module name — the mechanical linked-artifact detection. Parses
// only the import section; malformed inputs return false (a real
// instantiation error surfaces later). Ported verbatim from
// sdn-server/internal/flowrt/engine_link.go.
func wasmImportsModule(wasm []byte, module string) bool {
	if len(wasm) < 8 || string(wasm[0:4]) != "\x00asm" {
		return false
	}
	offset := 8
	readLEB := func() (uint32, bool) {
		var result uint32
		var shift uint
		for {
			if offset >= len(wasm) || shift > 28 {
				return 0, false
			}
			b := wasm[offset]
			offset++
			result |= uint32(b&0x7f) << shift
			if b&0x80 == 0 {
				return result, true
			}
			shift += 7
		}
	}
	for offset < len(wasm) {
		sectionID := wasm[offset]
		offset++
		size, ok := readLEB()
		if !ok || offset+int(size) > len(wasm) {
			return false
		}
		if sectionID != 2 { // import section
			offset += int(size)
			continue
		}
		end := offset + int(size)
		count, ok := readLEB()
		if !ok {
			return false
		}
		for i := uint32(0); i < count && offset < end; i++ {
			modLen, ok := readLEB()
			if !ok || offset+int(modLen) > end {
				return false
			}
			mod := string(wasm[offset : offset+int(modLen)])
			offset += int(modLen)
			nameLen, ok := readLEB()
			if !ok || offset+int(nameLen) > end {
				return false
			}
			offset += int(nameLen)
			if mod == module {
				return true
			}
			if offset >= end {
				return false
			}
			kind := wasm[offset]
			offset++
			switch kind {
			case 0x00: // func: type index
				if _, ok := readLEB(); !ok {
					return false
				}
			case 0x01: // table: reftype + limits
				offset++ // reftype
				flags, ok := readLEB()
				if !ok {
					return false
				}
				if _, ok := readLEB(); !ok {
					return false
				}
				if flags&1 != 0 {
					if _, ok := readLEB(); !ok {
						return false
					}
				}
			case 0x02: // memory: limits
				flags, ok := readLEB()
				if !ok {
					return false
				}
				if _, ok := readLEB(); !ok {
					return false
				}
				if flags&1 != 0 {
					if _, ok := readLEB(); !ok {
						return false
					}
				}
			case 0x03: // global: valtype + mutability
				offset += 2
			default:
				return false
			}
		}
		return false
	}
	return false
}
