/*
 * WHAT IS THIS PEER HOSTING? — the $PMM read, verified before a word of it is
 * rendered.
 *
 * Owner directive 2026-07-30: "clicking on a node shows what's being hosted on
 * that node in terms of modules, both data and functionality."
 *
 * THE ONLY SANCTIONED SOURCE is the peer's own signed Provider Module Manifest,
 * served anonymously at `/.well-known/sdn/modules.pmm` with
 * `Access-Control-Allow-Origin: *` (sdn-server/internal/pmm/handler.go:13,93).
 * It is fetched FROM THE PEER, not through this node: the standard's step 4
 * ("node key -> domain") is checked here as "the manifest was served by the
 * domain it claims", and a same-origin proxy would manufacture that failure for
 * every peer. This is the same refusal Themis made for /beta's storefront.
 *
 * THE REQUEST STAYS SIMPLE, deliberately. `Accept` is a CORS-SAFELISTED request
 * header, so `fetch(url, {headers: {Accept: 'application/json'}})` is a simple
 * cross-origin GET and never preflights — which matters, because the handler
 * answers OPTIONS with 405 and no CORS headers (handler.go:63-67, measured
 * against the live node 2026-07-31). One custom header here and every peer's
 * manifest would fail to load with a message about a method nobody used. No
 * credentials, no cookies: this read is anonymous by construction.
 *
 * NOTHING RENDERS UNVERIFIED. A manifest that does not verify renders NOTHING —
 * not a greyed list — because a greyed list of names still reads as "what this
 * provider offers", and a failed signature means we do not know that it is
 * theirs (Iris ruling, /beta storefront, ported verbatim in spirit from
 * repos/main-packages/spaceaware.io/beta/src/store/verifyPmm.ts).
 *
 * GROUPING IS `PLUGIN_TYPE` AND ONLY `PLUGIN_TYPE` (schema/PMM/main.fbs — "a
 * client grouping a catalogue MUST read this field and MUST NOT infer family
 * from the shape of MODULE_ID, which carries no normative structure"). An
 * absent or `Unspecified` family renders ungrouped and SAYS SO: live nodes
 * predate the field, and inventing a family for them would be the `Config
 * Trusted Peer` lesson repeated in a new column.
 *
 * Pure: no Svelte, no design imports, no DOM. `fetchManifest` takes its own
 * `fetch` so vitest never touches the network.
 */

/** Where every node serves it. Origin-relative, same on every host. */
export const MANIFEST_PATH = '/.well-known/sdn/modules.pmm';

/* --------------------------------------------------------------------------
 * 1. WHERE TO ASK
 * ------------------------------------------------------------------------ */

/**
 * A NAME WE CAN SPEAK HTTPS TO, taken from what the peer published — never
 * synthesized.
 *
 * Only `/dns4/`, `/dns6/` and `/dnsaddr/` yield one. An `/ip4/198.51.100.20/...`
 * address is deliberately NOT promoted to `https://198.51.100.20`: a browser
 * would refuse the certificate, and even if it did not, `PROVIDER_DOMAIN` can
 * never equal an IP literal, so every such fetch would end in a domain-mismatch
 * rejection that describes the address we invented rather than anything about
 * the peer. A peer reachable only by IP has no verifiable manifest origin, and
 * that is a state with its own sentence (`no-https-origin`), not an error.
 *
 * @param {string} addr a multiaddr
 * @returns {string} hostname, or '' when the address names none
 */
export function hostFromMultiaddr(addr) {
  const parts = String(addr ?? '').split('/').filter(Boolean);
  for (let i = 0; i < parts.length - 1; i += 1) {
    if (parts[i] === 'dns4' || parts[i] === 'dns6' || parts[i] === 'dnsaddr' || parts[i] === 'dns') {
      const host = parts[i + 1].trim().toLowerCase();
      // A hostname, not a path fragment and not a bare label with a slash in it.
      if (host && /^[a-z0-9.-]+$/.test(host)) return host;
    }
  }
  return '';
}

