// Catalog reel: how a space catalog is built, in plain words, for
// spacedatanetwork.org/catalog.html. 40 seconds: the crowded sky, sensors on
// the ground, sightings fit into an orbit, uncertainty that grows and
// shrinks, many digitally signed sources feeding one catalog, and a catalog
// anyone can add to and check.

import {
  W, H, AMBER, SAT, INK, MUTED, SANS, MONO, D, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack,
  add, mul, dot, cross, norm, rng, makeTau, OMEGA_K, orbitBasis, orbitPos, makeSky, lookAt, geoDir, project, occluded, createStudio,
} from "../kit.js";

export const meta = { name: "sdn-catalog-reel", media: "docs/media/catalog", poster: 780 };

const DURATION = 40;
const FPS = 30;
const T_SENS = 5.0; // radars and telescopes
const T_FIT = 11.0; // sightings become an orbit
const T_UNC = 17.5; // uncertainty
const T_SRC = 23.5; // many sources, one catalog
const T_CHECK = 30.0; // anyone can add, anyone can check
const T_END = 35.0; // lockup

const SATS = makeSky();
const LEO = SATS.filter((s) => s.kind === 0);

// Scene time: normal speed, slowed once the orbit is fit so the prediction
// plays out in frame.
const tau = makeTau((t) => 1 - 0.85 * seg(t, T_FIT + 4.4, T_FIT + 5.6), DURATION);

// The orbit the middle of the piece follows. Higher than most of the sky, so
// it reads clearly around the planet.
const FIT = { ...orbitBasis(52 * D, 210 * D), r: 1.45 };
FIT.w = OMEGA_K * Math.pow(FIT.r, -1.5);
const FIT_N = norm(cross(FIT.e1, FIT.e2));

// Ground sensors: phased-array radars and telescopes (lat, lon, kind, label,
// label offset).
const SENSORS = [
  [30.57, -86.21, "radar", "RADAR · FLORIDA", [-50, 84]],
  [39.14, -121.35, "radar", null],
  [41.75, -70.54, "radar", null],
  [48.72, -97.9, "radar", null],
  [33.82, -106.66, "scope", "TELESCOPE · NEW MEXICO", [-40, -64]],
];

// Camera keys: direction, distance, frame offset. Moves ease between keys.
const FIT_DIR = norm(add(mul(FIT_N, 0.95), mul(FIT.e1, 0.3)));
const KEYS = [
  [0.0, geoDir(14, -30, 0), 4.9, [0.33, -0.02]],
  [4.4, geoDir(22, -58, 0), 3.6, [0.32, -0.02]],
  [5.7, geoDir(28, -96, 0), 3.0, [0.3, -0.03]],
  [10.4, geoDir(30, -101, 0), 2.9, [0.3, -0.03]],
  [11.7, FIT_DIR, 4.15, [0.3, 0.0]],
  [22.9, norm(add(FIT_DIR, mul(FIT.e2, 0.1))), 4.0, [0.3, 0.0]],
  [24.3, geoDir(8, -10, 0), 3.4, [0.24, 0.0]],
  [DURATION, geoDir(8, 20, 0), 3.4, [0.24, 0.0]],
];
function cameraAt(t) {
  let i = 0;
  while (i < KEYS.length - 2 && t >= KEYS[i + 1][0]) i++;
  const [t0, d0, r0, o0] = KEYS[i];
  const [t1, d1, r1, o1] = KEYS[i + 1];
  const k = easeInOutCubic(seg(t, t0, t1));
  const dir = norm(add(mul(d0, 1 - k), mul(d1, k)));
  const dist = lerp(r0, r1, k);
  const pos = mul(dir, dist);
  return { ...lookAt(pos, [0, 0, 0], [0, 1, 0]), spin: 0.045 * t, off: [lerp(o0[0], o1[0], k), lerp(o0[1], o1[1], k)], zoom: 1, dist };
}

