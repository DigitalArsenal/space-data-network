var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class GrantRequest {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsGrantRequest(bb, obj) {
    return (obj || new GrantRequest()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsGrantRequest(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new GrantRequest()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDGR");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  reqId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  moduleId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  moduleVersion(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  moduleVariant(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  requesterPeerId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  requesterXpub(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  requesterDomain(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  requesterSigningPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  requesterSigningPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  requesterSigningPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  requesterEncryptionPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 22);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  requesterEncryptionPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 22);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  requesterEncryptionPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 22);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  requestedTimeoutMs() {
    const offset = this.bb.__offset(this.bb_pos, 24);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  requestedAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 26);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  static startGrantRequest(builder) {
    builder.startObject(12);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addReqId(builder, reqIdOffset) {
    builder.addFieldOffset(1, reqIdOffset, 0);
  }
  static addModuleId(builder, moduleIdOffset) {
    builder.addFieldOffset(2, moduleIdOffset, 0);
  }
  static addModuleVersion(builder, moduleVersionOffset) {
    builder.addFieldOffset(3, moduleVersionOffset, 0);
  }
  static addModuleVariant(builder, moduleVariantOffset) {
    builder.addFieldOffset(4, moduleVariantOffset, 0);
  }
  static addRequesterPeerId(builder, requesterPeerIdOffset) {
    builder.addFieldOffset(5, requesterPeerIdOffset, 0);
  }
  static addRequesterXpub(builder, requesterXpubOffset) {
    builder.addFieldOffset(6, requesterXpubOffset, 0);
  }
  static addRequesterDomain(builder, requesterDomainOffset) {
    builder.addFieldOffset(7, requesterDomainOffset, 0);
  }
  static addRequesterSigningPublicKey(builder, requesterSigningPublicKeyOffset) {
    builder.addFieldOffset(8, requesterSigningPublicKeyOffset, 0);
  }
  static createRequesterSigningPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startRequesterSigningPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addRequesterEncryptionPublicKey(builder, requesterEncryptionPublicKeyOffset) {
    builder.addFieldOffset(9, requesterEncryptionPublicKeyOffset, 0);
  }
  static createRequesterEncryptionPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startRequesterEncryptionPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addRequestedTimeoutMs(builder, requestedTimeoutMs) {
    builder.addFieldInt64(10, requestedTimeoutMs, BigInt("0"));
  }
  static addRequestedAtMs(builder, requestedAtMs) {
    builder.addFieldInt64(11, requestedAtMs, BigInt("0"));
  }
  static endGrantRequest(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 8);
    builder.requiredField(offset, 18);
    builder.requiredField(offset, 20);
    builder.requiredField(offset, 22);
    return offset;
  }
  static finishGrantRequestBuffer(builder, offset) {
    builder.finish(offset, "SDGR");
  }
  static finishSizePrefixedGrantRequestBuffer(builder, offset) {
    builder.finish(offset, "SDGR", true);
  }
  static createGrantRequest(builder, schemaVersion, reqIdOffset, moduleIdOffset, moduleVersionOffset, moduleVariantOffset, requesterPeerIdOffset, requesterXpubOffset, requesterDomainOffset, requesterSigningPublicKeyOffset, requesterEncryptionPublicKeyOffset, requestedTimeoutMs, requestedAtMs) {
    GrantRequest.startGrantRequest(builder);
    GrantRequest.addSchemaVersion(builder, schemaVersion);
    GrantRequest.addReqId(builder, reqIdOffset);
    GrantRequest.addModuleId(builder, moduleIdOffset);
    GrantRequest.addModuleVersion(builder, moduleVersionOffset);
    GrantRequest.addModuleVariant(builder, moduleVariantOffset);
    GrantRequest.addRequesterPeerId(builder, requesterPeerIdOffset);
    GrantRequest.addRequesterXpub(builder, requesterXpubOffset);
    GrantRequest.addRequesterDomain(builder, requesterDomainOffset);
    GrantRequest.addRequesterSigningPublicKey(builder, requesterSigningPublicKeyOffset);
    GrantRequest.addRequesterEncryptionPublicKey(builder, requesterEncryptionPublicKeyOffset);
    GrantRequest.addRequestedTimeoutMs(builder, requestedTimeoutMs);
    GrantRequest.addRequestedAtMs(builder, requestedAtMs);
    return GrantRequest.endGrantRequest(builder);
  }
}
export {
  GrantRequest
};
