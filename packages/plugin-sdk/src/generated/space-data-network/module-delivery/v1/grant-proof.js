var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class GrantProof {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsGrantProof(bb, obj) {
    return (obj || new GrantProof()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsGrantProof(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new GrantProof()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDGP");
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
  requesterPeerId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  requesterSigningPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  requesterSigningPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  requesterSigningPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  requesterEncryptionPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  requesterEncryptionPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  requesterEncryptionPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  challenge(index) {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  challengeLength() {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  challengeArray() {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  signature(index) {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  signatureLength() {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  signatureArray() {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  provedAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 22);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  static startGrantProof(builder) {
    builder.startObject(10);
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
  static addRequesterPeerId(builder, requesterPeerIdOffset) {
    builder.addFieldOffset(4, requesterPeerIdOffset, 0);
  }
  static addRequesterSigningPublicKey(builder, requesterSigningPublicKeyOffset) {
    builder.addFieldOffset(5, requesterSigningPublicKeyOffset, 0);
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
    builder.addFieldOffset(6, requesterEncryptionPublicKeyOffset, 0);
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
  static addChallenge(builder, challengeOffset) {
    builder.addFieldOffset(7, challengeOffset, 0);
  }
  static createChallengeVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startChallengeVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addSignature(builder, signatureOffset) {
    builder.addFieldOffset(8, signatureOffset, 0);
  }
  static createSignatureVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startSignatureVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addProvedAtMs(builder, provedAtMs) {
    builder.addFieldInt64(9, provedAtMs, BigInt("0"));
  }
  static endGrantProof(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 14);
    builder.requiredField(offset, 16);
    builder.requiredField(offset, 18);
    builder.requiredField(offset, 20);
    return offset;
  }
  static finishGrantProofBuffer(builder, offset) {
    builder.finish(offset, "SDGP");
  }
  static finishSizePrefixedGrantProofBuffer(builder, offset) {
    builder.finish(offset, "SDGP", true);
  }
  static createGrantProof(builder, schemaVersion, reqIdOffset, moduleIdOffset, moduleVersionOffset, requesterPeerIdOffset, requesterSigningPublicKeyOffset, requesterEncryptionPublicKeyOffset, challengeOffset, signatureOffset, provedAtMs) {
    GrantProof.startGrantProof(builder);
    GrantProof.addSchemaVersion(builder, schemaVersion);
    GrantProof.addReqId(builder, reqIdOffset);
    GrantProof.addModuleId(builder, moduleIdOffset);
    GrantProof.addModuleVersion(builder, moduleVersionOffset);
    GrantProof.addRequesterPeerId(builder, requesterPeerIdOffset);
    GrantProof.addRequesterSigningPublicKey(builder, requesterSigningPublicKeyOffset);
    GrantProof.addRequesterEncryptionPublicKey(builder, requesterEncryptionPublicKeyOffset);
    GrantProof.addChallenge(builder, challengeOffset);
    GrantProof.addSignature(builder, signatureOffset);
    GrantProof.addProvedAtMs(builder, provedAtMs);
    return GrantProof.endGrantProof(builder);
  }
}
export {
  GrantProof
};
