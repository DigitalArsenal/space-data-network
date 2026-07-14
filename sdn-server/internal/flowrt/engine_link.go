package flowrt

// Direct engine linkage for flow mounts (loop C.7, the B-iv end state of
// docs/flatsql-component-linkage.md): a flow compiled with engineLinkage
// "flatsql" imports the live store engine's function exports (module
// "flatsql") plus the deterministic memory-crossing shim (module
// "flatsql_link", embedded below — byte-identical to the SDK's
// FLATSQL_LINK_SHIM_WASM, pinned by sha256 in engine_link_test.go). Query
// submission then happens entirely in-wasm; the host's remaining duties are
// mechanical:
//
//   - register the live engine instance + shim into the flow's VM before
//     instantiation (wasmrt.WithLinkedModuleFrom / WithNamedWasm),
//   - wire the store db handle in (sdn_flatsql_link_init),
//   - hold the store engine lock for the duration of every linked drain
//     (SQLITE_THREADSAFE=0) and harvest engine body-references inside the
//     same critical section (flatsqlrt.Database.WithLinkedDrain),
//   - re-instantiate dependent flow instances when the store replaces a
//     poisoned engine (EngineLinkProvider.EngineEpoch moves).
//
// SECURITY: linking grants the artifact full read/write of the engine's
// linear memory — ALL store data. Mounts therefore hard-require an explicit
// EngineLink from the host (first-party, admin-configured flows only);
// untrusted modules never link and stay on the storage.flatsql_* hostcall
// bridge permanently.

import (
	_ "embed"
	"encoding/binary"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// EngineLinkCapability is the manifest capability the flow compiler stamps
// onto engine-linked artifacts. It is satisfied by the MOUNT's engine link
// (not by a hostcall handler), so capability provisioning excludes it.
const EngineLinkCapability = "storage_engine_link"

// LinkShimModuleName is the wasm import-module name of the memory-crossing
// shim.
const LinkShimModuleName = "flatsql_link"

// linkShimAOTCachePrefix names the shim's AOT artifacts in the shared cache.
// The mount-time compile (httpmount.go) and PrewarmLinkShimAOT MUST agree on
// it or a prewarmed shim won't be found.
const linkShimAOTCachePrefix = "flatsqllink"

// flatsqlLinkShimWasm is the deterministic flatsql_link shim (assembled by
// the SDK's src/flow/flatsqlLinkShim.js; also shipped in linked flow bundles
// as flatsql-link-shim.wasm). Its ONLY import is flatsql.memory — pure code,
// no data, no globals: position independent against any live engine.
//
//go:embed flatsql-link-shim.wasm
var flatsqlLinkShimWasm []byte

// PrewarmLinkShimAOT AOT-compiles the embedded flatsql_link shim into cacheDir
// under the same prefix engine-linked flow HTTP/cron mounts load (see
// httpmount.go). See flatsqlrt.PrewarmAOTArtifact. The shim is only loaded AOT
// for engine-linked flows on runtimes with the linked-AOT fix; prewarming it
// keeps a first-mount compile off the request path.
func PrewarmLinkShimAOT(cacheDir string) (path string, alreadyPresent bool, err error) {
	return flatsqlrt.PrewarmAOTArtifact(cacheDir, linkShimAOTCachePrefix, flatsqlLinkShimWasm)
}

// EngineLinkProvider is what a mount needs from the store to serve linked
// artifacts. *storage.FlatSQLStore implements it.
type EngineLinkProvider interface {
	// EngineRuntime returns the live engine runtime + database.
	EngineRuntime() (*flatsqlrt.Runtime, *flatsqlrt.Database)
	// EngineEpoch is bumped whenever the store replaces its engine; linked
	// instances built against an older epoch must be re-instantiated.
	EngineEpoch() uint64
	// RecoverPoisonedEngine replaces the engine if it is poisoned
	// (idempotent, cheap when healthy). Returns the current epoch.
	RecoverPoisonedEngine() (uint64, error)
}

// engineBodyRefTokenMagic marks engine body-ref tokens minted by linked
// artifacts ("SDNE" in the high 32 bits); hostcall-bridge tokens are small
// counters, so the namespaces never collide.
const engineBodyRefTokenMagic = uint64(0x53444E45) << 32

// engineRefEntrySize mirrors the flow runtime template's SdnEngineRefEntry
// (40 bytes little-endian).
const engineRefEntrySize = 40

// engineRefEntry is one decoded row of the flow's exported engine
// body-reference table.
type engineRefEntry struct {
	Token      uint64
	Generation uint64
	FNV1a64    uint64
	EnginePtr  uint32
	Size       uint32
	Frames     uint32
	Used       bool
}

func decodeEngineRefEntry(b []byte) engineRefEntry {
	return engineRefEntry{
		Token:      binary.LittleEndian.Uint64(b[0:]),
		Generation: binary.LittleEndian.Uint64(b[8:]),
		FNV1a64:    binary.LittleEndian.Uint64(b[16:]),
		EnginePtr:  binary.LittleEndian.Uint32(b[24:]),
		Size:       binary.LittleEndian.Uint32(b[28:]),
		Frames:     binary.LittleEndian.Uint32(b[32:]),
		Used:       binary.LittleEndian.Uint32(b[36:]) != 0,
	}
}

// wasmImportsModule reports whether portable wasm bytes import anything from
// the given module name — the mechanical linked-artifact detection (an
// artifact that imports "flatsql" NEEDS the live engine instance to
// instantiate at all). Parses only the import section; malformed inputs
// return false (instantiation will fail with a real error later).
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
