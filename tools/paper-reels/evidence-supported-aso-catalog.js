// Intro reel for "Evidence-Supported ASO Catalog" (Koury and Jah, 1.9).
// 56 seconds, in plain words with the paper's own math: every orbit is an
// estimate with evidence behind it, data levels, the TLE-to-numerical
// handoff and its caveats, agreement with Orekit, accuracy against precise
// orbits as forces are added, and covariance carried with the orbit.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO, D,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutCubic, easeInCubic, easeOutBack,
  add, sub, mul, dot, cross, norm, rng, makeTau, OMEGA_K, orbitBasis, orbitPos, makeSky,
  limbCamera, project, createStudio,
} from "../showreel/kit.js";
import { prepareMath, createPaperKit, keyedCamera, orbitThrough } from "./paper-kit.js";

export const meta = { name: "reel", media: "docs/media/papers/evidence-supported-aso-catalog", poster: 1000, crf: [32, 44], audio: { track: "covariance", start: 36 } };

const DURATION = 56;
const FPS = 30;
const T_LEVELS = 6.0; // four levels of data
const T_HAND = 13.0; // a TLE hands its state to a numerical propagator
const T_SPLIT = T_HAND + 2.4;
const T_OREKIT = 21.0; // agreement with an independent implementation
const T_ACC = 28.0; // error against precise orbits as forces are added
const T_LIMIT = T_ACC + 6.6;
const T_VCM = 38.0; // covariance carried with the orbit
const T_CLAIM = 47.0; // every orbit shows its evidence
const T_END = 51.5; // lockup

const SATS = makeSky();

// Scene time slows while the handoff is explained.
const tau = makeTau((t) => lerp(1, 0.3, seg(t, T_HAND + 0.6, T_HAND + 1.4) * (1 - seg(t, T_SPLIT + 3.5, T_OREKIT))), DURATION);

const ORBIT_CAM = keyedCamera([
  [T_HAND, 24, -70, 3.25, 0.36, -0.02],
  [T_OREKIT - 0.3, 22, -76, 3.2, 0.36, -0.02],
  [T_OREKIT, 18, -100, 3.0, 0.36, 0.0],
  [T_ACC, 14, -112, 3.0, 0.36, 0.0],
]);
const VCM_CAM = keyedCamera([
  [T_VCM, 30, 150, 3.4, 0.42, 0.0],
  [T_CLAIM, 26, 165, 3.2, 0.42, 0.0],
]);
function cameraAt(t) {
  if (t < T_HAND) return limbCamera(t, -40, 30);
  if (t < T_VCM) return ORBIT_CAM(t);
  if (t < T_CLAIM) return VCM_CAM(t);
  return limbCamera(t, 20, 32);
}

// The handoff orbit, phased so the epoch sits upper left of the globe and
// both forecasts run on across its face.
const HAND = orbitThrough(cameraAt(T_SPLIT), [1120, 500], 1.28, [0.8, 1]);
HAND.w = OMEGA_K * Math.pow(HAND.r, -1.5);
const U_EPOCH = 0;
HAND.u0 = U_EPOCH - HAND.w * tau(T_SPLIT - 0.4);

// Two codes on one orbit: HPOP and Orekit.
const TWIN = orbitThrough(cameraAt(T_OREKIT + 3.5), [1300, 560], 1.11, [-0.6, 1]);
TWIN.w = OMEGA_K * Math.pow(TWIN.r, -1.5) * 0.8;
TWIN.u0 = -TWIN.w * tau(T_OREKIT + 3.5);

// The covariance orbit.
const COV = orbitThrough(cameraAt(T_VCM + 4.5), [1330, 520], 1.07, [1, 0.15]);
COV.w = OMEGA_K * Math.pow(COV.r, -1.5) * 0.6;
COV.u0 = -COV.w * tau(T_VCM + 4.5);

