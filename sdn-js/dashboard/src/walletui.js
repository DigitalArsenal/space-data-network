/*
 * hd-wallet-ui, used DIRECTLY, in our page (owner directive 2026-07-27;
 * contract nst-node-admin-contract §11).
 *
 * OWNER LAW: "we do NOT load anything from a site." Every byte here comes
 * from THIS node — `/wallet-ui/*` and `/wallet-wasm/*`, staged by
 * deployment/wallet-wasm/stage-wallet-wasm.sh. There is no CDN fallback and
 * there must never be one: if the assets are unstaged the UI says SIGN-IN
 * UNAVAILABLE. The relay is injected and runs IN-PROCESS, so the wallet
 * performs no network I/O of its own at all.
 *
 * WHY THE MANUAL DRIVE (§11.4 "drive it through controller.prepare() /
 * executePrepared()"), and not `app.start()`:
 *   1. `start()` reads the transaction id out of `window.location` and
 *      rejects unless the path is exactly `/transaction/<64 hex>`.
 *   2. Worse — and decisive — `start()` NEVER hands back the identity. Its
 *      raw-challenge branch discards it, and the signed result carries only
 *      `keyId: "sha256:<hex>"`, never the Ed25519 public key §4 needs as
 *      `client_pubkey_hex`. `controller.unlockLegacy()` returns the public
 *      identity, which is the only place that key exists.
 *   3. That also fixes an ordering trap: the transaction embeds the
 *      challenge, so a `start()`-driven flow would have to mint the
 *      challenge BEFORE the operator unlocks — i.e. before the key it is
 *      bound to is knowable. Unlocking first removes the paradox.
 * So: unlock (wallet) → mint challenge (node) → prepare + executePrepared
 * (wallet: confirmation dialog, signing, result codec) → verify (node).
 *
 * Everything cryptographic stays inside the wallet: it reads the credential
 * controls, derives, signs, and wipes the inputs itself. The phrase and the
 * keys never enter our code and never leave the page.
 */

/** Same-origin entry points (contract §11.3/§11.4). */
export const WALLET_UI_ENTRY = '/wallet-ui/wallet-origin/index.js';
export const WALLET_UI_COMPAT = '/wallet-ui/compat/index.js';

/** The only operation this node can verify today (§11.4 / §12). */
export const RAW_CHALLENGE_OPERATION = 'sdn.auth.raw-challenge.v1';

/** The two legacy profiles the operation forces (§11.2). */
export const LEGACY_PROFILES = [
  {
    id: 'bip39-mnemonic-v1-legacy',
    label: 'RECOVERY PHRASE',
    hint: 'Sign in with your BIP-39 recovery phrase.',
  },
  {
    id: 'password-fast-v1-legacy',
    label: 'USERNAME & PASSWORD',
    hint: 'Sign in with the username and password your wallet was created from.',
  },
];

const HEX64 = /^[0-9a-f]{64}$/;

/** Random lowercase hex, `bytes` long (32 → the 64-hex transaction ids). */
export function randomHex(bytes = 32) {
  const buf = new Uint8Array(bytes);
  globalThis.crypto?.getRandomValues?.(buf);
  let out = '';
  for (const b of buf) out += b.toString(16).padStart(2, '0');
  return out;
}

