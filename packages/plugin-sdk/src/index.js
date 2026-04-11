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

export const KEY_BROKER_PROTOCOL_ID = "/orbpro/key-broker/1.0.0";
export const PUBLIC_KEY_PROTOCOL_ID = "/orbpro/public-key/1.0.0";
export const THIRDPARTY_CLIENT_LICENSE_PROTOCOL_ID =
  "/orbpro/third-party/client-license/1.0.0";
export const THIRDPARTY_SERVER_PLUGIN_PROTOCOL_ID =
  "/orbpro/third-party/server-plugin/1.0.0";

export {
  decodeKeyBrokerResponse,
  decodePublicKeyResponse,
  encodeKeyBrokerRequest,
} from "./key-broker-codec.js";

export {
  decodeThirdPartyClientLicenseRequest,
  decodeThirdPartyClientLicenseResponse,
  decodeThirdPartyServerPluginGrant,
  decodeThirdPartyServerPluginRegistration,
  encodeThirdPartyClientLicenseRequest,
  encodeThirdPartyClientLicenseResponse,
  encodeThirdPartyServerPluginGrant,
  encodeThirdPartyServerPluginRegistration,
} from "./third-party-codec.js";

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
