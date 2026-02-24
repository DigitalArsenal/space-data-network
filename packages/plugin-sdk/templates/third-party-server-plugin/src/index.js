import {
  decodeThirdPartyServerPluginGrant,
  encodeThirdPartyServerPluginRegistration,
} from "plugin-sdk/third-party-codec";

export async function registerServerPlugin({
  brokerFetch,
  pluginId = "__PLUGIN_ID__",
  pluginVersion = "0.1.0",
  vendorId = "__VENDOR_ID__",
  signingPublicKey,
  manifestHash,
  capabilities = ["license.issue", "license.audit"],
}) {
  const request = encodeThirdPartyServerPluginRegistration({
    schemaVersion: 1,
    pluginId,
    pluginVersion,
    vendorId,
    signingPublicKey,
    capabilities,
    manifestHash,
  });

  const responseBytes = await brokerFetch(request);
  return decodeThirdPartyServerPluginGrant(responseBytes);
}
