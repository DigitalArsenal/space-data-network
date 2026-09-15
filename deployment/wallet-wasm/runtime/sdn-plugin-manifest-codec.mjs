import * as flatbuffers from 'flatbuffers';

function normalizeString(value, fallback = null) {
  if (value === null || value === undefined) {
    return fallback;
  }
  const normalized = String(value).trim();
  return normalized.length > 0 ? normalized : fallback;
}

function normalizeArray(value) {
  return Array.isArray(value) ? value : [];
}

function normalizeBytes(value) {
  if (value instanceof Uint8Array) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  return new Uint8Array();
}

function sortObjectKeys(value) {
  if (Array.isArray(value)) {
    return value.map(sortObjectKeys);
  }
  if (value && typeof value === 'object' && !(value instanceof Uint8Array)) {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, sortObjectKeys(value[key])])
    );
  }
  return value;
}

function encodeProperties(value) {
  const normalized =
    value && typeof value === 'object' && !ArrayBuffer.isView(value)
      ? sortObjectKeys(value)
      : {};
  return JSON.stringify(normalized);
}

function decodeProperties(value) {
  if (!value) {
    return {};
  }
  return JSON.parse(value);
}

function normalizeTypeRef(typeRef = {}) {
  return {
    schemaName: normalizeString(typeRef.schemaName ?? typeRef.schema_name, null),
    fileIdentifier: normalizeString(
      typeRef.fileIdentifier ?? typeRef.file_identifier,
      null
    ),
    schemaHash: Array.from(
      normalizeBytes(typeRef.schemaHash ?? typeRef.schema_hash)
    ),
    acceptsAnyFlatbuffer:
      typeRef.acceptsAnyFlatbuffer ?? typeRef.accepts_any_flatbuffer ?? false,
  };
}

function normalizeAcceptedTypeSet(typeSet = {}) {
  return {
    setId: normalizeString(typeSet.setId ?? typeSet.set_id, ''),
    allowedTypes: normalizeArray(
      typeSet.allowedTypes ?? typeSet.allowed_types
    ).map(normalizeTypeRef),
    description: normalizeString(typeSet.description, null),
  };
}

function normalizePort(port = {}) {
  return {
    portId: normalizeString(port.portId ?? port.port_id, ''),
    displayName: normalizeString(port.displayName ?? port.display_name, null),
    acceptedTypeSets: normalizeArray(
      port.acceptedTypeSets ?? port.accepted_type_sets
    ).map(normalizeAcceptedTypeSet),
    minStreams: Math.max(0, Number(port.minStreams ?? port.min_streams ?? 1)),
    maxStreams: Math.max(0, Number(port.maxStreams ?? port.max_streams ?? 1)),
    required: port.required !== false,
    description: normalizeString(port.description, null),
  };
}

function normalizeMethod(method = {}) {
  return {
    methodId: normalizeString(method.methodId ?? method.method_id, ''),
    displayName: normalizeString(
      method.displayName ?? method.display_name,
      null
    ),
    inputPorts: normalizeArray(method.inputPorts ?? method.input_ports).map(
      normalizePort
    ),
    outputPorts: normalizeArray(method.outputPorts ?? method.output_ports).map(
      normalizePort
    ),
    maxBatch: Math.max(1, Number(method.maxBatch ?? method.max_batch ?? 1)),
    drainPolicy:
      normalizeString(method.drainPolicy ?? method.drain_policy, null) ??
      'drain-until-yield',
    description: normalizeString(method.description, null),
  };
}

function normalizeCapability(capability) {
  if (typeof capability === 'string') {
    return {
      capabilityId: normalizeString(capability, ''),
      required: true,
      description: null,
    };
  }
  return {
    capabilityId: normalizeString(
      capability?.capabilityId ?? capability?.capability_id,
      ''
    ),
    required: capability?.required !== false,
    description: normalizeString(capability?.description, null),
  };
}

function normalizeExternalInterface(externalInterface = {}) {
  return {
    interfaceId: normalizeString(
      externalInterface.interfaceId ?? externalInterface.interface_id,
      ''
    ),
    kind: normalizeString(externalInterface.kind, null),
    direction: normalizeString(externalInterface.direction, null),
    capability: normalizeString(externalInterface.capability, null),
    resource: normalizeString(externalInterface.resource, null),
    protocolId: normalizeString(
      externalInterface.protocolId ?? externalInterface.protocol_id,
      null
    ),
    topic: normalizeString(externalInterface.topic, null),
    path: normalizeString(externalInterface.path, null),
    required: externalInterface.required !== false,
    acceptedTypes: normalizeArray(
      externalInterface.acceptedTypes ?? externalInterface.accepted_types
    ).map(normalizeTypeRef),
    description: normalizeString(externalInterface.description, null),
    properties:
      externalInterface.properties &&
      typeof externalInterface.properties === 'object'
        ? sortObjectKeys(externalInterface.properties)
        : {},
  };
}

