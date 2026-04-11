import * as flatbuffers from "flatbuffers";
import { KeyBrokerRequest } from "./generated/orbpro/keybroker/key-broker-request.js";
import { KeyBrokerResponse } from "./generated/orbpro/keybroker/key-broker-response.js";
import { PublicKeyResponse } from "./generated/orbpro/keybroker/public-key-response.js";

function asUint8Array(value) {
  if (value instanceof Uint8Array) {
    return value;
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  return new Uint8Array(0);
}

export function encodeKeyBrokerRequest(packetBytes) {
  const packet = asUint8Array(packetBytes);
  if (packet.length === 0) {
    throw new Error("empty key-broker packet");
  }

  const builder = new flatbuffers.Builder(packet.length + 64);
  const packetOffset = KeyBrokerRequest.createPacketVector(builder, packet);
  const root = KeyBrokerRequest.createKeyBrokerRequest(builder, packetOffset);
  KeyBrokerRequest.finishKeyBrokerRequestBuffer(builder, root);
  return builder.asUint8Array();
}

export function decodePublicKeyResponse(messageBytes) {
  const message = asUint8Array(messageBytes);
  const bb = new flatbuffers.ByteBuffer(message);
  if (!PublicKeyResponse.bufferHasIdentifier(bb)) {
    throw new Error("invalid public key response identifier");
  }

  const response = PublicKeyResponse.getRootAsPublicKeyResponse(bb);
  const publicKey = response.publicKeyArray();
  if (!publicKey || publicKey.length === 0) {
    throw new Error("missing public key bytes");
  }
  return publicKey.slice();
}

export function decodeKeyBrokerResponse(messageBytes) {
  const message = asUint8Array(messageBytes);
  const bb = new flatbuffers.ByteBuffer(message);
  if (!KeyBrokerResponse.bufferHasIdentifier(bb)) {
    throw new Error("invalid key-broker response identifier");
  }

  const response = KeyBrokerResponse.getRootAsKeyBrokerResponse(bb);
  const status = response.status() >>> 0;
  const packet = response.packetArray() || new Uint8Array(0);

  return { status, packet: packet.slice() };
}
