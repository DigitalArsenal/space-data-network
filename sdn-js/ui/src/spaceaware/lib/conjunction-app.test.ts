import { describe, expect, it } from 'vitest';
import {
  classifyConjunctionAppNav,
  conjunctionAppSessionChip,
} from './conjunction-app';

describe('classifyConjunctionAppNav', () => {
  it('treats the root path as internal (the conjunction ship serves at /)', () => {
    expect(classifyConjunctionAppNav('/')).toBe('internal');
    expect(classifyConjunctionAppNav('')).toBe('internal');
  });

  it('accepts /console/conjunction as an in-app alias', () => {
    expect(classifyConjunctionAppNav('/console/conjunction')).toBe('internal');
    // Query string / hash ride along and must not change the classification.
    expect(classifyConjunctionAppNav('/console/conjunction?group=geo-watch')).toBe('internal');
  });

  it('marks the descoped 3D/Orbital route as descoped (ConjunctionView OPEN IN 3D target)', () => {
    expect(classifyConjunctionAppNav('/orbital')).toBe('descoped');
    expect(classifyConjunctionAppNav('/orbital?group=geo-watch')).toBe('descoped');
  });

  it('marks every other full-app screen as descoped', () => {
    for (const path of [
      '/login',
      '/console',
      '/console/node',
      '/console/peers',
      '/console/groups',
      '/console/data',
      '/console/channels',
      '/gantt',
      '/bmc2',
      '/bmc2/f4',
    ]) {
      expect(classifyConjunctionAppNav(path)).toBe('descoped');
    }
  });
});

describe('conjunctionAppSessionChip', () => {
  it('is a fixed honest public/anonymous chip (no login flow bundled)', () => {
    const chip = conjunctionAppSessionChip();
    expect(chip.label).toBe('PUBLIC · ANONYMOUS');
    expect(chip.color).toMatch(/^#[0-9a-fA-F]{6}$/);
  });
});