/** Random 32 bytes as unpadded base64url — the `resultToken` shape (43 chars). */
export function randomResultToken() {
  const buf = new Uint8Array(32);
  globalThis.crypto?.getRandomValues?.(buf);
  let bin = '';
  for (const b of buf) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/**
 * The node mints challenges as base64 RawStdEncoding (standard alphabet,
 * unpadded, §4). The wallet's `challengeBase64url` is the URL alphabet.
 * Same 32 bytes, different alphabet — this is a pure re-spelling, and the
 * ORIGINAL string is still what gets echoed back to /api/auth/verify.
 */
export function base64ToBase64Url(value) {
  return String(value ?? '').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/**
 * JCS-style canonical JSON, byte-identical to the wallet's own canonicalizer
 * (`yt` in wallet-origin/index.js): object keys sorted, no whitespace.
 * `requestSha256` must be the SHA-256 of this over the parsed request or the
 * wallet answers REQUEST_HASH_MISMATCH.
 */
export function canonicalJson(value) {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') {
    return JSON.stringify(value);
  }
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new TypeError('non-finite number is not canonicalizable');
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`;
  if (typeof value !== 'object') throw new TypeError('value is not canonicalizable');
  return `{${Object.keys(value)
    .sort()
    .map((k) => `${JSON.stringify(k)}:${canonicalJson(value[k])}`)
    .join(',')}}`;
}

async function sha256Hex(text) {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) throw new Error('This browser context has no SubtleCrypto (needs https or localhost).');
  const digest = await subtle.digest('SHA-256', new TextEncoder().encode(text));
  let out = '';
  for (const b of new Uint8Array(digest)) out += b.toString(16).padStart(2, '0');
  return out;
}

/** ISO-8601 with EXACTLY three fractional digits — the only shape accepted. */
export function isoMillis(epochMs) {
  return new Date(epochMs).toISOString();
}

/**
 * The per-node registry binding. The package's baked registry has exactly
 * ONE row for this operation (`sdn-node-console-v1` / `https://sdn.spaceaware.io`),
 * so every other node origin — localhost included — must inject its own
 * (§11.4). An injected registry also lifts the package's HTTPS-only check,
 * which is what makes `http://127.0.0.1:5001` work.
 */
export async function buildRegistryBinding(origin, displayName) {
  const clientId = 'sdn-node-console-v1';
  const requestOrigin = String(origin ?? '');
  const name = String(displayName ?? '').trim() || requestOrigin.replace(/^https?:\/\//, '');
  return Object.freeze({
    callbackUri: `${requestOrigin}/`,
    clientDisplayName: name.slice(0, 80),
    clientId,
    maxLifetimeSeconds: 300,
    operation: RAW_CHALLENGE_OPERATION,
    registryReleaseSha256: await sha256Hex(`${clientId}|${requestOrigin}|${RAW_CHALLENGE_OPERATION}`),
    requestOrigin,
  });
}

/** A registry object in the shape `resolveRegistryBinding` is looked up by. */
export function createRegistry(binding) {
  return {
    resolveRegistryBinding({ clientId, operation, requestOrigin }) {
      if (
        clientId !== binding.clientId ||
        operation !== binding.operation ||
        requestOrigin !== binding.requestOrigin
      ) {
        throw new Error('UNREGISTERED_TRANSACTION');
      }
      return binding;
    },
  };
}

/**
 * Build the transaction the wallet validates. Exactly the 13 permitted keys
 * — an extra one is INVALID_TRANSACTION — every field cross-checked against
 * the binding.
 */
export async function buildRawChallengeTransaction({ binding, challengeBase64url, nowMs = Date.now() }) {
  const request = { challengeBase64url, protocolVersion: 1 };
  return {
    callbackUri: binding.callbackUri,
    clientDisplayName: binding.clientDisplayName,
    clientId: binding.clientId,
    // Comfortably inside maxLifetimeSeconds, and long enough for a human to
    // read the confirmation dialog.
    expiresAt: isoMillis(nowMs + 120_000),
    operation: binding.operation,
    registryVersion: binding.registryReleaseSha256,
    request,
    requestOrigin: binding.requestOrigin,
    requestSha256: await sha256Hex(canonicalJson(request)),
    resultToken: randomResultToken(),
    schemaVersion: 1,
    state: randomHex(32),
    transactionId: randomHex(32),
  };
}

/**
 * The in-process relay (§11.4).
 * · `fetchTransaction` OMITTED  ⇒ `prepare(obj)` takes our object directly.
 * · `navigate` OMITTED         ⇒ no redirect, no callbackUri validation.
 * · `publishResult` REQUIRED   ⇒ this is where the signature comes back to
 *   us, in-process. It performs NO network I/O.
 */
export function createInProcessRelay() {
  let published = null;
  return {
    relay: {
      async publishResult(transaction, result) {
        published = result;
        return { accepted: true };
      },
    },
    /** A function, not a getter: a getter would be flattened to its value by
     *  any `{...spread}` and silently read `null` forever. */
    read: () => published,
  };
}

/**
 * Pull the §4 wire field out of the wallet's public identity projection.
 *
 * ONLY the signing public key leaves this function. The wallet's
 * `accountXpub` is deliberately DROPPED: on both raw-32-capable legacy
 * profiles it is a depth-0 MASTER xpub (contract §13.1, measured), which
 * enumerates every account and every chain address under the whole wallet.
 * The node refuses to store one and this page refuses to hold one — the
 * operator is resolved by signing key instead (§13.1(i)).
 */
export function readIdentity(identity) {
  const key = identity?.keys?.[0] ?? {};
  const pub = String(key.publicKeyHex ?? '').toLowerCase();
  if (!HEX64.test(pub)) throw new Error('The wallet returned an identity without a usable auth key.');
  return {
    clientPubKeyHex: pub,
    keyId: String(key.keyId ?? ''),
    derivationPath: String(key.path ?? ''),
    identityScheme: String(key.identityScheme ?? ''),
  };
}

/** Pull the raw-32 signature out of the wallet's published result. */
export function readSignature(result) {
  const hex = String(result?.signatureHex ?? '').toLowerCase();
  if (!/^[0-9a-f]{128}$/.test(hex)) throw new Error('The wallet returned no usable signature.');
  return { signatureHex: hex, identityScheme: String(result?.identityScheme ?? ''), keyId: String(result?.keyId ?? '') };
}

// ---------------------------------------------------------------------------
// Runtime (browser only below this line)
// ---------------------------------------------------------------------------

let modulePromise = null;

/**
 * Import the wallet application from THIS node. `wallet-origin/index.js` is
 * the module that exposes `controller`; compat does not forward it (§11.4).
 * Both import the bare specifier `hd-wallet-wasm`, resolved by the page's
 * import map to /wallet-wasm/runtime/index.mjs.
 */
export function loadWalletUI() {
  if (!modulePromise) {
    const entry = WALLET_UI_ENTRY;
    modulePromise = import(/* @vite-ignore */ entry);
    modulePromise.catch(() => {
      modulePromise = null;
    });
  }
  return modulePromise;
}

/** True when this node serves the wallet application (fail-open probe). */
export async function walletUIAvailable() {
  try {
    const res = await fetch(WALLET_UI_ENTRY, { method: 'HEAD', credentials: 'same-origin' });
    return res.ok;
  } catch {
    return false;
  }
}

/**
 * Mount the wallet application into `mount` and return the live app.
 * `wasm` MUST be the already-initialised hd-wallet-wasm instance the rest of
 * the dashboard uses — omitting it makes compat load the 5 MB core twice.
 */
export async function createWalletApp({ mount, wasm, binding, document: doc = globalThis.document }) {
  const { createWalletOriginApp } = await loadWalletUI();
  const bridge = createInProcessRelay();
  const app = createWalletOriginApp({
    document: doc,
    mount,
    registry: createRegistry(binding),
    relay: bridge.relay,
    wasm,
    window: globalThis.window,
    // credentialPrompt is deliberately NOT injected: the package's WebAuthn
    // rp.id is wallet.spacedatanetwork.org, which no node origin matches, so
    // the passkey path is inert here (§11.4). Nothing is loaded from it.
  });
  return { app, controller: app.controller, published: bridge };
}

/**
 * Ask the wallet to collect credentials for the chosen legacy profile.
 * For the password profile this is the PACKAGE'S OWN prompt. For the phrase
 * profile the package does not export its prompt, so we render the same
 * markup it does (`section.wallet-login` > `h1` + `form` + labelled input +
 * `.wallet-login-actions`) and hand the raw control to `unlockLegacy`, which
 * reads it, derives from it, and wipes it — our code never sees the value.
 */
export async function collectLegacyCredentials({ profile, controller, mount, title, document: doc }) {
  if (profile === 'password-fast-v1-legacy') {
    const { createPasswordCredentialPrompt } = await loadWalletUI();
    const prompt = createPasswordCredentialPrompt({
      // §15.1/§15.4: the SIGN-IN mount declines the quarantine capability, so
      // the wallet's own `installQuarantineManager` returns null at its first
      // line and renders nothing. This is a capability decision, NOT
      // `display:none` — the records are a real encrypted store and their
      // export/delete affordance lives on the stored-wallets surface instead
      // (see StoredWallets.svelte), never inside a sign-in transaction.
      controller: withoutQuarantine(controller),
      document: doc,
      mount,
      offerRememberedUnlock: false,
      title,
    });
    const value = prompt?.promise ? await prompt.promise : await prompt;
    return { controls: value, cancel: () => prompt?.cancel?.() };
  }

  const section = doc.createElement('section');
  section.className = 'wallet-login wallet-legacy-credentials';
  const heading = doc.createElement('h1');
  heading.textContent = title;
  const sub = doc.createElement('p');
  sub.textContent = 'Account 0';
  const form = doc.createElement('form');
  form.noValidate = true;
  const label = doc.createElement('label');
  label.textContent = 'Recovery phrase';
  const input = doc.createElement('input');
  input.type = 'password';
  input.name = 'mnemonic';
  input.required = true;
  input.autocomplete = 'off';
  input.spellcheck = false;
  label.append(input);
  form.append(label);
  const actions = doc.createElement('div');
  actions.className = 'wallet-login-actions';
  const submit = doc.createElement('button');
  submit.type = 'submit';
  submit.dataset.walletAction = 'confirm-legacy-mnemonic';
  submit.textContent = 'Continue';
  const cancel = doc.createElement('button');
  cancel.type = 'button';
  cancel.dataset.walletAction = 'cancel-login';
  cancel.textContent = 'Cancel';
  actions.append(submit, cancel);
  form.append(actions);
  section.append(heading, sub, form);
  mount.append(section);
  input.focus();

  let settle;
  const promise = new Promise((resolve, reject) => {
    settle = { resolve, reject };
  });
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    if (!input.value.trim()) return;
    settle.resolve({ mnemonicControl: input });
  });
  cancel.addEventListener('click', () => settle.reject(new Error('USER_CANCELLED')));
  return { controls: await promise, cancel: () => settle.reject(new Error('USER_CANCELLED')) };
}

/**
 * The controller as the SIGN-IN prompt may see it: everything the prompt
 * legitimately needs, minus `listQuarantinedWalletRecords`.
 *
 * A plain object rather than a Proxy so the absence is literal: the wallet
 * checks `typeof controller.listQuarantinedWalletRecords !== 'function'` and
 * bails. Methods are bound to the real controller so behaviour is unchanged.
 */
export function withoutQuarantine(controller) {
  if (!controller || typeof controller !== 'object') return controller;
  const shim = {};
  for (const name of [
    'registerCredentialControls',
    'supportsRememberedWallet',
    'canRestoreRememberedWallet',
    'unlockRemembered',
    'unlockPassword',
    'unlockLegacy',
    'forgetStoredWallet',
  ]) {
    if (typeof controller[name] === 'function') shim[name] = controller[name].bind(controller);
  }
  return shim;
}

/** The localStorage keys hd-wallet-ui quarantines (contract §15.1). */
export const QUARANTINE_KEYS = [
  'wallet_storage_metadata',
  'wallet_storage_encrypted',
  'wallet_storage_passkey_credential',
];

/**
 * Read the orphaned wallet records this origin still holds (§15.1): the
 * node's removed 2.0.6 `/login` page wrote a REAL encrypted wallet and a REAL
 * passkey credential here, and §12.1 orphaned them. Presence-based, exactly
 * like the wallet's own detector — a key that is absent yields no row.
 */
export function readQuarantinedRecords(storage = globalThis.localStorage) {
  const rows = [];
  for (const key of QUARANTINE_KEYS) {
    let value = null;
    try {
      value = storage?.getItem(key) ?? null;
    } catch {
      value = null;
    }
    if (value === null) continue;
    rows.push({ key, bytes: new TextEncoder().encode(value).length, value });
  }
  return rows;
}

/**
 * Sign the node's challenge with the unlocked wallet: prepare the
 * transaction, run `executePrepared` (the package renders its OWN
 * confirmation dialog, signs, and builds the result), and read the
 * signature out of the in-process relay.
 */
export async function signChallengeWithWallet({ controller, published, binding, challengeBase64, nowMs = Date.now() }) {
  const transaction = await buildRawChallengeTransaction({
    binding,
    challengeBase64url: base64ToBase64Url(challengeBase64),
    nowMs,
  });
  const prepared = await controller.prepare(transaction);
  await controller.executePrepared(prepared);
  return readSignature(published.read());
}