// Median 3D position error of the four geodetic spheres after 24 h (table, section 17.2).
const BARS = [
  ["POINT MASS", 46000, "46 km"],
  ["+ ZONAL HARMONICS TO DEGREE 20", 1300, "1.3 km"],
  ["+ SUN AND MOON", 257, "257 m"],
  ["+ RADIATION PRESSURE", 246, "246 m"],
  ["+ FULL FIELD 20 × 20, EARTH ORIENTATION", 6.2, "6.2 m"],
  ["+ SOLID TIDES, RELATIVITY", 4.8, "4.8 m"],
];

const LEVELS = [
  ["0", "RAW SENSOR DATA", "COUNTS, SIGNALS, IMAGES"],
  ["1", "MEASUREMENTS", "+ A MEASUREMENT MODEL"],
  ["2", "ESTIMATED STATES", "+ A DYNAMICS MODEL AND AN INFERENCE"],
  ["3", "AVERAGED ELEMENTS (TLE)", "+ A SECOND THEORY, SGP4, AND A SECOND FIT"],
];

const CONTRIBUTORS = ["ACTUAL PHYSICS", "DYNAMICS MODEL", "OBSERVATIONS", "MEASUREMENT MODEL", "INFERENCE METHOD"];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "WHITEPAPER · EVIDENCE-SUPPORTED ASO CATALOG", hudUrl: "SPACEDATANETWORK.ORG" });
  const { glc, sx, font, maskText, dotGlow, scrim, renderEarth, drawSatellites, strokeOrbitArc, eyebrow, block, beginSubframe, bloom, hud, stamp, badge } = studio;
  const math = await prepareMath({
    handoff: { key: "cat_handoff", px: 44, color: INK },
    dyn: { key: "cat_dyn", px: 44, color: INK },
    orekit: { key: "cat_orekit", px: 54, color: INK },
    cov: { key: "cat_cov", px: 56, color: INK },
    dn: { key: "cat_dn", px: 56, color: AMBER },
    rms: { key: "cat_rms", px: 48, color: AMBER },
  });
  const { eq, panel, label, stat, chip, paperLockup, strokePath, head } = createPaperKit(studio, math);

  // ------------------------------------------- every orbit is an estimate
  function drawContributors(t, alpha) {
    if (alpha <= 0) return;
    const x = 1420;
    CONTRIBUTORS.forEach((c, i) => {
      const t0 = 1.6 + i * 0.22;
      const k = easeOutBack(seg(t, t0, t0 + 0.45));
      chip(c, x, 250 + i * 86, k, alpha, { color: i === 0 ? AMBER : INK, px: 24, stroke: i === 0 ? AMBER : "rgba(245,245,247,0.45)" });
    });
    label("FIVE CONTRIBUTORS TO EVERY ORBIT", x, 172, 1.4, T_LEVELS - 0.45, t, { align: "center", color: MUTED, px: 22 });
  }

  // ------------------------------------------------ four levels of data
  function drawLevels(t, alpha) {
    if (alpha <= 0) return;
    const x = 1060;
    const w = 700;
    const h = 104;
    LEVELS.forEach(([n, name, adds], i) => {
      const y = 600 - i * 136;
      const t0 = T_LEVELS + 0.4 + i * 0.35;
      const k = easeOutExpo(seg(t, t0, t0 + 0.5));
      if (k <= 0) return;
      const top = i === 3;
      sx.save();
      sx.globalAlpha = alpha * k;
      sx.translate(0, (1 - k) * 30);
      panel(x, y, w, h, 1, { stroke: top ? AMBER : "rgba(245,245,247,0.25)" });
      sx.font = font(700, 54, SANS);
      sx.fillStyle = top ? AMBER : "rgba(245,245,247,0.35)";
      sx.textAlign = "left";
      sx.fillText(n, x + 32, y + 72);
      sx.font = font(650, 30, SANS);
      sx.letterSpacing = "2px";
      sx.fillStyle = top ? AMBER : INK;
      sx.fillText(name, x + 104, y + 46);
      sx.font = font(600, 20, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.fillText(adds, x + 104, y + 80);
      sx.restore();
      if (i > 0) {
        const ak = seg(t, t0 - 0.15, t0 + 0.2);
        sx.save();
        sx.globalAlpha = alpha * ak;
        sx.strokeStyle = "rgba(245,245,247,0.5)";
        sx.lineWidth = 2;
        sx.beginPath();
        sx.moveTo(x + 52, y + h + 30);
        sx.lineTo(x + 52, y + h + 6);
        sx.moveTo(x + 44, y + h + 14);
        sx.lineTo(x + 52, y + h + 6);
        sx.lineTo(x + 60, y + h + 14);
        sx.stroke();
        sx.restore();
      }
    });
    label("LEVEL", x + 34, 178, T_LEVELS + 0.3, T_HAND - 0.45, t, { px: 18 });
  }

  // ------------------------------------ the TLE-to-numerical handoff
  const hpop = (u) => {
    const k = Math.max(0, u - U_EPOCH);
    return { ...HAND, r: HAND.r + 0.09 * k * k };
  };
  function drawHandoff(cam, t, T, alpha) {
    if (alpha <= 0) return;
    const u = HAND.u0 + HAND.w * T;
    const split = easeInOutCubic(seg(t, T_SPLIT, T_SPLIT + 2.2));
    // the path so far, and the satellite reaching its epoch
    strokeOrbitArc(cam, HAND, U_EPOCH - 1.0, Math.min(u, U_EPOCH), "rgba(245,245,247,0.7)", alpha * 0.6, 1.8, 0.4);
    const qE = project(cam, orbitPos(HAND, U_EPOCH));
    if (u < U_EPOCH) head(cam, orbitPos(HAND, u), INK, alpha, 6);
    // the two forecasts from one state
    if (split > 0) {
      const uMax = U_EPOCH + 0.75 * split;
      strokePath(cam, (v) => orbitPos(HAND, v), U_EPOCH, uMax, CYAN, alpha * 0.95, 2.4, { dash: [10, 8] });
      strokePath(cam, (v) => orbitPos(hpop(v), v - 0.22 * (v - U_EPOCH) ** 2), U_EPOCH, uMax, AMBER, alpha * 0.95, 2.6);
      const qa = head(cam, orbitPos(HAND, uMax), CYAN, alpha, 6);
      const qb = head(cam, orbitPos(hpop(uMax), uMax - 0.22 * (uMax - U_EPOCH) ** 2), AMBER, alpha, 6);
      const lk = seg(t, T_SPLIT + 1.2, T_SPLIT + 1.6);
      if (qa && qb && lk > 0) {
        chip("SGP4", qa.x - 90, qa.y + 34, easeOutBack(lk), alpha, { color: CYAN, px: 20 });
        chip("NUMERICAL", qb.x + 110, qb.y - 30, easeOutBack(lk), alpha, { color: AMBER, px: 20 });
      }
      const wk = seg(t, T_SPLIT + 2.4, T_SPLIT + 2.8);
      if (qa && qb && wk > 0) chip("WHICH IS CLOSER? AN EMPIRICAL QUESTION", 1440, 790, easeOutBack(wk), alpha * (1 - seg(t, T_OREKIT - 0.5, T_OREKIT - 0.2)), { color: INK, px: 20 });
    }
    // the epoch: the TLE becomes one state
    const ek = seg(t, T_HAND + 1.0, T_HAND + 1.4);
    if (qE && ek > 0) {
      const ring = seg(t, T_HAND + 1.0, T_HAND + 2.0);
      sx.save();
      sx.globalAlpha = alpha * (1 - ring);
      sx.strokeStyle = AMBER;
      sx.lineWidth = 2.5;
      sx.beginPath();
      sx.arc(qE.x, qE.y, 10 + easeOutExpo(ring) * 70, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      head(cam, orbitPos(HAND, U_EPOCH), INK, alpha, 7);
      chip("TLE EPOCH · ONE ESTIMATED STATE", qE.x - 190, qE.y + 56, easeOutBack(ek), alpha, { color: INK, px: 20 });
    }
  }

  // ------------------------------------------------ HPOP against Orekit
  function drawTwin(cam, t, T, alpha) {
    if (alpha <= 0) return;
    const u = TWIN.u0 + TWIN.w * T;
    strokeOrbitArc(cam, TWIN, u - 1.3, u, AMBER, alpha * 0.9, 2.4, 0.5);
    strokeOrbitArc(cam, TWIN, u - 1.3, u, CYAN, alpha * 0.55, 1.2, 0.2, [6, 9]);
    const q = head(cam, orbitPos(TWIN, u), AMBER, alpha, 6);
    if (q) {
      sx.save();
      sx.globalAlpha = alpha;
      sx.strokeStyle = CYAN;
      sx.lineWidth = 2;
      sx.beginPath();
      sx.arc(q.x, q.y, 13, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      const lk = easeOutBack(seg(t, T_OREKIT + 0.8, T_OREKIT + 1.2));
      chip("HPOP", q.x + 82, q.y - 26, lk, alpha, { color: AMBER, px: 20 });
      chip("OREKIT 13.1", q.x + 122, q.y + 30, lk, alpha, { color: CYAN, px: 20 });
    }
    const orbits = ["400 km", "700 km SUN-SYNCHRONOUS", "GPS", "GEOSTATIONARY", "MOLNIYA"];
    sx.save();
    sx.font = font(600, 20, SANS);
    sx.letterSpacing = "2.5px";
    const widths = orbits.map((o) => sx.measureText(o).width + 36);
    sx.restore();
    let x = 150;
    orbits.forEach((o, i) => {
      const k = easeOutBack(seg(t, T_OREKIT + 2.0 + i * 0.12, T_OREKIT + 2.4 + i * 0.12));
      chip(o, x + widths[i] / 2, 470, k, alpha, { color: INK, px: 20, stroke: "rgba(245,245,247,0.4)" });
      x += widths[i] + 12;
    });
    label("FIVE ORBITS · 63 CASES · HOURLY FOR 24 h", 152, 422, T_OREKIT + 1.8, T_ACC - 0.45, t, { px: 18, color: AMBER });
  }

  // ------------------------------------- error as forces are added (log bars)
  function drawBars(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 1110;
    const x1 = 1790;
    const lo = Math.log10(1);
    const hi = Math.log10(100000);
    const X = (v) => lerp(x0, x1, (Math.log10(v) - lo) / (hi - lo));
    const y0 = 196;
    const row = 72;
    label("MEDIAN 3D POSITION ERROR AFTER ONE DAY", 150, 168, T_ACC + 0.3, T_VCM - 0.45, t, { px: 22, color: AMBER });
    label("FOUR LASER-RANGED GEODETIC SPHERES, STARTED FROM PRECISE ORBITS", 150, 204, T_ACC + 0.4, T_VCM - 0.45, t, { px: 18 });
    // decade grid
    ["1 m", "10 m", "100 m", "1 km", "10 km", "100 km"].forEach((s, i) => {
      const gx0 = lerp(x0, x1, i / 5);
      const k = seg(t, T_ACC + 0.4, T_ACC + 0.8);
      sx.save();
      sx.globalAlpha = alpha * k * 0.5;
      sx.strokeStyle = "rgba(245,245,247,0.35)";
      sx.lineWidth = 1;
      sx.setLineDash([3, 6]);
      sx.beginPath();
      sx.moveTo(gx0, y0 + 40);
      sx.lineTo(gx0, y0 + 60 + BARS.length * row);
      sx.stroke();
      sx.restore();
      label(s, gx0, y0 + 96 + BARS.length * row, T_ACC + 0.5, T_VCM - 0.45, t, { px: 18, align: "center", tracking: 1 });
    });
    BARS.forEach(([name, v, txt], i) => {
      const y = y0 + 60 + i * row;
      const t0 = T_ACC + 0.6 + i * 0.55;
      const k = easeOutExpo(seg(t, t0, t0 + 0.6));
      if (k <= 0) return;
      const last = i === BARS.length - 1;
      const col = last ? AMBER : i === 0 ? "rgba(255,59,48,0.85)" : "rgba(245,245,247,0.75)";
      label(name, x0 - 26, y + 34, t0, T_VCM - 0.45, t, { px: 19, align: "right", color: last ? AMBER : INK, tracking: 2 });
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = col;
      sx.beginPath();
      sx.roundRect(x0, y + 10, Math.max(4, (X(v) - x0) * k), 34, 6);
      sx.fill();
      sx.restore();
      if (last) dotGlow(X(v), y + 27, 30, AMBER, alpha * 0.4);
      label(txt, X(v) + 16, y + 37, t0 + 0.35, T_VCM - 0.45, t, { px: 24, color: last ? AMBER : INK, tracking: 1, weight: 700 });
    });
  }
  function drawAccStats(t) {
    const t0 = T_LIMIT - 1.6;
    stat("4.8 m", "SPHERES · 1 DAY", 1060, 860, t0, T_VCM - 0.45, t, { px: 80 });
    stat("14 m", "SPHERES · 3 DAYS", 1340, 860, t0 + 0.15, T_VCM - 0.45, t, { px: 80 });
    stat("29 m", "GPS · 1 DAY", 1630, 860, t0 + 0.3, T_VCM - 0.45, t, { px: 80, color: INK });
  }

  // ------------------------------------------- covariance with the orbit
  function drawCov(cam, t, T, alpha) {
    if (alpha <= 0) return;
    const u = COV.u0 + COV.w * T;
    strokeOrbitArc(cam, COV, u - 0.8, u, "rgba(245,245,247,0.8)", alpha * 0.6, 1.8, 0.3);
    strokeOrbitArc(cam, COV, u, u + 0.9, "rgba(245,165,36,0.9)", alpha * 0.5, 1.6, 0.2, [5, 8]);
    const p = orbitPos(COV, u);
    const q = project(cam, p);
    const q2 = project(cam, orbitPos(COV, u + 0.02));
    if (!q || !q2) return;
    const ang = Math.atan2(q2.y - q.y, q2.x - q.x);
    const grow = easeInOutCubic(seg(t, T_VCM + 1.2, T_CLAIM - 1.5));
    const a = lerp(30, 280, grow);
    const b = lerp(18, 40, grow);
    const R = rng(77);
    sx.save();
    sx.translate(q.x, q.y);
    sx.rotate(ang);
    sx.globalAlpha = alpha * seg(t, T_VCM + 0.8, T_VCM + 1.2);
    // samples drawn from the propagated covariance
    sx.fillStyle = "rgba(245,165,36,0.75)";
    for (let i = 0; i < 260; i++) {
      const r1 = Math.sqrt(-2 * Math.log(R() + 1e-9)) * Math.cos(2 * Math.PI * R());
      const r2 = Math.sqrt(-2 * Math.log(R() + 1e-9)) * Math.cos(2 * Math.PI * R());
      sx.fillRect(r1 * a * 0.5 - 1.2, r2 * b * 0.5 - 1.2, 2.4, 2.4);
    }
    [1, 2, 3].forEach((s, i) => {
      sx.strokeStyle = i === 0 ? AMBER : `rgba(245,165,36,${0.55 - i * 0.18})`;
      sx.lineWidth = i === 0 ? 2.2 : 1.4;
      sx.setLineDash(i === 0 ? [] : [6, 6]);
      sx.beginPath();
      sx.ellipse(0, 0, (a * s) / 2, (b * s) / 2, 0, 0, Math.PI * 2);
      sx.stroke();
    });
    sx.restore();
    head(cam, p, INK, alpha, 6);
    const lk = easeOutBack(seg(t, T_VCM + 2.0, T_VCM + 2.4));
    chip("ALONG-TRACK UNCERTAINTY GROWS", q.x + Math.cos(ang) * 40 + 20, q.y + 150, lk, alpha * (1 - seg(t, T_CLAIM - 0.5, T_CLAIM - 0.2)), { color: AMBER, px: 20 });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_LEVELS, 0.6], [T_HAND, 1], [T_OREKIT, 0.8], [T_ACC, 1], [T_VCM, 1], [T_CLAIM, 1], [T_END + 0.2, 1]]);
  const pulse = (t, c, w) => Math.exp(-Math.pow((t - c) / w, 2));
  const flash = (t) => 0.1 * pulse(t, T_HAND + 1.02, 0.05) + 0.08 * pulse(t, T_LIMIT - 1.6, 0.05);
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    const T = tau(t);
    const cam = cameraAt(t);
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const under = seg(t, T_ACC - 0.1, T_ACC + 0.4) * (1 - seg(t, T_VCM - 0.3, T_VCM + 0.1));
    const dim = 1 - 0.45 * seg(t, T_LEVELS, T_LEVELS + 0.5) * (1 - seg(t, T_HAND - 0.2, T_HAND));
    const earthFade = seg(t, 0, 1.2) * (1 - 0.8 * under) * dim * (1 - out);
    if (earthFade > 0.001) {
      const sunAz = t < T_HAND ? 40 : t < T_VCM ? 120 : t < T_CLAIM ? 70 : 40;
      renderEarth(cam, t, earthFade, 0.22 * (1 - seg(t, 1.5, 3)), sunAz, t < T_HAND || t >= T_CLAIM ? 18 : 8);
      if (under > 0) sx.filter = `blur(${14 * under}px)`;
      sx.drawImage(glc, 0, 0);
      sx.filter = "none";
    }
    const limb = t < T_HAND || t >= T_CLAIM;
    const satA = seg(t, 0.3, 0.9) * (limb ? 1 : 0.45) * (1 - under) * dim * (1 - out);
    if (satA > 0.01) drawSatellites(cam, t + 2.0, T, satA, t < T_HAND, SATS);
    if (t < T_LEVELS + 0.1) drawContributors(t, 1 - seg(t, T_LEVELS - 0.45, T_LEVELS - 0.05));
    if (t > T_LEVELS - 0.05 && t < T_HAND + 0.1) drawLevels(t, seg(t, T_LEVELS, T_LEVELS + 0.3) * (1 - seg(t, T_HAND - 0.4, T_HAND)));
    if (t > T_HAND - 0.05 && t < T_OREKIT + 0.1) drawHandoff(cam, t, T, seg(t, T_HAND, T_HAND + 0.3) * (1 - seg(t, T_OREKIT - 0.4, T_OREKIT)));
    if (t > T_OREKIT - 0.05 && t < T_ACC + 0.1) drawTwin(cam, t, T, seg(t, T_OREKIT, T_OREKIT + 0.3) * (1 - seg(t, T_ACC - 0.4, T_ACC)));
    if (t > T_ACC - 0.05 && t < T_VCM + 0.1) drawBars(t, seg(t, T_ACC, T_ACC + 0.3) * (1 - seg(t, T_VCM - 0.4, T_VCM)));
    if (t > T_VCM - 0.05 && t < T_CLAIM + 0.1) drawCov(cam, t, T, seg(t, T_VCM, T_VCM + 0.3) * (1 - seg(t, T_CLAIM - 0.4, T_CLAIM)));
    scrim((1 - out) * 0.9);

    // -------------------------------------------------------- the type
    if (t < T_LEVELS + 0.1) {
      eyebrow("WHITEPAPER · EDITION 1.9", 2, 0.5, T_LEVELS - 0.45, t);
      block([["Every orbit is an estimate.", INK], ["What supports it?", AMBER]], "A CATALOG OF HUMAN-MADE OBJECTS IN ORBIT, FROM MANY PROVIDERS", 0.6, T_LEVELS - 0.4, t, 84);
    }
    if (t > T_LEVELS - 0.05 && t < T_HAND + 0.1) {
      block([["Each step adds a model", INK], ["and loses information.", AMBER]], "A TLE SITS AT LEVEL 3: MORE PROCESSED, NOT MORE ACCURATE", T_LEVELS + 0.1, T_HAND - 0.4, t, 84);
    }
    if (t > T_HAND - 0.05 && t < T_OREKIT + 0.1) {
      eq("handoff", 150, 214, T_HAND + 0.5, T_OREKIT - 0.45, t);
      eq("dyn", 150, 330, T_SPLIT + 0.6, T_OREKIT - 0.45, t);
      label("SHARED PHYSICS + OBJECT-SPECIFIC FORCES + MODEL ERROR", 152, 404, T_SPLIT + 0.9, T_OREKIT - 0.45, t, { px: 18 });
      block([["Same starting state.", INK], ["Different physics after.", AMBER]], "A TLE CARRIES NO COVARIANCE: ITS UNCERTAINTY IS MARKED UNKNOWN", T_HAND + 0.1, T_OREKIT - 0.4, t, 84);
    }
    if (t > T_OREKIT - 0.05 && t < T_ACC + 0.1) {
      eq("orekit", 150, 250, T_OREKIT + 0.5, T_ACC - 0.45, t);
      label("LARGEST POSITION DIFFERENCE, IDENTICAL INPUTS", 152, 352, T_OREKIT + 0.9, T_ACC - 0.45, t, { px: 18 });
      block([["Checked against", INK], ["an independent code.", AMBER]], "HPOP AND OREKIT AGREE WITHIN 15 MM OVER A DAY", T_OREKIT + 0.1, T_ACC - 0.4, t, 84);
    }
    if (t > T_ACC - 0.05 && t < T_VCM + 0.1) {
      block([["Add the physics.", INK], ["Watch the error fall.", AMBER]], null, T_ACC + 0.1, T_VCM - 0.4, t, 84);
      studio.caption("FROM PRECISE STARTING STATES, NOT YET FROM ELEMENT SETS", null, T_ACC + 0.5, T_VCM - 0.34, t);
      drawAccStats(t);
    }
    if (t > T_VCM - 0.05 && t < T_CLAIM + 0.1) {
      eq("cov", 150, 220, T_VCM + 0.4, T_CLAIM - 0.45, t);
      eq("dn", 150, 360, T_VCM + 1.6, T_CLAIM - 0.45, t);
      label("MEAN-MOTION ROW, READ AS RELATIVE", 262, 350, T_VCM + 1.8, T_CLAIM - 0.45, t, { px: 20 });
      eq("rms", 150, 470, T_VCM + 2.6, T_CLAIM - 0.45, t);
      label("WHEN THE WEIGHTED RMS EXCEEDS 1", 152, 524, T_VCM + 2.9, T_CLAIM - 0.45, t, { px: 18 });
      block([["Uncertainty travels", INK], ["with the orbit.", AMBER]], "EVERY PRINTED U, V, W SIGMA OF FOUR SP MESSAGES, WITHIN 1 %", T_VCM + 0.1, T_CLAIM - 0.4, t, 84);
    }
    if (t > T_CLAIM - 0.05 && t < T_END + 0.1) {
      block([["Every orbit shows", INK], ["its evidence and its limits.", AMBER]], "DIGITALLY SIGNED · REPEATABLE IN A WEB BROWSER", T_CLAIM + 0.1, T_END - 0.4, t, 84);
      const st = seg(t, T_CLAIM + 1.0, T_CLAIM + 1.17);
      if (st > 0) stamp(1480, 300, st, 1 - seg(t, T_END - 0.4, T_END));
    }
    paperLockup({ eyebrow: "WHITEPAPER · EDITION 1.9", title: ["Evidence-Supported", "ASO Catalog"], byline: "Anthony “TJ” Koury III and Dr. Moriba Jah", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
