package flowrt

// Engine-link metadata for generic flow mounts. Import detection selects the
// application-blind linked-store runtime path; it does not inspect flow or
// application identity.

// EngineLinkCapability is the manifest capability a flow compiler stamps onto
// engine-linked artifacts. It is satisfied by a mount's engine link, not by a
// hostcall handler, so capability provisioning excludes it.
const EngineLinkCapability = "storage_engine_link"

// engineImportModule is the wasm import-module name a linked flow artifact
// imports from the linked store engine. A bundle importing it needs the generic
// store linkage to instantiate. Mirrors flatsqlrt.EngineImportModule without
// importing flatsqlrt.
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
