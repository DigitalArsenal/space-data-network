#!/usr/bin/env node
/**
 * check-kubo-lockstep.mjs
 *
 * This repository carries TWO independent Go modules that must speak the same
 * wire protocols:
 *
 *   sdn-server/go.mod   module github.com/spacedatanetwork/sdn-server
 *   kubo/go.mod         module github.com/ipfs/kubo   (vendored upstream tree)
 *
 * There is no go.work, no `replace`, and sdn-server imports nothing from kubo —
 * `grep -rn 'ipfs/kubo' sdn-server --include='*.go'` is empty. The "upstream
 * Kubo + SDN bolted on" arrangement is therefore two trees kept in lockstep BY
 * HAND, and until this check existed nothing failed when they drifted apart.
 *
 * Drift in the libp2p/IPFS stack is the dangerous kind: the two trees would
 * still each compile, and the divergence would only show up as peers that no
 * longer interoperate on /ipfs/kad/1.0.0, a transport, or a muxer.
 *
 * The rule: every module shared by both go.mod files whose path matches
 * INTEROP_PREFIXES must be pinned to the same version in both, unless the
 * divergence is declared in DECLARED_DIVERGENCES with a reason.
 *
 * WHEN THIS FAILS: align the two go.mod files, or add a dated entry to
 * DECLARED_DIVERGENCES explaining why the divergence is correct. Do not delete
 * the module from INTEROP_PREFIXES to make it green.
 *
 * See docs/sdn-js-helia-libp2p-layering.md ("Kubo lockstep") for the audit.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPTS_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(SCRIPTS_DIR, '..');

/** Module path prefixes whose versions decide whether the two trees interoperate. */
export const INTEROP_PREFIXES = [
  'github.com/libp2p/',
  'github.com/ipfs/',
  'github.com/ipld/',
  'github.com/multiformats/',
];

/**
 * Divergences that are known and accepted. Each needs a reason; an entry here is
 * a statement that somebody looked, not that the check was inconvenient.
 */
export const DECLARED_DIVERGENCES = [
  {
    module: 'github.com/ipfs/boxo',
    reviewed: '2026-09-18',
    reason:
      'sdn-server pulls boxo only indirectly (v0.35.2) while the vendored kubo tree pins a ' +
      'v0.35.3 pre-release directly. The two modules build independently, and boxo carries no ' +
      'wire-protocol identifiers that the DHT/transport handshake depends on. Revisit when ' +
      'sdn-server takes a direct boxo dependency.',
  },
];

/** Parse the `require` version pins out of a go.mod. */
export function parseGoMod(text) {
  const out = new Map();
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (line === '' || line.startsWith('//')) continue;
    const match = /^(?:require\s+)?([a-zA-Z0-9._~/-]+\.[a-zA-Z0-9._~/-]+)\s+(v[^\s]+)(\s+\/\/\s*indirect)?/.exec(
      line,
    );
    if (match == null) continue;
    out.set(match[1], { version: match[2], indirect: match[3] != null });
  }
  return out;
}

export function checkLockstep({ cwd = REPO_ROOT } = {}) {
  const server = parseGoMod(readFileSync(resolve(cwd, 'sdn-server/go.mod'), 'utf8'));
  const kubo = parseGoMod(readFileSync(resolve(cwd, 'kubo/go.mod'), 'utf8'));
  const declared = new Map(DECLARED_DIVERGENCES.map((d) => [d.module, d]));

  const violations = [];
  const compared = [];
  const staleAllowlist = [];

  for (const [module, serverPin] of server) {
    if (!INTEROP_PREFIXES.some((prefix) => module.startsWith(prefix))) continue;
    const kuboPin = kubo.get(module);
    if (kuboPin == null) continue; // not shared; nothing to keep in step
    compared.push(module);
    if (serverPin.version === kuboPin.version) {
      if (declared.has(module)) staleAllowlist.push(module);
      continue;
    }
    if (declared.has(module)) continue;
    violations.push(
      `${module}: sdn-server/go.mod pins ${serverPin.version}${serverPin.indirect ? ' (indirect)' : ''}, ` +
        `kubo/go.mod pins ${kuboPin.version}${kuboPin.indirect ? ' (indirect)' : ''}`,
    );
  }

  for (const module of staleAllowlist) {
    violations.push(
      `${module}: listed in DECLARED_DIVERGENCES but the two trees now agree; remove the entry`,
    );
  }

  return { violations, comparedCount: compared.length };
}

const invokedDirectly =
  process.argv[1] != null && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url));

if (invokedDirectly) {
  const repoFlag = process.argv.indexOf('--repo');
  const cwd = repoFlag === -1 ? REPO_ROOT : resolve(process.argv[repoFlag + 1]);
  const { violations, comparedCount } = checkLockstep({ cwd });
  if (violations.length > 0) {
    console.error('[kubo-lockstep] FAIL: sdn-server and the vendored kubo tree drifted apart.\n');
    for (const v of violations) console.error(`  - ${v}`);
    console.error(
      '\n  These two Go modules have no go.work and no import relationship, so nothing else',
    );
    console.error(
      '  catches this: both trees still compile, and the divergence surfaces only as peers that',
    );
    console.error(
      '  stop interoperating. Align the pins, or add a dated DECLARED_DIVERGENCES entry.',
    );
    process.exit(1);
  }
  console.log(
    `[kubo-lockstep] PASS: ${comparedCount} shared libp2p/IPFS/multiformats pins agree ` +
      `(${DECLARED_DIVERGENCES.length} declared divergence${DECLARED_DIVERGENCES.length === 1 ? '' : 's'})`,
  );
}
