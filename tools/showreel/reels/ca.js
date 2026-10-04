// Collision-avoidance reel: how a close approach gets answered, in plain
// words, for spacedatanetwork.org/collision-avoidance.html. 40 seconds: the
// question every operator asks, every object checked against every other, a
// close approach and its odds, several services with several answers, the
// blind window a planned maneuver opens, and one open baseline.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO, D, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack,
  add, sub, mul, dot, cross, len, norm, mix3, rng, makeTau, OMEGA_K, orbitBasis, orbitPos, makeSky, lookAt, geoDir,
  project, occluded, createStudio,
} from "../kit.js";

export const meta = { name: "sdn-ca-reel", media: "docs/media/ca", poster: 438, crf: [30, 42], audio: { track: "orbital-insertion", start: 182 } };

const DURATION = 40;
const FPS = 30;
const T_PAIRS = 5.0; // every object against every other
const T_CLOSE = 11.0; // one close approach: how close, how likely
const T_TCA = T_CLOSE + 3.9;
const T_MANY = 18.0; // several services, several answers
const T_BLIND = 24.0; // a planned maneuver nobody else can see
const T_DISC = T_BLIND + 3.2; // screening has to work without disclosure
const T_BASE = 30.5; // one open baseline
const T_END = 35.5; // lockup

// Scene time: the close approach creeps in and all but stops at its closest.
function rate(t) {
  const close = seg(t, T_CLOSE - 0.05, T_CLOSE + 0.05) * (1 - seg(t, T_MANY - 0.3, T_MANY));
  return lerp(1, 0.24, close) - 0.2 * seg(t, T_TCA - 0.8, T_TCA + 0.4) * (1 - seg(t, T_MANY - 0.3, T_MANY));
}
const tau = makeTau(rate, DURATION);

const SATS = makeSky();
const LEO = SATS.filter((s) => s.kind === 0);

// The pair: two orbits at nearly the same altitude, phased to cross at T_TCA.
const CONJ = (() => {
  const A = { ...orbitBasis(51.6 * D, 25 * D), r: 1.1 };
  const B = { ...orbitBasis(97.6 * D, 105 * D), r: 1.1025 };
  const X = norm(cross(cross(A.e1, A.e2), cross(B.e1, B.e2)));
  const uAt = (o, p) => Math.atan2(dot(p, o.e2), dot(p, o.e1));
  A.w = OMEGA_K * Math.pow(A.r, -1.5);
  B.w = OMEGA_K * Math.pow(B.r, -1.5);
  const tTCA = tau(T_TCA);
  A.u0 = uAt(A, X) - A.w * tTCA;
  B.u0 = uAt(B, X) - B.w * tTCA + 0.0035;
  return { A, B, X };
})();

// The maneuvering satellite: the catalog's orbit, and the one it burns into.
const MAN = { ...orbitBasis(43 * D, 330 * D), r: 1.3 };
MAN.w = OMEGA_K * Math.pow(MAN.r, -1.5);
const MAN_N = norm(cross(MAN.e1, MAN.e2));

// Nodes for the closing baseline (lat, lon).
const NODES = [
  [30.27, -97.74], [40.01, -105.27], [52.01, 4.36], [50.11, 8.68], [40.71, -74.0], [51.5, -0.12],
  [-23.55, -46.63], [-1.29, 36.82], [12.97, 77.59], [28.39, -80.6], [64.14, -21.94], [45.5, -73.57], [59.33, 18.07],
];
const LINKS = [[0, 1], [0, 4], [4, 5], [5, 2], [2, 3], [5, 10], [4, 11], [9, 4], [6, 9], [6, 7], [3, 7], [7, 8], [2, 12], [3, 8], [1, 9]];

