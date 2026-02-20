var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class ThirdPartyServerPluginGrant {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsThirdPartyServerPluginGrant(bb, obj) {
    return (obj || new ThirdPartyServerPluginGrant()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsThirdPartyServerPluginGrant(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new ThirdPartyServerPluginGrant()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("TPSG");
  }
  status() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 0;
  }
  grantId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  issuedAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  expiresAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  allowedAccounts(index, optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__string(this.bb.__vector(this.bb_pos + offset) + index * 4, optionalEncoding) : null;
  }
  allowedAccountsLength() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  policyHash(index) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  policyHashLength() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  policyHashArray() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startThirdPartyServerPluginGrant(builder) {
    builder.startObject(6);
  }
  static addStatus(builder, status) {
    builder.addFieldInt32(0, status, 0);
  }
  static addGrantId(builder, grantIdOffset) {
    builder.addFieldOffset(1, grantIdOffset, 0);
  }
  static addIssuedAtMs(builder, issuedAtMs) {
    builder.addFieldInt64(2, issuedAtMs, BigInt("0"));
  }
  static addExpiresAtMs(builder, expiresAtMs) {
    builder.addFieldInt64(3, expiresAtMs, BigInt("0"));
  }
  static addAllowedAccounts(builder, allowedAccountsOffset) {
    builder.addFieldOffset(4, allowedAccountsOffset, 0);
  }
  static createAllowedAccountsVector(builder, data) {
    builder.startVector(4, data.length, 4);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addOffset(data[i]);
    }
    return builder.endVector();
  }
  static startAllowedAccountsVector(builder, numElems) {
    builder.startVector(4, numElems, 4);
  }
  static addPolicyHash(builder, policyHashOffset) {
    builder.addFieldOffset(5, policyHashOffset, 0);
  }
  static createPolicyHashVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startPolicyHashVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endThirdPartyServerPluginGrant(builder) {
    const offset = builder.endObject();
    return offset;
  }
  static finishThirdPartyServerPluginGrantBuffer(builder, offset) {
    builder.finish(offset, "TPSG");
  }
  static finishSizePrefixedThirdPartyServerPluginGrantBuffer(builder, offset) {
    builder.finish(offset, "TPSG", true);
  }
  static createThirdPartyServerPluginGrant(builder, status, grantIdOffset, issuedAtMs, expiresAtMs, allowedAccountsOffset, policyHashOffset) {
    ThirdPartyServerPluginGrant.startThirdPartyServerPluginGrant(builder);
    ThirdPartyServerPluginGrant.addStatus(builder, status);
    ThirdPartyServerPluginGrant.addGrantId(builder, grantIdOffset);
    ThirdPartyServerPluginGrant.addIssuedAtMs(builder, issuedAtMs);
    ThirdPartyServerPluginGrant.addExpiresAtMs(builder, expiresAtMs);
    ThirdPartyServerPluginGrant.addAllowedAccounts(builder, allowedAccountsOffset);
    ThirdPartyServerPluginGrant.addPolicyHash(builder, policyHashOffset);
    return ThirdPartyServerPluginGrant.endThirdPartyServerPluginGrant(builder);
  }
}
export {
  ThirdPartyServerPluginGrant
};
