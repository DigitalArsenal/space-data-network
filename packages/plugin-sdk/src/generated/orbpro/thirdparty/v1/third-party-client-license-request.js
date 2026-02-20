var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class ThirdPartyClientLicenseRequest {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsThirdPartyClientLicenseRequest(bb, obj) {
    return (obj || new ThirdPartyClientLicenseRequest()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsThirdPartyClientLicenseRequest(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new ThirdPartyClientLicenseRequest()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("TPCR");
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
  accountIdHash(index) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  accountIdHashLength() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  accountIdHashArray() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  requestNonce(index) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  requestNonceLength() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  requestNonceArray() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  ephemeralPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  ephemeralPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  ephemeralPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  challengeToken(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  static startThirdPartyClientLicenseRequest(builder) {
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
  static addAccountIdHash(builder, accountIdHashOffset) {
    builder.addFieldOffset(3, accountIdHashOffset, 0);
  }
  static createAccountIdHashVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startAccountIdHashVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addRequestNonce(builder, requestNonceOffset) {
    builder.addFieldOffset(4, requestNonceOffset, 0);
  }
  static createRequestNonceVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startRequestNonceVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addEphemeralPublicKey(builder, ephemeralPublicKeyOffset) {
    builder.addFieldOffset(5, ephemeralPublicKeyOffset, 0);
  }
  static createEphemeralPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startEphemeralPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addChallengeToken(builder, challengeTokenOffset) {
    builder.addFieldOffset(6, challengeTokenOffset, 0);
  }
  static endThirdPartyClientLicenseRequest(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 8);
    builder.requiredField(offset, 10);
    builder.requiredField(offset, 12);
    builder.requiredField(offset, 14);
    return offset;
  }
  static finishThirdPartyClientLicenseRequestBuffer(builder, offset) {
    builder.finish(offset, "TPCR");
  }
  static finishSizePrefixedThirdPartyClientLicenseRequestBuffer(builder, offset) {
    builder.finish(offset, "TPCR", true);
  }
  static createThirdPartyClientLicenseRequest(builder, schemaVersion, pluginIdOffset, pluginVersionOffset, accountIdHashOffset, requestNonceOffset, ephemeralPublicKeyOffset, challengeTokenOffset) {
    ThirdPartyClientLicenseRequest.startThirdPartyClientLicenseRequest(builder);
    ThirdPartyClientLicenseRequest.addSchemaVersion(builder, schemaVersion);
    ThirdPartyClientLicenseRequest.addPluginId(builder, pluginIdOffset);
    ThirdPartyClientLicenseRequest.addPluginVersion(builder, pluginVersionOffset);
    ThirdPartyClientLicenseRequest.addAccountIdHash(builder, accountIdHashOffset);
    ThirdPartyClientLicenseRequest.addRequestNonce(builder, requestNonceOffset);
    ThirdPartyClientLicenseRequest.addEphemeralPublicKey(builder, ephemeralPublicKeyOffset);
    ThirdPartyClientLicenseRequest.addChallengeToken(builder, challengeTokenOffset);
    return ThirdPartyClientLicenseRequest.endThirdPartyClientLicenseRequest(builder);
  }
}
export {
  ThirdPartyClientLicenseRequest
};
