// Package modulert provides a generic module-sdk runtime that loads any
// space-data-module-sdk WASM binary, reads its FlatBuffer manifest, and
// provisions declared capabilities through the SDK hostcall bridge.
//
// This replaces all plugin-type-specific Go wrappers — every module is
// loaded and driven identically based solely on its manifest declarations.
package modulert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
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
	InputPorts  []ManifestPort
	OutputPorts []ManifestPort
	MaxBatch    uint32
	DrainPolicy string
}

// ManifestPort describes one input or output stream port for a method.
type ManifestPort struct {
	PortID           string
	DisplayName      string
	AcceptedTypeSets []ManifestAcceptedTypeSet
	MinStreams       uint16
	MaxStreams       uint16
	Required         bool
	Description      string
}

// ManifestAcceptedTypeSet describes accepted schemas and wire formats for a port.
type ManifestAcceptedTypeSet struct {
	SetID              string
	AllowedTypes       []ManifestFlatBufferTypeRef
	AllowedWireFormats []string
	Description        string
}

// ManifestFlatBufferTypeRef identifies an accepted FlatBuffer payload type.
type ManifestFlatBufferTypeRef struct {
	SchemaName     string
	FileIdentifier string
	SchemaVersion  string
	RootType       string
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

// parseManifestFlatBuffer parses either the current SDS PLG FlatBuffer
// (file_identifier "$PLG") or the older internal PluginManifest FlatBuffer
// (file_identifier "PMAN").
func parseManifestFlatBuffer(buf []byte) (*Manifest, error) {
	if len(buf) < 8 {
		return nil, errors.New("manifest too small")
	}
	switch identifier := string(buf[4:8]); identifier {
	case "$PLG":
		return parsePLGManifestFlatBuffer(buf)
	case "PMAN":
	default:
		return nil, fmt.Errorf("unexpected file identifier: %q", identifier)
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

func parsePLGManifestFlatBuffer(buf []byte) (*Manifest, error) {
	if !plg.PLGBufferHasIdentifier(buf) {
		return nil, fmt.Errorf("unexpected file identifier: %q", string(buf[4:8]))
	}

	root := plg.GetRootAsPLG(buf, 0)
	m := &Manifest{
		PluginID:     string(root.PLUGIN_ID()),
		Name:         string(root.NAME()),
		Version:      string(root.VERSION()),
		PluginFamily: pluginFamilyFromPLGType(fmt.Sprint(root.PLUGIN_TYPE())),
	}

	if root.METHODSLength() > 0 {
		m.Methods = readPLGMethodManifests(root)
	} else {
		var entry plg.EntryFunction
		for i := 0; i < root.ENTRY_FUNCTIONSLength(); i++ {
			if !root.ENTRY_FUNCTIONS(&entry, i) {
				continue
			}
			methodID := strings.TrimSpace(string(entry.NAME()))
			if methodID == "" {
				continue
			}
			m.Methods = append(m.Methods, ManifestMethod{
				MethodID:    methodID,
				DisplayName: methodID,
				Description: string(entry.DESCRIPTION()),
				InputPorts:  entryInputPorts(&entry),
				OutputPorts: entryOutputPorts(&entry),
				MaxBatch:    1,
			})
		}
	}

	var capability plg.PluginCapability
	seenCapabilities := map[string]bool{}
	for i := 0; i < root.CAPABILITIESLength(); i++ {
		if !root.CAPABILITIES(&capability, i) {
			continue
		}
		name := strings.TrimSpace(string(capability.NAME()))
		if name == "" || seenCapabilities[name] {
			continue
		}
		seenCapabilities[name] = true
		m.Capabilities = append(m.Capabilities, name)
	}

	attachKnownPLGProtocols(m)
	return m, nil
}

func readPLGMethodManifests(root *plg.PLG) []ManifestMethod {
	methods := make([]ManifestMethod, 0, root.METHODSLength())
	var method plg.PLGMethodManifest
	for i := 0; i < root.METHODSLength(); i++ {
		if !root.METHODS(&method, i) {
			continue
		}
		methodID := strings.TrimSpace(string(method.METHOD_ID()))
		if methodID == "" {
			continue
		}
		displayName := strings.TrimSpace(string(method.DISPLAY_NAME()))
		if displayName == "" {
			displayName = methodID
		}
		methods = append(methods, ManifestMethod{
			MethodID:    methodID,
			DisplayName: displayName,
			Description: string(method.DESCRIPTION()),
			InputPorts:  readPLGPorts(&method, true),
			OutputPorts: readPLGPorts(&method, false),
			MaxBatch:    method.MAX_BATCH(),
			DrainPolicy: fmt.Sprint(method.DRAIN_POLICY()),
		})
	}
	return methods
}

func readPLGPorts(method *plg.PLGMethodManifest, input bool) []ManifestPort {
	count := method.OUTPUT_PORTSLength()
	if input {
		count = method.INPUT_PORTSLength()
	}
	ports := make([]ManifestPort, 0, count)
	var port plg.PLGPortManifest
	for i := 0; i < count; i++ {
		ok := method.OUTPUT_PORTS(&port, i)
		if input {
			ok = method.INPUT_PORTS(&port, i)
		}
		if !ok {
			continue
		}
		portID := strings.TrimSpace(string(port.PORT_ID()))
		if portID == "" {
			continue
		}
		ports = append(ports, ManifestPort{
			PortID:           portID,
			DisplayName:      strings.TrimSpace(string(port.DISPLAY_NAME())),
			AcceptedTypeSets: readPLGAcceptedTypeSets(&port),
			MinStreams:       port.MIN_STREAMS(),
			MaxStreams:       port.MAX_STREAMS(),
			Required:         port.REQUIRED(),
			Description:      string(port.DESCRIPTION()),
		})
	}
	return ports
}

func readPLGAcceptedTypeSets(port *plg.PLGPortManifest) []ManifestAcceptedTypeSet {
	sets := make([]ManifestAcceptedTypeSet, 0, port.ACCEPTED_TYPE_SETSLength())
	var accepted plg.PLGAcceptedTypeSet
	for i := 0; i < port.ACCEPTED_TYPE_SETSLength(); i++ {
		if !port.ACCEPTED_TYPE_SETS(&accepted, i) {
			continue
		}
		sets = append(sets, ManifestAcceptedTypeSet{
			SetID:              strings.TrimSpace(string(accepted.SET_ID())),
			AllowedTypes:       readPLGAllowedTypes(&accepted),
			AllowedWireFormats: readPLGAllowedWireFormats(&accepted),
			Description:        string(accepted.DESCRIPTION()),
		})
	}
	return sets
}

func readPLGAllowedTypes(accepted *plg.PLGAcceptedTypeSet) []ManifestFlatBufferTypeRef {
	types := make([]ManifestFlatBufferTypeRef, 0, accepted.ALLOWED_TYPESLength())
	var typeRef plg.FlatBufferTypeRef
	for i := 0; i < accepted.ALLOWED_TYPESLength(); i++ {
		if !accepted.ALLOWED_TYPES(&typeRef, i) {
			continue
		}
		types = append(types, ManifestFlatBufferTypeRef{
			SchemaName:     strings.TrimSpace(string(typeRef.SCHEMA_NAME())),
			FileIdentifier: strings.TrimSpace(string(typeRef.FILE_IDENTIFIER())),
			SchemaVersion:  strings.TrimSpace(string(typeRef.SCHEMA_VERSION())),
			RootType:       strings.TrimSpace(string(typeRef.ROOT_TYPE())),
		})
	}
	return types
}

func readPLGAllowedWireFormats(accepted *plg.PLGAcceptedTypeSet) []string {
	formats := make([]string, 0, accepted.ALLOWED_WIRE_FORMATSLength())
	for i := 0; i < accepted.ALLOWED_WIRE_FORMATSLength(); i++ {
		formats = append(formats, fmt.Sprint(accepted.ALLOWED_WIRE_FORMATS(i)))
	}
	return formats
}

func entryInputPorts(entry *plg.EntryFunction) []ManifestPort {
	if entry.INPUT_SCHEMASLength() == 0 {
		return nil
	}
	sets := make([]ManifestAcceptedTypeSet, 0, entry.INPUT_SCHEMASLength())
	for i := 0; i < entry.INPUT_SCHEMASLength(); i++ {
		schemaName := strings.TrimSpace(string(entry.INPUT_SCHEMAS(i)))
		if schemaName == "" {
			continue
		}
		sets = append(sets, ManifestAcceptedTypeSet{
			SetID:              schemaName,
			AllowedTypes:       []ManifestFlatBufferTypeRef{{SchemaName: schemaName}},
			AllowedWireFormats: []string{"FLATBUFFER"},
		})
	}
	if len(sets) == 0 {
		return nil
	}
	return []ManifestPort{
		{
			PortID:           "request",
			DisplayName:      "Request",
			AcceptedTypeSets: sets,
			MinStreams:       1,
			MaxStreams:       1,
			Required:         true,
		},
	}
}

func entryOutputPorts(entry *plg.EntryFunction) []ManifestPort {
	schemaName := strings.TrimSpace(string(entry.OUTPUT_SCHEMA()))
	if schemaName == "" {
		return nil
	}
	return []ManifestPort{
		{
			PortID:      "response",
			DisplayName: "Response",
			AcceptedTypeSets: []ManifestAcceptedTypeSet{
				{
					SetID:              schemaName,
					AllowedTypes:       []ManifestFlatBufferTypeRef{{SchemaName: schemaName}},
					AllowedWireFormats: []string{"FLATBUFFER"},
				},
			},
			MinStreams: 1,
			MaxStreams: 1,
			Required:   true,
		},
	}
}

func attachKnownPLGProtocols(m *Manifest) {
	if m == nil || m.PluginID != "licensing" {
		return
	}
	if !manifestHasMethod(m, "server_handle_message") || !manifestHasCapability(m, "protocol_handle") {
		return
	}
	m.Protocols = append(m.Protocols, ProtocolDecl{
		ProtocolID:    "module-delivery",
		MethodID:      "server_handle_message",
		InputPortID:   "request",
		OutputPortID:  "response",
		Description:   "Handle the canonical SDS module-delivery licensing flow.",
		WireID:        "/space-data-network/module-delivery/1.0.0",
		TransportKind: "libp2p",
		Role:          "handle",
		AutoInstall:   true,
		Advertise:     false,
		DiscoveryKey:  "module-delivery",
	})
}

func manifestHasMethod(m *Manifest, methodID string) bool {
	for _, method := range m.Methods {
		if method.MethodID == methodID {
			return true
		}
	}
	return false
}

func manifestHasCapability(m *Manifest, capability string) bool {
	for _, candidate := range m.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func pluginFamilyFromPLGType(value string) string {
	switch strings.TrimSpace(value) {
	case "Sensor":
		return "SENSOR"
	case "Propagator":
		return "PROPAGATOR"
	case "Renderer":
		return "RENDERER"
	case "Analysis":
		return "ANALYSIS"
	case "DataSource":
		return "DATA_SOURCE"
	case "EW":
		return "EW"
	case "Comms":
		return "COMMS"
	case "Physics":
		return "PHYSICS"
	case "Shader":
		return "SHADER"
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
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
