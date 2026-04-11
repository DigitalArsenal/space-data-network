var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class GrantChallenge {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsGrantChallenge(bb, obj) {
    return (obj || new GrantChallenge()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsGrantChallenge(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new GrantChallenge()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDGC");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  reqId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  challenge(index) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  challengeLength() {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  challengeArray() {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  expiresAtMs() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  providerPeerId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  providerPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  providerPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  providerPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startGrantChallenge(builder) {
    builder.startObject(6);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addReqId(builder, reqIdOffset) {
    builder.addFieldOffset(1, reqIdOffset, 0);
  }
  static addChallenge(builder, challengeOffset) {
    builder.addFieldOffset(2, challengeOffset, 0);
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
  static addExpiresAtMs(builder, expiresAtMs) {
    builder.addFieldInt64(3, expiresAtMs, BigInt("0"));
  }
  static addProviderPeerId(builder, providerPeerIdOffset) {
    builder.addFieldOffset(4, providerPeerIdOffset, 0);
  }
  static addProviderPublicKey(builder, providerPublicKeyOffset) {
    builder.addFieldOffset(5, providerPublicKeyOffset, 0);
  }
  static createProviderPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startProviderPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endGrantChallenge(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 8);
    builder.requiredField(offset, 14);
    return offset;
  }
  static finishGrantChallengeBuffer(builder, offset) {
    builder.finish(offset, "SDGC");
  }
  static finishSizePrefixedGrantChallengeBuffer(builder, offset) {
    builder.finish(offset, "SDGC", true);
  }
  static createGrantChallenge(builder, schemaVersion, reqIdOffset, challengeOffset, expiresAtMs, providerPeerIdOffset, providerPublicKeyOffset) {
    GrantChallenge.startGrantChallenge(builder);
    GrantChallenge.addSchemaVersion(builder, schemaVersion);
    GrantChallenge.addReqId(builder, reqIdOffset);
    GrantChallenge.addChallenge(builder, challengeOffset);
    GrantChallenge.addExpiresAtMs(builder, expiresAtMs);
    GrantChallenge.addProviderPeerId(builder, providerPeerIdOffset);
    GrantChallenge.addProviderPublicKey(builder, providerPublicKeyOffset);
    return GrantChallenge.endGrantChallenge(builder);
  }
}
export {
  GrantChallenge
};
