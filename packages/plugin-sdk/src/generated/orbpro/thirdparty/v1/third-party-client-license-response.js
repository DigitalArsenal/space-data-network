var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class ThirdPartyClientLicenseResponse {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsThirdPartyClientLicenseResponse(bb, obj) {
    return (obj || new ThirdPartyClientLicenseResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsThirdPartyClientLicenseResponse(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new ThirdPartyClientLicenseResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("TPCS");
  }
  status() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 0;
  }
  keyVersion() {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 0;
  }
  expiresAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  wrappedKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  wrappedKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  wrappedKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  challengeId(index) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  challengeIdLength() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  challengeIdArray() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startThirdPartyClientLicenseResponse(builder) {
    builder.startObject(5);
  }
  static addStatus(builder, status) {
    builder.addFieldInt32(0, status, 0);
  }
  static addKeyVersion(builder, keyVersion) {
    builder.addFieldInt32(1, keyVersion, 0);
  }
  static addExpiresAtMs(builder, expiresAtMs) {
    builder.addFieldInt64(2, expiresAtMs, BigInt("0"));
  }
  static addWrappedKey(builder, wrappedKeyOffset) {
    builder.addFieldOffset(3, wrappedKeyOffset, 0);
  }
  static createWrappedKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startWrappedKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addChallengeId(builder, challengeIdOffset) {
    builder.addFieldOffset(4, challengeIdOffset, 0);
  }
  static createChallengeIdVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startChallengeIdVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endThirdPartyClientLicenseResponse(builder) {
    const offset = builder.endObject();
    return offset;
  }
  static finishThirdPartyClientLicenseResponseBuffer(builder, offset) {
    builder.finish(offset, "TPCS");
  }
  static finishSizePrefixedThirdPartyClientLicenseResponseBuffer(builder, offset) {
    builder.finish(offset, "TPCS", true);
  }
  static createThirdPartyClientLicenseResponse(builder, status, keyVersion, expiresAtMs, wrappedKeyOffset, challengeIdOffset) {
    ThirdPartyClientLicenseResponse.startThirdPartyClientLicenseResponse(builder);
    ThirdPartyClientLicenseResponse.addStatus(builder, status);
    ThirdPartyClientLicenseResponse.addKeyVersion(builder, keyVersion);
    ThirdPartyClientLicenseResponse.addExpiresAtMs(builder, expiresAtMs);
    ThirdPartyClientLicenseResponse.addWrappedKey(builder, wrappedKeyOffset);
    ThirdPartyClientLicenseResponse.addChallengeId(builder, challengeIdOffset);
    return ThirdPartyClientLicenseResponse.endThirdPartyClientLicenseResponse(builder);
  }
}
export {
  ThirdPartyClientLicenseResponse
};
