var __defProp = Object.defineProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: true, configurable: true, writable: true, value }) : obj[key] = value;
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key !== "symbol" ? key + "" : key, value);
import * as flatbuffers from "flatbuffers";
class BundleDescriptor {
  constructor() {
    __publicField(this, "bb", null);
    __publicField(this, "bb_pos", 0);
  }
  __init(i, bb) {
    this.bb_pos = i;
    this.bb = bb;
    return this;
  }
  static getRootAsBundleDescriptor(bb, obj) {
    return (obj || new BundleDescriptor()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static getSizePrefixedRootAsBundleDescriptor(bb, obj) {
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    return (obj || new BundleDescriptor()).__init(bb.readInt32(bb.position()) + bb.position(), bb);
  }
  static bufferHasIdentifier(bb) {
    return bb.__has_identifier("SDBD");
  }
  schemaVersion() {
    const offset = this.bb.__offset(this.bb_pos, 4);
    return offset ? this.bb.readUint32(this.bb_pos + offset) : 1;
  }
  cid(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 6);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  contentHash(index) {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.readUint8(this.bb.__vector(this.bb_pos + offset) + index) : 0;
  }
  contentHashLength() {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? this.bb.__vector_len(this.bb_pos + offset) : 0;
  }
  contentHashArray() {
    const offset = this.bb.__offset(this.bb_pos, 8);
    return offset ? new Uint8Array(this.bb.bytes().buffer, this.bb.bytes().byteOffset + this.bb.__vector(this.bb_pos + offset), this.bb.__vector_len(this.bb_pos + offset)) : null;
  }
  sizeBytes() {
    const offset = this.bb.__offset(this.bb_pos, 10);
    return offset ? this.bb.readUint64(this.bb_pos + offset) : BigInt("0");
  }
  moduleId(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 12);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  moduleVersion(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 14);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  runtime(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 16);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  abi(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 18);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  entrypoint(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 20);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  publicationCid(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 22);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  contentCodec(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 24);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  encryptionCodec(optionalEncoding) {
    const offset = this.bb.__offset(this.bb_pos, 26);
    return offset ? this.bb.__string(this.bb_pos + offset, optionalEncoding) : null;
  }
  static startBundleDescriptor(builder) {
    builder.startObject(12);
  }
  static addSchemaVersion(builder, schemaVersion) {
    builder.addFieldInt32(0, schemaVersion, 1);
  }
  static addCid(builder, cidOffset) {
    builder.addFieldOffset(1, cidOffset, 0);
  }
  static addContentHash(builder, contentHashOffset) {
    builder.addFieldOffset(2, contentHashOffset, 0);
  }
  static createContentHashVector(builder, data) {
    builder.startVector(1, data.length, 1);
    for (let i = data.length - 1; i >= 0; i--) {
      builder.addInt8(data[i]);
    }
    return builder.endVector();
  }
  static startContentHashVector(builder, numElems) {
    builder.startVector(1, numElems, 1);
  }
  static addSizeBytes(builder, sizeBytes) {
    builder.addFieldInt64(3, sizeBytes, BigInt("0"));
  }
  static addModuleId(builder, moduleIdOffset) {
    builder.addFieldOffset(4, moduleIdOffset, 0);
  }
  static addModuleVersion(builder, moduleVersionOffset) {
    builder.addFieldOffset(5, moduleVersionOffset, 0);
  }
  static addRuntime(builder, runtimeOffset) {
    builder.addFieldOffset(6, runtimeOffset, 0);
  }
  static addAbi(builder, abiOffset) {
    builder.addFieldOffset(7, abiOffset, 0);
  }
  static addEntrypoint(builder, entrypointOffset) {
    builder.addFieldOffset(8, entrypointOffset, 0);
  }
  static addPublicationCid(builder, publicationCidOffset) {
    builder.addFieldOffset(9, publicationCidOffset, 0);
  }
  static addContentCodec(builder, contentCodecOffset) {
    builder.addFieldOffset(10, contentCodecOffset, 0);
  }
  static addEncryptionCodec(builder, encryptionCodecOffset) {
    builder.addFieldOffset(11, encryptionCodecOffset, 0);
  }
  static endBundleDescriptor(builder) {
    const offset = builder.endObject();
    builder.requiredField(offset, 6);
    builder.requiredField(offset, 8);
    builder.requiredField(offset, 12);
    return offset;
  }
  static finishBundleDescriptorBuffer(builder, offset) {
    builder.finish(offset, "SDBD");
  }
  static finishSizePrefixedBundleDescriptorBuffer(builder, offset) {
    builder.finish(offset, "SDBD", true);
  }
  static createBundleDescriptor(builder, schemaVersion, cidOffset, contentHashOffset, sizeBytes, moduleIdOffset, moduleVersionOffset, runtimeOffset, abiOffset, entrypointOffset, publicationCidOffset, contentCodecOffset, encryptionCodecOffset) {
    BundleDescriptor.startBundleDescriptor(builder);
    BundleDescriptor.addSchemaVersion(builder, schemaVersion);
    BundleDescriptor.addCid(builder, cidOffset);
    BundleDescriptor.addContentHash(builder, contentHashOffset);
    BundleDescriptor.addSizeBytes(builder, sizeBytes);
    BundleDescriptor.addModuleId(builder, moduleIdOffset);
    BundleDescriptor.addModuleVersion(builder, moduleVersionOffset);
    BundleDescriptor.addRuntime(builder, runtimeOffset);
    BundleDescriptor.addAbi(builder, abiOffset);
    BundleDescriptor.addEntrypoint(builder, entrypointOffset);
    BundleDescriptor.addPublicationCid(builder, publicationCidOffset);
    BundleDescriptor.addContentCodec(builder, contentCodecOffset);
    BundleDescriptor.addEncryptionCodec(builder, encryptionCodecOffset);
    return BundleDescriptor.endBundleDescriptor(builder);
  }
}
export {
  BundleDescriptor
};
