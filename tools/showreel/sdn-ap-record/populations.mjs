// The seeded populations the reel flies: synthetic orbits in every regime, from a fixed seed. No catalog data is
// used or reproduced (the data license policy forbids republishing element sets derived per object).
import { RE, meanMotionOf, spinAt } from "./module.mjs";

const SEED = 20261024;
export const SIDEREAL = 1.00273791; // rev/day: one turn of the Earth, in revolutions per solar day

/** mulberry32. */
function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** One element row of a regime: `row` fills the epoch and the theory in. Returns the rows, each tagged with its regime. */
export function makePopulations(m, row) {
  const R = rng(SEED);
  const theta = (spinAt(m, 0) * 180) / Math.PI; // the Earth's rotation angle at the epoch, degrees
  const rows = [];
  const add = (kind, o) => rows.push({ kind, ...row({ norad: rows.length + 1, ...o }) });
  // The Brouwer mean motion (rev/day) of a mean semi-major axis (km): the module's sdnap_mean_motion_of. Each orbit's
  // draws stay in the order they had, so the seeded populations are the ones the reel records.
  const mm = (aKm, e, i) => meanMotionOf(m, aKm, e, i);
  // low orbit: four constellation-like shells (53, 43, 70 and 97.6 degrees), then scattered orbits
  const shell = (planes, per, inc, alt, phase) => {
    for (let p = 0; p < planes; p++) {
      for (let k = 0; k < per; k++) {
        const a = RE + alt + (R() - 0.5) * 6;
        const ecc = 0.0001 + 0.0004 * R();
        const i = inc + (R() - 0.5) * 0.1;
        add("leo", {
          meanMotion: mm(a, ecc, i),
          ecc,
          inc: i,
          raan: (360 * p) / planes,
          argp: R() * 360,
          ma: ((360 * k) / per + (phase * 360 * p) / (planes * per)) % 360,
          bterm: 0.008 + 0.01 * R(),
          agom: 0.01,
        });
      }
    }
  };
  shell(24, 22, 53.0, 550, 0.5);
  shell(12, 20, 43.0, 540, 0.5);
  shell(8, 16, 70.0, 570, 0.5);
  shell(10, 16, 97.6, 560, 0.5);
  for (let i = 0; i < 544; i++) {
    const inc = [52, 65, 82, 98, 28.5, 74][Math.floor(R() * 6)] + (R() - 0.5) * 3;
    const a = RE + 450 + 850 * R();
    const ecc = 0.0005 + 0.008 * R();
    add("leo", { meanMotion: mm(a, ecc, inc), ecc, inc, raan: R() * 360, argp: R() * 360, ma: R() * 360, bterm: 0.003 + 0.03 * R(), agom: 0.005 + 0.02 * R() });
  }
  // medium orbit: a navigation-class constellation, 6 planes of 4 at 55 degrees with a half-day period, and six others
  for (let p = 0; p < 6; p++) {
    for (let k = 0; k < 4; k++) {
      add("meo", { meanMotion: 2.00561, ecc: 0.001 + 0.01 * R(), inc: 55 + (R() - 0.5) * 0.5, raan: 60 * p, argp: R() * 360, ma: (90 * k + 15 * p) % 360, bterm: 0.01, agom: 0.02 });
    }
  }
  for (let i = 0; i < 6; i++) {
    const a = RE + 19100 + 400 * R();
    const inc = 56 + 8 * R();
    add("meo", { meanMotion: mm(a, 0.002, inc), ecc: 0.002, inc, raan: R() * 360, argp: R() * 360, ma: R() * 360, bterm: 0.01, agom: 0.02 });
  }
  // geostationary belt: the mean anomaly puts each object at a random Earth-fixed longitude at the epoch
  for (let i = 0; i < 160; i++) {
    const lon = R() * 360;
    const raan = R() * 360;
    const argp = R() * 360;
    add("geo", {
      meanMotion: SIDEREAL + (R() - 0.5) * 0.0004,
      ecc: 0.0002 * R(),
      inc: i < 90 ? 0.05 + 0.2 * R() : 1 + 14 * R(),
      raan,
      argp,
      ma: (((lon + theta - raan - argp) % 360) + 360) % 360,
      bterm: 0.01,
      agom: 0.03,
    });
  }
  // highly elliptical: half-day orbits at the critical inclination, and transfer orbits to the geostationary altitude
  for (let i = 0; i < 12; i++) {
    add("heo", { meanMotion: 2.00561, ecc: 0.72, inc: 63.4, raan: 30 * i, argp: 270, ma: (i * 137) % 360, bterm: 0.01, agom: 0.02 });
  }
  for (let i = 0; i < 40; i++) {
    const rp = RE + 250 + 450 * R();
    const ra = RE + 35786 + 1500 * (R() - 0.5);
    const ecc = (ra - rp) / (ra + rp);
    const inc = 5 + 25 * R();
    add("heo", { meanMotion: mm((rp + ra) / 2, ecc, inc), ecc, inc, raan: R() * 360, argp: R() * 360, ma: R() * 360, bterm: 0.01, agom: 0.02 });
  }
  return rows;
}
