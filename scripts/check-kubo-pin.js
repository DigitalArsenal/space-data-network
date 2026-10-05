#!/usr/bin/env node
/**
 * Kubo is linked into the SDN node (owner 2026-10-05: one process, one peer
 * ID, Kubo's updates ride SDN releases). This check holds everything that
 * names that Kubo to the one place that decides it: the github.com/ipfs/kubo
 * requirement in sdn-server/go.mod.
 *
 * It also holds SDN to upstream's build. Owner 2026-09-28: "we should NOT be
 * changing dependencies if the core Kubo is depending on those". Two ways an
 * importer drifts from what Kubo ships, both checked here:
 *   - replace and exclude directives apply only in the main module, so
 *     sdn-server/go.mod must carry Kubo's own, verbatim;
 *   - Go's minimum version selection takes the highest requirement, so one
 *     SDN dependency ahead of Kubo's silently rebuilds Kubo against it.
 *     `--build-list` compares the two build lists module by module (it needs
 *     the Go toolchain and the module cache, so the security lane runs it).
 *
 * Run standalone (ci-local.sh, the security lane) or via
 * check-version-consistency.js.
 */

const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

const REPO_ROOT = path.resolve(__dirname, "..");
const GO_MOD = "sdn-server/go.mod";

// Workflows that download the ipfs CLI as a build tool (it computes and pins
// the UI asset CIDs, so it must be the Kubo the node links). A workflow-level
// `env:` cannot call a script, so each spells the tag as a literal.
const KUBO_TOOL_MIRRORS = [
  ".github/workflows/beta-release-artifacts.yml",
  ".github/workflows/release-deploy.yml",
];

// Workflows that build bundles. None of them may stage a Kubo any more.
const BUNDLE_WORKFLOWS = [
  ".github/workflows/beta-release-artifacts.yml",
  ".github/workflows/live-dht-cross-platform.yml",
  ".github/workflows/release-deploy.yml",
];

function readIfPresent(relPath) {
  const full = path.join(REPO_ROOT, relPath);
  return fs.existsSync(full) ? fs.readFileSync(full, "utf8") : null;
}

