package modulert

// scheduledInputFrames adds a type only for a single, complete concrete contract.
// Every other manifest retains InvokeMethod's one untyped request frame.
func scheduledInputFrames(manifest *Manifest, methodID string, payload []byte) ([]InvokeInputFrame, error) {
	legacy := []InvokeInputFrame{{PortID: "request", Payload: payload}}
	if manifest == nil {
		return legacy, nil
	}
	for _, method := range manifest.Methods {
		if method.MethodID != methodID {
			continue
		}
		if len(method.InputPorts) != 1 {
			return legacy, nil
		}
		port := method.InputPorts[0]
		if port.PortID == "" {
			return legacy, nil
		}
		var types []ManifestFlatBufferTypeRef
		for _, set := range port.AcceptedTypeSets {
			types = append(types, set.AllowedTypes...)
		}
		if len(types) != 1 {
			return legacy, nil
		}
		typ := types[0]
		if typ.SchemaName == "" || len(typ.FileIdentifier) != 4 || typ.RootType == "" {
			return legacy, nil
		}
		return []InvokeInputFrame{{PortID: port.PortID, Payload: payload, SchemaName: typ.SchemaName,
			FileIdentifier: typ.FileIdentifier, RootTypeName: typ.RootType, WireFormat: payloadWireFormatFlatbuffer}}, nil
	}
	return legacy, nil
}
