#!/usr/bin/env node
/**
 * check-sdn-js-dependency-layering.mjs
 *
 * sdn-js runs TWO incompatible libp2p majors in one dependency tree, on purpose,
 * and ~370 lines of hand-written compatibility shims in sdn-js/src/helia.ts are
 * what hold them together:
 *
 *   node_modules/libp2p                    1.9.4  <- we build the node with this
 *   node_modules/helia/node_modules/libp2p 3.2.0  <- helia@6 wants ^3.0.6
 *
 * The shims (withHeliaStreamHandlerCompat, withHeliaDialProtocolStreamCompat,
 * addLegacyEventedStreamCompat, addLegacyWritableStreamCompat,
 * identifyCapabilityOnly) are calibrated against the EXACT calling conventions
 * of the versions above. Nothing in semver protects that: a helia 6.1.x PATCH or
 * MINOR that changes how it invokes handle() or dialProtocol() breaks us with no
 * major bump, and a single `npm i` / `npm update` / `npm audit fix` / Dependabot
 * bump moves all four helia packages forward at once, unreviewed, into exactly
 * the code the shims are pinned against.
 *
 * This check is the tripwire. It reads sdn-js/package-lock.json only (no install
 * needed) and fails when the reviewed layering moves, so the bump becomes a
 * deliberate review of the shims instead of a silent landing.
 *
 * WHEN THIS FAILS: do not bump the expectations to make it green. Re-read
 * sdn-js/src/helia.ts against the new upstream, run the helia suites
 * (src/helia.test.ts, src/helia-trustless-gateway.test.ts), then update
 * REVIEWED_LAYERING in the same commit as the dependency change.
 *
 * See docs/sdn-js-helia-libp2p-layering.md for the full audit.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPTS_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(SCRIPTS_DIR, '..');

/**
 * Every package whose exact resolved version the helia.ts shims depend on.
 * `why` is printed on failure so the next person knows what breaks.
 */
export const REVIEWED_LAYERING = [
  {
    path: 'node_modules/libp2p',
    version: '1.9.4',
    why: 'the libp2p major we construct the node with; the shims adapt ITS calling convention',
  },
  {
    path: 'node_modules/helia/node_modules/libp2p',
    version: '3.2.0',
    why: 'the libp2p major helia@6 declares (^3.0.6); the shims impersonate this one',
  },
  {
    path: 'node_modules/@libp2p/interface',
    version: '1.7.0',
    why: 'supplies serviceCapabilities for the identifyCapabilityOnly() stub',
  },
  {
    path: 'node_modules/helia/node_modules/@libp2p/interface',
    version: '3.2.2',
    why: 'the interface major helia checks capabilities against',
  },
  {
    path: 'node_modules/helia',
    version: '6.0.22',
    why: 'calls handle()/dialProtocol(); the shims are calibrated against this exact release',
  },
  {
    path: 'node_modules/@helia/unixfs',
    version: '7.1.0',
    why: 'released in lockstep with helia; moves together with the calling convention',
  },
  {
    path: 'node_modules/@helia/block-brokers',
    version: '5.1.4',
    why: 'bitswap/trustlessGateway brokers handed the shimmed libp2p',
  },
  {
    path: 'node_modules/@helia/routers',
    version: '5.0.3',
    why: 'libp2pRouting() receives the shimmed instance through an `as never` cast',
  },
];

/**
 * The shims survive because BOTH @libp2p/interface majors resolve
 * serviceCapabilities to Symbol.for('@libp2p/service-capabilities'), which is
 * cross-realm identical. If a third libp2p copy appears, the hoisting that makes
 * that work is no longer the shape that was reviewed.
 */
export const REVIEWED_LIBP2P_COPIES = [
  'node_modules/@helia/utils/node_modules/libp2p',
  'node_modules/helia/node_modules/libp2p',
  'node_modules/libp2p',
];

/**
 * Direct ranges whose caret floor must equal what is actually resolved.
 *
 * A floor below the resolved version is a range that lies: it tells a consumer
 * that a libp2p we have never built or tested against is an acceptable
 * resolution for our subtree, and the shims are not calibrated for it.
 */
export const HONEST_FLOOR_PREFIXES = ['libp2p', '@libp2p/', '@chainsafe/libp2p-', 'helia', '@helia/'];

export function checkLayering({ cwd = REPO_ROOT } = {}) {
  const lockPath = resolve(cwd, 'sdn-js/package-lock.json');
  const pkgPath = resolve(cwd, 'sdn-js/package.json');
  const lock = JSON.parse(readFileSync(lockPath, 'utf8'));
  const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
  const packages = lock.packages ?? {};
  const violations = [];

  for (const { path, version, why } of REVIEWED_LAYERING) {
    const entry = packages[path];
    if (entry == null) {
      violations.push(
        `${path}: expected ${version} but the package is ABSENT from the lockfile (${why})`,
      );
      continue;
    }
    if (entry.version !== version) {
      violations.push(`${path}: reviewed ${version}, lockfile has ${entry.version} (${why})`);
    }
  }

  const copies = Object.keys(packages)
    .filter((key) => /(^|\/)node_modules\/libp2p$/.test(key))
    .sort();
  const expected = [...REVIEWED_LIBP2P_COPIES].sort();
  if (copies.join('\n') !== expected.join('\n')) {
    violations.push(
      `libp2p copies changed.\n    reviewed: ${expected.join(', ')}\n    lockfile: ${copies.join(', ') || '(none)'}`,
    );
  }

  for (const [name, range] of Object.entries(pkg.dependencies ?? {})) {
    if (!HONEST_FLOOR_PREFIXES.some((p) => (p.endsWith('/') ? name.startsWith(p) : name === p))) {
      continue;
    }
    const caret = /^\^(\d+\.\d+\.\d+)$/.exec(range);
    if (caret == null) continue; // exact pins and npm: aliases are already honest
    const entry = packages[`node_modules/${name}`];
    if (entry == null) continue;
    if (caret[1] !== entry.version) {
      violations.push(
        `sdn-js/package.json declares "${name}": "${range}" but resolves ${entry.version}; ` +
          `raise the floor to ^${entry.version} so the range states what is actually built and tested`,
      );
    }
  }

  return violations;
}

const invokedDirectly =
  process.argv[1] != null && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url));

if (invokedDirectly) {
  const repoFlag = process.argv.indexOf('--repo');
  const cwd = repoFlag === -1 ? REPO_ROOT : resolve(process.argv[repoFlag + 1]);
  const violations = checkLayering({ cwd });
  if (violations.length > 0) {
    console.error('[sdn-js-layering] FAIL: the reviewed helia/libp2p layering moved.\n');
    for (const v of violations) console.error(`  - ${v}`);
    console.error(
      '\n  sdn-js/src/helia.ts carries hand-written shims calibrated against the versions above.',
    );
    console.error(
      '  Re-read those shims against the new upstream and run the helia suites BEFORE updating',
    );
    console.error(
      '  REVIEWED_LAYERING in this file. See docs/sdn-js-helia-libp2p-layering.md.',
    );
    process.exit(1);
  }
  console.log(
    `[sdn-js-layering] PASS: reviewed dual-stack layering intact (${REVIEWED_LAYERING.length} pins, ${REVIEWED_LIBP2P_COPIES.length} libp2p copies)`,
  );
}
