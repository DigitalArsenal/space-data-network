#!/usr/bin/env node
/**
 * Asserts that every place naming a Kubo version names the SAME one, and that
 * it is the version this suite actually ships.
 *
 * THIS IS THE CHECK THAT WAS MISSING.
 *
 * The node reported `kubo_version` over /api/v1/version from kubo/version.go —
 * the in-repo fork, "0.40.0-dev" — while every release path downloaded a stock
 * upstream v0.39.0. Nothing anywhere compared the two, so the API and the
 * dashboard header published a version this node had never contained, for
 * months. The fork is required by no go.mod, COPYed by no Dockerfile and built
 * by no CI lane; the full evidence is in sdn-server/docs/kubo-fork-audit.md.
 *
 * suite.versions.json kubo.shipped is the single source of truth:
 *   - scripts/generate-suite-version-info.js derives versioninfo.KuboVersion
 *     and sdn-js KUBO_VERSION from it;
 *   - scripts/kubo-version.sh reads it for the release shell scripts;
 *   - the workflows mirror it in a literal `env: KUBO_VERSION` (a workflow-level
 *     `env:` cannot call a script), and THIS check is what makes that safe.
 *
 * Run standalone (the ci-local.sh gate) or via check-version-consistency.js.
 */

const fs = require("fs");
const path = require("path");
const { spawnSync } = require("child_process");

const REPO_ROOT = path.resolve(__dirname, "..");

const KUBO_PIN_TAG_PATTERN = /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;

// Every file that spells the pin as a literal. A mirror nobody checks is just
// a second source of truth.
const KUBO_PIN_MIRRORS = [
  ".github/workflows/beta-release-artifacts.yml",
  ".github/workflows/live-dht-cross-platform.yml",
  ".github/workflows/release-deploy.yml",
];

function readIfPresent(relPath) {
  const full = path.join(REPO_ROOT, relPath);
  return fs.existsSync(full) ? fs.readFileSync(full, "utf8") : null;
}

/**
 * @param {{pass:(m:string)=>void, fail:(m:string)=>void, skip:(m:string)=>void}} report
 */
