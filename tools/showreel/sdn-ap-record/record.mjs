// Records the SDN-AP reel's data by running the SDN-AP module, so every orbit, series and number the reel
// (tools/showreel/reels/sdn-ap.js) shows can be reproduced.
//   node tools/showreel/sdn-ap-record/record.mjs [--modules DIR] [--check | --embed]
// --modules names a checkout of the public modules repository (default: $SDN_MODULES_ROOT, else
// ../space-data-network-modules): propagator/sdn-ap at the commit whose module bytes the reel records (c0c5da38,
// dist/isomorphic/module.wasm sha256 b9bc708baa50...). `git archive <commit> propagator/sdn-ap | tar -x -C DIR` is enough.
// The module is opened through that tree's own test helper, so its dependencies must be installed: from the stack,
// node tools/deps-bundle/deps-bundle.mjs attach DIR/propagator/sdn-ap
// With no mode it records and prints a summary. --check records and compares with the data block embedded in the
// reel: exit 0 when every recorded number is identical (it says so when the module bytes differ from the recorded
// ones), exit 1 when any number differs; --embed rewrites that block. The data is a function of the module and of
// this recording only: seeded populations (no catalog data), the epoch 2026-10-24 12:00 UTC, and the environment
// the module itself holds (F10.7 150, Ap 15).
//
// This is recording and conformance tooling, never imported by the reel. Every orbit, density, height, reentry
// time, semi-major axis (sdnap_mean_elements_at, sdnap_osculating_elements) and radial and in-track offset
// (sdnap_frame_to_uvw) is the module's; JS moves bytes and counts.

import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import {
  EPOCH_FRAC, EPOCH_WHOLE, densityKgM3, environmentAtEpoch, frameProbe, heightKm, jdAt, meanAKm, openSdnAp, oscAKm, oscStates, reentryRef, spinAt, stateAt, toUvw,
} from "./module.mjs";
import { SIDEREAL, makePopulations } from "./populations.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const REEL = join(here, "..", "reels", "sdn-ap.js");
const BEGIN = "// BEGIN GENERATED DATA";
const END = "// END GENERATED DATA";

// ---------------------------------------------------------------------- packing
// Typed arrays are packed into one byte string, each 4-byte aligned; the bank describes each by offset and length.
function packer() {
  const chunks = [];
  let cursor = 0;
  const pack = (array, kind) => {
    const bytes = Buffer.from(array.buffer, array.byteOffset, array.byteLength);
    const entry = { offset: cursor, length: array.length, kind };
    const padding = (4 - (bytes.length % 4)) % 4;
    chunks.push(bytes, Buffer.alloc(padding));
    cursor += bytes.length + padding;
    return entry;
  };
  // States [x y z vx vy vz] x n as Int16: one scale for the positions and one for the velocities.
  const quantize = (states) => {
    let pmax = 0;
    let vmax = 0;
    for (let i = 0; i < states.length; i += 6) {
      for (let c = 0; c < 3; c++) {
        pmax = Math.max(pmax, Math.abs(states[i + c]));
        vmax = Math.max(vmax, Math.abs(states[i + 3 + c]));
      }
    }
    const pscale = pmax / 32000;
    const vscale = vmax / 32000;
    const q = new Int16Array(states.length);
    for (let i = 0; i < states.length; i += 6) for (let c = 0; c < 6; c++) q[i + c] = Math.round(states[i + c] / (c < 3 ? pscale : vscale));
    return { pscale, vscale, q };
  };
  const packStates = (states, meta = {}) => {
    const { pscale, vscale, q } = quantize(states);
    return { ...meta, n: states.length / 6, pscale, vscale, data: pack(q, "i16") };
  };
  return { pack, packStates, quantize, bytes: () => Buffer.concat(chunks) };
}
const f32 = (a) => Float32Array.from(a);
const radius = (s, k) => Math.hypot(s[6 * k], s[6 * k + 1], s[6 * k + 2]);