function linkedKuboVersion(goMod) {
  return /^\s*github\.com\/ipfs\/kubo\s+(v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\s*$/m.exec(goMod)?.[1] ?? null;
}

// directives returns the replace and exclude lines of a go.mod, normalised
// (block form or single-line form, comments dropped), sorted.
function directives(goMod) {
  const out = [];
  let block = null;
  for (const raw of goMod.split("\n")) {
    const line = raw.replace(/\/\/.*$/, "").trim();
    if (line === "") continue;
    if (block) {
      if (line === ")") {
        block = null;
      } else {
        out.push(`${block} ${line.replace(/\s+/g, " ")}`);
      }
      continue;
    }
    const opened = /^(replace|exclude)\s*\($/.exec(line);
    if (opened) {
      block = opened[1];
      continue;
    }
    const single = /^(replace|exclude)\s+(.+)$/.exec(line);
    if (single) out.push(`${single[1]} ${single[2].replace(/\s+/g, " ")}`);
  }
  return out.sort();
}

// kuboGoMod returns upstream Kubo's go.mod at version, from the module cache
// (any build of sdn-server has put it there), or null when it is not cached.
function kuboGoMod(version) {
  const run = spawnSync("go", ["env", "GOMODCACHE"], { encoding: "utf8" });
  const cache = String(run.stdout ?? "").trim();
  if (run.status !== 0 || cache === "") return null;
  const file = path.join(cache, "cache", "download", "github.com", "ipfs", "kubo", "@v", `${version}.mod`);
  return fs.existsSync(file) ? fs.readFileSync(file, "utf8") : null;
}

/**
 * @param {{pass:(m:string)=>void, fail:(m:string)=>void, skip:(m:string)=>void}} report
 */
function checkKuboPin(report) {
  const { pass, fail, skip } = report;

  const goMod = readIfPresent(GO_MOD);
  const linked = goMod === null ? null : linkedKuboVersion(goMod);
  if (linked === null) {
    fail(`${GO_MOD} must require github.com/ipfs/kubo at a release version: the node links Kubo`);
    return;
  }
  pass(`${GO_MOD} links github.com/ipfs/kubo ${linked}`);
  const bare = linked.replace(/^v/, "");

  const upstream = kuboGoMod(linked);
  if (upstream === null) {
    skip(`Kubo ${linked}'s go.mod is not in the module cache; its replace/exclude directives were not compared`);
  } else {
    const want = directives(upstream);
    const have = directives(goMod);
    const missing = want.filter((d) => !have.includes(d));
    const extra = have.filter((d) => !want.includes(d));
    if (missing.length === 0 && extra.length === 0) {
      pass(`${GO_MOD} carries Kubo ${linked}'s ${want.length} replace/exclude directive(s) verbatim`);
    } else {
      fail(
        `${GO_MOD} replace/exclude directives differ from Kubo ${linked}'s (they reach only the main module, ` +
          `so without them SDN builds a Kubo upstream never shipped): missing ${JSON.stringify(missing)}, ` +
          `not Kubo's ${JSON.stringify(extra)}`,
      );
    }
  }

  const generatedGo = readIfPresent("sdn-server/internal/versioninfo/generated.go");
  const goPin = generatedGo?.match(/KuboVersion\s*=\s*"([^"]+)"/)?.[1] ?? null;
  if (goPin === bare) {
    pass(`versioninfo.KuboVersion = ${goPin}`);
  } else {
    fail(`versioninfo.KuboVersion=${goPin} but the node links Kubo ${linked}. Run: node scripts/generate-suite-version-info.js`);
  }
  const generatedTS = readIfPresent("sdn-js/src/version-info.generated.ts");
  const tsPin = generatedTS?.match(/KUBO_VERSION\s*=\s*"([^"]+)"/)?.[1] ?? null;
  if (tsPin === bare) {
    pass(`sdn-js KUBO_VERSION = ${tsPin}`);
  } else {
    fail(`sdn-js KUBO_VERSION=${tsPin} but the node links Kubo ${linked}. Run: node scripts/generate-suite-version-info.js`);
  }

  for (const rel of KUBO_TOOL_MIRRORS) {
    const src = readIfPresent(rel);
    if (src === null) {
      skip(`${rel} not present`);
      continue;
    }
    const declared = src.match(/^\s*KUBO_VERSION:\s*(\S+)\s*$/m)?.[1] ?? null;
    if (declared === linked) {
      pass(`${rel}: KUBO_VERSION = ${declared} (the ipfs build tool is the linked Kubo)`);
    } else {
      fail(`${rel}: KUBO_VERSION=${declared} but the node links Kubo ${linked}; the asset CIDs must come from the same Kubo`);
    }
  }
  for (const rel of BUNDLE_WORKFLOWS) {
    const src = readIfPresent(rel);
    if (src === null) continue;
    if (/--kubo-path|KUBO_PLATFORM|runtime\/kubo/.test(src)) {
      fail(`${rel} still stages a Kubo binary into a bundle; Kubo is linked into the node`);
    } else {
      pass(`${rel} stages no Kubo binary`);
    }
  }

  const kuboVersionScript = path.join(REPO_ROOT, "scripts/kubo-version.sh");
  const run = spawnSync("bash", [kuboVersionScript], { encoding: "utf8" });
  const printed = String(run.stdout ?? "").trim();
  if (run.status !== 0) {
    fail(`scripts/kubo-version.sh exited ${run.status}: ${String(run.stderr ?? "").trim()}`);
  } else if (printed === linked) {
    pass(`scripts/kubo-version.sh prints ${printed}`);
  } else {
    fail(`scripts/kubo-version.sh prints ${printed} but the node links Kubo ${linked}`);
  }
}

// The Kubo packages the node links (internal/kubo imports these). The check
// compares the modules their code is compiled from, not the whole module
// graph: `go list -m all` also lists modules only a test or a tool of some
// dependency ever named, which no build of Kubo contains.
const KUBO_PACKAGES = [
  "github.com/ipfs/kubo",
  "github.com/ipfs/kubo/commands",
  "github.com/ipfs/kubo/config",
  "github.com/ipfs/kubo/config/serialize",
  "github.com/ipfs/kubo/core",
  "github.com/ipfs/kubo/core/coreapi",
  "github.com/ipfs/kubo/core/corehttp",
  "github.com/ipfs/kubo/core/node/libp2p",
  "github.com/ipfs/kubo/plugin/loader",
  "github.com/ipfs/kubo/repo",
  "github.com/ipfs/kubo/repo/fsrepo",
];