function normalizeBuildArtifact(artifact = {}) {
  return {
    artifactId: normalizeString(artifact.artifactId ?? artifact.artifact_id, ''),
    kind: normalizeString(artifact.kind, null),
    path: normalizeString(artifact.path, ''),
    target: normalizeString(artifact.target, null),
    entrySymbol: normalizeString(
      artifact.entrySymbol ?? artifact.entry_symbol,
      null
    ),
  };
}

export function normalizeSdnPluginManifest(manifest = {}) {
  return {
    pluginId: normalizeString(manifest.pluginId ?? manifest.plugin_id, ''),
    name: normalizeString(manifest.name, null),
    version: normalizeString(manifest.version, null),
    pluginFamily: normalizeString(
      manifest.pluginFamily ?? manifest.plugin_family,
      null
    ),
    description: normalizeString(manifest.description, null),
    methods: normalizeArray(manifest.methods).map(normalizeMethod),
    capabilities: normalizeArray(manifest.capabilities).map(normalizeCapability),
    externalInterfaces: normalizeArray(
      manifest.externalInterfaces ?? manifest.external_interfaces
    ).map(normalizeExternalInterface),
    schemasUsed: normalizeArray(
      manifest.schemasUsed ?? manifest.schemas_used
    ).map(normalizeTypeRef),
    buildArtifacts: normalizeArray(
      manifest.buildArtifacts ?? manifest.build_artifacts
    ).map(normalizeBuildArtifact),
    abiVersion: Math.max(1, Number(manifest.abiVersion ?? manifest.abi_version ?? 1)),
  };
}

function createString(builder, value) {
  return value ? builder.createString(value) : 0;
}

function createOffsetsVector(builder, offsets) {
  builder.startVector(4, offsets.length, 4);
  for (let index = offsets.length - 1; index >= 0; index -= 1) {
    builder.addOffset(offsets[index]);
  }
  return builder.endVector();
}

function createTypeRef(builder, typeRef) {
  const schemaNameOffset = createString(builder, typeRef.schemaName);
  const fileIdentifierOffset = createString(builder, typeRef.fileIdentifier);
  const schemaHashOffset =
    typeRef.schemaHash.length > 0
      ? builder.createByteVector(Uint8Array.from(typeRef.schemaHash))
      : 0;

  builder.startObject(4);
  builder.addFieldOffset(0, schemaNameOffset, 0);
  builder.addFieldOffset(1, fileIdentifierOffset, 0);
  builder.addFieldOffset(2, schemaHashOffset, 0);
  builder.addFieldInt8(3, typeRef.acceptsAnyFlatbuffer ? 1 : 0, 0);
  return builder.endObject();
}

function createAcceptedTypeSet(builder, typeSet) {
  const setIdOffset = createString(builder, typeSet.setId);
  const allowedTypeOffsets = typeSet.allowedTypes.map((value) =>
    createTypeRef(builder, value)
  );
  const allowedTypesOffset =
    allowedTypeOffsets.length > 0
      ? createOffsetsVector(builder, allowedTypeOffsets)
      : 0;
  const descriptionOffset = createString(builder, typeSet.description);

  builder.startObject(3);
  builder.addFieldOffset(0, setIdOffset, 0);
  builder.addFieldOffset(1, allowedTypesOffset, 0);
  builder.addFieldOffset(2, descriptionOffset, 0);
  return builder.endObject();
}

function createPort(builder, port) {
  const portIdOffset = createString(builder, port.portId);
  const displayNameOffset = createString(builder, port.displayName);
  const acceptedTypeSetOffsets = port.acceptedTypeSets.map((value) =>
    createAcceptedTypeSet(builder, value)
  );
  const acceptedTypeSetsOffset =
    acceptedTypeSetOffsets.length > 0
      ? createOffsetsVector(builder, acceptedTypeSetOffsets)
      : 0;
  const descriptionOffset = createString(builder, port.description);

  builder.startObject(7);
  builder.addFieldOffset(0, portIdOffset, 0);
  builder.addFieldOffset(1, displayNameOffset, 0);
  builder.addFieldOffset(2, acceptedTypeSetsOffset, 0);
  builder.addFieldInt16(3, port.minStreams, 1);
  builder.addFieldInt16(4, port.maxStreams, 1);
  builder.addFieldInt8(5, port.required ? 1 : 0, 1);
  builder.addFieldOffset(6, descriptionOffset, 0);
  return builder.endObject();
}