// ---------------------------------------------------------------------- the recording
function record({ m, THEORY, FRAME, sha256 }) {
  const out = { facts: {}, tracks: {} };
  const { pack, packStates, quantize, bytes } = packer();
  const row = (o) => ({ epochWhole: EPOCH_WHOLE, epochFrac: EPOCH_FRAC, bterm: 0.012, agom: 0.01, theory: THEORY.SDNAP, ...o });
  const trace = (i, at) => stateAt(m, i, at);

  out.facts.environment = environmentAtEpoch(m);
  out.facts.epoch = { jdWhole: EPOCH_WHOLE, jdFrac: EPOCH_FRAC, utc: "2026-10-24T12:00:00Z" };

  // The swarm: seeded orbits in every regime, a state every 10 minutes (low orbit) or 5 (the rest) for 200 minutes.
  const rows = makePopulations(m, row);
  const counts = {};
  for (const r of rows) counts[r.kind] = (counts[r.kind] ?? 0) + 1;
  out.facts.population = counts;
  const order = ["leo", "meo", "geo", "heo"];
  const swarm = (name, subset, dt, n) => {
    m.init(subset);
    const states = new Float64Array(subset.length * n * 6);
    for (let k = 0; k < n; k++) {
      const p = m.atMinutes(new Float64Array(subset.length).fill(k * dt));
      if (p.status.some((s) => s !== 0)) throw new Error(`${name} refused at ${k * dt} min`);
      for (let i = 0; i < subset.length; i++) {
        const at = (i * n + k) * 6;
        states.set([p.x[i], p.y[i], p.z[i], p.vx[i], p.vy[i], p.vz[i]], at);
      }
    }
    const { pscale, vscale, q } = quantize(states);
    out.tracks[name] = { count: subset.length, n, dt, pscale, vscale, kinds: subset.map((r) => order.indexOf(r.kind)), data: pack(q, "i16") };
    out.facts[`${name}Quantization`] = { positionKm: pscale, velocityKmS: vscale };
  };
  swarm("swarmLeo", rows.filter((r) => r.kind === "leo"), 10, 21);
  swarm("swarmHigh", rows.filter((r) => r.kind !== "leo"), 5, 41);

  // The Earth's rotation angle over the swarm's clock, from the module's own TEME to Earth-fixed rotation.
  {
    const spins = [];
    for (let t = 0; t <= 200; t += 10) spins.push(spinAt(m, t));
    for (let i = 1; i < spins.length; i++) while (spins[i] < spins[i - 1]) spins[i] += 2 * Math.PI;
    out.tracks.spin = { dt: 10, data: pack(f32(spins), "f32"), n: spins.length };
    out.facts.theta0 = spins[0];
  }

  // One revolution of a featured orbit for each regime.
  const loop = (name, set, minutes, dt) => {
    m.init([set]);
    const n = Math.round(minutes / dt) + 1;
    const s = new Float64Array(6 * n);
    for (let k = 0; k < n; k++) s.set(trace(0, k * dt), 6 * k);
    out.tracks[name] = packStates(s, { dt, minutes });
    let rmin = 1e12;
    let rmax = 0;
    for (let k = 0; k < n; k++) {
      rmin = Math.min(rmin, radius(s, k));
      rmax = Math.max(rmax, radius(s, k));
    }
    out.facts[name] = { perigeeRadiusKm: rmin, apogeeRadiusKm: rmax, periodHours: 24 / set.meanMotion };
  };
  loop("loopMeo", row({ meanMotion: 2.00561, ecc: 0.001, inc: 55, raan: 20, argp: 0, ma: 0, norad: 11 }), 720, 3);
  loop("loopGeo", row({ meanMotion: SIDEREAL, ecc: 0.0002, inc: 0.05, raan: 0, argp: 0, ma: 100, norad: 12 }), 1440, 20);
  loop("loopHeo", row({ meanMotion: 2.00561, ecc: 0.72, inc: 63.4, raan: 330, argp: 270, ma: 0, norad: 13 }), 720, 3);

  // A cislunar-class orbit: 0.1 rev/day, e 0.9, for 20 days.
  {
    m.init([row({ meanMotion: 0.1, ecc: 0.9, inc: 28.5, raan: 40, argp: 150, ma: 0, norad: 14 })]);
    const days = 20;
    const dt = 60;
    const n = days * 24 + 1;
    const s = new Float64Array(6 * n);
    for (let k = 0; k < n; k++) s.set(trace(0, k * dt), 6 * k);
    out.tracks.cislunar = packStates(s, { dt, minutes: days * 1440 });
    let rmin = 1e12;
    let rmax = 0;
    for (let k = 0; k < n; k++) {
      rmin = Math.min(rmin, radius(s, k));
      rmax = Math.max(rmax, radius(s, k));
    }
    out.facts.cislunar = { meanMotionRevDay: 0.1, eccentricity: 0.9, perigeeRadiusKm: rmin, apogeeRadiusKm: rmax, periodDays: 10 };
  }

  // Mean against osculating: one low orbit for three revolutions. The module gives the osculating semi-major axis of
  // each of its states (sdnap_osculating_elements) and the theory's mean one (sdnap_mean_elements_at).
  {
    m.init([row({ meanMotion: 15.2, ecc: 0.001, inc: 51.6, raan: 120, argp: 90, ma: 0, norad: 20 })]);
    const minutes = 285;
    const n = minutes + 1;
    const s = new Float64Array(6 * n);
    const aOsc = [];
    const aMean = [];
    const alt = [];
    for (let k = 0; k < n; k++) {
      const st = trace(0, k);
      s.set(st, 6 * k);
      aOsc.push(oscAKm(m, st));
      aMean.push(meanAKm(m, 0, k));
      alt.push(heightKm(m, st));
    }
    out.tracks.mo = { ...packStates(s, { dt: 1, minutes }), aOsc: pack(f32(aOsc), "f32"), aMean: pack(f32(aMean), "f32"), alt: pack(f32(alt), "f32") };
    let crossings = 0;
    for (let k = 1; k < n; k++) if ((aOsc[k] - aMean[k]) * (aOsc[k - 1] - aMean[k - 1]) < 0) crossings++;
    out.facts.mo = {
      cyclesPerRevolution: crossings / 2 / (minutes / (1440 / 15.2)),
      meanMotionRevDay: 15.2,
      eccentricity: 0.001,
      inclinationDeg: 51.6,
      aMeanKm: aMean[0],
      aMeanLastKm: aMean[n - 1],
      aOscMinKm: Math.min(...aOsc),
      aOscMaxKm: Math.max(...aOsc),
      altMinKm: Math.min(...alt),
      altMaxKm: Math.max(...alt),
    };
  }

  // Drag: a set that comes down (the README's M28 case: 16.08 rev/day, B_eff 0.02 m^2/kg), integrated to the ground by
  // the module's osculating integrator, with its reentry time and place from the module's reference.
  {
    m.init([row({ meanMotion: 16.08, ecc: 0.0004, inc: 51.6, raan: 40, argp: 90, ma: 10, bterm: 0.02, agom: 0, norad: 90040 })]);
    const y0 = trace(0, 0);
    const ref = reentryRef(m, y0, 0.02, 0, 12);
    if (ref.seconds < 0) throw new Error("no reentry within 12 days");
    const T = ref.seconds;
    // a state every 3 minutes to the end; the last 200 minutes every 15 s
    const times = [];
    for (let t = 0; t < T - 12000; t += 180) times.push(t);
    for (let t = Math.ceil((T - 12000) / 15) * 15; t < T; t += 15) times.push(t);
    times.push(T);
    const o = oscStates(m, y0, 0.02, 0, 0, [], times, 10);
    const n = times.length;
    const alt = [];
    const spin = [];
    for (let k = 0; k < n; k++) {
      alt.push(heightKm(m, [o[6 * k], o[6 * k + 1], o[6 * k + 2]]));
      spin.push(spinAt(m, times[k] / 60));
    }
    for (let k = 1; k < n; k++) while (spin[k] < spin[k - 1]) spin[k] += 2 * Math.PI;
    // the perigee altitude of each revolution: the minimum of the module's heights over each 95-minute window
    const perigee = [];
    for (let t0 = 0; t0 < T; t0 += 5700) {
      let lo = 1e9;
      for (let k = 0; k < n; k++) if (times[k] >= t0 && times[k] < t0 + 5700) lo = Math.min(lo, alt[k]);
      if (lo < 1e9) perigee.push([t0 + 2850, lo]);
    }
    // the last 200 minutes in the Earth-fixed frame (the module's TEME to Earth-fixed rotation), for the ground track
    const ecef = [];
    const first = times.findIndex((t) => t >= T - 12000);
    for (let k = first; k < n; k++) {
      const { out: e } = frameProbe(m, times[k] / 60, [o[6 * k], o[6 * k + 1], o[6 * k + 2], o[6 * k + 3], o[6 * k + 4], o[6 * k + 5]], 2);
      ecef.push(e[0], e[1], e[2]);
    }
    out.tracks.dragEcef = { first, data: pack(f32(ecef), "f32") };
    out.tracks.drag = {
      ...packStates(o),
      times: pack(f32(times), "f32"),
      alt: pack(f32(alt), "f32"),
      spin: pack(f32(spin), "f32"),
      perigee: pack(f32(perigee.flat()), "f32"),
      nPerigee: perigee.length,
    };
    out.facts.drag = {
      meanMotionRevDay: 16.08,
      eccentricity: 0.0004,
      inclinationDeg: 51.6,
      bEffM2Kg: 0.02,
      agomM2Kg: 0,
      startAltitudeKm: alt[0],
      reentrySeconds: T,
      reentryDays: T / 86400,
      reentryLatDeg: ref.lat,
      reentryLonDeg: ref.lon,
      integratorStepSeconds: 10,
    };
  }

  // The density the drag meets: the module's NRLMSISE-00 path, 70 to 800 km.
  {
    const heights = [];
    const logRho = [];
    for (let h = 70; h <= 800; h += h < 300 ? 5 : 10) {
      heights.push(h);
      logRho.push(Math.log10(densityKgM3(m, 0, h)));
    }
    out.tracks.density = { heights: pack(f32(heights), "f32"), n: heights.length, data: pack(f32(logRho), "f32") };
    out.facts.density = Object.fromEntries([150, 200, 250, 300, 400, 500, 600, 800].map((h) => [h, densityKgM3(m, 0, h)]));
  }

  // A maneuver: +1 m/s along the velocity at 20 minutes, with and without it, for 24 hours; the burned object's position
  // relative to the unburned one in the unburned object's radial and in-track axes (sdnap_frame_to_uvw).
  {
    m.init([row({ meanMotion: 15.5, ecc: 0.0005, inc: 51.6, raan: 120, argp: 90, ma: 30, bterm: 0.012, agom: 0.01, norad: 90030 })]);
    const y0 = trace(0, 0);
    const burnAt = 20 * 60; // s
    const dvKmS = 0.001;
    const times = [];
    for (let t = 0; t <= 24 * 3600; t += t < 3 * 3600 ? 30 : 300) times.push(t);
    const free = oscStates(m, y0, 0.012, 0.01, 0, [], times, 10);
    const burned = oscStates(m, y0, 0.012, 0.01, 0, [[burnAt, burnAt, 0, dvKmS, 0]], times, 10);
    const n = times.length;
    const sep = [];
    const alongKm = [];
    const radialKm = [];
    for (let k = 0; k < n; k++) {
      const r = [free[6 * k], free[6 * k + 1], free[6 * k + 2]];
      const v = [free[6 * k + 3], free[6 * k + 4], free[6 * k + 5]];
      const d = [burned[6 * k] - r[0], burned[6 * k + 1] - r[1], burned[6 * k + 2] - r[2]];
      sep.push(Math.hypot(...d));
      const [radial, along] = toUvw(m, r, v, d);
      radialKm.push(radial);
      alongKm.push(along);
    }
    out.tracks.man = {
      n,
      burnAtSeconds: burnAt,
      dvKmS,
      times: pack(f32(times), "f32"),
      sep: pack(f32(sep), "f32"),
      along: pack(f32(alongKm), "f32"),
      radial: pack(f32(radialKm), "f32"),
    };
    out.facts.man = {
      alongAfter24hKm: alongKm[n - 1],
      radialMaxKm: Math.max(...radialKm.map(Math.abs)),
      dvMS: dvKmS * 1000,
      burnAtMinutes: burnAt / 60,
      sepAfter1hKm: sep[times.findIndex((t) => t >= 3600)],
      sepAfter3hKm: sep[times.findIndex((t) => t >= 3 * 3600)],
      sepAfter24hKm: sep[n - 1],
      startAltitudeKm: heightKm(m, [free[0], free[1], free[2]]),
    };
  }

  // Resonance: 36 geostationary objects, 10 degrees apart, in the Earth-fixed frame for 120 days.
  {
    const theta = (spinAt(m, 0) * 180) / Math.PI;
    const rowsG = [];
    for (let k = 0; k < 36; k++) {
      const lon = 5 + 10 * k;
      rowsG.push(row({ meanMotion: SIDEREAL, ecc: 0.0001, inc: 0.05, raan: 0, argp: 0, ma: (((lon + theta) % 360) + 360) % 360, bterm: 0.01, agom: 0.02, norad: 100 + k }));
    }
    m.init(rowsG);
    const days = 120;
    const n = days + 1;
    const ecef = new Float32Array(36 * n * 3);
    const startLonDeg = [];
    const endLonDeg = [];
    for (let d = 0; d < n; d++) {
      const [whole, frac] = jdAt(d * 1440);
      const p = m.atJd(whole, frac, FRAME.ECEF);
      if (p.status.some((s) => s !== 0)) throw new Error("the resonance run was refused");
      for (let i = 0; i < 36; i++) {
        const at = (i * n + d) * 3;
        ecef[at] = p.x[i] / 1000;
        ecef[at + 1] = p.y[i] / 1000;
        ecef[at + 2] = p.z[i] / 1000;
        const lon = (Math.atan2(p.y[i], p.x[i]) * 180) / Math.PI;
        if (d === 0) startLonDeg.push(lon);
        if (d === days) endLonDeg.push(lon);
      }
    }
    out.tracks.res = { count: 36, n, dt: 1440, data: pack(ecef, "f32") };
    out.facts.res = { days, startLonDeg, endLonDeg };
  }

  const data = bytes();
  out.binaryBytes = data.length;
  out.moduleSha256 = sha256;
  return { bank: out, data };
}

