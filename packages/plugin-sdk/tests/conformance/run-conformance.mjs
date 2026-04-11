#!/usr/bin/env node

import { runClientPluginConformance } from "./client-plugin-conformance.mjs";
import { runModuleDeliveryConformance } from "./module-delivery-conformance.mjs";
import { runSDKClientConformance } from "./sdk-client-conformance.mjs";
import { runServerPluginConformance } from "./server-plugin-conformance.mjs";

async function main() {
  const results = [];
  results.push(await runModuleDeliveryConformance());
  results.push(await runSDKClientConformance());
  results.push(await runClientPluginConformance());
  results.push(await runServerPluginConformance());

  console.log(
    JSON.stringify(
      {
        ok: true,
        suites: results,
      },
      null,
      2,
    ),
  );
}

main().catch((error) => {
  console.error(`[plugin-sdk conformance] ${error.message}`);
  process.exit(1);
});
