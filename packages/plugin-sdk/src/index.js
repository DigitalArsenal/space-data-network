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
