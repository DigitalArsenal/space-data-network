var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class ThirdPartyServerPluginRegistration {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsThirdPartyServerPluginRegistration(bb, obj) {
    return (obj || new ThirdPartyServerPluginRegistration()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsThirdPartyServerPluginRegistration(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new ThirdPartyServerPluginRegistration()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("TPSR");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint16(this.bb_pos + offset) : 1;
  }
  pluginId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  pluginVersion(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  vendorId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  signingPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  signingPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  signingPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  capabilities(index, optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__string(this.bb.__vector(this.bb_pos + offset) + index * 4, optionalEncoding) : null;
  }
  capabilitiesLength() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  manifestHash(index) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  manifestHashLength() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  manifestHashArray() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startThirdPartyServerPluginRegistration(builder) {
    builder.startObject(7);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt16(0, schemaVersion, 1);
  }
  static addPluginId(builder, pluginIdOffset) {
    builder.addFieldOffset(1, pluginIdOffset, 0);
  }
  static addPluginVersion(builder, pluginVersionOffset) {
    builder.addFieldOffset(2, pluginVersionOffset, 0);
  }
  static addVendorId(builder, vendorIdOffset) {
    builder.addFieldOffset(3, vendorIdOffset, 0);
  }
  static addSigningPublicKey(builder, signingPublicKeyOffset) {
    builder.addFieldOffset(4, signingPublicKeyOffset, 0);
  }
  static createSigningPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startSigningPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addCapabilities(builder, capabilitiesOffset) {
    builder.addFieldOffset(5, capabilitiesOffset, 0);
  }
  static createCapabilitiesVector(builder, data) {
    builder.startVector(4, data.length, 4);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addOffset(data[i]);
    }
    return builder.endVector();
  }
  static startCapabilitiesVector(builder, numElems) {
    builder.startVector(4, numElems, 4);
  }
  static addManifestHash(builder, manifestHashOffset) {
    builder.addFieldOffset(6, manifestHashOffset, 0);
  }
  static createManifestHashVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startManifestHashVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endThirdPartyServerPluginRegistration(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 8);
    builder.requiredField(offset, 10);
    builder.requiredField(offset, 12);
    builder.requiredField(offset, 16);
    return offset;
  }
  static finishThirdPartyServerPluginRegistrationBuffer(builder, offset) {
    builder.finish(offset, "TPSR");
  }
  static finishSizePrefixedThirdPartyServerPluginRegistrationBuffer(builder, offset) {
    builder.finish(offset, "TPSR", true);
  }
  static createThirdPartyServerPluginRegistration(builder, schemaVersion, pluginIdOffset, pluginVersionOffset, vendorIdOffset, signingPublicKeyOffset, capabilitiesOffset, manifestHashOffset) {
    ThirdPartyServerPluginRegistration.startThirdPartyServerPluginRegistration(builder);
    ThirdPartyServerPluginRegistration.addSchemaVersion(builder, schemaVersion);
    ThirdPartyServerPluginRegistration.addPluginId(builder, pluginIdOffset);
    ThirdPartyServerPluginRegistration.addPluginVersion(builder, pluginVersionOffset);
    ThirdPartyServerPluginRegistration.addVendorId(builder, vendorIdOffset);
    ThirdPartyServerPluginRegistration.addSigningPublicKey(builder, signingPublicKeyOffset);
    ThirdPartyServerPluginRegistration.addCapabilities(builder, capabilitiesOffset);
    ThirdPartyServerPluginRegistration.addManifestHash(builder, manifestHashOffset);
    return ThirdPartyServerPluginRegistration.endThirdPartyServerPluginRegistration(builder);
  }
}
export {
  ThirdPartyServerPluginRegistration
};
