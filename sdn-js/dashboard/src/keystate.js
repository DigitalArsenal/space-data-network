/*
 * OPERATOR-ROW VOCABULARY — the words the owner has to be able to read
 * without knowing the auth contract.
 *
 * Owner directive 2026-07-29, on seeing the live table: "I have no idea what
 * awaiting proof is, or where these keys came from." IRIS ruled the register
 * (consult 2026-07-29): "proof" leaves the dashboard entirely, every state is
 * a plain sentence, and the sentence is VISIBLE TEXT in the edit modal — never
 * a tooltip only.
 *
 * Pure: no fetch, no design imports, no DOM. Tone values are theme TOKEN NAMES
 * (the view resolves them through theme.js) so this module can be unit-tested
 * and can never fork the palette.
 *
 * The fact underneath every ruling here is one field: `signing_pubkey_hex`.
 * The node binds it on the first successful sign-in (TOFU, contract §5.3), or
 * an operator writes it into the config file explicitly. Those are DIFFERENT
 * states and the table used to render both as "BOUND" — which is how a config
 * row could read "BOUND · 0 SIGN-INS" and mean nothing to anybody.
 */

/** Sign-in count as a non-negative integer, whatever the wire sent. */
export function signInCount(user) {
  const n = Number(user?.connection_count ?? 0);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}

/** True when the node has a signing key pinned to this xpub. */
export function hasBoundKey(user) {
  return Boolean(String(user?.signing_pubkey_hex ?? '').trim());
}

/** True when the row comes from the node's on-disk config allowlist. */
export const isFromConfig = (user) => String(user?.source ?? '').trim().toLowerCase() === 'config';

/** First 4 hex of a pinned key, for "the node recorded 0d80…". */
export function shortKey(hex) {
  const v = String(hex ?? '').trim().toLowerCase();
  if (!v) return '';
  return v.length <= 8 ? v : `${v.slice(0, 4)}…`;
}

/**
 * KEY STATE — three states, not two, keyed on `connection_count`
 * (IRIS amended ruling 2026-07-29: never print PROVEN beside 0 SIGN-INS).
 *
 *   notset     no signing key pinned — nobody has signed in with this xpub
 *   declared   a key is pinned but no sign-in has ever happened: an operator
 *              asserted it in the config file
 *   proven     a key is pinned AND at least one sign-in used it
 *
 * `declared` exists because the live node has exactly such a row (an admin
 * publisher key with an explicit signing_pubkey_hex and 0 sign-ins). Calling
 * it "proven" would claim a sign-in that never happened; calling it "not set"
 * would claim the node will accept any key, which it will not.
 *
 * `contractTerm` is the auth contract's own vocabulary. It belongs in a
 * `title=` and NOWHERE else: it is the word the owner could not read.
 */
export function keyState(user) {
  if (!hasBoundKey(user)) {
    return {
      id: 'notset',
      label: 'KEY NOT SET',
      tone: 'amber',
      contractTerm: 'AWAITING PROOF (admin contract §5.3)',
      sentence:
        'Approved, but nobody has signed in with this key yet. The first key that signs in gets recorded and pinned to this xpub permanently.',
    };
  }
  if (signInCount(user) === 0) {
    return {
      id: 'declared',
      label: 'KEY DECLARED',
      tone: 'textDim',
      contractTerm: 'signing_pubkey_hex asserted in config (admin contract §5.3)',
      sentence:
        "The signing key was written into the node's config file, so this account can act without signing in first. Nobody has signed in with it yet, and the node will refuse any other key.",
    };
  }
  return {
    id: 'proven',
    label: 'KEY PROVEN',
    tone: 'green',
    contractTerm: 'BOUND (admin contract §5.3)',
    sentence:
      'Signed in and verified. The node recorded this key and will refuse any other key on this xpub.',
  };
}

/** WHERE THE ROW CAME FROM — the owner's second question, answered in words. */
export function provenance(user) {
  if (isFromConfig(user)) {
    return {
      id: 'config',
      label: 'FROM CONFIG FILE',
      tone: 'textDim',
      sentence:
        "This key comes from the node's config file — change it there, not here.",
      locked: true,
    };
  }
  return {
    id: 'database',
    label: 'IN NODE DATABASE',
    tone: 'textDim',
    sentence: 'This key was added on this node and is stored in its database.',
    locked: false,
  };
}

/** "4 SIGN-INS" / "1 SIGN-IN" / "NO SIGN-INS" — never a bare 0. */
export function signInsLabel(user) {
  const n = signInCount(user);
  if (n === 0) return 'NO SIGN-INS';
  return `${n} SIGN-IN${n === 1 ? '' : 'S'}`;
}

/**
 * The subscript line: key state, sign-ins, provenance.
 *
 * TRUST LEFT THIS LINE ON 2026-07-31 and became a COLUMN (owner: "the accounts
 * table does not have the trust as a separate column either"). It is not
 * rendered twice six inches apart — the tier appears once per row, in the
 * column that can be sorted and filtered by it. `trustLabel` is still accepted
 * so every existing caller keeps compiling; it is deliberately unused.
 */
export function operatorMeta(user, trustLabel) {
  void trustLabel;
  const key = keyState(user);
  const prov = provenance(user);
  return [
    { id: 'key', k: '', v: key.label, tone: key.tone },
    { id: 'signins', k: '', v: signInsLabel(user), tone: 'textDim' },
    { id: 'source', k: '', v: prov.label, tone: prov.tone },
  ];
}

/**
 * The peers panel's subscript line (same grammar — two adjacent tables with
 * different grammars is the mess the owner named).
 *
 * EFFECTIVE STAYS HERE while TRUST moves to a column (IRIS 2026-07-31). They
 * are two facts: one is what an operator asserted, the other is what the node
 * computed from the web of trust, and the modal's whole APPLY correction turned
 * on the difference. A column the page cannot set would read as an input.
 */
export function peerMeta(peer, trustLabel, effectiveLabel) {
  void trustLabel;
  const items = [
    { id: 'effective', k: 'EFFECTIVE', v: String(effectiveLabel ?? '').toUpperCase(), tone: 'effective' },
  ];
  if (peer?.computed_valid) {
    items.push({ id: 'wot', k: '', v: 'WEB OF TRUST', tone: 'textDim' });
  }
  const notes = String(peer?.notes ?? '').trim();
  if (notes) items.push({ id: 'notes', k: '', v: notes, tone: 'textFaint' });
  return items;
}

/**
 * PEER ID cell for an operator row. The node's user JSON carries NO peer_id
 * field (internal/auth/userstore.go:26-46), which is why the column rendered
 * an em-dash for every row ever displayed. The peer id is DERIVED from the
 * xpub in the page instead; this function only names the outcome so the view
 * never prints a dash (IRIS D3).
 */
export function peerIdCell(derived, xpub, attempted = false) {
  const id = String(derived ?? '').trim();
  if (id) return { id, label: id, pending: false, missing: false };
  // Nothing to derive FROM, or a derivation that ran and failed (the wallet
  // runtime is unstaged, or the xpub is not a key this node can convert):
  // both are "this row is not xpub-keyed", never a dash.
  if (attempted || !String(xpub ?? '').trim()) {
    return { id: '', label: 'NOT XPUB-KEYED', pending: false, missing: true };
  }
  return { id: '', label: 'DERIVING…', pending: true, missing: false };
}
