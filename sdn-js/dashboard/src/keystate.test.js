/*
 * The words the owner must be able to read (graph task
 * sdn-operator-keys-table-redesign; IRIS ruling + amendment 2026-07-29).
 *
 * These are regression tests on VOCABULARY, which is unusual and deliberate:
 * the defect the owner reported was not a broken control, it was a table that
 * said "AWAITING PROOF" and "—" at him. Each expectation below is a sentence
 * from the ruling, so a future edit that quietly reintroduces contract jargon
 * into the visible register fails here rather than on his screen.
 */
import { describe, expect, it } from 'vitest';

import {
  hasBoundKey,
  isFromConfig,
  keyState,
  operatorMeta,
  peerIdCell,
  peerMeta,
  provenance,
  shortKey,
  signInCount,
  signInsLabel,
} from './keystate.js';

const KEY = 'e31e29b7c8b76bd08012ae0a917eced0067a183e9f1de980a7ef377115578f93';

describe('key state — three values, keyed on the sign-in count', () => {
  it('no signing key is KEY NOT SET, in amber, and never the word "proof"', () => {
    const s = keyState({ source: 'config', connection_count: 0 });
    expect(s.id).toBe('notset');
    expect(s.label).toBe('KEY NOT SET');
    expect(s.tone).toBe('amber');
    expect(s.label).not.toMatch(/proof/i);
    expect(s.sentence).not.toMatch(/proof/i);
    // The contract's own term survives, but only where it cannot shout at
    // anyone: a title attribute.
    expect(s.contractTerm).toMatch(/AWAITING PROOF/);
  });

  it('a key with zero sign-ins is DECLARED, never PROVEN (the live publisher row)', () => {
    const s = keyState({ source: 'config', signing_pubkey_hex: KEY, connection_count: 0 });
    expect(s.id).toBe('declared');
    expect(s.label).toBe('KEY DECLARED');
    expect(s.label).not.toMatch(/PROVEN|VERIFIED/);
    expect(s.sentence).toMatch(/config file/i);
  });

  it('a key that has signed in is PROVEN, in green', () => {
    const s = keyState({ source: 'database', signing_pubkey_hex: KEY, connection_count: 8 });
    expect(s.id).toBe('proven');
    expect(s.label).toBe('KEY PROVEN');
    expect(s.tone).toBe('green');
  });

  it('PROVEN and "NO SIGN-INS" can never appear together', () => {
    for (const user of [
      { signing_pubkey_hex: KEY, connection_count: 0, source: 'config' },
      { signing_pubkey_hex: KEY, connection_count: 0, source: 'database' },
      { connection_count: 0, source: 'config' },
    ]) {
      const items = operatorMeta(user, 'admin').map((i) => i.v);
      if (items.includes('NO SIGN-INS')) expect(items).not.toContain('KEY PROVEN');
    }
  });

  it('reads the two facts it depends on off the wire shape the node actually sends', () => {
    expect(hasBoundKey({ signing_pubkey_hex: KEY })).toBe(true);
    expect(hasBoundKey({ signing_pubkey_hex: '   ' })).toBe(false);
    expect(hasBoundKey({})).toBe(false);
    expect(isFromConfig({ source: 'config' })).toBe(true);
    expect(isFromConfig({ source: 'CONFIG' })).toBe(true);
    expect(isFromConfig({ source: 'database' })).toBe(false);
  });
});

describe('provenance — the owner\'s "where did these come from"', () => {
  it('a config row says so, and says where to change it', () => {
    const p = provenance({ source: 'config' });
    expect(p.label).toBe('FROM CONFIG FILE');
    expect(p.locked).toBe(true);
    expect(p.sentence).toMatch(/config file/i);
  });

  it('a runtime row says it lives in the database', () => {
    const p = provenance({ source: 'database' });
    expect(p.label).toBe('IN NODE DATABASE');
    expect(p.locked).toBe(false);
  });
});

describe('sign-in counts', () => {
  it('never renders a bare zero', () => {
    expect(signInsLabel({ connection_count: 0 })).toBe('NO SIGN-INS');
    expect(signInsLabel({})).toBe('NO SIGN-INS');
    expect(signInsLabel({ connection_count: 1 })).toBe('1 SIGN-IN');
    expect(signInsLabel({ connection_count: 8 })).toBe('8 SIGN-INS');
  });

  it('refuses junk from the wire rather than printing NaN', () => {
    expect(signInCount({ connection_count: 'seven' })).toBe(0);
    expect(signInCount({ connection_count: -3 })).toBe(0);
    expect(signInCount({ connection_count: 2.7 })).toBe(2);
  });
});

describe('the subscript line (IRIS §1: exactly four items, in this order)', () => {
  it('is trust, key state, sign-ins, provenance — and nothing else', () => {
    const items = operatorMeta(
      { source: 'database', signing_pubkey_hex: KEY, connection_count: 8, trust_level: 'admin' },
      'admin'
    );
    expect(items).toHaveLength(4);
    expect(items.map((i) => i.id)).toEqual(['trust', 'key', 'signins', 'source']);
    expect(items[0].v).toBe('ADMIN');
    expect(items[1].v).toBe('KEY PROVEN');
    expect(items[2].v).toBe('8 SIGN-INS');
    expect(items[3].v).toBe('IN NODE DATABASE');
  });

  it('peers share the grammar, and only carry what the peer actually has', () => {
    const bare = peerMeta({}, 'standard', 'standard');
    expect(bare.map((i) => i.id)).toEqual(['trust', 'effective']);
    const full = peerMeta({ computed_valid: true, notes: 'sfo2 box' }, 'full', 'admin');
    expect(full.map((i) => i.id)).toEqual(['trust', 'effective', 'wot', 'notes']);
    expect(full[3].v).toBe('sfo2 box');
  });
});

describe('the PEER ID cell (IRIS D3: never an em-dash)', () => {
  it('shows the derived id once it exists', () => {
    const cell = peerIdCell('12D3KooWKh3diobFtzBk2RvdwR4TuFB8nkU31th8Mc2iKb7bZBWs', 'xpub6B…');
    expect(cell.id).toMatch(/^12D3KooW/);
    expect(cell.pending).toBe(false);
    expect(cell.label).not.toBe('—');
  });

  it('says it is still deriving rather than printing a dash', () => {
    const cell = peerIdCell('', 'xpub6B…');
    expect(cell.pending).toBe(true);
    expect(cell.label).toBe('DERIVING…');
    expect(cell.label).not.toBe('—');
  });

  it('says a row is not xpub-keyed when there is nothing to derive from', () => {
    const cell = peerIdCell('', '');
    expect(cell.missing).toBe(true);
    expect(cell.label).toBe('NOT XPUB-KEYED');
    expect(cell.label).not.toBe('—');
  });

  it('a derivation that ran and failed is not xpub-keyed either — never a stuck spinner', () => {
    const cell = peerIdCell('', 'xpub6B…', true);
    expect(cell.pending).toBe(false);
    expect(cell.missing).toBe(true);
    expect(cell.label).toBe('NOT XPUB-KEYED');
  });
});

describe('pinned-key display', () => {
  it('shows a short prefix, never the whole key', () => {
    expect(shortKey(KEY)).toBe('e31e…');
    expect(shortKey('')).toBe('');
  });
});