function checkKuboPin(report) {
  const { pass, fail, skip } = report;

  const manifest = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, "suite.versions.json"), "utf8"));
  const shipped = String(manifest.kubo?.shipped ?? "").trim();

  if (!KUBO_PIN_TAG_PATTERN.test(shipped)) {
    fail(`suite.versions.json kubo.shipped must be a release tag like "v0.39.0" (got "${shipped}")`);
    return;
  }
  pass(`suite.versions.json kubo.shipped = ${shipped}`);

  // The generated constants carry the bare version, no leading "v" — the shape
  // /api/v1/version has always published and internal/update's compareVersions
  // parses.
  const expectedBare = shipped.replace(/^v/, "");

  const generatedGo = readIfPresent("sdn-server/internal/versioninfo/generated.go");
  const goPin = generatedGo?.match(/KuboVersion\s*=\s*"([^"]+)"/)?.[1] ?? null;
  if (goPin === null) {
    fail("sdn-server/internal/versioninfo/generated.go: KuboVersion not found");
  } else if (goPin === expectedBare) {
    pass(`versioninfo.KuboVersion = ${goPin} (matches the shipped pin)`);
  } else {
    fail(
      `versioninfo.KuboVersion=${goPin} but this suite ships kubo ${shipped}. ` +
        "The node would report a Kubo it does not run. Run: node scripts/generate-suite-version-info.js",
    );
  }

  const generatedTS = readIfPresent("sdn-js/src/version-info.generated.ts");
  const tsPin = generatedTS?.match(/KUBO_VERSION\s*=\s*"([^"]+)"/)?.[1] ?? null;
  if (tsPin === null) {
    fail("sdn-js/src/version-info.generated.ts: KUBO_VERSION not found");
  } else if (tsPin === expectedBare) {
    pass(`sdn-js KUBO_VERSION = ${tsPin} (matches the shipped pin)`);
  } else {
    fail(`sdn-js KUBO_VERSION=${tsPin} but this suite ships kubo ${shipped}`);
  }

  for (const rel of KUBO_PIN_MIRRORS) {
    const src = readIfPresent(rel);
    if (src === null) {
      skip(`${rel} not present`);
      continue;
    }
    const declared = src.match(/^\s*KUBO_VERSION:\s*(\S+)\s*$/m)?.[1] ?? null;
    if (declared === null) {
      fail(`${rel}: no KUBO_VERSION declared (a hardcoded dist.ipfs.tech URL is not checkable — declare the pin)`);
    } else if (declared === shipped) {
      pass(`${rel}: KUBO_VERSION = ${declared}`);
    } else {
      fail(`${rel}: KUBO_VERSION=${declared} but suite.versions.json ships ${shipped}`);
    }

    // A literal tag anywhere else in the file is a pin that escaped the env
    // var — exactly how release-deploy.yml spelled v0.39.0 twice inside a URL
    // where nothing could see it.
    const strays = [...src.matchAll(/kubo\/(v\d+\.\d+\.\d+)/g)].map((m) => m[1]);
    const badStrays = [...new Set(strays)].filter((v) => v !== shipped);
    if (badStrays.length > 0) {
      fail(`${rel}: hardcoded kubo ${badStrays.join(", ")} in a URL; use \${KUBO_VERSION}`);
    }
  }

  // Prove kubo-version.sh agrees with the manifest rather than trusting that
  // it parses it correctly — the release scripts take the pin from it.
  const kuboVersionScript = path.join(REPO_ROOT, "scripts/kubo-version.sh");
  if (!fs.existsSync(kuboVersionScript)) {
    fail("scripts/kubo-version.sh is missing; the release scripts read the pin through it");
  } else {
    const run = spawnSync("bash", [kuboVersionScript], { encoding: "utf8" });
    const printed = String(run.stdout ?? "").trim();
    if (run.status !== 0) {
      fail(`scripts/kubo-version.sh exited ${run.status}: ${String(run.stderr ?? "").trim()}`);
    } else if (printed === shipped) {
      pass(`scripts/kubo-version.sh prints ${printed}`);
    } else {
      fail(`scripts/kubo-version.sh prints ${printed} but the manifest ships ${shipped}`);
    }
  }

  // The in-repo fork, IF it is still in the tree. Nothing ships it, so this is
  // not a pin check — it is the drift check the fork never had: SDNAgentVersion
  // documents itself as keeping step with internal/versioninfo.SuiteVersion and
  // had silently fallen a release behind. Conditional on purpose: deleting
  // kubo/ must not fail this lane.
  const forkSrc = readIfPresent("kubo/version.go");
  if (forkSrc === null) {
    skip("kubo/ fork not in the tree (nothing ships it; see sdn-server/docs/kubo-fork-audit.md)");
    return;
  }
  const forkAgent = forkSrc.match(/SDNAgentVersion\s*=\s*"([^"]+)"/)?.[1] ?? null;
  if (forkAgent === null) {
    skip("kubo/version.go: SDNAgentVersion not found");
  } else if (forkAgent === manifest.suiteVersion) {
    pass(`kubo/version.go SDNAgentVersion = ${forkAgent} (tracks the suite version)`);
  } else {
    fail(
      `kubo/version.go SDNAgentVersion=${forkAgent} but the suite is ${manifest.suiteVersion}; ` +
        "the constant documents itself as keeping step with internal/versioninfo.SuiteVersion",
    );
  }
}

module.exports = { checkKuboPin };

if (require.main === module) {
  let failures = 0;
  console.log("--- Shipped Kubo version pin ---");
  checkKuboPin({
    pass: (m) => console.log(`  PASS: ${m}`),
    fail: (m) => {
      failures++;
      console.log(`  FAIL: ${m}`);
    },
    skip: (m) => console.log(`  SKIP: ${m}`),
  });
  if (failures > 0) {
    console.log(`\n${failures} Kubo pin inconsistenc${failures === 1 ? "y" : "ies"} detected.`);
    process.exit(1);
  }
  console.log("\nKubo pin is consistent everywhere.");
}