/**
 * Every host this peer's own records name, in the order they were published.
 * `addrs` is what the node observed; the vCard's X-SDN-MULTIADDR values are what
 * the peer says about itself. Both are the peer's claims; neither is invented
 * here, and duplicates collapse.
 *
 * @param {{addrs?: string[], vcardAddrs?: string[]}} node
 */
export function manifestHosts({ addrs = [], vcardAddrs = [] } = {}) {
  const out = [];
  for (const addr of [...(addrs ?? []), ...(vcardAddrs ?? [])]) {
    const host = hostFromMultiaddr(addr);
    if (host && !out.includes(host)) out.push(host);
  }
  return out;
}

/**
 * The origin to fetch from: `https://<first published dns host>`, or '' when the
 * peer published none. HTTPS ALWAYS — this page is served over https and a plain
 * http fetch is mixed content the browser blocks without telling the operator
 * anything they could act on.
 */
export function manifestOrigin(node) {
  const [host] = manifestHosts(node);
  return host ? `https://${host}` : '';
}

/** The full URL, for display as well as for fetching. */
export function manifestURL(node) {
  const origin = manifestOrigin(node);
  return origin ? `${origin}${MANIFEST_PATH}` : '';
}

/* --------------------------------------------------------------------------
 * 2. WHAT CAME BACK
 * ------------------------------------------------------------------------ */

const HEX = /^[0-9a-f]*$/;

/**
 * Hex (what the JSON projection emits for SIGNATURE) or a byte array (what a
 * FlatBuffer decode would hand us) → bytes. Anything else is not a signature.
 * @returns {Uint8Array|null}
 */
export function toBytes(value) {
  if (value instanceof Uint8Array) return value;
  if (Array.isArray(value)) {
    if (value.some((b) => !Number.isInteger(b) || b < 0 || b > 255)) return null;
    return new Uint8Array(value);
  }
  if (typeof value !== 'string') return null;
  const clean = value.trim().toLowerCase();
  if (!clean || clean.length % 2 !== 0 || !HEX.test(clean)) return null;
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = Number.parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  return out;
}

/**
 * The record, in the shape the rest of this module reads. SDS keys keep their
 * IDL capitalization exactly (standing rule) — this function renames nothing, it
 * only fills in the fields a partial record left out so no consumer has to guard
 * for `undefined`.
 */
export function normalizeManifest(json) {
  const m = json && typeof json === 'object' ? json : {};
  const trust = m.TRUST && typeof m.TRUST === 'object' ? m.TRUST : {};
  return {
    PROVIDER_DOMAIN: String(m.PROVIDER_DOMAIN ?? ''),
    PROVIDER_NAME: String(m.PROVIDER_NAME ?? ''),
    DESCRIPTION: String(m.DESCRIPTION ?? ''),
    EPOCH: Number(m.EPOCH ?? 0),
    TRUST: {
      PROVIDER_DOMAIN: String(trust.PROVIDER_DOMAIN ?? ''),
      NODE_PEER_ID: String(trust.NODE_PEER_ID ?? ''),
      NODE_XPUB: String(trust.NODE_XPUB ?? ''),
      SIGNING_PUBLIC_KEY: String(trust.SIGNING_PUBLIC_KEY ?? ''),
      SIGNING_KEY_PATH: String(trust.SIGNING_KEY_PATH ?? ''),
      SIGNATURE_ALGORITHM: String(trust.SIGNATURE_ALGORITHM ?? ''),
    },
    MODULES: Array.isArray(m.MODULES) ? m.MODULES : [],
    CANONICAL_URL: String(m.CANONICAL_URL ?? ''),
    EXPIRES_AT: String(m.EXPIRES_AT ?? ''),
    UPDATED_AT: String(m.UPDATED_AT ?? ''),
    SIGNATURE: m.SIGNATURE ?? '',
    SIGNED_STATEMENT: String(m.SIGNED_STATEMENT ?? ''),
  };
}

