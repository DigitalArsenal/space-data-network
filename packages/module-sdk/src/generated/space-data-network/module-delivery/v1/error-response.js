var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class ErrorResponse {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsErrorResponse(bb, obj) {
    return (obj || new ErrorResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsErrorResponse(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new ErrorResponse()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDER");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  reqId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  code(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  message(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  retryable() {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? !!this.bb.readInt8(this.bb_pos + offset) : false;
  }
  static startErrorResponse(builder) {
    builder.startObject(5);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addReqId(builder, reqIdOffset) {
    builder.addFieldOffset(1, reqIdOffset, 0);
  }
  static addCode(builder, codeOffset) {
    builder.addFieldOffset(2, codeOffset, 0);
  }
  static addMessage(builder, messageOffset) {
    builder.addFieldOffset(3, messageOffset, 0);
  }
  static addRetryable(builder, retryable) {
    builder.addFieldInt8(4, +retryable, 0);
  }
  static endErrorResponse(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 8);
    builder.requiredField(offset, 10);
    return offset;
  }
  static finishErrorResponseBuffer(builder, offset) {
    builder.finish(offset, "SDER");
  }
  static finishSizePrefixedErrorResponseBuffer(builder, offset) {
    builder.finish(offset, "SDER", true);
  }
  static createErrorResponse(builder, schemaVersion, reqIdOffset, codeOffset, messageOffset, retryable) {
    ErrorResponse.startErrorResponse(builder);
    ErrorResponse.addSchemaVersion(builder, schemaVersion);
    ErrorResponse.addReqId(builder, reqIdOffset);
    ErrorResponse.addCode(builder, codeOffset);
    ErrorResponse.addMessage(builder, messageOffset);
    ErrorResponse.addRetryable(builder, retryable);
    return ErrorResponse.endErrorResponse(builder);
  }
}
export {
  ErrorResponse
};