function createMethod(builder, method) {
  const methodIdOffset = createString(builder, method.methodId);
  const displayNameOffset = createString(builder, method.displayName);
  const inputPortOffsets = method.inputPorts.map((value) =>
    createPort(builder, value)
  );
  const inputPortsOffset =
    inputPortOffsets.length > 0 ? createOffsetsVector(builder, inputPortOffsets) : 0;
  const outputPortOffsets = method.outputPorts.map((value) =>
    createPort(builder, value)
  );
  const outputPortsOffset =
    outputPortOffsets.length > 0
      ? createOffsetsVector(builder, outputPortOffsets)
      : 0;
  const drainPolicyOffset = createString(builder, method.drainPolicy);
  const descriptionOffset = createString(builder, method.description);

  builder.startObject(7);
  builder.addFieldOffset(0, methodIdOffset, 0);
  builder.addFieldOffset(1, displayNameOffset, 0);
  builder.addFieldOffset(2, inputPortsOffset, 0);
  builder.addFieldOffset(3, outputPortsOffset, 0);
  builder.addFieldInt32(4, method.maxBatch, 1);
  builder.addFieldOffset(5, drainPolicyOffset, 0);
  builder.addFieldOffset(6, descriptionOffset, 0);
  return builder.endObject();
}

function createCapability(builder, capability) {
  const capabilityIdOffset = createString(builder, capability.capabilityId);
  const descriptionOffset = createString(builder, capability.description);

  builder.startObject(3);
  builder.addFieldOffset(0, capabilityIdOffset, 0);
  builder.addFieldInt8(1, capability.required ? 1 : 0, 1);
  builder.addFieldOffset(2, descriptionOffset, 0);
  return builder.endObject();
}

function createExternalInterface(builder, externalInterface) {
  const interfaceIdOffset = createString(builder, externalInterface.interfaceId);
  const kindOffset = createString(builder, externalInterface.kind);
  const directionOffset = createString(builder, externalInterface.direction);
  const capabilityOffset = createString(builder, externalInterface.capability);
  const resourceOffset = createString(builder, externalInterface.resource);
  const protocolIdOffset = createString(builder, externalInterface.protocolId);
  const topicOffset = createString(builder, externalInterface.topic);
  const pathOffset = createString(builder, externalInterface.path);
  const acceptedTypeOffsets = externalInterface.acceptedTypes.map((value) =>
    createTypeRef(builder, value)
  );
  const acceptedTypesOffset =
    acceptedTypeOffsets.length > 0
      ? createOffsetsVector(builder, acceptedTypeOffsets)
      : 0;
  const descriptionOffset = createString(builder, externalInterface.description);
  const propertiesOffset = createString(
    builder,
    encodeProperties(externalInterface.properties)
  );

  builder.startObject(12);
  builder.addFieldOffset(0, interfaceIdOffset, 0);
  builder.addFieldOffset(1, kindOffset, 0);
  builder.addFieldOffset(2, directionOffset, 0);
  builder.addFieldOffset(3, capabilityOffset, 0);
  builder.addFieldOffset(4, resourceOffset, 0);
  builder.addFieldOffset(5, protocolIdOffset, 0);
  builder.addFieldOffset(6, topicOffset, 0);
  builder.addFieldOffset(7, pathOffset, 0);
  builder.addFieldInt8(8, externalInterface.required ? 1 : 0, 1);
  builder.addFieldOffset(9, acceptedTypesOffset, 0);
  builder.addFieldOffset(10, descriptionOffset, 0);
  builder.addFieldOffset(11, propertiesOffset, 0);
  return builder.endObject();
}

function createBuildArtifact(builder, artifact) {
  const artifactIdOffset = createString(builder, artifact.artifactId);
  const kindOffset = createString(builder, artifact.kind);
  const pathOffset = createString(builder, artifact.path);
  const targetOffset = createString(builder, artifact.target);
  const entrySymbolOffset = createString(builder, artifact.entrySymbol);

  builder.startObject(5);
  builder.addFieldOffset(0, artifactIdOffset, 0);
  builder.addFieldOffset(1, kindOffset, 0);
  builder.addFieldOffset(2, pathOffset, 0);
  builder.addFieldOffset(3, targetOffset, 0);
  builder.addFieldOffset(4, entrySymbolOffset, 0);
  return builder.endObject();
}