// ------------------------------------------------------------------ camera
const KEYS = [
  [0.0, geoDir(16, -40, 0), 4.9, [0.33, -0.02]],
  [4.6, geoDir(22, -62, 0), 3.7, [0.32, -0.02]],
  [10.4, geoDir(26, -80, 0), 3.5, [0.32, -0.02]],
];
function keyCamera(t, keys) {
  let i = 0;
  while (i < keys.length - 2 && t >= keys[i + 1][0]) i++;
  const [t0, d0, r0, o0] = keys[i];
  const [t1, d1, r1, o1] = keys[i + 1];
  const k = easeInOutCubic(seg(t, t0, t1));
  const dir = norm(add(mul(d0, 1 - k), mul(d1, k)));
  const pos = mul(dir, lerp(r0, r1, k));
  return { ...lookAt(pos, [0, 0, 0], [0, 1, 0]), off: [lerp(o0[0], o1[0], k), lerp(o0[1], o1[1], k)], zoom: 1, dist: lerp(r0, r1, k) };
}
const MAN_DIR = norm(add(mul(MAN_N, 0.9), mul(MAN.e2, -0.42)));
function cameraAt(t) {
  const spin = 0.045 * t;
  if (t < T_CLOSE) return { ...keyCamera(t, KEYS), spin };
  if (t < T_BLIND) {
    // close-up on the crossing, pulled from the wide shot
    const X = CONJ.X;
    const side = norm(cross(X, [0, 1, 0]));
    const push = lerp(1.62, 1.52, easeInOutCubic(seg(t, T_CLOSE, T_TCA)));
    const near = add(add(mul(X, push), mul(side, 0.34 - 0.1 * seg(t, T_CLOSE, T_MANY))), mul(norm(cross(side, X)), 0.12));
    const wide = keyCamera(T_CLOSE, KEYS);
    const k = easeOutExpo(seg(t, T_CLOSE, T_CLOSE + 0.9));
    const pos = mix3(wide.pos, near, k);
    const target = mix3([0, 0, 0], mul(X, 1.08), k);
    const cam = lookAt(pos, target, [0, 1, 0]);
    return { ...cam, spin, off: [lerp(0.32, 0.14, k), lerp(-0.02, 0.1, k)], zoom: 1, dist: len(pos) };
  }
  if (t < T_BASE) {
    const k = easeInOutCubic(seg(t, T_BLIND - 0.2, T_BLIND + 0.8));
    const dir = norm(add(mul(MAN_DIR, 1), mul(MAN.e1, 0.15 * seg(t, T_BLIND, T_BASE))));
    const pos = mul(dir, lerp(4.4, 4.0, k));
    return { ...lookAt(pos, [0, 0, 0], [0, 1, 0]), spin, off: [0.2, 0.0], zoom: 1, dist: len(pos) };
  }
  const k = easeInOutCubic(seg(t, T_BASE - 0.2, T_BASE + 0.9));
  const pos = mul(norm(add(mul(geoDir(36, -40, 0), k), mul(MAN_DIR, 1 - k))), lerp(4.0, 3.1, k));
  return { ...lookAt(pos, [0, 0, 0], [0, 1, 0]), spin, off: [lerp(0.2, 0.32, k), lerp(0, -0.06, k)], zoom: 1, dist: len(pos) };
}

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "SPACE DATA NETWORK · COLLISION AVOIDANCE", hudUrl: "SPACEDATANETWORK.ORG" });
  const {
    glc, sx, gx, font, maskText, decodeText, dotGlow, scrim, pill, badge, renderEarth, drawSatellites, drawOrbitPath,
    strokeOrbitArc, arcPoint, strokeArc, eyebrow, lockup, block, beginSubframe, bloom, hud,
  } = studio;

  // ------------------------------------------- every object against every other
  function drawPairs(cam, t, T, alpha) {
    if (alpha <= 0) return;
    const R = rng(1000 + Math.round(FRAME_T * 30));
    const vis = [];
    for (let i = 0; i < LEO.length && vis.length < 900; i += 3) {
      const o = LEO[i];
      const p = orbitPos(o, o.u0 + o.w * T);
      if (occluded(cam, p)) continue;
      const q = project(cam, p);
      if (q && q.x > 0 && q.x < W && q.y > 0 && q.y < H) vis.push(q);
    }
    const n = Math.round(lerp(60, 420, seg(t, T_PAIRS + 0.3, T_PAIRS + 3.5)));
    sx.save();
    sx.globalCompositeOperation = "lighter";
    sx.lineWidth = 1;
    for (let k = 0; k < n && vis.length > 2; k++) {
      const a = vis[Math.floor(R() * vis.length)];
      const b = vis[Math.floor(R() * vis.length)];
      sx.strokeStyle = `rgba(245,165,36,${0.22 + 0.3 * R()})`;
      sx.globalAlpha = alpha;
      sx.beginPath();
      sx.moveTo(a.x, a.y);
      sx.lineTo(b.x, b.y);
      sx.stroke();
    }
    sx.restore();
    // the count: objects, and the pairs they make
    const k = easeOutExpo(seg(FRAME_T, T_PAIRS + 0.4, T_PAIRS + 3.6));
    const N = Math.round(50000 * k);
    const pairs = Math.round((N * (N - 1)) / 2);
    const x = 152;
    maskText(sx, "OBJECTS TRACKED", x, 190, 24, 600, MUTED, T_PAIRS + 0.3, T_CLOSE - 0.45, t, { tracking: 4 });
    maskText(sx, "PAIRS TO CHECK", x, 380, 24, 600, AMBER, T_PAIRS + 0.45, T_CLOSE - 0.45, t, { tracking: 4 });
    const out = easeInExpo(seg(t, T_CLOSE - 0.45, T_CLOSE - 0.1));
    const inK = easeOutExpo(seg(t, T_PAIRS + 0.3, T_PAIRS + 0.8));
    sx.save();
    sx.globalAlpha = alpha * inK * (1 - out);
    sx.textAlign = "left";
    sx.font = font(700, 76, SANS);
    sx.letterSpacing = "-2px";
    sx.fillStyle = INK;
    sx.fillText(N.toLocaleString("en-US"), x, 284);
    sx.font = font(700, 96, SANS);
    sx.letterSpacing = "-3px";
    sx.fillStyle = AMBER;
    sx.fillText(pairs.toLocaleString("en-US"), x, 494);
    sx.restore();
  }

  // ------------------------------------------------- one close approach
  function drawConjunction(cam, t, T, alpha) {
    if (alpha <= 0) return;
    const { A, B } = CONJ;
    const qa = drawOrbitPath(cam, A, T, AMBER, alpha * 0.9, 2.2, 0.35);
    const qb = drawOrbitPath(cam, B, T, CYAN, alpha * 0.9, 2.2, 0.35);
    drawOrbitPath(cam, A, T, AMBER, alpha * 0.18, 1, 1, false);
    drawOrbitPath(cam, B, T, CYAN, alpha * 0.18, 1, 1, false);
    if (!qa || !qb) return;
    const lab = alpha * seg(t, T_CLOSE + 1.0, T_CLOSE + 1.4) * (1 - seg(t, T_TCA - 0.9, T_TCA - 0.6));
    if (lab > 0) {
      [[qa, AMBER, "SATELLITE A", 1, 64], [qb, CYAN, "SATELLITE B", -1, 84]].forEach(([q, col, name, side, dy]) => {
        const lx = q.x + side * 60;
        const ly = q.y + dy;
        sx.save();
        sx.globalAlpha = lab;
        sx.strokeStyle = col;
        sx.lineWidth = 1.2;
        sx.beginPath();
        sx.moveTo(q.x + side * 8, q.y + 10);
        sx.lineTo(lx, ly);
        sx.lineTo(lx + side * 30, ly);
        sx.stroke();
        sx.textAlign = side < 0 ? "right" : "left";
        sx.font = font(600, 28, SANS);
        sx.letterSpacing = "3px";
        sx.fillStyle = col;
        sx.fillText(name, lx + side * 40, ly + 9);
        sx.restore();
      });
    }
    const near = seg(t, T_TCA - 1.05, T_TCA - 0.375) * (1 - seg(t, T_MANY - 0.4, T_MANY));
    [[A, qa, AMBER], [B, qb, CYAN]].forEach(([o, q, col]) => {
      const q2 = project(cam, orbitPos(o, o.u0 + o.w * (T + 0.02)));
      if (!q2) return;
      const ang = Math.atan2(q2.y - q.y, q2.x - q.x);
      const pulse = 1 + 0.06 * Math.sin(t * 12);
      sx.save();
      sx.translate(q.x, q.y);
      sx.rotate(ang);
      sx.globalAlpha = alpha * near * 0.9;
      sx.strokeStyle = col;
      sx.lineWidth = 1.6;
      sx.setLineDash([6, 5]);
      sx.beginPath();
      sx.ellipse(0, 0, 86 * pulse, 26 * pulse, 0, 0, Math.PI * 2);
      sx.stroke();
      sx.setLineDash([]);
      sx.globalAlpha = alpha * near * 0.12;
      sx.fillStyle = col;
      sx.fill();
      sx.restore();
    });
    const tca = seg(t, T_TCA - 0.525, T_TCA - 0.15) * (1 - seg(t, T_MANY - 0.4, T_MANY));
    if (tca <= 0) return;
    const mx = (qa.x + qb.x) / 2;
    const my = (qa.y + qb.y) / 2;
    const sw = seg(t, T_TCA - 0.225, T_TCA + 1.4);
    sx.save();
    sx.strokeStyle = RED;
    sx.globalAlpha = alpha * (1 - sw) * 0.9;
    sx.lineWidth = 2;
    sx.beginPath();
    sx.arc(mx, my, 20 + easeOutExpo(sw) * 260, 0, Math.PI * 2);
    sx.stroke();
    sx.globalAlpha = alpha * tca;
    sx.setLineDash([4, 4]);
    sx.beginPath();
    sx.moveTo(qa.x, qa.y);
    sx.lineTo(qb.x, qb.y);
    sx.stroke();
    sx.setLineDash([]);
    sx.restore();
    dotGlow(mx, my, 50 * tca, RED, 0.22 * alpha * tca * (1 - seg(t, T_TCA + 0.45, T_TCA + 1.05)));
    const lx = mx + 150;
    const ly = my - 260;
    const lk = easeOutExpo(seg(t, T_TCA - 0.375, T_TCA + 0.225));
    sx.save();
    sx.globalAlpha = alpha * tca;
    sx.strokeStyle = "rgba(255,59,48,0.8)";
    sx.lineWidth = 1.5;
    sx.beginPath();
    sx.moveTo(mx, my);
    sx.lineTo(lerp(mx, lx, lk), lerp(my, ly + 40, lk));
    sx.lineTo(lerp(mx, lx + 470, lk), lerp(my, ly + 40, lk));
    sx.stroke();
    sx.save();
    sx.globalAlpha = alpha * tca * lk;
    sx.fillStyle = "rgba(0,0,0,0.66)";
    sx.beginPath();
    sx.roundRect(lx - 18, ly - 38, 512, 216, 14);
    sx.fill();
    sx.restore();
    sx.translate(lx + 22, ly);
    sx.fillStyle = RED;
    sx.beginPath();
    sx.moveTo(0, -22);
    sx.lineTo(22, 16);
    sx.lineTo(-22, 16);
    sx.closePath();
    sx.fill();
    sx.fillStyle = "#1a0503";
    sx.font = font(800, 24, SANS);
    sx.textAlign = "center";
    sx.fillText("!", 0, 11);
    sx.restore();
    maskText(sx, "CLOSE APPROACH", lx + 58, ly + 12, 30, 700, RED, T_TCA - 0.375, T_MANY - 0.45, t, { tracking: 3 });
    sx.save();
    sx.globalAlpha = alpha * tca;
    sx.font = font(500, 26, MONO);
    sx.letterSpacing = "1px";
    sx.fillStyle = INK;
    decodeText(sx, "WHEN  IN 2 DAYS · 14:02 UTC", lx, ly + 82, T_TCA - 0.225, 0.5, t, 7);
    decodeText(sx, "MISS  214 m", lx, ly + 118, T_TCA - 0.075, 0.45, t, 8);
    decodeText(sx, "RISK  1 IN 8,300", lx, ly + 154, T_TCA + 0.075, 0.45, t, 9);
    sx.restore();
  }

  // ------------------------------------------ several services, several answers
  const ANSWERS = [
    ["SERVICE A", "214 m", "1 IN 8,300", 0],
    ["SERVICE B", "480 m", "1 IN 95,000", 1],
    ["SERVICE C", "130 m", "1 IN 2,400", 2],
  ];
  function drawAnswers(t, alpha) {
    if (alpha <= 0) return;
    const cw = 470;
    const ch = 190;
    const x0 = 1300;
    ANSWERS.forEach(([name, miss, risk, i]) => {
      const t0 = T_MANY + 0.35 + i * 0.3;
      const k = easeOutBack(seg(t, t0, t0 + 0.45));
      if (k <= 0) return;
      const cx = x0 + (i - 1) * 40;
      const cy = 260 + i * 230;
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2);
      sx.translate(cx, cy);
      sx.scale(k, k);
      sx.beginPath();
      sx.roundRect(-cw / 2, -ch / 2, cw, ch, 18);
      sx.fillStyle = "rgba(22,22,24,0.9)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.3)";
      sx.lineWidth = 1.5;
      sx.stroke();
      sx.font = font(600, 24, SANS);
      sx.letterSpacing = "3.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "left";
      sx.fillText(name, -cw / 2 + 32, -ch / 2 + 50);
      sx.font = font(500, 34, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = INK;
      decodeText(sx, `MISS  ${miss}`, -cw / 2 + 32, -ch / 2 + 108, t0 + 0.25, 0.4, t, 40 + i);
      sx.fillStyle = AMBER;
      decodeText(sx, `RISK  ${risk}`, -cw / 2 + 32, -ch / 2 + 156, t0 + 0.35, 0.4, t, 50 + i);
      sx.restore();
    });
    // a question between them, once all three are in
    const qk = easeOutBack(seg(t, T_MANY + 2.0, T_MANY + 2.4));
    if (qk > 0) {
      sx.save();
      sx.globalAlpha = alpha * clamp(qk * 2);
      sx.font = font(600, 26, SANS);
      sx.letterSpacing = "4px";
      sx.fillStyle = INK;
      sx.textAlign = "center";
      sx.fillText("WHICH ONE IS RIGHT?", x0, 950);
      sx.restore();
    }
  }

  // ---------------------------------------------- the blind window
  // The planned orbit rises from the catalog's orbit after the burn point.
  const U_BURN = (() => {
    // the burn sits upper right of the frame when the scene opens
    const cam = cameraAt(T_BLIND + 1.0);
    let best = 0;
    let score = -Infinity;
    for (let u = 0; u < Math.PI * 2; u += 0.01) {
      let sc = 0;
      for (const du of [-0.4, 0, 0.6, 1.2, 1.8]) {
        const q = project(cam, orbitPos(MAN, u + du));
        if (!q) { sc -= 1e6; continue; }
        if (q.x < 1050 && q.y > 520) sc -= 2000 + (1050 - q.x) + (q.y - 520);
        sc -= 4 * Math.max(0, 120 - q.y) + 4 * Math.max(0, q.y - 980);
        if (du === 0) sc += q.x;
      }
      if (sc > score) { score = sc; best = u; }
    }
    return best;
  })();
  // The satellite reaches the burn point one second into the scene.
  const T_BURN = T_BLIND + 1.0;
  const manU0 = U_BURN - MAN.w * T_BURN;
  const raised = (u) => {
    const k = clamp((u - U_BURN) / 0.9);
    return { ...MAN, r: MAN.r + 0.09 * k * k * (3 - 2 * k) };
  };
  function strokePath(cam, pos, u0, u1, color, alpha, width, dash) {
    let prev = null;
    sx.save();
    sx.strokeStyle = color;
    sx.globalAlpha = alpha;
    sx.lineWidth = width;
    sx.lineCap = "round";
    if (dash) sx.setLineDash(dash);
    sx.beginPath();
    const N = Math.max(8, Math.ceil(Math.abs(u1 - u0) * 50));
    for (let i = 0; i <= N; i++) {
      const p = pos(lerp(u0, u1, i / N));
      const q = project(cam, p);
      const hid = occluded(cam, p);
      if (q && prev && !hid && !prev.hid) {
        sx.moveTo(prev.x, prev.y);
        sx.lineTo(q.x, q.y);
        gx.globalAlpha = alpha * 0.5;
        gx.strokeStyle = color;
        gx.lineWidth = width * 1.5;
        gx.beginPath();
        gx.moveTo(prev.x / 2, prev.y / 2);
        gx.lineTo(q.x / 2, q.y / 2);
        gx.stroke();
      }
      prev = q ? { ...q, hid } : null;
    }
    sx.stroke();
    sx.restore();
  }
  function tag(q, text, color, dx, dy, alpha) {
    if (!q || alpha <= 0) return;
    sx.save();
    sx.globalAlpha = alpha;
    sx.font = font(600, 23, SANS);
    sx.letterSpacing = "3px";
    const tw = sx.measureText(text).width + 40;
    let lx = dx > 0 ? q.x + dx : q.x + dx - tw;
    lx = clamp(lx, 70, W - 70 - tw);
    const ly = q.y + dy - 22;
    sx.strokeStyle = "rgba(245,245,247,0.55)";
    sx.lineWidth = 1.2;
    sx.beginPath();
    sx.moveTo(q.x, q.y);
    sx.lineTo(dx > 0 ? lx : lx + tw, ly + 22);
    sx.stroke();
    pill(lx, ly, tw, 44, color, "rgba(0,0,0,0.66)");
    sx.fillStyle = color;
    sx.textAlign = "left";
    sx.fillText(text, lx + 20, ly + 30);
    sx.restore();
  }
  function drawBlind(cam, t, alpha) {
    if (alpha <= 0) return;
    const u = manU0 + MAN.w * t;
    const uStart = U_BURN - 1.6;
    // the catalog's orbit, which everyone else screens against
    strokePath(cam, (v) => orbitPos(MAN, v), uStart, U_BURN + 2.6, "rgba(245,245,247,0.85)", alpha * 0.55, 1.6, [6, 7]);
    // the planned orbit, known only to its operator
    const plan = seg(t, T_BLIND + 0.2, T_BLIND + 0.8);
    if (plan > 0) strokePath(cam, (v) => orbitPos(raised(v), v), U_BURN, lerp(U_BURN, U_BURN + 2.6, easeInOutCubic(plan)), AMBER, alpha * 0.95, 2.6);
    strokePath(cam, (v) => orbitPos(MAN, v), uStart, Math.min(u, U_BURN), AMBER, alpha * 0.95, 2.6);
    // the satellite on its real path, and the catalog's ghost on the old one
    const real = orbitPos(u > U_BURN ? raised(u) : MAN, u);
    const ghost = orbitPos(MAN, u);
    const qr = occluded(cam, real) ? null : project(cam, real);
    const qg = occluded(cam, ghost) ? null : project(cam, ghost);
    if (qg && u > U_BURN + 0.05) {
      sx.save();
      sx.globalAlpha = alpha;
      sx.strokeStyle = INK;
      sx.lineWidth = 2;
      sx.beginPath();
      sx.arc(qg.x, qg.y, 8, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
    }
    if (qr) {
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = AMBER;
      sx.beginPath();
      sx.arc(qr.x, qr.y, 7, 0, Math.PI * 2);
      sx.fill();
      sx.restore();
      dotGlow(qr.x, qr.y, 24, AMBER, alpha * 0.8);
      dotGlow(qr.x, qr.y, 8, "#ffffff", alpha * 0.6);
    }
    // the burn
    const bk = seg(t, T_BURN, T_BURN + 0.8);
    if (bk > 0 && bk < 1) {
      const qb = project(cam, orbitPos(MAN, U_BURN));
      if (qb) {
        sx.save();
        sx.globalAlpha = alpha * (1 - bk);
        sx.strokeStyle = AMBER;
        sx.lineWidth = 2.5;
        sx.beginPath();
        sx.arc(qb.x, qb.y, 10 + easeOutExpo(bk) * 80, 0, Math.PI * 2);
        sx.stroke();
        sx.restore();
        dotGlow(qb.x, qb.y, 70 * (1 - bk), AMBER, alpha * 0.7);
      }
    }
    const la = alpha * seg(t, T_BURN + 0.5, T_BURN + 0.9);
    tag(qr, "WHERE IT IS GOING", AMBER, 50, -70, la);
    tag(qg, "WHAT EVERYONE ELSE SEES", INK, -50, 74, la * seg(u, U_BURN + 0.3, U_BURN + 0.5));
    const pb = project(cam, orbitPos(MAN, U_BURN));
    tag(pb, "PLANNED BURN", AMBER, 50, 70, alpha * seg(t, T_BLIND + 0.3, T_BLIND + 0.6) * (1 - seg(t, T_BURN + 0.6, T_BURN + 0.9)));
  }

  // ----------------------------------------------- one open baseline
  function drawBaseline(cam, t, alpha) {
    if (alpha <= 0) return;
    const P = NODES.map(([la, lo]) => geoDir(la, lo, cam.spin));
    LINKS.forEach(([i, j], k) => {
      const t0 = T_BASE + 0.4 + k * 0.04;
      const d = easeInOutCubic(seg(t, t0, t0 + 0.4));
      if (d > 0) strokeArc(cam, P[i], P[j], d, AMBER, 1.6, alpha * 0.6, AMBER, alpha * 0.35);
    });
    P.forEach((p, i) => {
      const s = mul(p, 1.004);
      if (occluded(cam, s)) return;
      const q = project(cam, s);
      const k = easeOutBack(seg(t, T_BASE + 0.3 + i * 0.05, T_BASE + 0.7 + i * 0.05));
      if (!q || k <= 0) return;
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = AMBER;
      sx.beginPath();
      sx.arc(q.x, q.y, 5 * k, 0, Math.PI * 2);
      sx.fill();
      sx.restore();
      dotGlow(q.x, q.y, 18 * k, AMBER, alpha * 0.45);
      const ck = seg(t, T_BASE + 1.6 + i * 0.07, T_BASE + 1.9 + i * 0.07);
      if (ck > 0) badge("check", q.x + 18, q.y - 18, 28 * easeOutBack(ck), AMBER, alpha);
    });
    const sk = seg(t, T_BASE + 2.4, T_BASE + 2.8);
    if (sk > 0) {
      sx.save();
      sx.globalAlpha = alpha * sk;
      sx.font = font(600, 24, SANS);
      sx.letterSpacing = "3.5px";
      const label = "SAME INPUTS · SAME ANSWER ON EVERY NODE";
      const tw = sx.measureText(label).width + 48;
      const lx = W - 120 - tw;
      pill(lx, 150, tw, 50, AMBER, "rgba(0,0,0,0.66)");
      sx.fillStyle = AMBER;
      sx.textAlign = "left";
      sx.fillText(label, lx + 24, 184);
      sx.restore();
    }
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_PAIRS, 0.8], [T_CLOSE, 1], [T_MANY, 1], [T_BLIND, 1], [T_DISC, 0.5], [T_BASE, 1], [T_END + 0.2, 1]]);
  const pulse = (t, c, w) => Math.exp(-Math.pow((t - c) / w, 2));
  const flash = (t) => 0.12 * pulse(t, T_CLOSE + 0.01, 0.05) + 0.12 * pulse(t, T_TCA - 0.205, 0.06) + 0.1 * pulse(t, T_BLIND + 0.01, 0.05);
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    const T = tau(t);
    const cam = cameraAt(t);
    beginSubframe();
    const under = seg(t, T_MANY - 0.1, T_MANY + 0.3) * (1 - seg(t, T_BLIND - 0.2, T_BLIND + 0.2));
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const earthFade = seg(t, 0, 1.2) * (1 - 0.8 * under) * (1 - out);
    if (earthFade > 0.001) {
      const sunAz = t < T_CLOSE ? 52 : t < T_BLIND ? 128 : t < T_BASE ? 60 : lerp(60, 120, seg(t, T_BASE, T_BASE + 1));
      renderEarth(cam, t, earthFade, 0.25 * (1 - seg(t, 1.5, 3)), sunAz, t >= T_CLOSE && t < T_BLIND ? 4 : 20);
      if (under > 0) sx.filter = `blur(${14 * under}px)`;
      sx.drawImage(glc, 0, 0);
      sx.filter = "none";
    }
    const satA = seg(t, 0.3, 0.9) * (1 - 0.55 * seg(t, T_CLOSE, T_CLOSE + 0.3)) * (1 - under) * (1 - 0.5 * seg(t, T_BLIND, T_BLIND + 0.4)) * (1 - out);
    if (satA > 0.01) drawSatellites(cam, t + 2.0, T, satA, true, SATS);
    if (t > T_PAIRS && t < T_CLOSE) drawPairs(cam, t, T, seg(t, T_PAIRS, T_PAIRS + 0.4) * (1 - seg(t, T_CLOSE - 0.4, T_CLOSE)));
    if (t > T_CLOSE - 0.05 && t < T_MANY + 0.1) drawConjunction(cam, t, T, seg(t, T_CLOSE, T_CLOSE + 0.45) * (1 - seg(t, T_MANY - 0.4, T_MANY)));
    if (t > T_MANY && t < T_BLIND) drawAnswers(t, 1 - seg(t, T_BLIND - 0.4, T_BLIND - 0.05));
    if (t > T_BLIND - 0.05 && t < T_BASE + 0.1) drawBlind(cam, t, seg(t, T_BLIND, T_BLIND + 0.3) * (1 - seg(t, T_BASE - 0.4, T_BASE)));
    if (t > T_BASE && t < T_END + 0.5) drawBaseline(cam, t, seg(t, T_BASE, T_BASE + 0.3) * (1 - seg(t, T_END - 0.3, T_END + 0.1)));
    scrim((1 - out) * 0.9);

    // -------------------------------------------------------- the type
    if (t < T_PAIRS + 0.1) {
      eyebrow("COLLISION AVOIDANCE", 2, 0.5, T_PAIRS - 0.45, t);
      block([["Will anything", INK], ["come too close?", AMBER]], "EVERY OPERATOR ASKS, SEVERAL TIMES A DAY", 0.6, T_PAIRS - 0.4, t, 88);
    }
    if (t > T_PAIRS - 0.05 && t < T_CLOSE + 0.1) {
      block([["Check every object", INK], ["against every other.", AMBER]], "TEN TIMES THE OBJECTS, A HUNDRED TIMES THE PAIRS", T_PAIRS + 0.1, T_CLOSE - 0.4, t, 84);
    }
    if (t > T_CLOSE - 0.05 && t < T_MANY + 0.1) {
      block([["How close?", INK], ["How likely?", AMBER]], "DISTANCE ALONE CAN'T TELL A NEAR MISS FROM A NON-EVENT", T_CLOSE + 0.1, T_MANY - 0.4, t, 88);
    }
    if (t > T_MANY - 0.05 && t < T_BLIND + 0.1) {
      block([["Several services.", INK], ["Several answers.", AMBER]], "THEIR INPUTS AND METHODS CAN'T BE COMPARED", T_MANY + 0.1, T_BLIND - 0.4, t, 88);
    }
    if (t > T_BLIND - 0.05 && t < T_DISC + 0.1) {
      block([["A planned maneuver", INK], ["is invisible to others.", AMBER]], "UNTIL TRACKING CATCHES UP, HOURS TO DAYS LATER", T_BLIND + 0.1, T_DISC - 0.35, t, 84);
    }
    if (t > T_DISC - 0.05 && t < T_BASE + 0.1) {
      block([["Screening has to work", INK], ["without disclosure.", AMBER]], "COMPUTING ON DATA NOBODY ELSE CAN READ", T_DISC + 0.05, T_BASE - 0.4, t, 84);
    }
    if (t > T_BASE - 0.05 && t < T_END + 0.1) {
      block([["One open baseline.", INK], ["Anyone can check the math.", AMBER]], "DIGITALLY SIGNED DATA, SCREENED WITH OPEN SOFTWARE", T_BASE + 0.1, T_END - 0.4, t, 80);
    }
    lockup({ title: "Collision Avoidance", subtitle: "One answer per close approach, open to anyone", url: "SPACEDATANETWORK.ORG", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
