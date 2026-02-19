var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class PublicKeyResponse {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsPublicKeyResponse(bb, obj) {
    return (obj || new PublicKeyResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsPublicKeyResponse(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new PublicKeyResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("OBPK");
  }
  publicKey(index) {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  publicKeyLength() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  publicKeyArray() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startPublicKeyResponse(builder) {
    builder.startObject(1);
  }
  static addPublicKey(builder, publicKeyOffset) {
    builder.addFieldOffset(0, publicKeyOffset, 0);
  }
  static createPublicKeyVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startPublicKeyVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endPublicKeyResponse(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 4);
    return offset;
  }
  static finishPublicKeyResponseBuffer(builder, offset) {
    builder.finish(offset, "OBPK");
  }
  static finishSizePrefixedPublicKeyResponseBuffer(builder, offset) {
    builder.finish(offset, "OBPK", true);
  }
  static createPublicKeyResponse(builder, publicKeyOffset) {
    PublicKeyResponse.startPublicKeyResponse(builder);
    PublicKeyResponse.addPublicKey(builder, publicKeyOffset);
    return PublicKeyResponse.endPublicKeyResponse(builder);
  }
}
export {
  PublicKeyResponse
};
