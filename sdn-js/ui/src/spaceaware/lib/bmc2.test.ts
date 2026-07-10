import { describe, expect, it } from 'vitest';
import {
  BMC2_CARD_VARIANT_STYLE,
  BMC2_DEMO_TAG_TITLE,
  BMC2_INACTIVE_TAB_STYLE,
  BMC2_INDEX_CARDS,
  BMC2_KICKERS,
  BMC2_MODES_ORDER,
  BMC2_MODE_ACCENTS,
  BMC2_MODE_TABS,
  bmc2Route,
  bmc2TabAccent,
} from './bmc2';
import { BMC2_MODES, type Bmc2Mode } from '../router';

describe('bmc2Route', () => {
  it('routes to the index for a null mode', () => {
    expect(bmc2Route(null)).toBe('/bmc2');
  });

  it('routes to each F-key board', () => {
    for (const mode of BMC2_MODES) {
      expect(bmc2Route(mode)).toBe(`/bmc2/${mode}`);
    }
  });
});

describe('bmc2TabAccent', () => {
  it('returns the mode accent for the active tab', () => {
    expect(bmc2TabAccent('f1', 'f1')).toBe(BMC2_MODE_ACCENTS.f1);
    expect(bmc2TabAccent('f4', 'f4')).toBe(BMC2_MODE_ACCENTS.f4);
  });

  it('returns the shared inactive style for every non-active tab', () => {
    for (const mode of BMC2_MODES) {
      if (mode === 'f2') continue;
      expect(bmc2TabAccent(mode, 'f2')).toBe(BMC2_INACTIVE_TAB_STYLE);
    }
  });
});

describe('mode tables', () => {
  it('BMC2_MODE_TABS covers every mode, in F1..F6 order', () => {
    expect(BMC2_MODE_TABS.map((t) => t.id)).toEqual(BMC2_MODES_ORDER);
    expect(BMC2_MODES_ORDER).toEqual(BMC2_MODES);
  });

  it('BMC2_MODE_ACCENTS and BMC2_KICKERS cover every mode', () => {
    const modeSet = new Set<Bmc2Mode>(BMC2_MODES);
    expect(new Set(Object.keys(BMC2_MODE_ACCENTS))).toEqual(modeSet);
    expect(new Set(Object.keys(BMC2_KICKERS))).toEqual(modeSet);
  });

  it('F1/F2/F3 share the same cyan accent (only F4/F5/F6 carry a distinct semantic accent)', () => {
    expect(BMC2_MODE_ACCENTS.f2).toEqual(BMC2_MODE_ACCENTS.f1);
    expect(BMC2_MODE_ACCENTS.f3).toEqual(BMC2_MODE_ACCENTS.f1);
    expect(BMC2_MODE_ACCENTS.f4).not.toEqual(BMC2_MODE_ACCENTS.f1);
    expect(BMC2_MODE_ACCENTS.f5).not.toEqual(BMC2_MODE_ACCENTS.f1);
    expect(BMC2_MODE_ACCENTS.f6).not.toEqual(BMC2_MODE_ACCENTS.f1);
  });
});

describe('BMC2_INDEX_CARDS', () => {
  it('has exactly one card per mode, in F1..F6 order, each with 3 meta rows', () => {
    expect(BMC2_INDEX_CARDS.map((c) => c.mode)).toEqual(BMC2_MODES_ORDER);
    for (const card of BMC2_INDEX_CARDS) {
      expect(card.meta).toHaveLength(3);
      expect(BMC2_CARD_VARIANT_STYLE).toHaveProperty(card.variant);
    }
  });

  it('never appends a directional arrow glyph to card copy (token hard rule)', () => {
    const arrowGlyphs = ['→', '←', '↗', '»', '▸'];
    for (const card of BMC2_INDEX_CARDS) {
      for (const glyph of arrowGlyphs) {
        expect(card.title).not.toContain(glyph);
        expect(card.description).not.toContain(glyph);
      }
    }
  });
});

describe('BMC2_DEMO_TAG_TITLE', () => {
  it('is non-empty (every board carries a titled DEMO tag per D3)', () => {
    expect(BMC2_DEMO_TAG_TITLE.length).toBeGreaterThan(0);
  });
});