const encoder = new TextEncoder();

/**
 * Bytewise UTF-8 ordering. `sort()` compares UTF-16 code units, which is a
 * DIFFERENT order above the BMP: one astral character in a MODULE_ID would
 * reorder the statement and the signature would fail with no diagnosable cause.
 */
export function compareUtf8(left, right) {
  const a = encoder.encode(left);
  const b = encoder.encode(right);
  const shared = Math.min(a.length, b.length);
  for (let i = 0; i < shared; i += 1) if (a[i] !== b[i]) return a[i] - b[i];
  return a.length - b.length;
}

/**
 * Rebuild the exact bytes `PMM.SIGNATURE` covers, from the RECORD'S OWN FIELDS.
 *
 * The standard requires the rebuild and requires rejecting any difference from a
 * carried `SIGNED_STATEMENT` (schema/PMM/main.fbs). Verifying the carried string
 * instead would only prove the provider can sign their own sentence, not that
 * the sentence describes the modules we are about to list. Mirrors
 * sdn-server/internal/pmm/manifest.go:253-274 line for line: UTF-8, LF, every
 * line terminated including the last, enums as IDL symbol names, booleans as
 * 1/0, modules sorted bytewise ascending by MODULE_ID.
 */
export function buildCanonicalStatement(manifest) {
  const m = normalizeManifest(manifest);
  const lines = [
    'SDN-MODULE-MANIFEST-V1',
    `domain:${m.PROVIDER_DOMAIN}`,
    `peer:${m.TRUST.NODE_PEER_ID}`,
    `key:${m.TRUST.SIGNING_PUBLIC_KEY}`,
    `epoch:${String(m.EPOCH)}`,
    `expires:${m.EXPIRES_AT}`,
  ];
  const ordered = [...m.MODULES].sort((a, b) =>
    compareUtf8(String(a?.MODULE_ID ?? ''), String(b?.MODULE_ID ?? ''))
  );
  for (const e of ordered) {
    lines.push(
      `module:${[
        String(e?.MODULE_ID ?? ''),
        String(e?.VERSION ?? ''),
        String(e?.CONTENT_HASH ?? ''),
        String(e?.TRUST_TIER ?? ''),
        String(e?.ACCESS_POLICY ?? ''),
        e?.DEFAULT_ENABLED ? '1' : '0',
        String(e?.ENTRY_STATE ?? ''),
      ].join(' ')}`
    );
  }
  return `${lines.join('\n')}\n`;
}

/**
 * ONE REASON, in the provider's own vocabulary, and the surface refuses on it.
 * Every string here is a SENTENCE, never an HTTP code: the owner has already
 * caught this page rendering `HTTP 521` as if it were a state.
 */
export const VERIFY_DETAIL = {
  NO_TRUST_ANCHOR: 'This manifest carries no signing key to check it against.',
  DOMAIN_MISMATCH: 'The manifest names a different provider than the address it came from.',
  STATEMENT_MISMATCH: "The manifest's own text does not match the modules it lists.",
  EPOCH_ROLLBACK: 'This is an older manifest than one already verified for this provider.',
  MANIFEST_EXPIRED: 'This manifest has expired. The provider has to re-sign it before it can be believed.',
  SIGNATURE_UNSUPPORTED: 'This browser cannot check the signature algorithm this provider signed with.',
  SIGNATURE_INVALID: 'The signature does not verify against the key the manifest names.',
};

/** Ed25519 is the default; an empty SIGNATURE_ALGORITHM means ed25519. */
const isEd25519 = (alg) => {
  const named = String(alg ?? '').trim().toLowerCase();
  return named === '' || named === 'ed25519';
};

