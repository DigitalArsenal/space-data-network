var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class KeyBrokerResponse {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsKeyBrokerResponse(bb, obj) {
    return (obj || new KeyBrokerResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsKeyBrokerResponse(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new KeyBrokerResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("OBKS");
  }
  status() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 0;
  }
  packet(index) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  packetLength() {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  packetArray() {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  static startKeyBrokerResponse(builder) {
    builder.startObject(2);
  }
  static addStatus(builder, status) {
    builder.addFieldInt32(0, status, 0);
  }
  static addPacket(builder, packetOffset) {
    builder.addFieldOffset(1, packetOffset, 0);
  }
  static createPacketVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startPacketVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static endKeyBrokerResponse(builder) {
    const offset = builder.endObject();
    return offset;
  }
  static finishKeyBrokerResponseBuffer(builder, offset) {
    builder.finish(offset, "OBKS");
  }
  static finishSizePrefixedKeyBrokerResponseBuffer(builder, offset) {
    builder.finish(offset, "OBKS", true);
  }
  static createKeyBrokerResponse(builder, status, packetOffset) {
    KeyBrokerResponse.startKeyBrokerResponse(builder);
    KeyBrokerResponse.addStatus(builder, status);
    KeyBrokerResponse.addPacket(builder, packetOffset);
    return KeyBrokerResponse.endKeyBrokerResponse(builder);
  }
}
export {
  KeyBrokerResponse
};
