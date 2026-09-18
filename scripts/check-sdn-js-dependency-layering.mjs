#!/usr/bin/env node
/**
 * check-sdn-js-dependency-layering.mjs
 *
 * sdn-js used to run TWO incompatible libp2p majors in one dependency tree on
 * purpose — libp2p 1.9.4 for the node it built, libp2p 3.x nested under helia@6
 * — held together by ~370 lines of hand-written compatibility shims in
 * sdn-js/src/helia.ts. This script existed to pin that arrangement in place so
 * a stray `npm update` could not move either half into code the shims were not
 * calibrated against.
 *
 * THAT ARRANGEMENT IS GONE. The split is closed: one libp2p, one
 * @libp2p/interface, one helia, and the shims that bridged them are deleted.
 * libp2p 1.x was EOL at 1.9.4, so GHSA-vrf4-mx87-p53w (@libp2p/peer-store
 * PeerRecord poisoning, 8.2) had no in-range fix while that half existed; the
 * only way to close it was to close the split.
 *
 * So this check now asserts the OPPOSITE invariant, which is the durable payoff:
 * every package on the libp2p/helia critical path resolves to exactly ONE copy.
 * A second copy is how the old arrangement grew, and it is how the shims would
 * have to come back.
 *
 * It reads sdn-js/package-lock.json only (no install needed).
 *
 * WHEN THIS FAILS: a dependency bump re-split one of these packages. Find the
 * dependent that pulled in the second copy (`npm ls <package>`) and either move
 * it forward with the rest of the tree or record the duplicate DELIBERATELY in
 * KNOWN_DUPLICATE_LEAVES below, with the reason. Do not delete the rule.
 *
 * See docs/sdn-js-helia-libp2p-layering.md for the audit this replaced.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPTS_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(SCRIPTS_DIR, '..');

/**
 * Packages that must resolve to exactly one copy in the whole tree.
 *
 * `why` is printed on failure. Each of these carries cross-module identity:
 * instances made by one copy are not recognised by another, which is precisely
 * what the deleted shims were compensating for.
 */
export const SINGLE_COPY_PACKAGES = [
  {
    name: 'libp2p',
    why: 'two libp2p majors in one tree is the arrangement the helia.ts shims existed for; one copy is what deleted them',
  },
  {
    name: '@libp2p/interface',
    why: 'the service-capability symbols and Stream/MessageStream types every libp2p module type-checks against',
  },
  {
    name: '@libp2p/crypto',
    why: 'key classes on the noise handshake path; a second copy means two incompatible PrivateKey shapes',
  },
  {
    name: '@libp2p/peer-id',
    why: 'PeerID identity: a second copy makes peerIdFromString() output that another copy does not accept',
  },
  {
    name: '@libp2p/peer-store',
    why: 'GHSA-vrf4-mx87-p53w lived in a nested copy that our direct range could not reach',
  },
  {
    name: '@multiformats/multiaddr',
    why: 'multiaddr 13 changed the Multiaddr shape; a mixed tree throws "ma.getComponents is not a function"',
  },
  {
    name: 'helia',
    why: 'helia composes a node from its own core class; two copies means two block stores and two routing tables',
  },
  {
    name: '@helia/interface',
    why: 'the Router/BlockBroker contracts helia checks by shape at start()',
  },
  {
    name: '@helia/utils',
    why: 'AbstractSession, shared by every block broker session',
  },
];

/**
 * Minimum resolved versions for packages that carry a published advisory.
 *
 * These are floors for the WHOLE tree, not just the hoisted copy: the reason
 * GHSA-vrf4-mx87-p53w survived so long is that the vulnerable copy was nested
 * where the direct range could not reach it.
 */
export const SECURITY_FLOORS = [
  {
    name: '@libp2p/peer-store',
    minimum: '12.0.24',
    why: 'GHSA-vrf4-mx87-p53w (8.2) — PeerStore accepts attacker-signed PeerRecords for a victim peer ID',
  },
  {
    name: '@libp2p/kad-dht',
    minimum: '16.2.6',
    why: 'GHSA-32mq-hpph-xfvr (7.5)',
  },
];

/**
 * Duplicate copies that are known, understood and tolerated, with the reason.
 *
 * Every entry here is a leaf: a byte-helper package with no cross-copy identity
 * (no CID, no PeerId, no Stream), reached only through a dependency we do not
 * import. Anything with cross-module identity belongs in SINGLE_COPY_PACKAGES
 * instead.
 */
export const KNOWN_DUPLICATE_LEAVES = [
  {
    name: 'multiformats',
    maxCopies: 4,
    why:
      '@helia/bitswap depends on @helia/libp2p, which still pins @chainsafe/libp2p-{noise,yamux} ' +
      '(the frozen pre-rename line) and @libp2p/http; those nest multiformats 13. sdn-js imports ' +
      'none of them and does no `instanceof CID`, so the copies never meet.',
  },
  {
    name: 'uint8arrays',
    maxCopies: 4,
    why: 'same @helia/libp2p subtree; byte helpers with no cross-copy identity',
  },
  {
    name: 'uint8arraylist',
    maxCopies: 3,
    why: 'same @helia/libp2p subtree',
  },
];