async function verifyEd25519(subtle, publicKey, signature, message) {
  const key = await subtle.importKey('raw', publicKey, { name: 'Ed25519' }, false, ['verify']);
  return subtle.verify({ name: 'Ed25519' }, key, signature, message);
}

/**
 * May this manifest be believed? Fails closed at every step, in the order the
 * standard mandates.
 *
 * @param {object} manifest the record as served
 * @param {{servedFrom: string, now?: Date, subtle?: SubtleCrypto|null,
 *          highestVerifiedEpoch?: number|null}} options
 * @returns {Promise<{ok: true, statement: string} | {ok: false, reason: string, detail: string}>}
 */
export async function verifyManifest(manifest, options = {}) {
  const m = normalizeManifest(manifest);
  const reject = (reason) => ({ ok: false, reason, detail: VERIFY_DETAIL[reason] });

  if (!m.TRUST.SIGNING_PUBLIC_KEY) return reject('NO_TRUST_ANCHOR');

  // The checkable half of "node key -> domain": the bytes came from the domain
  // they claim. The DNS proof itself needs a resolver, and this page resolves
  // nothing off-origin, so what we cannot check we do not imply.
  let servedHost = '';
  try {
    servedHost = new URL(options.servedFrom ?? '').hostname.toLowerCase();
  } catch {
    servedHost = '';
  }
  const claimed = m.PROVIDER_DOMAIN.trim().toLowerCase();
  const anchorClaim = m.TRUST.PROVIDER_DOMAIN.trim().toLowerCase();
  if (!claimed || claimed !== servedHost || (anchorClaim && anchorClaim !== servedHost)) {
    return reject('DOMAIN_MISMATCH');
  }

  const statement = buildCanonicalStatement(m);
  if (m.SIGNED_STATEMENT && m.SIGNED_STATEMENT !== statement) return reject('STATEMENT_MISMATCH');

  // Rollback is checked BEFORE the signature: a correctly-signed OLD manifest is
  // precisely the attack, and its signature being valid is not a defence.
  const highest = options.highestVerifiedEpoch;
  if (highest !== null && highest !== undefined && m.EPOCH < highest) return reject('EPOCH_ROLLBACK');

  if (m.EXPIRES_AT) {
    const expiry = Date.parse(m.EXPIRES_AT);
    const now = (options.now ?? new Date()).getTime();
    if (Number.isFinite(expiry) && expiry <= now) return reject('MANIFEST_EXPIRED');
  }

  if (!isEd25519(m.TRUST.SIGNATURE_ALGORITHM)) return reject('SIGNATURE_UNSUPPORTED');

  const publicKey = toBytes(m.TRUST.SIGNING_PUBLIC_KEY);
  const signature = toBytes(m.SIGNATURE);
  if (!publicKey || publicKey.length !== 32) return reject('SIGNATURE_INVALID');
  if (!signature || signature.length !== 64) return reject('SIGNATURE_INVALID');

  // `subtle: null` MEANS null — a caller stating "this environment has no
  // WebCrypto" must not be silently handed the global one. `??` would do exactly
  // that, and the test for the no-WebCrypto branch would pass by verifying for
  // real. Presence of the key is the switch, not its truthiness.
  const subtle = 'subtle' in options ? options.subtle : globalThis.crypto?.subtle ?? null;
  if (!subtle) return reject('SIGNATURE_UNSUPPORTED');

  let valid = false;
  try {
    valid = await verifyEd25519(subtle, publicKey, signature, encoder.encode(statement));
  } catch {
    // WebCrypto Ed25519 is not universal. A browser that cannot check it says so
    // and the list stays closed — it never quietly passes.
    return reject('SIGNATURE_UNSUPPORTED');
  }
  return valid ? { ok: true, statement } : reject('SIGNATURE_INVALID');
}

/* --------------------------------------------------------------------------
 * 3. WHAT IT SAYS
 * ------------------------------------------------------------------------ */

