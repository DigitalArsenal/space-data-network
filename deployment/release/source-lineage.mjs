/**
 * source-lineage — refuse to publish a binary that silently un-does code the
 * fleet is already running.
 *
 * WHY (two near-misses, both 2026-08-09):
 * a lane twice came within one command of installing a host-01 binary built
 * from a branch that did not contain fixes ALREADY LIVE on that host — first
 * the auth-CORS fix (e8c20f48), then the per-call work (e1c2bb2f). Neither was
 * caught by the update lane. Nothing in it objected, and nothing could have:
 * both candidates were well-formed, correctly signed, and carried a HIGHER
 * sequence than what was installed — because `sequence` is publish time, and
 * publish time says nothing whatsoever about code lineage. You can build a
 * three-week-old branch this afternoon and it will always look newer.
 *
 * The second one was stopped by a hand-posted ledger NOTICE that happened to
 * make the roller rebase. Vigilance caught both; structure should.
 *
 * THE TEST: the new artifact's source commit must be a DESCENDANT of (or equal
 * to) the source commit of the artifact currently newest on the feed —
 * `git merge-base --is-ancestor <live> <new>`. That is the exact question
 * "does this build contain everything the fleet already has?", and git answers
 * it definitively.
 *
 * Rollback stays possible: it just has to be SAID. `--rollback "<reason>"`
 * records the intent in the signed manifest, and the installer then requires
 * its own `--allow-rollback` to act on it. Deliberate reversion works; silent
 * reversion cannot.
 *
 * WHERE IT RUNS: at publish time, on the operator's machine, which is the only
 * place in the lane with a git repository — fleet hosts have none. The verdict
 * travels to them inside the signed manifest (ManifestProvenance), so the
 * installer enforces a claim it can trust rather than recomputing one it
 * cannot.
 */
import { execFileSync } from 'node:child_process';

export class SourceLineageRefusal extends Error {
  constructor(message, detail = {}) {
    super(message);
    this.name = 'SourceLineageRefusal';
    Object.assign(this, detail);
  }
}

export const LINEAGE_DESCENDANT = 'descendant';
export const LINEAGE_ROLLBACK = 'rollback';
export const LINEAGE_INITIAL = 'initial';

const defaultGit = (repoRoot) => (args) =>
  execFileSync('git', ['-C', repoRoot, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });

/**
 * Work out which source commit the fleet is converging on right now.
 *
 * Three sources, most to least direct. The fallbacks matter: every manifest
 * published before this existed carries no provenance, but the lane has always
 * encoded the commit in the VERSION string (`<prefix>.<shortsha>`), so ancestry
 * is checkable against the entire existing feed from day one rather than only
 * against publishes made after this shipped.
 *
 * @returns {{commit: string, version: string, sequence: number, via: string}|null}
 *          null means the feed has no prior artifact — an initial publish.
 */
export function resolveLiveSourceCommit({
  feedBaseUrl,
  channel,
  platform,
  arch,
  versionPrefix,
  fetch,
}) {
  const feedRel = `cli-bundle/${channel}/${platform}/${arch}`;
  const indexUrl = `${feedBaseUrl}/${feedRel}/index.json`;

  // "Could not reach the feed" and "the feed has no artifacts" must NOT collapse
  // into the same answer. If a transient network failure read as "initial
  // publish", every flaky moment would silently switch the ancestry guard off —
  // and a guard that disarms itself exactly when the world is unreliable is
  // worse than no guard, because it still looks armed. Unverifiable is not
  // verified (owner premise law): only a definite 404 means "nothing published
  // here yet".
  const response = fetch(indexUrl);
  if (response.status === 404) {
    return null;
  }
  if (response.status !== 200) {
    throw new SourceLineageRefusal(
      `could not read the update feed index at ${indexUrl} ` +
        `(${response.error ? response.error : `HTTP ${response.status}`}). Source-commit ancestry cannot be ` +
        'checked, so a build that silently reverts live code could not be detected — refusing rather than ' +
        'publishing with the guard effectively off.',
      { indexUrl, status: response.status },
    );
  }

  let index;
  try {
    index = JSON.parse(response.body);
  } catch (error) {
    throw new SourceLineageRefusal(
      `the update feed index at ${indexUrl} is not valid JSON (${error.message}) — refusing to publish against ` +
        'a feed whose current head cannot be determined.',
      { indexUrl },
    );
  }

  const updates = Array.isArray(index?.updates) ? index.updates : [];
  if (updates.length === 0) {
    return null;
  }
  const newest = updates.reduce((a, b) => (Number(b.sequence) > Number(a.sequence) ? b : a));

  if (typeof newest.source_commit === 'string' && newest.source_commit.length > 0) {
    return { commit: newest.source_commit, version: newest.version, sequence: newest.sequence, via: 'index entry' };
  }

  // Fallbacks may fail softly: unlike the index, they are not the difference
  // between "checked" and "unchecked" — the version-suffix derivation below
  // still yields an answer, and if nothing does, this function refuses.
  try {
    const manifestResponse = fetch(`${feedBaseUrl}/${feedRel}/${newest.version}/manifest.json`);
    if (manifestResponse.status === 200) {
      const manifest = JSON.parse(manifestResponse.body);
      const commit = manifest?.provenance?.source_commit;
      if (typeof commit === 'string' && commit.length > 0) {
        return { commit, version: newest.version, sequence: newest.sequence, via: 'manifest provenance' };
      }
    }
  } catch {
    /* fall through to the version-string derivation */
  }

  const prefix = `${versionPrefix}.`;
  if (typeof newest.version === 'string' && newest.version.startsWith(prefix)) {
    const commit = newest.version.slice(prefix.length);
    if (/^[0-9a-f]{7,40}$/i.test(commit)) {
      return { commit, version: newest.version, sequence: newest.sequence, via: 'version suffix' };
    }
  }

  throw new SourceLineageRefusal(
    `cannot determine the source commit of the artifact currently newest on the feed ` +
      `(${newest.update_id} / ${newest.version}). Ancestry is unverifiable, and an unverifiable ancestry ` +
      'is not a verified one — refusing rather than publishing blind.',
    { newest },
  );
}