export function encodeSdnPluginManifest(manifest) {
  const normalized = normalizeSdnPluginManifest(manifest);
  const builder = new flatbuffers.Builder(2048);

  const pluginIdOffset = createString(builder, normalized.pluginId);
  const nameOffset = createString(builder, normalized.name);
  const versionOffset = createString(builder, normalized.version);
  const pluginFamilyOffset = createString(builder, normalized.pluginFamily);
  const descriptionOffset = createString(builder, normalized.description);
  const methodOffsets = normalized.methods.map((value) => createMethod(builder, value));
  const methodsOffset =
    methodOffsets.length > 0 ? createOffsetsVector(builder, methodOffsets) : 0;
  const capabilityOffsets = normalized.capabilities.map((value) =>
    createCapability(builder, value)
  );
  const capabilitiesOffset =
    capabilityOffsets.length > 0
      ? createOffsetsVector(builder, capabilityOffsets)
      : 0;
  const externalInterfaceOffsets = normalized.externalInterfaces.map((value) =>
    createExternalInterface(builder, value)
  );
  const externalInterfacesOffset =
    externalInterfaceOffsets.length > 0
      ? createOffsetsVector(builder, externalInterfaceOffsets)
      : 0;
  const schemaOffsets = normalized.schemasUsed.map((value) =>
    createTypeRef(builder, value)
  );
  const schemasUsedOffset =
    schemaOffsets.length > 0 ? createOffsetsVector(builder, schemaOffsets) : 0;
  const artifactOffsets = normalized.buildArtifacts.map((value) =>
    createBuildArtifact(builder, value)
  );
  const buildArtifactsOffset =
    artifactOffsets.length > 0
      ? createOffsetsVector(builder, artifactOffsets)
      : 0;

  builder.startObject(11);
  builder.addFieldOffset(0, pluginIdOffset, 0);
  builder.addFieldOffset(1, nameOffset, 0);
  builder.addFieldOffset(2, versionOffset, 0);
  builder.addFieldOffset(3, pluginFamilyOffset, 0);
  builder.addFieldOffset(4, descriptionOffset, 0);
  builder.addFieldOffset(5, methodsOffset, 0);
  builder.addFieldOffset(6, capabilitiesOffset, 0);
  builder.addFieldOffset(7, externalInterfacesOffset, 0);
  builder.addFieldOffset(8, schemasUsedOffset, 0);
  builder.addFieldOffset(9, buildArtifactsOffset, 0);
  builder.addFieldInt32(10, normalized.abiVersion, 1);
  const root = builder.endObject();
  builder.requiredField(root, 4);
  builder.finish(root, 'PMAN');
  return builder.asUint8Array();
}

function toByteBuffer(data) {
  if (data instanceof flatbuffers.ByteBuffer) {
    return data;
  }
  const bytes = normalizeBytes(data);
  if (bytes.length === 0) {
    throw new TypeError(
      'Expected ByteBuffer, Uint8Array, ArrayBufferView, or ArrayBuffer.'
    );
  }
  return new flatbuffers.ByteBuffer(bytes);
}

function fieldOffset(fieldIndex) {
  return 4 + fieldIndex * 2;
}

function readStringField(bb, tablePosition, fieldIndex) {
  const offset = bb.__offset(tablePosition, fieldOffset(fieldIndex));
  return offset ? bb.__string(tablePosition + offset) : null;
}

function readBoolField(bb, tablePosition, fieldIndex, fallback) {
  const offset = bb.__offset(tablePosition, fieldOffset(fieldIndex));
  return offset ? bb.readInt8(tablePosition + offset) !== 0 : fallback;
}

function readUint16Field(bb, tablePosition, fieldIndex, fallback) {
  const offset = bb.__offset(tablePosition, fieldOffset(fieldIndex));
  return offset ? bb.readUint16(tablePosition + offset) : fallback;
}

function readUint32Field(bb, tablePosition, fieldIndex, fallback) {
  const offset = bb.__offset(tablePosition, fieldOffset(fieldIndex));
  return offset ? bb.readUint32(tablePosition + offset) : fallback;
}

function readByteVectorField(bb, tablePosition, fieldIndex) {
  const offset = bb.__offset(tablePosition, fieldOffset(fieldIndex));
  if (!offset) {
    return [];
  }
  const start = bb.__vector(tablePosition + offset);
  const length = bb.__vector_len(tablePosition + offset);
  return Array.from(bb.bytes().slice(start, start + length));
}