/**
 * DATA vs FUNCTIONALITY — THE OWNER'S TWO WORDS, AND WHAT THEY ARE NOT.
 *
 * THEMIS RULED (consult 2026-07-31, task
 * sdn-peers-search-trust-column-hosted-modules): the two-bucket split is a UI
 * META-HEADER, never schema-sanctioned. It may sit ABOVE the mandatory
 * per-`PLUGIN_TYPE` sections and must never replace them — which is why
 * `offeringSections` renders bucket → FAMILY → entries and never bucket →
 * entries. The families are the standard's; the two words above them are this
 * page's, answering the owner's sentence.
 *
 * The DATA side is the families whose SUBJECT is records: a module that fetches,
 * parses, validates, interpolates or exports them.
 *
 * TWO FAMILIES ARE ON NEITHER SIDE, both by ruling:
 *   · `Unspecified` — a standalone ungrouped section. The standard requires it
 *     be rendered as "not stated", never folded into a family, and live nodes
 *     predate the field entirely.
 *   · `Flow` — a composed graph of other modules, mixed by construction. Themis
 *     declined to place it, so this page does not place it either: it gets its
 *     own section rather than being forced into a bucket that would misstate it.
 */
export const DATA_FAMILIES = ['DataSource', 'Parser', 'Validator', 'Interpolator', 'Exporter'];

/** Families the standard does not place on either side. Rendered on their own. */
export const UNPLACED_FAMILIES = ['Flow'];

/**
 * THE FAMILY IS NOT COVERED BY THE SIGNATURE — said out loud, because it is the
 * one thing on this surface that the manifest's signature does not vouch for.
 *
 * The canonical statement's `module:` line carries MODULE_ID, VERSION,
 * CONTENT_HASH, TRUST_TIER, ACCESS_POLICY, DEFAULT_ENABLED and ENTRY_STATE
 * (sdn-server/internal/pmm/manifest.go:270-271). `PLUGIN_TYPE` is NOT among
 * them (Themis: "PLUGIN_TYPE is absent from the signed statement —
 * unauthenticated, flag as tamper-risk"). So the sections on this page are the
 * provider's unauthenticated claim about how to file its modules, while the
 * identities, versions and hashes inside them are signed. The page says which is
 * which rather than letting a verified badge cover both.
 */
export const GROUPING_UNSIGNED_NOTE =
  'Module identity, version, content hash, tier, access and state are covered by the signature. The FAMILY headings are not — they are the provider’s own filing, and the signed statement does not carry them.';

/**
 * Every family the standard defines, in IDL order, with the words this page
 * renders. A family arriving that is NOT in this table is shown VERBATIM rather
 * than folded into "not stated": the enum is append-only, and a node newer than
 * this page is not a node with no answer.
 */
export const FAMILY_LABEL = {
  Sensor: 'SENSOR',
  Propagator: 'PROPAGATOR',
  Renderer: 'RENDERER',
  Analysis: 'ANALYSIS',
  DataSource: 'DATA SOURCE',
  EW: 'ELECTRONIC WARFARE',
  Comms: 'COMMUNICATIONS',
  Physics: 'PHYSICS',
  Shader: 'SHADER',
  Parser: 'PARSER',
  Validator: 'VALIDATOR',
  Interpolator: 'INTERPOLATOR',
  Exporter: 'EXPORTER',
  Foundation: 'FOUNDATION',
  Infrastructure: 'INFRASTRUCTURE',
  Licensing: 'LICENSING',
  Storefront: 'STOREFRONT',
  Publisher: 'PUBLISHER',
  Basilisk: 'BASILISK',
  Maneuver: 'MANEUVER',
  Flow: 'FLOW',
};

/**
 * The bucket a family belongs to:
 * 'data' | 'functionality' | 'unplaced' | 'unstated'.
 */
