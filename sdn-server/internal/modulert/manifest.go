// Package modulert provides a generic module-sdk runtime that loads any
// space-data-module-sdk WASM binary, reads its FlatBuffer manifest, and
// provisions declared capabilities through the sdn_host hostcall bridge.
//
// This replaces all plugin-type-specific Go wrappers — every module is
// loaded and driven identically based solely on its manifest declarations.
package modulert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Manifest is the parsed module manifest extracted from the embedded FlatBuffer.
type Manifest struct {
	PluginID     string
	Name         string
	Version      string
	PluginFamily string
	Methods      []ManifestMethod
	Capabilities []string
	Protocols    []ProtocolDecl
	Timers       []TimerDecl
}

// ManifestMethod describes a callable method on the module.
type ManifestMethod struct {
	MethodID    string
	DisplayName string
	Description string
}

// ProtocolDecl describes a libp2p protocol the module serves.
type ProtocolDecl struct {
	ProtocolID    string
	MethodID      string
	InputPortID   string
	OutputPortID  string
	Description   string
	WireID        string
	TransportKind string
	Role          string
	AutoInstall   bool
	Advertise     bool
	DiscoveryKey  string
}

// TimerDecl describes a periodic timer the module declares.
type TimerDecl struct {
	TimerID           string
	MethodID          string
	DefaultIntervalMs uint64
	Description       string
}

// CapabilityKind string constants matching PluginManifest.fbs CapabilityKind enum.
var capabilityNames = map[int]string{
	0: "clock", 1: "random", 2: "logging", 3: "timers",
	4: "pubsub", 5: "protocol_dial", 6: "protocol_handle",
	7: "storage_query", 8: "scene_access", 9: "entity_access",
	10: "render_hooks", 11: "http", 12: "filesystem",
	13: "pipe", 14: "network", 15: "database",
	16: "storage_adapter", 17: "storage_write", 18: "wallet_sign",
	19: "ipfs", 20: "tls", 21: "mqtt", 22: "websocket",
	23: "tcp", 24: "udp", 25: "process_exec",
	26: "context_read", 27: "context_write",
	28: "crypto_hash", 29: "crypto_sign", 30: "crypto_verify",
	31: "crypto_encrypt", 32: "crypto_decrypt", 33: "crypto_key_agreement",
	34: "crypto_kdf", 35: "schedule_cron",
}

var pluginFamilyNames = map[int]string{
	0: "SENSOR", 1: "PROPAGATOR", 2: "RENDERER", 3: "ANALYSIS",
	4: "DATA_SOURCE", 5: "COMMS", 6: "SHADER", 7: "SDF",
	8: "INFRASTRUCTURE", 9: "FLOW", 10: "BRIDGE",
}

// ReadManifest reads and parses the FlatBuffer manifest from the WASM module.
func ReadManifest(mod *wasmrt.Module) (*Manifest, error) {
	sizeResults, err := mod.Execute("plugin_get_manifest_flatbuffer_size")
	if err != nil {
		return nil, fmt.Errorf("plugin_get_manifest_flatbuffer_size: %w", err)
	}
	size := wasmrt.ToInt32(sizeResults[0])
	if size <= 0 {
		return nil, errors.New("manifest size is 0")
	}

	ptrResults, err := mod.Execute("plugin_get_manifest_flatbuffer")
	if err != nil {
		return nil, fmt.Errorf("plugin_get_manifest_flatbuffer: %w", err)
	}
	ptr := uint32(wasmrt.ToInt32(ptrResults[0]))
	if ptr == 0 {
		return nil, errors.New("manifest pointer is null")
	}

	buf, err := mod.ReadMemory(ptr, uint32(size))
	if err != nil {
		return nil, fmt.Errorf("read manifest bytes: %w", err)
	}

	return parseManifestFlatBuffer(buf)
}

// parseManifestFlatBuffer parses a PluginManifest FlatBuffer (file_identifier "PMAN").
// This is a manual parser that reads the FlatBuffer wire format directly,
// avoiding a dependency on generated FlatBuffer Go code.
func parseManifestFlatBuffer(buf []byte) (*Manifest, error) {
	if len(buf) < 8 {
		return nil, errors.New("manifest too small")
	}
	// Verify file identifier
	if string(buf[4:8]) != "PMAN" {
		return nil, fmt.Errorf("unexpected file identifier: %q", string(buf[4:8]))
	}

	m := &Manifest{}

	root := readUint32(buf, 0)
	vtOff := root - readUint32(buf, int(root))
	vtLen := readUint16(buf, int(vtOff))
	// tableLen := readUint16(buf, int(vtOff)+2)

	getField := func(idx int) uint32 {
		off := 4 + idx*2
		if off+2 > int(vtLen) {
			return 0
		}
		foff := readUint16(buf, int(vtOff)+off)
		if foff == 0 {
			return 0
		}
		return root + uint32(foff)
	}

	// Field 0: plugin_id (string)
	if off := getField(0); off != 0 {
		m.PluginID = readFBString(buf, off)
	}
	// Field 1: name (string)
	if off := getField(1); off != 0 {
		m.Name = readFBString(buf, off)
	}
	// Field 2: version (string)
	if off := getField(2); off != 0 {
		m.Version = readFBString(buf, off)
	}
	// Field 3: plugin_family (ubyte enum)
	if off := getField(3); off != 0 && int(off) < len(buf) {
		fam := int(buf[off])
		if name, ok := pluginFamilyNames[fam]; ok {
			m.PluginFamily = name
		}
	}
	// Field 4: methods (vector of tables)
	if off := getField(4); off != 0 {
		m.Methods = readMethods(buf, off)
	}
	// Field 5: capabilities (vector of tables)
	if off := getField(5); off != 0 {
		m.Capabilities = readCapabilities(buf, off)
	}
	// Field 6: timers (vector of tables)
	if off := getField(6); off != 0 {
		m.Timers = readTimers(buf, off)
	}
	// Field 7: protocols (vector of tables)
	if off := getField(7); off != 0 {
		m.Protocols = readProtocols(buf, off)
	}

	return m, nil
}