// ---------------------------------------------------------------------- the block in the reel
// The bytes go in as hexadecimal: no letter run of it can spell a word the spelling check knows.
function formatBlock({ bank, data }) {
  const lines = data.toString("hex").match(/.{1,160}/g).join("\n");
  return `${BEGIN}
// Recorded output of the SDN-AP module: propagator/sdn-ap/dist/isomorphic/module.wasm, sha256 ${bank.moduleSha256}
// (the public modules repository at c0c5da38). Seeded populations only (no catalog data); epoch 2026-10-24 12:00:00 UTC;
// the environment in force is the module's own (F10.7 150, F10.7a 150, Ap 15). Positions and velocities are TEME km and
// km/s (Int16 with one scale per track), Earth-fixed km where named; series are Float32. BANK describes each array
// (offset and length into the bytes below, ${bank.binaryBytes} bytes, hexadecimal) and holds the numbers the module reported (BANK.facts).
// Regenerate or reproduce it with tools/showreel/sdn-ap-record/record.mjs (--embed, --check).
const BANK = ${JSON.stringify(bank, null, 1)};
const BANK_HEX = \`
${lines}
\`;
${END}
`;
}
function readBlock(source) {
  const i = source.indexOf(BEGIN);
  const j = source.indexOf(END);
  if (i < 0 || j < i) throw new Error(`${REEL} has no generated data block`);
  return { i, j: j + END.length + 1, text: source.slice(i, j + END.length + 1) };
}
// What differs between two blocks: the recorded numbers (facts, tracks, bytes) and the module they were recorded from.
function compare(hadText, nowText) {
  const part = (text) => {
    const bank = JSON.parse(/const BANK = (\{[\s\S]*?\n\});\nconst BANK_HEX/.exec(text)[1]);
    return { sha: bank.moduleSha256, facts: JSON.stringify(bank.facts), tracks: JSON.stringify(bank.tracks), bytes: /BANK_HEX = `\n([\s\S]*?)\n`;/.exec(text)[1] };
  };
  const a = part(hadText);
  const b = part(nowText);
  return {
    numbers: ["facts", "tracks", "bytes"].filter((key) => a[key] !== b[key]),
    module: a.sha === b.sha ? null : `the reel records module ${a.sha.slice(0, 12)}, this tree's is ${b.sha.slice(0, 12)}`,
  };
}

