// The per-node seal every release carries (owner 2026-10-07: UI packages
// first, then "updates even the binary need to be encrypted per node since
// they go over noise protocol"). One place for who the recipients are, how the
// publisher host seals for them, and the check that what comes back is sealed
// for exactly them; publish-ui-update.mjs and publish-fleet-update.mjs both
// use it.

import { readFileSync } from 'node:fs';

const shellQuote = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`;

/** The nodes in nodesFile that run on platform/arch ('any' takes every node). */
export function recipientNodes({ nodesFile, platform = 'any', arch = 'any' }) {
  const fleet = JSON.parse(readFileSync(nodesFile, 'utf8'));
  return (fleet.nodes ?? []).filter(
    (node) => platform === 'any' || ((node.platform ?? 'linux') === platform && (node.arch ?? 'amd64') === arch),
  );
}

/**
 * Each node in nodesFile (fleet-nodes.json) that runs on platform/arch, as an
 * envelope recipient: the sealed-transport key and fingerprint its own
 * /api/node/info advertises, and the build it runs (/api/v1/id). A node with
 * an `ssh` host is asked on that host; one without is asked directly.
 * platform 'any' takes every node. refuse(message) throws the caller's
 * refusal.
 */
export function collectRecipients({ nodesFile, platform = 'any', arch = 'any', run, log, refuse, tag }) {
  const recipients = [];
  for (const node of recipientNodes({ nodesFile, platform, arch })) {
    const ask = (route) => {
      const url = node.node_url + route;
      const body = node.ssh
        ? run('ssh', ['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', node.ssh, `curl -sk -m 15 ${shellQuote(url)}`])
        : run('curl', ['-sk', '-m', '15', url]);
      return JSON.parse(body.toString());
    };
    const info = ask('/api/node/info');
    const id = ask('/api/v1/id');
    const sealed = info.sealed_transport ?? {};
    if (!/^0[23][0-9a-f]{64}$/.test(sealed.encryption_key ?? '') || !sealed.fingerprint) {
      refuse(`${node.name} advertises no sealed-transport key at ${node.node_url}/api/node/info; it could not open the release`);
    }
    if (!id.bundle_version) refuse(`${node.name} reports no bundle_version; it is not a bundle install the lane serves`);
    recipients.push({
      name: node.name,
      peer_id: info.peer_id,
      encryption_key: sealed.encryption_key,
      fingerprint: sealed.fingerprint,
      bundle_version: id.bundle_version,
    });
    log(`[${tag}] recipient ${node.name} ${String(info.peer_id).slice(-8)} key ${sealed.fingerprint} build ${id.bundle_version}`);
  }
  if (recipients.length === 0) refuse(`${nodesFile} names no node on ${platform}/${arch}`);
  return recipients;
}

/** The recipients file `update seal --recipients` reads. */
export function recipientsDocument(recipients) {
  return `${JSON.stringify(
    recipients.map(({ name, peer_id, encryption_key, fingerprint }) => ({ name, peer_id, encryption_key, fingerprint })),
    null,
    2,
  )}\n`;
}

/**
 * `update seal` on the publisher host: remoteDir holds manifest.unsigned.json
 * and recipients.json; the bundle is at bundleRemote. Writes
 * manifest.sealed.json (the envelope recorded, wasm.hash the ciphertext's) and
 * update.wasm (the ciphertext carrier) beside them.
 */
export function sealOnPublisher({ run, publisherSSH, publisherBin, remoteDir, bundleRemote, log, tag }) {
  const out = run('ssh', [
    publisherSSH,
    `${shellQuote(publisherBin)} update seal --manifest ${remoteDir}/manifest.unsigned.json ` +
      `--bundle ${bundleRemote} --recipients ${remoteDir}/recipients.json ` +
      `--out-manifest ${remoteDir}/manifest.sealed.json --out-carrier ${remoteDir}/update.wasm`,
  ]);
  log(out.toString().trim().replace(/^/gm, `[${tag}] `));
}

/** Refuses unless manifest's envelope is sealed for exactly recipients. */
export function assertSealedFor(manifest, recipients, refuse) {
  const sealedFor = new Set((manifest.envelope?.recipients ?? []).map((r) => Buffer.from(r.key_id, 'base64').toString()));
  const missing = recipients.filter((r) => !sealedFor.has(r.fingerprint));
  if (missing.length || sealedFor.size !== recipients.length) {
    refuse(`the envelope is not sealed for exactly the recipients (missing: ${missing.map((r) => r.name).join(', ') || 'none'})`);
  }
}