function readTableVector(bb, tablePosition, fieldIndex, readItem) {
  const offset = bb.__offset(tablePosition, fieldOffset(fieldIndex));
  if (!offset) {
    return [];
  }
  const start = bb.__vector(tablePosition + offset);
  const length = bb.__vector_len(tablePosition + offset);
  const values = [];
  for (let index = 0; index < length; index += 1) {
    values.push(readItem(bb.__indirect(start + index * 4)));
  }
  return values;
}

export function decodeSdnPluginManifest(data) {
  const bb = toByteBuffer(data);
  if (!bb.__has_identifier('PMAN')) {
    throw new Error('SDN plugin manifest buffer identifier mismatch.');
  }

  const root = bb.readInt32(bb.position()) + bb.position();

  const readTypeRef = (tablePosition) => ({
    schemaName: readStringField(bb, tablePosition, 0),
    fileIdentifier: readStringField(bb, tablePosition, 1),
    schemaHash: readByteVectorField(bb, tablePosition, 2),
    acceptsAnyFlatbuffer: readBoolField(bb, tablePosition, 3, false),
  });

  const readAcceptedTypeSet = (tablePosition) => ({
    setId: readStringField(bb, tablePosition, 0) ?? '',
    allowedTypes: readTableVector(bb, tablePosition, 1, readTypeRef),
    description: readStringField(bb, tablePosition, 2),
  });

  const readPort = (tablePosition) => ({
    portId: readStringField(bb, tablePosition, 0) ?? '',
    displayName: readStringField(bb, tablePosition, 1),
    acceptedTypeSets: readTableVector(bb, tablePosition, 2, readAcceptedTypeSet),
    minStreams: readUint16Field(bb, tablePosition, 3, 1),
    maxStreams: readUint16Field(bb, tablePosition, 4, 1),
    required: readBoolField(bb, tablePosition, 5, true),
    description: readStringField(bb, tablePosition, 6),
  });

  const readMethod = (tablePosition) => ({
    methodId: readStringField(bb, tablePosition, 0) ?? '',
    displayName: readStringField(bb, tablePosition, 1),
    inputPorts: readTableVector(bb, tablePosition, 2, readPort),
    outputPorts: readTableVector(bb, tablePosition, 3, readPort),
    maxBatch: readUint32Field(bb, tablePosition, 4, 1),
    drainPolicy:
      readStringField(bb, tablePosition, 5) ?? 'drain-until-yield',
    description: readStringField(bb, tablePosition, 6),
  });

  const readCapability = (tablePosition) => ({
    capabilityId: readStringField(bb, tablePosition, 0) ?? '',
    required: readBoolField(bb, tablePosition, 1, true),
    description: readStringField(bb, tablePosition, 2),
  });

  const readExternalInterface = (tablePosition) => ({
    interfaceId: readStringField(bb, tablePosition, 0) ?? '',
    kind: readStringField(bb, tablePosition, 1),
    direction: readStringField(bb, tablePosition, 2),
    capability: readStringField(bb, tablePosition, 3),
    resource: readStringField(bb, tablePosition, 4),
    protocolId: readStringField(bb, tablePosition, 5),
    topic: readStringField(bb, tablePosition, 6),
    path: readStringField(bb, tablePosition, 7),
    required: readBoolField(bb, tablePosition, 8, true),
    acceptedTypes: readTableVector(bb, tablePosition, 9, readTypeRef),
    description: readStringField(bb, tablePosition, 10),
    properties: decodeProperties(readStringField(bb, tablePosition, 11)),
  });

  const readBuildArtifact = (tablePosition) => ({
    artifactId: readStringField(bb, tablePosition, 0) ?? '',
    kind: readStringField(bb, tablePosition, 1),
    path: readStringField(bb, tablePosition, 2) ?? '',
    target: readStringField(bb, tablePosition, 3),
    entrySymbol: readStringField(bb, tablePosition, 4),
  });

  return {
    pluginId: readStringField(bb, root, 0) ?? '',
    name: readStringField(bb, root, 1),
    version: readStringField(bb, root, 2),
    pluginFamily: readStringField(bb, root, 3),
    description: readStringField(bb, root, 4),
    methods: readTableVector(bb, root, 5, readMethod),
    capabilities: readTableVector(bb, root, 6, readCapability),
    externalInterfaces: readTableVector(bb, root, 7, readExternalInterface),
    schemasUsed: readTableVector(bb, root, 8, readTypeRef),
    buildArtifacts: readTableVector(bb, root, 9, readBuildArtifact),
    abiVersion: readUint32Field(bb, root, 10, 1),
  };
}