/**
 * Decide, and refuse, on lineage.
 *
 * @param {string} repoRoot        the sdn git repository
 * @param {string} sourceCommit    the commit this artifact was built from
 * @param {?string} liveCommit     commit of the currently-newest published artifact
 * @param {?string} rollbackReason non-empty = the operator declares a rollback
 * @param {Function} [git]         injected git runner (tests)
 */
export function assertSourceLineage({ repoRoot, sourceCommit, liveCommit, rollbackReason = '', git = defaultGit(repoRoot) }) {
  const resolve = (ref, label) => {
    try {
      return git(['rev-parse', '--verify', `${ref}^{commit}`]).trim();
    } catch {
      throw new SourceLineageRefusal(
        `${label} commit ${ref} is not present in ${repoRoot}. A binary whose source cannot be identified ` +
          'cannot be reasoned about — fetch the commit (or publish from the repo it was built in) and retry.',
        { ref, label },
      );
    }
  };

  const source = resolve(sourceCommit, 'source');

  if (!liveCommit) {
    return { lineage: LINEAGE_INITIAL, sourceCommit: source, supersedesCommit: '', reason: '' };
  }
  const live = resolve(liveCommit, 'live');

  if (isAncestor(git, live, source)) {
    // Equal counts: republishing the same commit is not a regression.
    return { lineage: LINEAGE_DESCENDANT, sourceCommit: source, supersedesCommit: live, reason: '' };
  }

  if (rollbackReason && rollbackReason.trim().length > 0) {
    return {
      lineage: LINEAGE_ROLLBACK,
      sourceCommit: source,
      supersedesCommit: live,
      reason: rollbackReason.trim(),
    };
  }

  let mergeBase = '(none)';
  try {
    mergeBase = git(['merge-base', live, source]).trim();
  } catch {
    /* unrelated histories */
  }
  const missing = describeMissing(git, source, live);

  throw new SourceLineageRefusal(
    `SILENT REVERT REFUSED: this build's source commit ${short(source)} is NOT a descendant of ${short(live)}, ` +
      `the commit the fleet's newest published artifact was built from. Their common ancestor is ${short(mergeBase)}. ` +
      `Publishing it would take the fleet BACKWARDS over ${missing}. ` +
      'Rebase onto the live commit and rebuild, or — if reverting is what you actually mean — ' +
      're-run with --rollback "<why>", which records the intent in the signed manifest and requires ' +
      '`update install --allow-rollback` on every host that accepts it.',
    { sourceCommit: source, liveCommit: live, mergeBase },
  );
}

function isAncestor(git, ancestor, descendant) {
  try {
    git(['merge-base', '--is-ancestor', ancestor, descendant]);
    return true;
  } catch (error) {
    // git exits 1 for "not an ancestor" and >1 for real errors. Only 1 is an
    // answer; anything else is a broken question and must not read as "no".
    if (error.status === 1) return false;
    throw new SourceLineageRefusal(
      `git could not decide whether ${short(ancestor)} is an ancestor of ${short(descendant)}: ${error.message}`,
      { ancestor, descendant },
    );
  }
}

function describeMissing(git, source, live) {
  try {
    const count = git(['rev-list', '--count', `${source}..${live}`]).trim();
    return `${count} commit(s) it does not contain`;
  } catch {
    return 'commits it does not contain';
  }
}

const short = (sha) => (typeof sha === 'string' && sha.length > 12 ? sha.slice(0, 12) : String(sha));