// Where the satellite should be when the fit completes: the phase that keeps
// the three passes and the satellite on the right of the frame, clear of the
// type in the lower left.
const T_FITTED = T_FIT + 4.1;
const PASSES = [[-1.9, "PASS 1 · RADAR"], [-1.15, "PASS 2 · TELESCOPE"], [-0.45, "PASS 3 · RADAR"]];
const U_SAT = (() => {
  const cam = cameraAt(T_FITTED);
  let best = 0;
  let score = -Infinity;
  for (let u = 0; u < Math.PI * 2; u += 0.01) {
    // The type owns the lower left; everything else is fair game.
    let sc = 0;
    for (const du of [-2.05, -1.9, -1.15, -0.45, 0, 0.5, 1.0, 1.6]) {
      const q = project(cam, orbitPos(FIT, u + du));
      if (!q) { sc -= 1e6; continue; }
      if (q.x < 1050 && q.y > 520) sc -= 2000 + (1050 - q.x) + (q.y - 520);
      sc -= 4 * Math.max(0, 110 - q.y) + 4 * Math.max(0, q.y - 990);
      if (du < 0) sc += 0.5 * q.x;
    }
    if (sc > score) { score = sc; best = u; }
  }
  return best;
})();
FIT.u0 = U_SAT - FIT.w * tau(T_FITTED);

// Sightings: three passes behind the satellite, each a handful of noisy points.
const OBS = (() => {
  const R = rng(91);
  const list = [];
  PASSES.forEach(([c, label], pass) => {
    for (let j = 0; j < 5; j++) {
      list.push({ u: U_SAT + c + (j - 2) * 0.06, dr: (R() - 0.5) * 0.05, pass, label: j === 2 ? label : null, i: list.length });
    }
  });
  return list;
})();

