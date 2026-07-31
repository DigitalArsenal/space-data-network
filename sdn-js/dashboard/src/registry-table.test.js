
// ---- Owner 2026-07-31: per-column filters + always-visible pagination ------
import { filterColumns, paginate, PEER_FIELD_TEXT } from './registry-table.js';

const colPeers = [
  { id: '16UiuAAA', name: 'Alpha Station', organization: 'DA', trust_level: 'full', effective_trust_level: 'full' },
  { id: '16UiuBBB', name: 'Beta Node', trust_level: 'standard', effective_trust_level: 'marginal' },
  { id: '16UiuCCC', name: '', trust_level: 'never' },
];

describe('filterColumns (per-column, dropdown replaced)', () => {
  it('empty filters return everything, as a copy', () => {
    const out = filterColumns(colPeers, { peer: '', name: '', trust: '' });
    expect(out).toHaveLength(3);
    expect(out).not.toBe(colPeers);
  });
  it('filters each field independently and ANDs across columns', () => {
    expect(filterColumns(colPeers, { peer: 'bbb' })).toHaveLength(1);
    expect(filterColumns(colPeers, { name: 'alpha' })[0].id).toBe('16UiuAAA');
    expect(filterColumns(colPeers, { peer: '16uiu', name: 'beta' })).toHaveLength(1);
    expect(filterColumns(colPeers, { peer: 'aaa', name: 'beta' })).toHaveLength(0);
  });
  it('trust matches asserted AND effective tiers', () => {
    expect(filterColumns(colPeers, { trust: 'full' })).toHaveLength(1);
    expect(filterColumns(colPeers, { trust: 'marginal' })[0].id).toBe('16UiuBBB');
  });
  it('unknown filter key matches nothing rather than everything', () => {
    expect(filterColumns(colPeers, { bogus: 'x' })).toHaveLength(0);
  });
  it('PEER_FIELD_TEXT tolerates absent fields', () => {
    expect(PEER_FIELD_TEXT.name({})).toBe('');
    expect(PEER_FIELD_TEXT.trust({})).toContain('unknown');
  });
});

describe('paginate (always-visible pager)', () => {
  const rows = Array.from({ length: 60 }, (_, i) => ({ id: i }));
  it('windows and reports totals', () => {
    const p = paginate(rows, 1, 25);
    expect(p.rows).toHaveLength(25);
    expect(p).toMatchObject({ page: 1, pages: 3, total: 60, start: 1, end: 25 });
  });
  it('last page is partial and honest', () => {
    const p = paginate(rows, 3, 25);
    expect(p.rows).toHaveLength(10);
    expect(p).toMatchObject({ page: 3, start: 51, end: 60 });
  });
  it('clamps an out-of-range page instead of stranding it', () => {
    expect(paginate(rows, 99, 25).page).toBe(3);
    expect(paginate(rows, 0, 25).page).toBe(1);
  });
  it('empty list still reports a renderable window', () => {
    expect(paginate([], 1, 25)).toMatchObject({ page: 1, pages: 1, total: 0, start: 0, end: 0 });
  });
});