// compiledModules maps each module that Kubo's packages are compiled from to
// the version (and replacement) the build at dir selects for it.
function compiledModules(dir) {
  const format = "{{with .Module}}{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}{{end}}";
  const run = spawnSync("go", ["list", "-deps", "-f", format, ...KUBO_PACKAGES], {
    cwd: dir,
    encoding: "utf8",
    maxBuffer: 64 << 20,
  });
  if (run.status !== 0) {
    throw new Error(`go list -deps in ${dir}: ${String(run.stderr ?? "").trim()}`);
  }
  const modules = new Map();
  for (const line of String(run.stdout).split("\n")) {
    const [mod, ...rest] = line.trim().split(" ");
    if (mod && !mod.startsWith("github.com/ipfs/kubo") && rest.length > 0) modules.set(mod, rest.join(" "));
  }
  return modules;
}

/**
 * Compares, module by module, what Kubo's linked packages are compiled from
 * in SDN's build with what they are compiled from in Kubo's own.
 */
function checkKuboBuildList(report) {
  const { pass, fail } = report;
  const goMod = fs.readFileSync(path.join(REPO_ROOT, GO_MOD), "utf8");
  const linked = linkedKuboVersion(goMod);
  // Kubo's own go.mod, for its own directives: comparing against SDN's copy
  // would only check SDN against itself.
  const dl = spawnSync("go", ["mod", "download", "-json", `github.com/ipfs/kubo@${linked}`], { encoding: "utf8" });
  if (dl.status !== 0) throw new Error(`go mod download github.com/ipfs/kubo@${linked}: ${String(dl.stderr ?? "").trim()}`);
  const kuboDirectives = directives(fs.readFileSync(JSON.parse(dl.stdout).GoMod, "utf8"));
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), "sdn-kubo-build-list-"));
  try {
    const goLine = /^go\s+(\S+)\s*$/m.exec(goMod)?.[1] ?? "1.26";
    const lines = ["module sdnkubobuildlist", "", `go ${goLine}`, "", `require github.com/ipfs/kubo ${linked}`, ""];
    lines.push(...kuboDirectives);
    fs.writeFileSync(path.join(scratch, "go.mod"), `${lines.join("\n")}\n`);
    const imports = KUBO_PACKAGES.map((pkg) => `\t_ "${pkg}"`).join("\n");
    fs.writeFileSync(path.join(scratch, "pin.go"), `package sdnkubobuildlist\n\nimport (\n${imports}\n)\n`);
    const tidy = spawnSync("go", ["mod", "tidy"], { cwd: scratch, encoding: "utf8" });
    if (tidy.status !== 0) throw new Error(`go mod tidy for Kubo ${linked}: ${String(tidy.stderr ?? "").trim()}`);
    const kubo = compiledModules(scratch);
    const sdn = compiledModules(path.join(REPO_ROOT, "sdn-server"));
    const drift = [];
    for (const [mod, version] of kubo) {
      if (sdn.get(mod) !== version) drift.push(`${mod}: Kubo ${version}, SDN ${sdn.get(mod) ?? "(absent)"}`);
    }
    for (const mod of sdn.keys()) {
      if (!kubo.has(mod)) drift.push(`${mod}: compiled into Kubo's packages in SDN's build only`);
    }
    if (drift.length === 0) {
      pass(`Kubo ${linked}'s linked packages compile from the same ${kubo.size} module versions in SDN as in Kubo`);
    } else {
      fail(`SDN builds Kubo ${linked} from different dependencies:\n    ${drift.join("\n    ")}`);
    }
  } finally {
    fs.rmSync(scratch, { recursive: true, force: true });
  }
}

module.exports = { checkKuboPin, checkKuboBuildList };

if (require.main === module) {
  let failures = 0;
  const report = {
    pass: (m) => console.log(`  PASS: ${m}`),
    fail: (m) => {
      failures++;
      console.log(`  FAIL: ${m}`);
    },
    skip: (m) => console.log(`  SKIP: ${m}`),
  };
  console.log("--- Kubo linked into SDN ---");
  checkKuboPin(report);
  if (process.argv.includes("--build-list")) {
    console.log("--- Kubo's build list ---");
    try {
      checkKuboBuildList(report);
    } catch (error) {
      report.fail(error instanceof Error ? error.message : String(error));
    }
  }
  if (failures > 0) {
    console.log(`\n${failures} Kubo inconsistenc${failures === 1 ? "y" : "ies"} detected.`);
    process.exit(1);
  }
  console.log("\nThe linked Kubo is consistent everywhere.");
}
