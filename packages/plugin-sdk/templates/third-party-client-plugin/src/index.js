import {
  decodeThirdPartyClientLicenseResponse,
  encodeThirdPartyClientLicenseRequest,
} from "plugin-sdk/third-party-codec";

function randomBytes(length) {
  const out = new Uint8Array(length);
  crypto.getRandomValues(out);
  return out;
}

export async function requestLicense({
  brokerFetch,
  pluginId = "__PLUGIN_ID__",
  pluginVersion = "0.1.0",
  accountIdHash,
  ephemeralPublicKey,
  challengeToken = "",
}) {
  const request = encodeThirdPartyClientLicenseRequest({
    schemaVersion: 1,
    pluginId,
    pluginVersion,
    accountIdHash,
    requestNonce: randomBytes(16),
    ephemeralPublicKey,
    challengeToken,
  });

  const responseBytes = await brokerFetch(request);
  return decodeThirdPartyClientLicenseResponse(responseBytes);
}