// FlatBuffer wire format helpers

func readUint32(buf []byte, off int) uint32 {
	if off+4 > len(buf) {
		return 0
	}
	return binary.LittleEndian.Uint32(buf[off:])
}

func readUint16(buf []byte, off int) uint16 {
	if off+2 > len(buf) {
		return 0
	}
	return binary.LittleEndian.Uint16(buf[off:])
}

func readFBString(buf []byte, off uint32) string {
	soff := off + readUint32(buf, int(off))
	slen := readUint32(buf, int(soff))
	start := int(soff) + 4
	if start+int(slen) > len(buf) {
		return ""
	}
	return strings.TrimRight(string(buf[start:start+int(slen)]), "\x00")
}

func readVectorLen(buf []byte, off uint32) (uint32, uint32) {
	voff := off + readUint32(buf, int(off))
	vlen := readUint32(buf, int(voff))
	return voff + 4, vlen
}

func readTableField(buf []byte, tableOff uint32, fieldIdx int) uint32 {
	vtOff := tableOff - readUint32(buf, int(tableOff))
	vtLen := readUint16(buf, int(vtOff))
	off := 4 + fieldIdx*2
	if off+2 > int(vtLen) {
		return 0
	}
	foff := readUint16(buf, int(vtOff)+off)
	if foff == 0 {
		return 0
	}
	return tableOff + uint32(foff)
}

func readMethods(buf []byte, off uint32) []ManifestMethod {
	base, count := readVectorLen(buf, off)
	methods := make([]ManifestMethod, 0, count)
	for i := uint32(0); i < count; i++ {
		toff := base + i*4
		tableOff := toff + readUint32(buf, int(toff))
		m := ManifestMethod{}
		if f := readTableField(buf, tableOff, 0); f != 0 {
			m.MethodID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 1); f != 0 {
			m.DisplayName = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 6); f != 0 {
			m.Description = readFBString(buf, f)
		}
		methods = append(methods, m)
	}
	return methods
}

func readCapabilities(buf []byte, off uint32) []string {
	base, count := readVectorLen(buf, off)
	caps := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		toff := base + i*4
		tableOff := toff + readUint32(buf, int(toff))
		// Field 0: capability (ushort enum)
		if f := readTableField(buf, tableOff, 0); f != 0 && int(f)+2 <= len(buf) {
			kind := int(readUint16(buf, int(f)))
			if name, ok := capabilityNames[kind]; ok {
				caps = append(caps, name)
			}
		}
	}
	return caps
}

func readTimers(buf []byte, off uint32) []TimerDecl {
	base, count := readVectorLen(buf, off)
	timers := make([]TimerDecl, 0, count)
	for i := uint32(0); i < count; i++ {
		toff := base + i*4
		tableOff := toff + readUint32(buf, int(toff))
		t := TimerDecl{}
		if f := readTableField(buf, tableOff, 0); f != 0 {
			t.TimerID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 1); f != 0 {
			t.MethodID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 3); f != 0 && int(f)+8 <= len(buf) {
			t.DefaultIntervalMs = binary.LittleEndian.Uint64(buf[f:])
		}
		if f := readTableField(buf, tableOff, 4); f != 0 {
			t.Description = readFBString(buf, f)
		}
		timers = append(timers, t)
	}
	return timers
}

func readProtocols(buf []byte, off uint32) []ProtocolDecl {
	base, count := readVectorLen(buf, off)
	protos := make([]ProtocolDecl, 0, count)
	for i := uint32(0); i < count; i++ {
		toff := base + i*4
		tableOff := toff + readUint32(buf, int(toff))
		p := ProtocolDecl{AutoInstall: true} // default from schema
		if f := readTableField(buf, tableOff, 0); f != 0 {
			p.ProtocolID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 1); f != 0 {
			p.MethodID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 2); f != 0 {
			p.InputPortID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 3); f != 0 {
			p.OutputPortID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 4); f != 0 {
			p.Description = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 5); f != 0 {
			p.WireID = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 6); f != 0 {
			p.TransportKind = readFBString(buf, f)
		}
		if f := readTableField(buf, tableOff, 7); f != 0 {
			p.Role = readFBString(buf, f)
		}
		// Field 9: auto_install (bool, default true)
		if f := readTableField(buf, tableOff, 9); f != 0 && int(f) < len(buf) {
			p.AutoInstall = buf[f] != 0
		}
		// Field 10: advertise (bool, default false)
		if f := readTableField(buf, tableOff, 10); f != 0 && int(f) < len(buf) {
			p.Advertise = buf[f] != 0
		}
		// Field 11: discovery_key (string)
		if f := readTableField(buf, tableOff, 11); f != 0 {
			p.DiscoveryKey = readFBString(buf, f)
		}
		protos = append(protos, p)
	}
	return protos
}
