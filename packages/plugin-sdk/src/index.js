export const MODULE_DELIVERY_PROTOCOL_ID =
  "/space-data-network/module-delivery/1.0.0";

export const MODULE_DELIVERY_MESSAGE_TYPES = Object.freeze({
  GRANT_REQUEST: "grant_request",
  GRANT_CHALLENGE: "grant_challenge",
  GRANT_PROOF: "grant_proof",
  GRANT_RESPONSE: "grant_response",
  ERROR_RESPONSE: "error_response",
});

export {
  ModuleDeliveryMessageType,
  decodeBundleDescriptor,
  decodeErrorResponse,
  decodeGrantChallenge,
  decodeGrantProof,
  decodeGrantRequest,
  decodeGrantResponse,
  decodeModuleDeliveryMessage,
  decodeWrappedContentKey,
  encodeBundleDescriptor,
  encodeErrorResponse,
  encodeGrantChallenge,
  encodeGrantProof,
  encodeGrantRequest,
  encodeGrantResponse,
  encodeModuleDeliveryMessage,
  encodeWrappedContentKey,
} from "./module-delivery-codec.js";

export {
  DEFAULT_SDN_DISCOVERY_VERSION,
  SDS_EXCHANGE_PROTOCOL_ID,
  SDS_MESSAGE_TYPES,
  SDS_RESPONSE_CODES,
  FlatSQLClient,
  IPFSClient,
  SDKHTTPError,
  SDSExchangeClient,
  SDNClient,
  WebClient,
  computeSDNDiscoveryCID,
  computeSdnDiscoveryCID,
  createPluginSDK,
} from "./plugin-client.js";
