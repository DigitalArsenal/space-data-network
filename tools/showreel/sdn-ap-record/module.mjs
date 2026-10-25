// The SDN-AP module as the recording uses it. It is opened through the public modules repository's own test
// helper (propagator/sdn-ap/test/lib/module.mjs, which runs the built dist/isomorphic/module.wasm through the
// module SDK's browser harness in Node), and the functions below call the exports the recording needs. JS here only
// moves bytes and converts units: every state, density, height, mean motion and reentry time comes out of the module.
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

export const EPOCH_WHOLE = 2461337.5; // 2026-10-24 00:00 UTC as a Julian date
export const EPOCH_FRAC = 0.5; // 12:00 UTC
const D50_OFFSET = 2433281.5; // Julian date of 1950 January 0.0, the module's day count
export const RE = 6378.1363; // km, EGM-96 (SPEC 1.3)
export const MU = 398600.4415; // km^3/s^2, EGM-96 (SPEC 1.3)
export const KE = 0.074366859954178854; // SPEC 1.3
const DEG = Math.PI / 180;

/** Opens the module of a checkout of the public modules repository: { m, THEORY, FRAME, sha256 }. */
export async function openSdnAp(modulesRoot) {
  const helper = join(modulesRoot, "propagator", "sdn-ap", "test", "lib", "module.mjs");
  if (!existsSync(helper)) throw new Error(`no propagator/sdn-ap in ${modulesRoot}: pass --modules <a checkout of the public modules repository>`);
  let lib;
  try {
    lib = await import(pathToFileURL(helper).href);
  } catch (error) {
    if (error.code !== "ERR_MODULE_NOT_FOUND") throw error;
    throw new Error(`${error.message}\nthe helper's dependencies are not installed: from the stack, node tools/deps-bundle/deps-bundle.mjs attach ${join(modulesRoot, "propagator", "sdn-ap")}`);
  }
  const m = await lib.openModule({ threads: 0 });
  return { m, THEORY: lib.THEORY, FRAME: lib.FRAME, sha256: createHash("sha256").update(readFileSync(lib.wasmPath)).digest("hex") };
}

/** The Julian date (whole, fraction) `minutes` after the epoch. */
export function jdAt(minutes) {
  const days = EPOCH_FRAC + minutes / 1440;
  return [EPOCH_WHOLE + Math.floor(days), days - Math.floor(days)];
}
export const d50At = (minutes) => EPOCH_WHOLE + EPOCH_FRAC + minutes / 1440 - D50_OFFSET;

const view = (m, pointer, count) => new Float64Array(m.harness.memory.buffer, pointer, count);

/** sdnap_frame_probe: a state (km, km/s) rotated by `mode` (2: TEME to Earth-fixed) at `minutes` after the epoch. */
export function frameProbe(m, minutes, state, mode) {
  const x = m.x;
  const [whole, frac] = jdAt(minutes);
  const pin = x.sdnap_alloc(48);
  const pout = x.sdnap_alloc(48);
  view(m, pin, 6).set(state);
  const status = x.sdnap_frame_probe(whole, frac, pin, pout, mode);
  const out = Array.from(view(m, pout, 6));
  x.sdnap_free(pin);
  x.sdnap_free(pout);
  return { status, out };
}

/** The Earth's rotation angle (rad) at `minutes`, as the module's own TEME to Earth-fixed rotation shows it. */
export function spinAt(m, minutes) {
  const { out } = frameProbe(m, minutes, [RE, 0, 0, 0, 0, 0], 2);
  return -Math.atan2(out[1], out[0]);
}

/** sdnap_geodetic_height_km of a position (km). */
export function heightKm(m, r) {
  const p = m.x.sdnap_alloc(24);
  view(m, p, 3).set([r[0], r[1], r[2]]);
  const h = m.x.sdnap_geodetic_height_km(p);
  m.x.sdnap_free(p);
  return h;
}

/** SPEC 1.4: the mean semi-major axis (km) of a Brouwer mean motion (rev/day), a = R_E (k_e / n)^(2/3), n in rad/min. */
export const meanA = (revPerDay) => RE * Math.pow(KE / ((revPerDay * 2 * Math.PI) / 1440), 2 / 3);

/** SPEC 1.4 inverted: the Brouwer mean motion (rev/day) of a mean semi-major axis (km). */
export const revPerDay = (aKm) => ((KE / Math.pow(aKm / RE, 1.5)) * 1440) / (2 * Math.PI);

/**
 * sdnap_osculating_states, the module's own osculating integrator: TEME states at ascending times `ts` (s from the
 * epoch) from the epoch state y0, with `burns` [t0, t1, u, v, w] (an impulse in km/s when t1 == t0), RK4 at `step` s.
 */
export function oscStates(m, y0, bterm, agom, flags, burns, ts, step = 10) {
  const x = m.x;
  const nb = burns.length;
  const pY = x.sdnap_alloc(48);
  const pB = x.sdnap_alloc(Math.max(8, 40 * nb));
  const pT = x.sdnap_alloc(8 * ts.length);
  const pO = x.sdnap_alloc(48 * ts.length);
  view(m, pY, 6).set(y0);
  if (nb) view(m, pB, 5 * nb).set(burns.flat());
  view(m, pT, ts.length).set(ts);
  const status = x.sdnap_osculating_states(EPOCH_WHOLE, EPOCH_FRAC, pY, bterm, agom, flags, pB, nb, pT, ts.length, pO, step);
  const out = status === 0 ? view(m, pO, 6 * ts.length).slice() : null;
  for (const p of [pY, pB, pT, pO]) x.sdnap_free(p);
  if (!out) throw new Error("sdnap_osculating_states refused");
  return out;
}

/** sdnap_reentry_reference: seconds from the epoch to 80 km (negative: not within maxDays), and the latitude and longitude (deg) there. */
export function reentryRef(m, y0, bterm, agom, maxDays) {
  const x = m.x;
  const pY = x.sdnap_alloc(48);
  const pL = x.sdnap_alloc(16);
  view(m, pY, 6).set(y0);
  const seconds = x.sdnap_reentry_reference(EPOCH_WHOLE, EPOCH_FRAC, pY, bterm, agom, maxDays, pL);
  const [lat, lon] = view(m, pL, 2);
  x.sdnap_free(pY);
  x.sdnap_free(pL);
  return { seconds, lat, lon };
}

/** sdnap_density_probe: the density (kg/m^3) at an altitude over (latDeg, lonDeg) at `minutes` after the epoch, the module's NRLMSISE-00 path. */
export function densityKgM3(m, minutes, altKm, latDeg = 0, lonDeg = 0) {
  return m.x.sdnap_density_probe(d50At(minutes), (RE + altKm) / RE, latDeg * DEG, lonDeg * DEG, 0);
}

/** The environment the module holds at the epoch (sdnap_environment_at): F10.7, its 81-day mean, Ap, the density correction s. */
export function environmentAtEpoch(m) {
  const p = m.x.sdnap_alloc(32);
  m.x.sdnap_environment_at(d50At(0), p);
  const [f107, f107a, ap, s] = view(m, p, 4);
  m.x.sdnap_free(p);
  return { f107, f107a, ap, s };
}

/** The TEME state (km, km/s) of row i of the loaded set at `minutes` after its epoch. */
export function stateAt(m, i, minutes) {
  const p = m.atMinutes(new Float64Array(i + 1).fill(minutes));
  return [p.x[i], p.y[i], p.z[i], p.vx[i], p.vy[i], p.vz[i]];
}
