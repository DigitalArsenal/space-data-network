var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class WrappedContentKey {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsWrappedContentKey(bb, obj) {
    return (obj || new WrappedContentKey()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsWrappedContentKey(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new WrappedContentKey()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDWK");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  wrappingAlgorithm(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  recipientKeyId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  recipientPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  recipientPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  recipientPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  ephemeralPublicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  ephemeralPublicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  ephemeralPublicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  nonce(index) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  nonceLength() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  nonceArray() {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  ciphertext(index) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  ciphertextLength() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  ciphertextArray() {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  tag(index) {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  tagLength() {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  tagArray() {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startWrappedContentKey(builder) {
    builder.startObject(8);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addWrappingAlgorithm(builder, wrappingAlgorithmOffset) {
    builder.addFieldOffset(1, wrappingAlgorithmOffset, 0);
  }
  static addRecipientKeyId(builder, recipientKeyIdOffset) {
    builder.addFieldOffset(2, recipientKeyIdOffset, 0);
  }
  static addRecipientPublicKey(builder, recipientPublicKeyOffset) {
    builder.addFieldOffset(3, recipientPublicKeyOffset, 0);
  }
  static createRecipientPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startRecipientPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addEphemeralPublicKey(builder, ephemeralPublicKeyOffset) {
    builder.addFieldOffset(4, ephemeralPublicKeyOffset, 0);
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
  static addNonce(builder, nonceOffset) {
    builder.addFieldOffset(5, nonceOffset, 0);
  }
  static createNonceVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startNonceVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addCiphertext(builder, ciphertextOffset) {
    builder.addFieldOffset(6, ciphertextOffset, 0);
  }
  static createCiphertextVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startCiphertextVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addTag(builder, tagOffset) {
    builder.addFieldOffset(7, tagOffset, 0);
  }
  static createTagVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startTagVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endWrappedContentKey(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 12);
    builder.requiredField(offset, 14);
    builder.requiredField(offset, 16);
    return offset;
  }
  static finishWrappedContentKeyBuffer(builder, offset) {
    builder.finish(offset, "SDWK");
  }
  static finishSizePrefixedWrappedContentKeyBuffer(builder, offset) {
    builder.finish(offset, "SDWK", true);
  }
  static createWrappedContentKey(builder, schemaVersion, wrappingAlgorithmOffset, recipientKeyIdOffset, recipientPublicKeyOffset, ephemeralPublicKeyOffset, nonceOffset, ciphertextOffset, tagOffset) {
    WrappedContentKey.startWrappedContentKey(builder);
    WrappedContentKey.addSchemaVersion(builder, schemaVersion);
    WrappedContentKey.addWrappingAlgorithm(builder, wrappingAlgorithmOffset);
    WrappedContentKey.addRecipientKeyId(builder, recipientKeyIdOffset);
    WrappedContentKey.addRecipientPublicKey(builder, recipientPublicKeyOffset);
    WrappedContentKey.addEphemeralPublicKey(builder, ephemeralPublicKeyOffset);
    WrappedContentKey.addNonce(builder, nonceOffset);
    WrappedContentKey.addCiphertext(builder, ciphertextOffset);
    WrappedContentKey.addTag(builder, tagOffset);
    return WrappedContentKey.endWrappedContentKey(builder);
  }
}
export {
  WrappedContentKey
};