export function familyBucket(pluginType) {
  const raw = String(pluginType ?? '').trim();
  if (!raw || raw === 'Unspecified') return 'unstated';
  if (UNPLACED_FAMILIES.includes(raw)) return 'unplaced';
  return DATA_FAMILIES.includes(raw) ? 'data' : 'functionality';
}

/**
 * The family's words. An absent or `Unspecified` family is FAMILY NOT STATED —
 * the standard requires it be rendered ungrouped and never as a member of any
 * family, and live nodes predate the field entirely.
 */
export function familyLabel(pluginType) {
  const raw = String(pluginType ?? '').trim();
  if (!raw || raw === 'Unspecified') return 'FAMILY NOT STATED';
  return FAMILY_LABEL[raw] ?? raw.toUpperCase();
}

/** Lifecycle, as words. ACTIVE is the norm and earns no badge. */
export function entryStateLabel(state) {
  const raw = String(state ?? '').trim().toUpperCase();
  if (raw === 'DEPRECATED') return 'DEPRECATED';
  if (raw === 'WITHDRAWN') return 'WITHDRAWN';
  if (raw === 'REVOKED') return 'REVOKED';
  return '';
}

/** Who may load it, as words — the fact an operator reading a peer cares about. */
export function accessLabel(policy) {
  const raw = String(policy ?? '').trim().toUpperCase();
  if (raw === 'ANONYMOUS') return 'ANONYMOUS';
  if (raw === 'AUTHENTICATED') return 'SIGN-IN REQUIRED';
  if (raw === 'ENTITLED') return 'LICENCE REQUIRED';
  return '';
}

/**
 * The offering, sectioned for the screen: two buckets in the owner's words, each
 * a list of families, each family a list of entries. A bucket with no entries is
 * omitted rather than rendered empty — except that when the WHOLE manifest is
 * ungrouped, the third bucket carries everything and states why.
 *
 * Entries sort by MODULE_ID bytewise, the same order the signed statement uses,
 * so what is on screen is in the order the signature covers.
 */
export function offeringSections(modules) {
  const buckets = { data: new Map(), functionality: new Map(), unplaced: new Map(), unstated: new Map() };
  for (const entry of modules ?? []) {
    const family = String(entry?.PLUGIN_TYPE ?? '').trim();
    const bucket = buckets[familyBucket(family)];
    const key = familyLabel(family);
    if (!bucket.has(key)) bucket.set(key, []);
    bucket.get(key).push(entry);
  }
  const render = (id, label, map) => ({
    id,
    label,
    families: [...map.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([label2, entries]) => ({
        label: label2,
        entries: [...entries].sort((a, b) =>
          compareUtf8(String(a?.MODULE_ID ?? ''), String(b?.MODULE_ID ?? ''))
        ),
      })),
    count: [...map.values()].reduce((n, list) => n + list.length, 0),
  });
  return [
    render('data', 'DATA', buckets.data),
    render('functionality', 'FUNCTIONALITY', buckets.functionality),
    render('unplaced', 'COMPOSED FLOWS', buckets.unplaced),
    render('unstated', 'FAMILY NOT STATED', buckets.unstated),
  ].filter((section) => section.count > 0);
}

/* --------------------------------------------------------------------------
 * 4. THE READ, AS A STATE
 * ------------------------------------------------------------------------ */

/**
 * Fetch one peer's manifest. Simple GET, anonymous, JSON by Accept — see this
 * file's header for why not one byte more than that goes on the wire.
 *
 * Returns a RESULT, never throws: the caller renders states, and an exception is
 * not one of them.
 *
 * @param {string} origin e.g. `https://sdn.spaceaware.io`
 * @param {{fetchImpl?: typeof fetch, signal?: AbortSignal}} [opts]
 */
