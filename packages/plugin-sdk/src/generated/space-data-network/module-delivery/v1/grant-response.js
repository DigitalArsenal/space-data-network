var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
import { BundleDescriptor } from "../../../space-data-network/module-delivery/v1/bundle-descriptor.js";
import { WrappedContentKey } from "../../../space-data-network/module-delivery/v1/wrapped-content-key.js";
class GrantResponse {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsGrantResponse(bb, obj) {
    return (obj || new GrantResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsGrantResponse(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new GrantResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDGS");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  reqId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  entitlementStatus(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  capabilityToken(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  expiresAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  grantedDomain(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  grantedTimeoutMs() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  grantSignature(index) {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  grantSignatureLength() {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  grantSignatureArray() {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  grantVerifierPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  grantVerifierPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  grantVerifierPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  bundleDescriptor(obj) {
    const offset = this.bb.__offset(this.bb_pos, 22);
    return offset ? (obj || new BundleDescriptor()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  wrappedContentKey(obj) {
    const offset = this.bb.__offset(this.bb_pos, 24);
    return offset ? (obj || new WrappedContentKey()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  static startGrantResponse(builder) {
    builder.startObject(11);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addReqId(builder, reqIdOffset) {
    builder.addFieldOffset(1, reqIdOffset, 0);
  }
  static addEntitlementStatus(builder, entitlementStatusOffset) {
    builder.addFieldOffset(2, entitlementStatusOffset, 0);
  }
  static addCapabilityToken(builder, capabilityTokenOffset) {
    builder.addFieldOffset(3, capabilityTokenOffset, 0);
  }
  static addExpiresAtMs(builder, expiresAtMs) {
    builder.addFieldInt64(4, expiresAtMs, BigInt("0"));
  }
  static addGrantedDomain(builder, grantedDomainOffset) {
    builder.addFieldOffset(5, grantedDomainOffset, 0);
  }
  static addGrantedTimeoutMs(builder, grantedTimeoutMs) {
    builder.addFieldInt64(6, grantedTimeoutMs, BigInt("0"));
  }
  static addGrantSignature(builder, grantSignatureOffset) {
    builder.addFieldOffset(7, grantSignatureOffset, 0);
  }
  static createGrantSignatureVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startGrantSignatureVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addGrantVerifierPublicKey(builder, grantVerifierPublicKeyOffset) {
    builder.addFieldOffset(8, grantVerifierPublicKeyOffset, 0);
  }
  static createGrantVerifierPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startGrantVerifierPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addBundleDescriptor(builder, bundleDescriptorOffset) {
    builder.addFieldOffset(9, bundleDescriptorOffset, 0);
  }
  static addWrappedContentKey(builder, wrappedContentKeyOffset) {
    builder.addFieldOffset(10, wrappedContentKeyOffset, 0);
  }
  static endGrantResponse(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 14);
    builder.requiredField(offset, 22);
    builder.requiredField(offset, 24);
    return offset;
  }
  static finishGrantResponseBuffer(builder, offset) {
    builder.finish(offset, "SDGS");
  }
  static finishSizePrefixedGrantResponseBuffer(builder, offset) {
    builder.finish(offset, "SDGS", true);
  }
}
export {
  GrantResponse
};
