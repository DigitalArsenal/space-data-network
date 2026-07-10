import { describe, expect, it } from 'vitest';
import {
  AUTH_STEP_TIMINGS_MS,
  NODE_KEY_FORMAT_ERROR,
  NODE_KEY_REQUIRED_ERROR,
  OPERATOR_REQUIRED_ERROR,
  STAR_COUNT,
  STAR_SEED,
  buildAuthSteps,
  createSeededRandom,
  generateOrbitArcs,
  generateStarfield,
  isRecognizedNodeKey,
  nodeStepLabels,
  operatorStepLabels,
  validateNodeKeyForm,
  validateOperatorForm,
} from './login';

// Not currently wired into a vitest `include` glob for `ui/src/**` (no such
// harness exists yet in this package — root `vitest.config.mts` only covers
// `src/**/*.test.ts`). Colocated here per U1.1 loop-task instructions so the
// pure logic is testable now and picked up automatically whenever a
// `ui/src` test project is added. Verified manually in the meantime via
// `npx vitest run --config <scratch config> ui/src/spaceaware/lib/login.test.ts`.

describe('starfield PRNG (seed-42 Park-Miller LCG)', () => {
  it('matches golden values for the first 10 draws', () => {
    const rnd = createSeededRandom(STAR_SEED);
    const got = Array.from({ length: 10 }, () => rnd());
    const golden = [
      0.00032870750889587566, 0.5245871020129822, 0.7354235321913956, 0.26330554078487006,
      0.37622397131110724, 0.19628582577979464, 0.9758738810084173, 0.512318108469396,
      0.5304490451377114, 0.2571016295147602,
    ];
    expect(got).toEqual(golden);
  });

  it('is deterministic across independent generators with the same seed', () => {
    const a = createSeededRandom(STAR_SEED);
    const b = createSeededRandom(STAR_SEED);
    for (let i = 0; i < 50; i++) {
      expect(a()).toBe(b());
    }
  });
});

describe('generateStarfield', () => {
  const w = 1200;
  const h = 800;

  it('produces exactly STAR_COUNT stars', () => {
    expect(generateStarfield(w, h)).toHaveLength(STAR_COUNT);
  });

  it('matches golden star values (first, second, last) for a 1200x800 canvas', () => {
    const stars = generateStarfield(w, h);
    expect(stars[0]).toEqual({
      x: 0.3944490106750508,
      y: 419.66968161038574,
      r: 1.0618811789722562,
      color: '207,227,236',
      alpha: 0.35,
    });
    expect(stars[1]).toEqual({
      x: 235.54299093575358,
      y: 780.6991048067339,
      r: 0.8610862976224565,
      color: '207,227,236',
      alpha: 0.27,
    });
    expect(stars[169]).toEqual({
      x: 1196.1744938027925,
      y: 336.4782290237389,
      r: 1.2882946022266033,
      color: '207,227,236',
      alpha: 0.49,
    });
  });

  it('is deterministic: identical inputs produce identical output (redraw-on-resize safety)', () => {
    expect(generateStarfield(w, h)).toEqual(generateStarfield(w, h));
  });

  it('only ever emits the 3 documented star colors', () => {
    const allowed = new Set(['207,227,236', '127,215,226', '255,217,160']);
    for (const star of generateStarfield(w, h)) {
      expect(allowed.has(star.color)).toBe(true);
    }
  });

  it('keeps radius in [0.4, 1.3) and alpha in [0.12, 0.72]', () => {
    for (const star of generateStarfield(w, h)) {
      expect(star.r).toBeGreaterThanOrEqual(0.4);
      expect(star.r).toBeLessThan(1.3);
      expect(star.alpha).toBeGreaterThanOrEqual(0.12);
      expect(star.alpha).toBeLessThanOrEqual(0.72);
    }
  });
});

describe('generateOrbitArcs', () => {
  it('produces 3 arcs with the documented center, radii, style and dash', () => {
    const arcs = generateOrbitArcs(1200, 800);
    expect(arcs).toHaveLength(3);
    for (const arc of arcs) {
      expect(arc.cx).toBe(600);
      expect(arc.cy).toBe(800 * 1.85);
    }
    expect(arcs[0].strokeStyle).toBe('rgba(90,150,180,0.06)');
    expect(arcs[0].dash).toEqual([]);
    expect(arcs[1].strokeStyle).toBe('rgba(53,201,216,0.09)');
    expect(arcs[1].dash).toEqual([]);
    expect(arcs[2].strokeStyle).toBe('rgba(90,150,180,0.06)');
    expect(arcs[2].dash).toEqual([2, 7]);
  });
});

describe('validateOperatorForm', () => {
  it('requires both fields', () => {
    expect(validateOperatorForm('', '')).toBe(OPERATOR_REQUIRED_ERROR);
    expect(validateOperatorForm('  ', 'secret')).toBe(OPERATOR_REQUIRED_ERROR);
    expect(validateOperatorForm('op@sdn.io', '')).toBe(OPERATOR_REQUIRED_ERROR);
  });

  it('passes when both fields are present', () => {
    expect(validateOperatorForm('op@sdn.io', 'secret')).toBeNull();
  });
});

describe('node-key validation', () => {
  it('flags empty input as required', () => {
    expect(validateNodeKeyForm('')).toBe(NODE_KEY_REQUIRED_ERROR);
    expect(validateNodeKeyForm('   ')).toBe(NODE_KEY_REQUIRED_ERROR);
  });

  it('flags unrecognized prefixes', () => {
    expect(validateNodeKeyForm('not-a-key')).toBe(NODE_KEY_FORMAT_ERROR);
    expect(isRecognizedNodeKey('not-a-key')).toBe(false);
  });

  it('accepts the documented prefixes', () => {
    for (const key of [
      '16Uiu2HAm1Lbvwj...',
      '12D3KooWAbc123',
      '/ip4/127.0.0.1/tcp/4001',
      '/dns/node.example.com/tcp/4001',
    ]) {
      expect(validateNodeKeyForm(key)).toBeNull();
      expect(isRecognizedNodeKey(key)).toBe(true);
    }
  });

  it('trims before validating', () => {
    expect(validateNodeKeyForm('  16Uiu2HAm  ')).toBeNull();
  });
});

describe('mock auth timing + step view models', () => {
  it('matches the documented timing constants', () => {
    expect(AUTH_STEP_TIMINGS_MS).toEqual({ step1: 700, step2: 1450, complete: 2150, redirect: 2900 });
  });

  it('has the documented operator/node step labels', () => {
    expect(operatorStepLabels()).toEqual(['CREDENTIAL CHECK', 'P2P HANDSHAKE', 'SESSION SEALED']);
    expect(nodeStepLabels()).toEqual(['PEER KEY VERIFY', 'P2P HANDSHAKE', 'SESSION SEALED']);
  });

  it('renders pending/active/done row states correctly', () => {
    const labels = operatorStepLabels();
    const steps = buildAuthSteps(labels, 1);
    expect(steps[0]).toMatchObject({ glyph: '✓', color: '#5ad6a0', status: 'OK' });
    expect(steps[1]).toMatchObject({
      glyph: '◌',
      color: '#35c9d8',
      status: 'RUNNING',
      anim: 'sa-spin 0.9s linear infinite',
    });
    expect(steps[2]).toMatchObject({ glyph: '·', color: '#44586a', status: 'QUEUED', anim: 'none' });
  });

  it('marks every step done once step advances past all of them', () => {
    const steps = buildAuthSteps(operatorStepLabels(), 3);
    expect(steps.every((s) => s.glyph === '✓' && s.status === 'OK')).toBe(true);
  });
});