export async function fetchManifest(origin, { fetchImpl = globalThis.fetch, signal } = {}) {
  if (!origin) return { ok: false, reason: 'no-origin' };
  const url = `${origin}${MANIFEST_PATH}`;
  let res;
  try {
    res = await fetchImpl(url, {
      method: 'GET',
      headers: { Accept: 'application/json' },
      credentials: 'omit',
      redirect: 'follow',
      signal,
    });
  } catch {
    // A refused connection, a DNS failure, a CORS refusal and an offline machine
    // are ONE fact to an operator: this node could not be reached from here.
    return { ok: false, reason: 'unreachable', url };
  }
  if (!res?.ok) return { ok: false, reason: 'not-served', status: Number(res?.status ?? 0), url };
  let json;
  try {
    json = await res.json();
  } catch {
    return { ok: false, reason: 'not-a-manifest', url };
  }
  return { ok: true, manifest: normalizeManifest(json), url, servedFrom: url };
}

/**
 * THE WHOLE READ, AS ONE SENTENCE THE SCREEN CAN SHOW.
 *
 * Every branch is a state with words. `status` is carried for the DETAIL
 * affordance the peer modal already uses for raw codes — it is never the answer
 * (the owner saw `HTTP 521` once; that was a defect, not a design).
 *
 * @returns {{id: string, title: string, detail: string, code?: string}}
 */
export function hostedState({
  phase = 'idle',
  origin = '',
  fetchReason = '',
  status = 0,
  verifyReason = '',
  count = 0,
} = {}) {
  // NOTHING HERE INFERS NAT, A FIREWALL OR REACHABILITY (PeerEditModal's
  // standing rule, restated by Iris 2026-07-31): this page cannot measure any of
  // them. "No https origin" is a fact about what the peer published. What that
  // MEANS about the peer's network is the operator's inference to make, not this
  // sentence's to assert — the last time this surface explained a peer to itself
  // it printed `HTTP 521` and the owner had to correct it.
  if (!origin) {
    return {
      id: 'no-origin',
      title: 'MODULES NOT READ',
      detail: 'This node has not reached an https origin for this peer.',
    };
  }
  if (phase === 'reading') {
    return { id: 'reading', title: 'READING THE MANIFEST…', detail: `Asking ${origin} what it hosts.` };
  }
  if (phase !== 'done') {
    return { id: 'idle', title: '', detail: '' };
  }
  if (fetchReason === 'unreachable') {
    return {
      id: 'unreachable',
      title: 'MODULES NOT READ',
      // The manifest is read FROM THE PEER, so "your browser could not reach it"
      // is the measured fact and the only one. It is deliberately not the same
      // sentence as "this peer is down".
      detail: `This browser did not reach ${origin}. The manifest is read from the peer directly, never through this node.`,
    };
  }
  if (fetchReason === 'not-served') {
    return {
      id: 'not-served',
      title: 'MODULES NOT READ',
      detail: 'A server answered, but did not serve a $PMM manifest.',
      // The raw code lives behind an explicit DETAIL affordance, never inline —
      // the correction the owner already made once on this surface.
      code: status ? `HTTP ${status}` : '',
    };
  }
  if (fetchReason === 'not-a-manifest') {
    return {
      id: 'not-a-manifest',
      title: 'MODULES NOT READ',
      detail: 'A server answered, but did not serve a $PMM manifest.',
    };
  }
  if (verifyReason) {
    // NOTHING BUT THE REASON (Iris 2026-07-31, and /beta's precedent): a
    // manifest that does not verify renders NO LIST — not a greyed one, not a
    // count, not a family header. A greyed list still reads as "what this
    // provider offers", and a failed signature is exactly the state in which we
    // do not know that it is theirs.
    return {
      id: 'unverified',
      title: 'MANIFEST NOT VERIFIED',
      detail: VERIFY_DETAIL[verifyReason] ?? 'The manifest could not be verified.',
      code: verifyReason,
    };
  }
  if (!count) {
    return {
      id: 'empty',
      title: 'THIS PEER HOSTS NO MODULES',
      detail: 'Its manifest verified, and it lists none.',
    };
  }
  return { id: 'ok', title: '', detail: '' };
}