// ---------------------------------------------------------------------- command line
const VALUE_FLAGS = new Set(["modules"]);
const BOOLEAN_FLAGS = new Set(["check", "embed"]);
function parseArgs(argv) {
  const parsed = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith("--")) throw new Error(`unexpected argument ${a}`);
    const key = a.slice(2);
    if (BOOLEAN_FLAGS.has(key)) parsed[key] = true;
    else if (VALUE_FLAGS.has(key)) {
      const value = argv[++i];
      if (value === undefined || value.startsWith("--")) throw new Error(`--${key} needs a value`);
      parsed[key] = value;
    } else throw new Error(`unknown option --${key}`);
  }
  if (parsed.check && parsed.embed) throw new Error("--check and --embed are separate modes");
  return parsed;
}

const args = parseArgs(process.argv.slice(2));
const modules = resolve(args.modules ?? process.env.SDN_MODULES_ROOT ?? join(here, "..", "..", "..", "..", "space-data-network-modules"));
const sdn = await openSdnAp(modules);
process.stdout.write(`module ${sdn.sha256} from ${modules}\n`);
const recorded = record(sdn);
const block = formatBlock(recorded);
const { bank } = recorded;
process.stdout.write(`recorded ${recorded.data.length} bytes; ${Object.keys(bank.tracks).length} tracks; reentry ${bank.facts.drag.reentryDays.toFixed(3)} d, mean a ${bank.facts.mo.aMeanKm.toFixed(3)} km, swing ${(bank.facts.mo.aOscMaxKm - bank.facts.mo.aOscMinKm).toFixed(3)} km\n`);
if (args.check || args.embed) {
  if (!existsSync(REEL)) throw new Error(`no reel at ${REEL}`);
  const source = readFileSync(REEL, "utf8");
  const had = readBlock(source);
  if (args.check) {
    const found = had.text === block ? { numbers: [], module: null } : compare(had.text, block);
    if (found.numbers.length) {
      process.stdout.write(`DIFFERS in ${found.numbers.join(", ")}${found.module ? ` (${found.module})` : ""}\n`);
      process.exitCode = 1;
    } else process.stdout.write(`reproduces: every recorded number is identical${found.module ? ` (${found.module})` : had.text === block ? ", and the block is exactly what the recording writes" : ""}\n`);
  } else {
    writeFileSync(REEL, source.slice(0, had.i) + block + source.slice(had.j));
    process.stdout.write(`embedded into ${REEL}\n`);
  }
}
