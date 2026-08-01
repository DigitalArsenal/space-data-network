#!/usr/bin/env node
/**
 * kubo-update-watch — the daily check behind the owner ruling 2026-08-01:
 * "the update server needs to check for kubo updates every day and wrap /
 * redeploy the SDN client", with all instances updating in place.
 *
 * Emits ONE JSON verdict with two independent drift signals:
 *
 *  - kubo drift:  upstream ipfs/kubo latest release vs the kubo version this
 *    fork embeds (kubo/version.go CurrentVersionNumber). A fork rebase is
 *    ENGINEERING, not automation — the watcher's job is to surface the drift
 *    the day it appears, not to auto-merge a patched fork unattended.
 *
 *  - feed drift:  the sdn repo's origin/main tip vs the sourceCommit of the
 *    newest published cli-bundle payload. THIS half is mechanizable: when the
 *    feed is behind main, the operator lane is build (Docker linux/amd64,
 *    locally) -> publish-fleet-update.mjs -> `update install` on each box.
 *
 * Run from the sdn repo root. Exit code: 0 = no drift, 3 = drift present
 * (either signal), >0 other = error. Machine-readable stdout only.
 */
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

const feedIndexUrl =
  process.argv[2] ||
  'https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/index.json';

const sh = (cmd, args) =>
  execFileSync(cmd, args, { stdio: ['ignore', 'pipe', 'pipe'] }).toString().trim();

function upstreamKuboLatest() {
  const raw = sh('curl', [
    '-s',
    '-m',
    '20',
    '-H',
    'Accept: application/vnd.github+json',
    'https://api.github.com/repos/ipfs/kubo/releases/latest',
  ]);
  const d = JSON.parse(raw);
  if (!d.tag_name) throw new Error('github release response had no tag_name');
  return { tag: d.tag_name, publishedAt: d.published_at };
}

function forkKuboVersion() {
  const src = readFileSync('kubo/version.go', 'utf8');
  const m = src.match(/CurrentVersionNumber\s*=\s*"([^"]+)"/);
  if (!m) throw new Error('CurrentVersionNumber not found in kubo/version.go');
  return m[1];
}

function numeric(v) {
  const m = String(v).match(/(\d+)\.(\d+)\.(\d+)/);
  return m ? m.slice(1, 4).map(Number) : null;
}

function kuboBehind(upstreamTag, fork) {
  const a = numeric(upstreamTag);
  const b = numeric(fork);
  if (!a || !b) return null; // unparseable — surface, don't guess
  for (let i = 0; i < 3; i += 1) {
    if (a[i] !== b[i]) return a[i] > b[i];
  }
  return false;
}

sh('git', ['fetch', '-q', 'origin']);
const mainSha = sh('git', ['rev-parse', '--short=8', 'origin/main']);

const index = JSON.parse(sh('curl', ['-s', '-m', '20', feedIndexUrl]));
const newest = (index.updates || []).slice().sort((x, y) => y.sequence - x.sequence)[0];
let publishedCommit = null;
if (newest) {
  // provenance.sourceCommit rides in the bundle, not the index — the version
  // string carries it by construction (<prefix>.<shortsha>).
  const m = String(newest.version).match(/\.([0-9a-f]{8})$/);
  publishedCommit = m ? m[1] : null;
}

const upstream = upstreamKuboLatest();
const fork = forkKuboVersion();
const feedBehind =
  publishedCommit === null ? null : sh('git', ['rev-parse', '--short=8', 'origin/main']).startsWith(publishedCommit) ? false : true;

const verdict = {
  checkedAt: new Date().toISOString(),
  kubo: {
    upstream: upstream.tag,
    upstreamPublishedAt: upstream.publishedAt,
    fork,
    behind: kuboBehind(upstream.tag, fork),
  },
  feed: {
    indexUrl: feedIndexUrl,
    newestUpdateId: newest ? newest.update_id : null,
    newestSequence: newest ? newest.sequence : null,
    publishedCommit,
    mainSha,
    behind: feedBehind,
  },
  action:
    feedBehind === true
      ? 'publish: build locally (Docker linux/amd64) then deployment/release/publish-fleet-update.mjs, then `update install` per box'
      : kuboBehind(upstream.tag, fork)
        ? 'kubo drift: fork rebase required — file/refresh the graph task'
        : 'none',
};

console.log(JSON.stringify(verdict, null, 2));
process.exit(verdict.feed.behind === true || verdict.kubo.behind === true ? 3 : 0);