const ROWS = [
  ["ISS (ZARYA)", "SATELLITE OPERATOR", 2],
  ["COSMOS 2251 DEBRIS", "RADAR NETWORK", 0],
  ["GOES 16", "TELESCOPES", 1],
  ["STARLINK-1007", "SATELLITE OPERATOR", 2],
  ["SL-16 ROCKET BODY", "RADAR NETWORK", 0],
  ["HUBBLE", "UNIVERSITY LAB", 3],
  ["NOAA 20", "TELESCOPES", 1],
];
const SOURCES = ["RADAR NETWORK", "TELESCOPES", "SATELLITE OPERATOR", "UNIVERSITY LAB"];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "SPACE DATA NETWORK · THE CATALOG", hudUrl: "SPACEDATANETWORK.ORG" });
  const {
    glc, sx, gx, font, maskText, decodeText, dotGlow, scrim, pill, badge, keyIcon, bezierPts, pointOn, strokeOn,
    renderEarth, drawSatellites, drawOrbitPath, strokeOrbitArc, eyebrow, lockup, LX, lineYs, block, caption,
    beginSubframe, bloom, hud,
  } = studio;

  const upAt = (lat, lon, spin) => geoDir(lat, lon, spin);
  // Unit direction at a site: azimuth from north, elevation above the horizon.
  function skyDir(up, az, el) {
    const east = norm(cross([0, 1, 0], up));
    const north = cross(up, east);
    return norm(add(mul(up, Math.sin(el * D)), mul(add(mul(north, Math.cos(az * D)), mul(east, Math.sin(az * D))), Math.cos(el * D))));
  }

  // ---------------------------------------------------- sensors on the ground
  function drawSensors(cam, t, alpha) {
    if (alpha <= 0) return;
    const spin = cam.spin;
    const seen = [];
    SENSORS.forEach(([lat, lon, kind, label, loff], i) => {
      const k = easeOutBack(seg(t, T_SENS + 0.35 + i * 0.12, T_SENS + 0.75 + i * 0.12));
      if (k <= 0) return;
      const up = upAt(lat, lon, spin);
      const site = mul(up, 1.003);
      if (occluded(cam, mul(up, 1.01))) return;
      const q = project(cam, site);
      if (!q) return;
      const radar = kind === "radar";
      const az0 = (i * 67) % 360;
      const sweep = radar ? az0 + 55 * Math.sin(t * 1.25 + i) : az0 + 25 * Math.sin(t * 0.7 + i);
      const half = radar ? 55 : 6;
      const el = radar ? 28 : 62;
      const reach = radar ? 0.3 : 0.32;
      // the field of view as a translucent fan
      const pts = [];
      for (let a = -half; a <= half; a += radar ? 6 : 2.5) {
        const p = add(site, mul(skyDir(up, sweep + a, el), reach * k));
        const pq = project(cam, p);
        if (pq) pts.push(pq);
      }
      if (pts.length > 1) {
        sx.save();
        sx.globalAlpha = alpha * 0.85;
        const col = radar ? "245,165,36" : "245,245,247";
        const g = sx.createRadialGradient(q.x, q.y, 0, q.x, q.y, Math.hypot(pts[0].x - q.x, pts[0].y - q.y) + 1);
        g.addColorStop(0, `rgba(${col},0.3)`);
        g.addColorStop(1, `rgba(${col},0.0)`);
        sx.fillStyle = g;
        sx.beginPath();
        sx.moveTo(q.x, q.y);
        pts.forEach((p) => sx.lineTo(p.x, p.y));
        sx.closePath();
        sx.fill();
        sx.strokeStyle = `rgba(${col},0.55)`;
        sx.lineWidth = 1;
        sx.stroke();
        sx.restore();
      }
      // which satellites the fan catches right now
      for (let s = 0; s < LEO.length; s += 3) {
        const o = LEO[s];
        const p = orbitPos(o, o.u0 + o.w * tau(t));
        const v = add(p, mul(site, -1));
        const dist = Math.hypot(v[0], v[1], v[2]);
        if (dist > reach * 1.25) continue;
        const dir = mul(v, 1 / dist);
        const elev = Math.asin(clamp(dot(dir, up), -1, 1)) / D;
        if (elev < el - (radar ? 18 : 4) || elev > el + (radar ? 18 : 4)) continue;
        const east = norm(cross([0, 1, 0], up));
        const north = cross(up, east);
        const az = Math.atan2(dot(dir, east), dot(dir, north)) / D;
        let da = ((az - sweep + 540) % 360) - 180;
        if (Math.abs(da) > half + 2) continue;
        seen.push(p);
      }
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = radar ? AMBER : INK;
      sx.beginPath();
      sx.arc(q.x, q.y, 5 * k, 0, Math.PI * 2);
      sx.fill();
      const pulse = (t * 0.9 + i * 0.3) % 1;
      sx.strokeStyle = radar ? AMBER : INK;
      sx.globalAlpha = alpha * (1 - pulse) * 0.7;
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.arc(q.x, q.y, 5 + pulse * 22, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      dotGlow(q.x, q.y, 18, radar ? AMBER : "#ffffff", alpha * 0.4);
      if (label) {
        const la = alpha * seg(t, T_SENS + 1.2 + i * 0.1, T_SENS + 1.6 + i * 0.1);
        sx.save();
        sx.globalAlpha = la;
        sx.font = font(600, 24, SANS);
        sx.letterSpacing = "3px";
        const tw = sx.measureText(label).width + 44;
        const lx = loff[0] > 0 ? q.x + loff[0] : q.x + loff[0] - tw;
        const ly = q.y + loff[1] - 23;
        sx.strokeStyle = "rgba(245,245,247,0.6)";
        sx.lineWidth = 1.2;
        sx.beginPath();
        sx.moveTo(q.x, q.y);
        sx.lineTo(loff[0] > 0 ? lx : lx + tw, ly + 23);
        sx.stroke();
        pill(lx, ly, tw, 46, radar ? AMBER : "rgba(245,245,247,0.6)", "rgba(0,0,0,0.66)");
        sx.fillStyle = radar ? AMBER : INK;
        sx.textAlign = "left";
        sx.fillText(label, lx + 22, ly + 31);
        sx.restore();
      }
    });
    // every satellite inside a fan lights up: a sighting
    sx.save();
    sx.globalCompositeOperation = "lighter";
    for (const p of seen) {
      if (occluded(cam, p)) continue;
      const q = project(cam, p);
      if (!q) continue;
      sx.globalAlpha = alpha;
      sx.fillStyle = AMBER;
      sx.fillRect(q.x - 2.5, q.y - 2.5, 5, 5);
      dotGlow(q.x, q.y, 8, AMBER, alpha * 0.25);
    }
    sx.restore();
  }

  // ------------------------------------------------ sightings → an orbit
  function drawFit(cam, t, alpha) {
    if (alpha <= 0) return;
    const uNow = FIT.u0 + FIT.w * tau(t);
    // the sightings, pass by pass, each with an error bar
    OBS.forEach((o) => {
      const t0 = T_FIT + 0.45 + o.pass * 0.65 + (o.i % 5) * 0.07;
      const k = seg(t, t0, t0 + 0.25);
      if (k <= 0) return;
      const radial = mul(norm(orbitPos(FIT, o.u)), 1);
      const p = add(orbitPos(FIT, o.u), mul(radial, o.dr));
      if (occluded(cam, p)) return;
      const q = project(cam, p);
      const qa = project(cam, add(p, mul(radial, 0.022)));
      const qb = project(cam, add(p, mul(radial, -0.022)));
      if (!q || !qa || !qb) return;
      const flash = 1 - seg(t, t0, t0 + 0.6);
      sx.save();
      sx.globalAlpha = alpha * k;
      sx.strokeStyle = "rgba(245,245,247,0.7)";
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.moveTo(qa.x, qa.y);
      sx.lineTo(qb.x, qb.y);
      sx.stroke();
      sx.fillStyle = INK;
      sx.beginPath();
      sx.arc(q.x, q.y, 4.5, 0, Math.PI * 2);
      sx.fill();
      sx.restore();
      dotGlow(q.x, q.y, 8 + 12 * flash, "#ffffff", alpha * (0.2 + 0.35 * flash));
      if (o.label) {
        const la = alpha * seg(t, t0 + 0.1, t0 + 0.4) * (1 - seg(t, T_UNC - 0.6, T_UNC - 0.2));
        const out = norm2(sub2(q, project(cam, [0, 0, 0])));
        sx.save();
        sx.globalAlpha = la;
        sx.font = font(600, 24, SANS);
        sx.letterSpacing = "3px";
        const tw = sx.measureText(o.label).width + 40;
        let lx = q.x + out[0] * 40 + (out[0] < 0 ? -tw : 0);
        if (lx + tw > W - 70) lx = q.x - 40 - tw;
        if (lx < 70) lx = q.x + 40;
        const ly = q.y + out[1] * 40 - 22;
        pill(lx, ly, tw, 44, "rgba(245,245,247,0.45)", "rgba(0,0,0,0.66)");
        sx.fillStyle = "rgba(245,245,247,0.9)";
        sx.textAlign = "left";
        sx.fillText(o.label, lx + 20, ly + 30);
        sx.restore();
      }
    });
    // the fit: an orbit drawn through the passes, then the satellite on it
    const fk = easeInOutCubic(seg(t, T_FIT + 2.75, T_FIT + 4.1));
    const uA = U_SAT - 2.45;
    if (fk > 0) strokeOrbitArc(cam, FIT, uA, lerp(uA, uNow, fk), AMBER, alpha * 0.95, 2.6, 0.7);
    // the rest of the orbit, faint, once the fit is in
    const rest = seg(t, T_FIT + 4.0, T_FIT + 4.8);
    if (rest > 0) strokeOrbitArc(cam, FIT, uNow, uA + Math.PI * 2, AMBER, alpha * 0.22 * rest, 1.2, 0.2, [3, 7]);
    if (fk >= 1) {
      const p = orbitPos(FIT, uNow);
      const q = project(cam, p);
      if (q && !occluded(cam, p)) {
        sx.save();
        sx.globalAlpha = alpha;
        sx.fillStyle = AMBER;
        sx.beginPath();
        sx.arc(q.x, q.y, 6.5, 0, Math.PI * 2);
        sx.fill();
        sx.restore();
        dotGlow(q.x, q.y, 22, AMBER, alpha * 0.8);
        dotGlow(q.x, q.y, 7, "#ffffff", alpha * 0.7);
      }
    }
  }
  const sub2 = (a, b) => [a.x - b.x, a.y - b.y];
  const norm2 = (v) => {
    const l = Math.hypot(v[0], v[1]) || 1;
    return [v[0] / l, v[1] / l];
  };

  // --------------------------------------------- uncertainty grows, shrinks
  function drawUncertainty(cam, t, alpha) {
    if (alpha <= 0) return;
    const uNow = FIT.u0 + FIT.w * tau(t);
    const go = easeInOutCubic(seg(t, T_UNC + 0.5, T_UNC + 3.0));
    const ping = T_UNC + 3.6;
    const fix = easeInOutExpo(seg(t, ping + 0.15, ping + 0.9));
    const lead = lerp(0.12, 0.9, go);
    const u = uNow + lead;
    const p = orbitPos(FIT, u);
    const q = project(cam, p);
    const q2 = project(cam, orbitPos(FIT, u + 0.02));
    if (!q || !q2 || occluded(cam, p)) return;
    const ang = Math.atan2(q2.y - q.y, q2.x - q.x);
    const grow = go * (1 - 0.82 * fix);
    const a = lerp(16, 170, grow);
    const b = lerp(10, 64, grow);
    // the predicted track ahead, dashed
    strokeOrbitArc(cam, FIT, uNow, u, "rgba(245,245,247,0.8)", alpha * 0.6, 1.4, 0, [5, 7]);
    sx.save();
    sx.translate(q.x, q.y);
    sx.rotate(ang);
    sx.globalAlpha = alpha * 0.95;
    sx.strokeStyle = AMBER;
    sx.lineWidth = 2;
    sx.setLineDash([7, 5]);
    sx.beginPath();
    sx.ellipse(0, 0, a, b, 0, 0, Math.PI * 2);
    sx.stroke();
    sx.setLineDash([]);
    sx.globalAlpha = alpha * 0.14;
    sx.fillStyle = AMBER;
    sx.fill();
    sx.restore();
    sx.save();
    sx.globalAlpha = alpha;
    sx.fillStyle = "rgba(245,245,247,0.9)";
    sx.beginPath();
    sx.arc(q.x, q.y, 4, 0, Math.PI * 2);
    sx.fill();
    sx.restore();
    // how far ahead the prediction looks, in plain words
    const labels = [
      [T_UNC + 0.5, "PREDICTED · IN 6 HOURS"],
      [T_UNC + 1.4, "PREDICTED · IN 1 DAY"],
      [T_UNC + 2.3, "PREDICTED · IN 3 DAYS"],
      [ping + 0.3, "UPDATED · NEW SIGHTING"],
    ];
    let li = 0;
    while (li < labels.length - 1 && FRAME_T >= labels[li + 1][0]) li++;
    const la = alpha * seg(t, T_UNC + 0.5, T_UNC + 0.8);
    sx.save();
    sx.globalAlpha = la;
    sx.font = font(600, 24, SANS);
    sx.letterSpacing = "3px";
    const tw = sx.measureText(labels[li][1]).width + 40;
    const lx = clamp(q.x - tw / 2, 70, W - 70 - tw);
    const ly = clamp(q.y - Math.max(a, b) - 70, 90, H - 200);
    pill(lx, ly, tw, 44, li === 3 ? AMBER : "rgba(245,245,247,0.45)", "rgba(0,0,0,0.66)");
    sx.fillStyle = li === 3 ? AMBER : INK;
    sx.textAlign = "left";
    sx.fillText(labels[li][1], lx + 20, ly + 30);
    sx.restore();
    // the new sighting: a ping at the satellite
    const pk = seg(t, ping, ping + 0.7);
    if (pk > 0 && pk < 1) {
      const sp = orbitPos(FIT, uNow);
      const sq = project(cam, sp);
      if (sq) {
        sx.save();
        sx.globalAlpha = alpha * (1 - pk);
        sx.strokeStyle = INK;
        sx.lineWidth = 2;
        sx.beginPath();
        sx.arc(sq.x, sq.y, 8 + easeOutExpo(pk) * 70, 0, Math.PI * 2);
        sx.stroke();
        sx.restore();
        dotGlow(sq.x, sq.y, 40 * (1 - pk), "#ffffff", alpha * 0.6);
      }
    }
  }

  // ---------------------------------------- many sources, one catalog
  const PX = 1270;
  const PY = 126;
  const PW = 560;
  const ROW_H = 76;
  const SX0 = 1060;
  const srcY = (j) => 196 + j * 112;
  function drawCatalog(t, alpha) {
    if (alpha <= 0) return;
    const pin = easeOutExpo(seg(t, T_SRC + 0.2, T_SRC + 0.6));
    const PH = 100 + ROWS.length * ROW_H + 70;
    sx.save();
    sx.globalAlpha = alpha * pin;
    sx.translate(0, (1 - pin) * 30);
    sx.beginPath();
    sx.roundRect(PX, PY, PW, PH, 18);
    sx.fillStyle = "rgba(22,22,24,0.88)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.28)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 26, SANS);
    sx.letterSpacing = "3.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("THE CATALOG", PX + 32, PY + 56);
    sx.strokeStyle = "rgba(245,245,247,0.14)";
    sx.beginPath();
    sx.moveTo(PX + 20, PY + 84);
    sx.lineTo(PX + PW - 20, PY + 84);
    sx.stroke();
    sx.restore();
    // source chips on the left
    const extra = seg(t, T_CHECK + 0.4, T_CHECK + 0.8);
    [...SOURCES, ...(extra > 0 ? ["YOUR NODE"] : [])].forEach((name, j) => {
      const k = j < 4 ? easeOutBack(seg(t, T_SRC + 0.3 + j * 0.1, T_SRC + 0.7 + j * 0.1)) : easeOutBack(extra);
      if (k <= 0) return;
      sx.save();
      sx.font = font(600, 22, SANS);
      sx.letterSpacing = "2.5px";
      const w = sx.measureText(name).width + 70;
      sx.globalAlpha = alpha * clamp(k * 2);
      const x = SX0 - w / 2;
      const y = srcY(j) - 27;
      sx.translate(SX0, srcY(j));
      sx.scale(k, k);
      sx.translate(-SX0, -srcY(j));
      pill(x, y, w, 54, j === 4 ? AMBER : "rgba(245,245,247,0.5)", "rgba(0,0,0,0.6)");
      sx.fillStyle = j === 4 ? AMBER : INK;
      sx.beginPath();
      sx.arc(x + 26, srcY(j), 6, 0, Math.PI * 2);
      sx.fill();
      sx.textAlign = "left";
      sx.fillText(name, x + 46, srcY(j) + 8);
      sx.restore();
    });
    // records fly in, digitally signed, and land as rows
    const rows = [...ROWS.map((r, i) => [...r, T_SRC + 0.9 + i * 0.62]), ["NEW OBJECT 2026-118A", "YOUR NODE", 4, T_CHECK + 1.0]];
    rows.forEach(([name, src, j, t0], i) => {
      const slot = Math.min(i, ROWS.length - 1);
      const shift = i === ROWS.length ? 0 : easeInOutCubic(seg(t, T_CHECK + 1.0, T_CHECK + 1.4));
      const rowY = PY + 100 + slot * ROW_H + 30 - (i < ROWS.length ? shift * ROW_H : 0);
      const fly = easeInOutCubic(seg(t, t0, t0 + 0.55));
      if (fly <= 0) return;
      const land = seg(t, t0 + 0.55, t0 + 0.7);
      const visible = i < ROWS.length ? 1 - seg(rowY, PY + 104, PY + 78) : 1;
      if (land < 1) {
        const from = [SX0 + 90, srcY(j)];
        const to = [PX + 40, rowY];
        const pts = bezierPts(from, [from[0] + 90, from[1]], [to[0] - 90, to[1]], to, 32);
        const [x, y] = pointOn(pts, fly);
        sx.save();
        sx.globalAlpha = alpha * (1 - land);
        sx.beginPath();
        sx.roundRect(x - 34, y - 22, 68, 44, 8);
        sx.fillStyle = "rgba(30,30,34,0.95)";
        sx.fill();
        sx.strokeStyle = AMBER;
        sx.lineWidth = 1.5;
        sx.stroke();
        sx.fillStyle = "rgba(245,245,247,0.55)";
        sx.fillRect(x - 20, y - 9, 30, 4);
        sx.fillRect(x - 20, y + 1, 22, 4);
        sx.restore();
        badge("check", x + 30, y - 20, 22, AMBER, alpha * (1 - land));
        dotGlow(x, y, 26, AMBER, 0.3 * alpha * (1 - land));
      }
      if (land > 0 && visible > 0) {
        sx.save();
        sx.globalAlpha = alpha * visible * clamp(land * 2);
        sx.font = font(600, 28, SANS);
        sx.letterSpacing = "0px";
        sx.fillStyle = INK;
        sx.textAlign = "left";
        decodeText(sx, name, PX + 32, rowY - 2, t0 + 0.55, 0.35, t, 300 + i);
        sx.font = font(600, 17, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillStyle = MUTED;
        sx.fillText(src, PX + 32, rowY + 26);
        sx.restore();
        badge("check", PX + PW - 48, rowY + 6, 36 * easeOutBack(land), AMBER, alpha * visible);
      }
    });
    // footer: the version names its inputs and recipe; a rerun matches
    const fk = seg(t, T_CHECK + 1.9, T_CHECK + 2.3);
    if (fk > 0) {
      const fy = PY + 100 + ROWS.length * ROW_H + 40;
      sx.save();
      sx.globalAlpha = alpha * fk;
      sx.strokeStyle = "rgba(245,245,247,0.14)";
      sx.beginPath();
      sx.moveTo(PX + 20, fy - 26);
      sx.lineTo(PX + PW - 20, fy - 26);
      sx.stroke();
      sx.font = font(600, 18, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "left";
      sx.fillText("VERSION 2026-10-04 · RECIPE LISTED", PX + 32, fy);
      sx.restore();
      const rk = seg(t, T_CHECK + 2.6, T_CHECK + 3.6);
      const done = seg(t, T_CHECK + 3.6, T_CHECK + 3.8);
      const bx = PX + PW / 2 - 180;
      const by = PY + PH + 30;
      if (rk > 0) {
        sx.save();
        sx.globalAlpha = alpha * clamp(rk * 4);
        pill(bx, by, 360, 60, done > 0 ? AMBER : "rgba(245,245,247,0.6)", "rgba(0,0,0,0.6)");
        sx.font = font(600, 21, SANS);
        sx.letterSpacing = "3px";
        sx.fillStyle = done > 0 ? AMBER : INK;
        sx.textAlign = "center";
        sx.fillText(done > 0 ? "RERUN · SAME RESULT" : "RERUN THE RECIPE", bx + 180 + 16, by + 38);
        if (done <= 0) {
          sx.strokeStyle = INK;
          sx.lineWidth = 2;
          sx.lineCap = "round";
          const a0 = t * 7;
          sx.beginPath();
          sx.arc(bx + 34, by + 30, 10, a0, a0 + 1.7);
          sx.stroke();
        }
        sx.restore();
        if (done > 0) badge("check", bx + 34, by + 30, 30 * easeOutBack(done), AMBER, alpha);
      }
    }
  }

  // ----------------------------------------------------------- subframe
  const ca = studio.cutPulses([[T_SENS, 1], [T_FIT, 0.8], [T_UNC, 0.6], [T_SRC, 1], [T_CHECK, 0.6], [T_END + 0.2, 1]]);
  const pulse = (t, c, w) => Math.exp(-Math.pow((t - c) / w, 2));
  const flash = (t) => 0.12 * pulse(t, T_SENS + 0.01, 0.05) + 0.12 * pulse(t, T_SRC + 0.01, 0.05);
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    const cam = cameraAt(t);
    beginSubframe();
    const under = seg(t, T_SRC - 0.1, T_SRC + 0.3);
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const earthFade = seg(t, 0, 1.2) * (1 - 0.78 * under) * (1 - out);
    if (earthFade > 0.001) {
      const night = seg(t, T_SENS - 0.3, T_SENS + 0.6) * (1 - seg(t, T_FIT - 0.6, T_FIT + 0.3));
      renderEarth(cam, t, earthFade, 0.25 * (1 - seg(t, 1.5, 3)), lerp(52, 128, night), lerp(22, 12, night));
      if (under > 0) sx.filter = `blur(${14 * under}px)`;
      sx.drawImage(glc, 0, 0);
      sx.filter = "none";
    }
    const satA = seg(t, 0.3, 0.9) * (1 - 0.6 * seg(t, T_FIT - 0.3, T_FIT + 0.4)) * (1 - under) * (1 - out);
    if (satA > 0.01) drawSatellites(cam, t + 2.0, tau(t), satA, true, SATS);
    if (t > T_SENS - 0.05 && t < T_FIT + 0.4) drawSensors(cam, t, seg(t, T_SENS, T_SENS + 0.4) * (1 - seg(t, T_FIT - 0.4, T_FIT)));
    const fitA = seg(t, T_FIT, T_FIT + 0.4) * (1 - seg(t, T_SRC - 0.4, T_SRC));
    if (fitA > 0) drawFit(cam, t, fitA);
    if (t > T_UNC && t < T_SRC) drawUncertainty(cam, t, seg(t, T_UNC, T_UNC + 0.4) * (1 - seg(t, T_SRC - 0.4, T_SRC)));
    if (t > T_SRC && t < T_END + 0.5) drawCatalog(t, 1 - seg(t, T_END - 0.3, T_END + 0.1));
    scrim((1 - out) * 0.9);

    // ------------------------------------------------------- the type
    if (t < T_SENS + 0.1) {
      eyebrow("THE CATALOG", 2, 0.5, T_SENS - 0.45, t);
      block([["Everything in orbit,", INK], ["in one shared list.", AMBER]], "WHERE EACH OBJECT IS, AND HOW SURE ANYONE IS", 0.6, T_SENS - 0.4, t, 84);
    }
    if (t > T_SENS - 0.05 && t < T_FIT + 0.1) {
      block([["Radars and telescopes", INK], ["watch the sky.", AMBER]], "NO SINGLE SENSOR SEES EVERYTHING", T_SENS + 0.1, T_FIT - 0.4, t, 84);
    }
    if (t > T_FIT - 0.05 && t < T_UNC + 0.1) {
      block([["Sightings become", INK], ["an orbit.", AMBER]], "SOFTWARE FITS A PATH THROUGH A FEW PASSES", T_FIT + 0.1, T_UNC - 0.4, t, 84);
    }
    if (t > T_UNC - 0.05 && t < T_SRC + 0.1) {
      block([["Every orbit carries", INK], ["its uncertainty.", AMBER]], "HOW SURE, NOT JUST WHERE. NEW SIGHTINGS SHRINK IT", T_UNC + 0.1, T_SRC - 0.4, t, 84);
    }
    if (t > T_SRC - 0.05 && t < T_CHECK + 0.1) {
      block([["Many sources.", INK], ["One catalog.", AMBER]], "EVERY RECORD DIGITALLY SIGNED BY ITS SOURCE", T_SRC + 0.1, T_CHECK - 0.4, t, 88);
    }
    if (t > T_CHECK - 0.05 && t < T_END + 0.1) {
      block([["Anyone can add to it.", INK], ["Anyone can check it.", AMBER]], "EACH VERSION NAMES ITS INPUTS AND ITS RECIPE", T_CHECK + 0.1, T_END - 0.4, t, 84);
    }
    lockup({ title: "The Catalog", subtitle: "One shared catalog of everything in orbit", url: "SPACEDATANETWORK.ORG", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
