var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
import { ErrorResponse } from "../../../space-data-network/module-delivery/v1/error-response.js";
import { GrantChallenge } from "../../../space-data-network/module-delivery/v1/grant-challenge.js";
import { GrantProof } from "../../../space-data-network/module-delivery/v1/grant-proof.js";
import { GrantRequest } from "../../../space-data-network/module-delivery/v1/grant-request.js";
import { GrantResponse } from "../../../space-data-network/module-delivery/v1/grant-response.js";
import { ModuleDeliveryMessageType } from "../../../space-data-network/module-delivery/v1/module-delivery-message-type.js";
class ModuleDeliveryMessage {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsModuleDeliveryMessage(bb, obj) {
    return (obj || new ModuleDeliveryMessage()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsModuleDeliveryMessage(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new ModuleDeliveryMessage()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDMD");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  messageType() {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.readUint8(this.bb_pos + offset) : ModuleDeliveryMessageType.NONE;
  }
  grantRequest(obj) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? (obj || new GrantRequest()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  grantChallenge(obj) {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? (obj || new GrantChallenge()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  grantProof(obj) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? (obj || new GrantProof()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  grantResponse(obj) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? (obj || new GrantResponse()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  errorResponse(obj) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? (obj || new ErrorResponse()).__init(this.bb.__indirect(this.bb_pos + offset), this.bb) : null;
  }
  static startModuleDeliveryMessage(builder) {
    builder.startObject(7);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addMessageType(builder, messageType) {
    builder.addFieldInt8(1, messageType, ModuleDeliveryMessageType.NONE);
  }
  static addGrantRequest(builder, grantRequestOffset) {
    builder.addFieldOffset(2, grantRequestOffset, 0);
  }
  static addGrantChallenge(builder, grantChallengeOffset) {
    builder.addFieldOffset(3, grantChallengeOffset, 0);
  }
  static addGrantProof(builder, grantProofOffset) {
    builder.addFieldOffset(4, grantProofOffset, 0);
  }
  static addGrantResponse(builder, grantResponseOffset) {
    builder.addFieldOffset(5, grantResponseOffset, 0);
  }
  static addErrorResponse(builder, errorResponseOffset) {
    builder.addFieldOffset(6, errorResponseOffset, 0);
  }
  static endModuleDeliveryMessage(builder) {
    const offset = builder.endObject();
    return offset;
  }
  static finishModuleDeliveryMessageBuffer(builder, offset) {
    builder.finish(offset, "SDMD");
  }
  static finishSizePrefixedModuleDeliveryMessageBuffer(builder, offset) {
    builder.finish(offset, "SDMD", true);
  }
}
export {
  ModuleDeliveryMessage
};