/**
 * Packages that must NOT be direct dependencies any more.
 *
 * Each was retired by this upgrade, and each would silently drag a second
 * libp2p major back into the tree if it returned.
 */
export const RETIRED_DEPENDENCIES = [
  {
    name: '@spacedatanetwork/libp2p-webrtc-v1',
    why: 'an alias for @libp2p/webrtc@4, which pins @libp2p/interface ^1 — a libp2p-1 transport',
  },
  {
    name: '@chainsafe/libp2p-gossipsub',
    why: 'moved into the js-libp2p monorepo as @libp2p/gossipsub; the @chainsafe line targets @libp2p/interface ^2',
  },
  {
    name: '@chainsafe/libp2p-noise',
    why: 'moved into the js-libp2p monorepo as @libp2p/noise',
  },
  {
    name: '@chainsafe/libp2p-yamux',
    why: 'moved into the js-libp2p monorepo as @libp2p/yamux',
  },
  {
    name: '@helia/routers',
    why: 'end of the helia-6 line (@helia/interface ^6); replaced by @helia/fallback-router',
  },
  {
    name: '@helia/block-brokers',
    why: 'end of the helia-6 line; replaced by @helia/bitswap + @helia/trustless-gateway-client',
  },
];

/**
 * Direct ranges whose caret floor must equal what is actually resolved.
 *
 * A floor below the resolved version is a range that lies: it tells a consumer
 * that a libp2p we have never built or tested against is an acceptable
 * resolution for our subtree.
 */
export const HONEST_FLOOR_PREFIXES = ['libp2p', '@libp2p/', '@chainsafe/libp2p-', 'helia', '@helia/'];

function compareVersions(left, right) {
  const leftParts = String(left).split('.').map((part) => Number.parseInt(part, 10) || 0);
  const rightParts = String(right).split('.').map((part) => Number.parseInt(part, 10) || 0);
  for (let index = 0; index < Math.max(leftParts.length, rightParts.length); index += 1) {
    const difference = (leftParts[index] ?? 0) - (rightParts[index] ?? 0);
    if (difference !== 0) return difference;
  }
  return 0;
}

function copiesOf(packages, name) {
  const suffix = `node_modules/${name}`;
  return Object.entries(packages)
    .filter(([key]) => key === suffix || key.endsWith(`/${suffix}`))
    .map(([key, value]) => ({ path: key, version: value?.version ?? '(unknown)' }))
    .sort((a, b) => a.path.localeCompare(b.path));
}

export function checkLayering({ cwd = REPO_ROOT } = {}) {
  const lockPath = resolve(cwd, 'sdn-js/package-lock.json');
  const pkgPath = resolve(cwd, 'sdn-js/package.json');
  const lock = JSON.parse(readFileSync(lockPath, 'utf8'));
  const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
  const packages = lock.packages ?? {};
  const violations = [];

  for (const { name, why } of SINGLE_COPY_PACKAGES) {
    const copies = copiesOf(packages, name);
    if (copies.length === 0) {
      violations.push(`${name}: expected exactly one copy but the package is ABSENT from the lockfile (${why})`);
      continue;
    }
    if (copies.length > 1) {
      violations.push(
        `${name}: expected ONE copy, found ${copies.length} (${why})\n` +
          copies.map((copy) => `      ${copy.version}  ${copy.path}`).join('\n'),
      );
    }
  }

  for (const { name, minimum, why } of SECURITY_FLOORS) {
    for (const copy of copiesOf(packages, name)) {
      if (compareVersions(copy.version, minimum) < 0) {
        violations.push(
          `${name}: ${copy.path} resolves ${copy.version}, below the ${minimum} security floor (${why})`,
        );
      }
    }
  }

  for (const { name, maxCopies, why } of KNOWN_DUPLICATE_LEAVES) {
    const copies = copiesOf(packages, name);
    if (copies.length > maxCopies) {
      violations.push(
        `${name}: ${copies.length} copies, more than the ${maxCopies} recorded as understood (${why})\n` +
          copies.map((copy) => `      ${copy.version}  ${copy.path}`).join('\n'),
      );
    }
  }

  for (const { name, why } of RETIRED_DEPENDENCIES) {
    if (pkg.dependencies?.[name] != null) {
      violations.push(`${name}: retired by the libp2p 3 upgrade but declared again in sdn-js/package.json (${why})`);
    }
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
    console.error('[sdn-js-layering] FAIL: the single-major libp2p/helia layering moved.\n');
    for (const v of violations) console.error(`  - ${v}`);
    console.error(
      '\n  sdn-js ran two libp2p majors once, bridged by ~370 lines of shims in sdn-js/src/helia.ts.',
    );
    console.error(
      '  Closing that split is what deleted them and what fixed GHSA-vrf4-mx87-p53w, which had no',
    );
    console.error(
      '  in-range fix while libp2p 1.x was in the tree. Find the dependent that re-split the package',
    );
    console.error(
      '  (`npm ls <package>`) and move it forward, or record the duplicate deliberately in this file.',
    );
    process.exit(1);
  }
  console.log(
    `[sdn-js-layering] PASS: one copy each of ${SINGLE_COPY_PACKAGES.length} critical packages, ` +
      `${SECURITY_FLOORS.length} security floors held, ${RETIRED_DEPENDENCIES.length} retired deps absent`,
  );
}
