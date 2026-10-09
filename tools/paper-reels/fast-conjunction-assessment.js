// Intro reel for "Fast All-vs-All Conjunction Screening" (Koury, 1.8).
// 56 seconds, in plain words with the paper's own math: every object against
// every other, a bound each trajectory computes for itself, a grid that
// proposes pairs on the GPU and a module that decides in f64, refinement and
// the odds, the measured times, and screening on encrypted positions.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutCubic, easeInCubic, easeOutBack,
  rng, makeTau, OMEGA_K, orbitPos, makeSky, occluded, project, limbCamera, createStudio,
} from "../showreel/kit.js";
import { prepareMath, createPaperKit } from "./paper-kit.js";

export const meta = { name: "reel", media: "docs/media/papers/fast-conjunction-assessment", poster: 750, crf: [29, 40], audio: { track: "orbital-insertion", start: 120 } };

const DURATION = 56;
const FPS = 30;
const T_PAIRS = 5.5; // every object against every other
const T_BOUND = 12.5; // each trajectory bounds its own motion
const T_GRID = 21.5; // the GPU proposes, the module decides
const T_REF = 29.5; // the closest moment, then the odds
const T_RES = 38.0; // the measured times
const T_PRIV = 46.0; // screening on encrypted positions
const T_END = 51.5; // lockup

const SATS = makeSky();
const LEO = SATS.filter((s) => s.kind === 0);
const tau = makeTau(() => 1, DURATION);

function cameraAt(t) {
  if (t < T_RES) return limbCamera(t, -40, 30);
  return limbCamera(t, 10, 26);
}

// Boxes for the grid scene: object segments widened by d/2 + D.
const BOXES = (() => {
  const R = rng(31);
  const list = [];
  for (let i = 0; i < 46; i++) {
    const big = i === 7 || i === 29;
    const w = big ? 260 : lerp(28, 72, R());
    const h = big ? 56 : lerp(24, 64, R());
    list.push({ x: lerp(1000, 1760 - w, R()), y: lerp(170, 640 - h, R()), w, h, t: R() });
  }
  return list;
})();
const OVERLAPS = (() => {
  const o = [];
  BOXES.forEach((a, i) => BOXES.forEach((b, j) => {
    if (j <= i) return;
    if (a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h) o.push([i, j]);
  }));
  return o;
})();

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "WHITEPAPER · FAST ALL-VS-ALL CONJUNCTION SCREENING", hudUrl: "SPACEDATANETWORK.ORG" });
  const { glc, sx, gx, font, maskText, dotGlow, scrim, renderEarth, drawSatellites, eyebrow, block, beginSubframe, bloom, hud, padlock } = studio;
  const math = await prepareMath({
    pairs: { key: "ca_pairs", px: 60, color: INK },
    bound: { key: "ca_bound", px: 46, color: INK },
    sgp4: { key: "ca_sgp4", px: 44, color: INK },
    cheb: { key: "ca_cheb", px: 44, color: AMBER },
    test: { key: "ca_test", px: 50, color: AMBER },
    newton: { key: "ca_newton", px: 50, color: INK },
    q: { key: "ca_q", px: 50, color: INK },
    pcmax: { key: "ca_pcmax", px: 46, color: AMBER },
    he: { key: "ca_he", px: 54, color: INK },
  });
  const { eq, panel, label, stat, chip, paperLockup, strokePath } = createPaperKit(studio, math);

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
      if (q && q.x > 900 && q.x < W && q.y > 0 && q.y < H) vis.push(q);
    }
    const n = Math.round(lerp(40, 320, seg(t, T_PAIRS + 0.3, T_PAIRS + 3.5)));
    sx.save();
    sx.globalCompositeOperation = "lighter";
    sx.lineWidth = 1;
    for (let k = 0; k < n && vis.length > 2; k++) {
      const a = vis[Math.floor(R() * vis.length)];
      const b = vis[Math.floor(R() * vis.length)];
      sx.strokeStyle = `rgba(245,165,36,${0.18 + 0.28 * R()})`;
      sx.globalAlpha = alpha;
      sx.beginPath();
      sx.moveTo(a.x, a.y);
      sx.lineTo(b.x, b.y);
      sx.stroke();
    }
    sx.restore();
  }

  // ------------------------------------------- the self-bounded motion diagram
  function drawBound(t, alpha) {
    if (alpha <= 0) return;
    const draw = (x0, y0, x1, y1, D, bend, col, t0, name) => {
      const k = easeInOutCubic(seg(t, t0, t0 + 0.9));
      if (k <= 0) return;
      const dx = x1 - x0;
      const dy = y1 - y0;
      const L = Math.hypot(dx, dy);
      const nx = -dy / L;
      const ny = dx / L;
      const xe = lerp(x0, x1, k);
      const ye = lerp(y0, y1, k);
      // the tube: the straight line widened by D
      sx.save();
      sx.globalAlpha = alpha * 0.16;
      sx.fillStyle = col;
      sx.beginPath();
      sx.moveTo(x0 + nx * D, y0 + ny * D);
      sx.lineTo(xe + nx * D, ye + ny * D);
      sx.arc(xe, ye, D, Math.atan2(ny, nx), Math.atan2(-ny, -nx), dx < 0);
      sx.lineTo(x0 - nx * D, y0 - ny * D);
      sx.arc(x0, y0, D, Math.atan2(-ny, -nx), Math.atan2(ny, nx), dx < 0);
      sx.fill();
      sx.restore();
      // the straight line r_k + v_k τ
      strokePathScreen([[x0, y0], [xe, ye]], col, alpha * 0.6, 1.5, [8, 8]);
      // the true path inside the tube
      const pts = [];
      for (let i = 0; i <= 40; i++) {
        const f = (i / 40) * k;
        const off = Math.sin(f * Math.PI) * bend * D;
        pts.push([lerp(x0, x1, f) + nx * off, lerp(y0, y1, f) + ny * off]);
      }
      strokePathScreen(pts, col, alpha, 3, null, 0.5);
      const [hx, hy] = pts[pts.length - 1];
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = col;
      sx.beginPath();
      sx.arc(hx, hy, 7, 0, Math.PI * 2);
      sx.fill();
      sx.restore();
      dotGlow(hx, hy, 24, col, alpha * 0.7);
      label(name, x0 - 10, y0 - D - 20, t0 + 0.2, T_GRID - 0.45, t, { px: 20, color: col });
      // the bound D, marked across the tube at mid-span
      const mk = seg(t, t0 + 0.9, t0 + 1.2);
      if (mk > 0) {
        const mx = lerp(x0, x1, 0.5);
        const my = lerp(y0, y1, 0.5);
        sx.save();
        sx.globalAlpha = alpha * mk;
        sx.strokeStyle = INK;
        sx.lineWidth = 1.5;
        sx.beginPath();
        sx.moveTo(mx, my);
        sx.lineTo(mx + nx * D, my + ny * D);
        sx.stroke();
        sx.font = font("italic 500", 30, "Times New Roman, serif");
        sx.fillStyle = INK;
        sx.fillText(name === "OBJECT 1" ? "D₁" : "D₂", mx + nx * D * 0.5 + 12, my + ny * D * 0.5 + 6);
        sx.restore();
      }
    };
    draw(1060, 250, 1700, 330, 46, 0.75, AMBER, T_BOUND + 0.6, "OBJECT 1");
    draw(1080, 620, 1720, 470, 40, -0.8, CYAN, T_BOUND + 1.0, "OBJECT 2");
    // the closest approach of the straight lines, against d + D1 + D2
    const ck = seg(t, T_BOUND + 3.0, T_BOUND + 3.5);
    if (ck > 0) {
      const ax = 1560;
      const ay = 310;
      const bx = 1580;
      const by = 503;
      sx.save();
      sx.globalAlpha = alpha * ck;
      sx.strokeStyle = RED;
      sx.lineWidth = 2;
      sx.setLineDash([5, 5]);
      sx.beginPath();
      sx.moveTo(ax, ay);
      sx.lineTo(lerp(ax, bx, ck), lerp(ay, by, ck));
      sx.stroke();
      sx.restore();
    }
    eq("test", 1040, 760, T_BOUND + 3.4, T_GRID - 0.45, t);
    label("A PAIR CAN CLOSE ONLY IF", 1042, 700, T_BOUND + 3.3, T_GRID - 0.45, t, { px: 20, color: MUTED });
  }
  function strokePathScreen(pts, color, alpha, width, dash = null, glowA = 0) {
    if (alpha <= 0) return;
    sx.save();
    sx.strokeStyle = color;
    sx.globalAlpha = alpha;
    sx.lineWidth = width;
    sx.lineCap = "round";
    if (dash) sx.setLineDash(dash);
    sx.beginPath();
    pts.forEach(([x, y], i) => (i ? sx.lineTo(x, y) : sx.moveTo(x, y)));
    sx.stroke();
    sx.restore();
    if (glowA > 0) {
      gx.strokeStyle = color;
      gx.globalAlpha = alpha * glowA;
      gx.lineWidth = width * 1.5;
      gx.beginPath();
      pts.forEach(([x, y], i) => (i ? gx.lineTo(x / 2, y / 2) : gx.moveTo(x / 2, y / 2)));
      gx.stroke();
    }
  }

  // ------------------------------------------------ grid: propose, decide
  function drawGrid(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 980;
    const y0 = 150;
    const cell = 80;
    const gk = easeOutExpo(seg(t, T_GRID + 0.2, T_GRID + 0.8));
    sx.save();
    sx.globalAlpha = alpha * gk * 0.35;
    sx.strokeStyle = "rgba(245,245,247,0.6)";
    sx.lineWidth = 1;
    for (let i = 0; i <= 10; i++) {
      sx.beginPath();
      sx.moveTo(x0 + i * cell, y0);
      sx.lineTo(x0 + i * cell, y0 + 6 * cell);
      sx.stroke();
    }
    for (let j = 0; j <= 6; j++) {
      sx.beginPath();
      sx.moveTo(x0, y0 + j * cell);
      sx.lineTo(x0 + 10 * cell, y0 + j * cell);
      sx.stroke();
    }
    sx.restore();
    const hit = new Set(OVERLAPS.flat());
    const pk = seg(t, T_GRID + 2.0, T_GRID + 2.4);
    const dk = seg(t, T_GRID + 4.2, T_GRID + 4.6);
    BOXES.forEach((b, i) => {
      const k = easeOutBack(seg(t, T_GRID + 0.6 + b.t * 1.0, T_GRID + 0.9 + b.t * 1.0));
      if (k <= 0) return;
      const proposed = hit.has(i) && pk > 0;
      const col = proposed ? AMBER : "rgba(245,245,247,0.55)";
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2) * (proposed ? 1 : 1 - 0.5 * pk);
      sx.strokeStyle = col;
      sx.lineWidth = proposed ? 2 : 1.3;
      sx.fillStyle = proposed ? "rgba(245,165,36,0.12)" : "rgba(245,245,247,0.04)";
      const cx = b.x + b.w / 2;
      const cy = b.y + b.h / 2;
      sx.beginPath();
      sx.rect(cx - (b.w / 2) * k, cy - (b.h / 2) * k, b.w * k, b.h * k);
      sx.fill();
      sx.stroke();
      sx.restore();
    });
    // the f64 decision: pairs that pass get a check, a few drop out
    OVERLAPS.forEach(([i, j], n) => {
      const a = BOXES[i];
      const b = BOXES[j];
      const mx = (Math.max(a.x, b.x) + Math.min(a.x + a.w, b.x + b.w)) / 2;
      const my = (Math.max(a.y, b.y) + Math.min(a.y + a.h, b.y + b.h)) / 2;
      if (pk > 0) dotGlow(mx, my, 16, AMBER, alpha * pk * 0.5);
      const k = easeOutBack(seg(t, T_GRID + 4.2 + n * 0.05, T_GRID + 4.5 + n * 0.05));
      if (k > 0) studio.badge(n % 7 === 3 ? "cross" : "check", mx, my, 30 * k, n % 7 === 3 ? RED : AMBER, alpha);
    });
    const tOut = T_REF - 0.45;
    stat("829,990", "ON THE GPU, f32 + 0.01 km SLACK", 1000, 800, T_GRID + 2.3, tOut, t, { px: 72, color: INK });
    stat("828,910", "PASSED THE f64 RE-TEST", 1480, 800, T_GRID + 4.4, tOut, t, { px: 72 });
    label("FULL CATALOG, ONE DAY OF CANDIDATES", 1002, 712, T_GRID + 2.3, tOut, t, { px: 18 });
    void dk;
  }

  // -------------------------------------- refinement: Newton on the range rate
  function drawRefine(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 1020;
    const x1 = 1780;
    const yb = 600;
    const range = (u) => 30 + 340 * Math.pow((u - 0.58) / 0.6, 2);
    const X = (u) => lerp(x0, x1, u);
    const Y = (u) => yb - Math.min(420, range(u));
    const ak = seg(t, T_REF + 0.3, T_REF + 0.7);
    sx.save();
    sx.globalAlpha = alpha * ak;
    sx.strokeStyle = "rgba(245,245,247,0.5)";
    sx.lineWidth = 1.5;
    sx.beginPath();
    sx.moveTo(x0, 150);
    sx.lineTo(x0, yb);
    sx.lineTo(x1, yb);
    sx.stroke();
    sx.restore();
    label("RANGE", x0 - 10, 136, T_REF + 0.4, T_RES - 0.45, t, { px: 18 });
    label("TIME", x1, yb + 40, T_REF + 0.4, T_RES - 0.45, t, { px: 18, align: "right" });
    const pts = [];
    for (let i = 0; i <= 120; i++) {
      const u = 0.04 + (i / 120) * 0.92;
      pts.push([X(u), Y(u)]);
    }
    studio.strokeOn(pts, easeInOutCubic(seg(t, T_REF + 0.5, T_REF + 1.5)), CYAN, 3, alpha, 0.5);
    // Newton iterates walk down to the root of the range rate
    const it = [0.12, 0.36, 0.5, 0.56, 0.575, 0.58];
    it.forEach((u, i) => {
      const t0 = T_REF + 1.8 + i * 0.35;
      const k = easeOutBack(seg(t, t0, t0 + 0.3));
      if (k <= 0) return;
      const last = i === it.length - 1;
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = last ? AMBER : INK;
      sx.beginPath();
      sx.arc(X(u), Y(u), (last ? 9 : 6) * k, 0, Math.PI * 2);
      sx.fill();
      sx.restore();
      if (i > 0) strokePathScreen([[X(it[i - 1]), Y(it[i - 1])], [X(u), Y(u)]], "rgba(245,245,247,0.6)", alpha * clamp(k), 1.5, [4, 5]);
      if (last) {
        dotGlow(X(u), Y(u), 40, AMBER, alpha * 0.6);
        sx.save();
        sx.globalAlpha = alpha * k;
        sx.strokeStyle = AMBER;
        sx.setLineDash([4, 6]);
        sx.beginPath();
        sx.moveTo(X(u), Y(u) + 14);
        sx.lineTo(X(u), yb);
        sx.stroke();
        sx.restore();
      }
    });
    label("CLOSEST APPROACH", X(0.58), yb + 40, T_REF + 4.0, T_RES - 0.45, t, { px: 18, align: "center", color: AMBER });
    eq("pcmax", 150, 570, T_REF + 4.6, T_RES - 0.45, t);
    label("MAXIMUM PROBABILITY: NEEDS NO COVARIANCE, AN UPPER BOUND", 152, 630, T_REF + 4.8, T_RES - 0.45, t, { px: 18 });
  }

  // ----------------------------------------------- the measured times
  function drawFlashes(cam, t, T, alpha) {
    if (alpha <= 0) return;
    for (let i = 0; i < LEO.length; i += 37) {
      const o = LEO[i];
      const ph = (i * 0.618) % 1;
      const k = ((t - T_RES) * 0.9 + ph) % 1;
      if (k > 0.25) continue;
      const p = orbitPos(o, o.u0 + o.w * T);
      if (occluded(cam, p)) continue;
      const q = project(cam, p);
      if (!q || q.x < 900) continue;
      const f = 1 - k / 0.25;
      sx.save();
      sx.globalAlpha = alpha * f;
      sx.strokeStyle = AMBER;
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.arc(q.x, q.y, 4 + (1 - f) * 24, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      dotGlow(q.x, q.y, 10, AMBER, alpha * f * 0.6);
    }
  }
  function drawResults(t) {
    const tOut = T_PRIV - 0.45;
    stat("19 s", "SGP4 · CPU", 960, 260, T_RES + 0.5, tOut, t, { px: 104 });
    stat("21 s", "SGP4 · GPU", 1250, 260, T_RES + 0.7, tOut, t, { px: 104, color: INK });
    stat("~8 min", "NUMERICAL HPOP", 1500, 260, T_RES + 0.9, tOut, t, { px: 104, color: INK });
    stat("292,516", "CLOSE APPROACHES WITHIN 5 km OVER THREE DAYS", 1000, 470, T_RES + 1.6, tOut, t, { px: 80 });
    stat("13,088", "WITH A CALIBRATED COVARIANCE PROBABILITY", 1000, 640, T_RES + 2.4, tOut, t, { px: 80, color: INK });
  }

  // -------------------------------------- screening on encrypted positions
  function drawPrivate(t, alpha) {
    if (alpha <= 0) return;
    const A = [1200, 380];
    const B = [1640, 380];
    [[A, "OPERATOR A", AMBER], [B, "OPERATOR B", CYAN]].forEach(([p, name, col], i) => {
      const k = easeOutBack(seg(t, T_PRIV + 0.3 + i * 0.15, T_PRIV + 0.7 + i * 0.15));
      const lockK = easeInExpo(seg(t, T_PRIV + 0.9, T_PRIV + 1.1));
      padlock(p[0], p[1], 120 * k, 1 - lockK, lockK >= 1 ? col : INK, alpha);
      label(name, p[0], p[1] + 110, T_PRIV + 0.4, T_END - 0.45, t, { px: 22, color: col, align: "center" });
    });
    const k = easeInOutCubic(seg(t, T_PRIV + 1.4, T_PRIV + 2.2));
    strokePathScreen([[A[0] + 90, A[1]], [lerp(A[0] + 90, B[0] - 90, k), B[1]]], "rgba(245,245,247,0.6)", alpha, 1.6, [6, 7]);
    if (k >= 1) chip("CLOSE · NOT CLOSE", (A[0] + B[0]) / 2, A[1] - 70, easeOutBack(seg(t, T_PRIV + 2.2, T_PRIV + 2.6)), alpha, { color: AMBER, px: 20 });
    eq("he", 1420, 640, T_PRIV + 1.0, T_END - 0.45, t, { align: "center" });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_PAIRS, 0.8], [T_BOUND, 1], [T_GRID, 1], [T_REF, 1], [T_RES, 1], [T_PRIV, 0.8], [T_END + 0.2, 1]]);
  const flash = (t) => 0.08 * Math.exp(-Math.pow((t - (T_GRID + 4.2)) / 0.05, 2));
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    const T = tau(t);
    const cam = cameraAt(t);
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const under = seg(t, T_BOUND - 0.1, T_BOUND + 0.4) * (1 - seg(t, T_RES - 0.3, T_RES + 0.1)) + seg(t, T_PRIV - 0.1, T_PRIV + 0.4);
    const earthFade = seg(t, 0, 1.2) * (1 - 0.8 * clamp(under)) * (1 - out);
    if (earthFade > 0.001) {
      renderEarth(cam, t, earthFade, 0.22 * (1 - seg(t, 1.5, 3)), t < T_RES ? 40 : 70, 18);
      if (under > 0) sx.filter = `blur(${14 * clamp(under)}px)`;
      sx.drawImage(glc, 0, 0);
      sx.filter = "none";
    }
    const satA = seg(t, 0.3, 0.9) * (1 - clamp(under)) * (1 - out);
    if (satA > 0.01) drawSatellites(cam, t + 2.0, T, satA, t < T_PAIRS, SATS);
    if (t > T_PAIRS && t < T_BOUND) drawPairs(cam, t, T, seg(t, T_PAIRS, T_PAIRS + 0.4) * (1 - seg(t, T_BOUND - 0.4, T_BOUND)));
    if (t > T_BOUND - 0.05 && t < T_GRID + 0.1) drawBound(t, seg(t, T_BOUND, T_BOUND + 0.3) * (1 - seg(t, T_GRID - 0.4, T_GRID)));
    if (t > T_GRID - 0.05 && t < T_REF + 0.1) drawGrid(t, seg(t, T_GRID, T_GRID + 0.3) * (1 - seg(t, T_REF - 0.4, T_REF)));
    if (t > T_REF - 0.05 && t < T_RES + 0.1) drawRefine(t, seg(t, T_REF, T_REF + 0.3) * (1 - seg(t, T_RES - 0.4, T_RES)));
    if (t > T_RES - 0.05 && t < T_PRIV + 0.1) drawFlashes(cam, t, T, seg(t, T_RES, T_RES + 0.3) * (1 - seg(t, T_PRIV - 0.4, T_PRIV)));
    if (t > T_PRIV - 0.05 && t < T_END + 0.1) drawPrivate(t, seg(t, T_PRIV, T_PRIV + 0.3) * (1 - seg(t, T_END - 0.4, T_END)));
    scrim((1 - out) * 0.9);

    // -------------------------------------------------------- the type
    if (t < T_PAIRS + 0.1) {
      eyebrow("WHITEPAPER · EDITION 1.8", 2, 0.5, T_PAIRS - 0.45, t);
      block([["Will anything", INK], ["come too close?", AMBER]], "EVERY CATALOG OBJECT, CHECKED AGAINST EVERY OTHER", 0.6, T_PAIRS - 0.4, t, 88);
    }
    if (t > T_PAIRS - 0.05 && t < T_BOUND + 0.1) {
      eq("pairs", 150, 250, T_PAIRS + 0.4, T_BOUND - 0.45, t);
      label("PAIRS AMONG n OBJECTS", 152, 340, T_PAIRS + 0.7, T_BOUND - 0.45, t, { px: 18 });
      stat("4,321", "STEPS OF 60 s OVER THREE DAYS", 152, 470, T_PAIRS + 1.4, T_BOUND - 0.45, t, { px: 64, color: INK });
      stat("~2.3 trillion", "PAIR-STEPS", 680, 470, T_PAIRS + 1.8, T_BOUND - 0.45, t, { px: 64 });
      block([["32,514 objects.", INK], ["528.6 million pairs.", AMBER]], "AT 10 km/s, A CLOSE PASS LASTS UNDER A SECOND", T_PAIRS + 0.1, T_BOUND - 0.4, t, 84);
    }
    if (t > T_BOUND - 0.05 && t < T_GRID + 0.1) {
      eq("bound", 150, 214, T_BOUND + 0.4, T_GRID - 0.45, t);
      eq("sgp4", 150, 330, T_BOUND + 1.4, T_GRID - 0.45, t);
      label("SGP4 ELEMENT SETS", 152, 390, T_BOUND + 1.6, T_GRID - 0.45, t, { px: 18 });
      eq("cheb", 150, 470, T_BOUND + 2.2, T_GRID - 0.45, t);
      label("A TRAJECTORY FROM ANY PROPAGATOR, MANEUVERS INCLUDED", 152, 520, T_BOUND + 2.4, T_GRID - 0.45, t, { px: 18 });
      block([["Each orbit bounds", INK], ["its own motion.", AMBER]], "PAIRS THAT CANNOT CLOSE ARE DROPPED WITHOUT FURTHER WORK", T_BOUND + 0.1, T_GRID - 0.4, t, 84);
    }
    if (t > T_GRID - 0.05 && t < T_REF + 0.1) {
      block([["The GPU proposes.", INK], ["The module decides.", AMBER]], "EVERY REPORTED NUMBER COMES FROM THE f64 MODULE", T_GRID + 0.1, T_REF - 0.4, t, 88);
    }
    if (t > T_REF - 0.05 && t < T_RES + 0.1) {
      eq("newton", 150, 230, T_REF + 0.4, T_RES - 0.45, t);
      eq("q", 150, 390, T_REF + 1.2, T_RES - 0.45, t);
      label("PROVES ONE MINIMUM, THEN ABOUT TEN EVALUATIONS", 152, 470, T_REF + 1.4, T_RES - 0.45, t, { px: 18 });
      block([["Find the closest moment.", INK], ["Then the odds.", AMBER]], "TIME, MISS DISTANCE, RELATIVE SPEED, PROBABILITY", T_REF + 0.1, T_RES - 0.4, t, 84);
    }
    if (t > T_RES - 0.05 && t < T_PRIV + 0.1) {
      drawResults(t);
      block([["The full catalog,", INK], ["three days ahead.", AMBER]], "EVERY RUN ON ONE CATALOG AND PROPAGATOR, THE SAME CONJUNCTIONS", T_RES + 0.1, T_PRIV - 0.4, t, 88);
    }
    if (t > T_PRIV - 0.05 && t < T_END + 0.1) {
      block([["Screen together", INK], ["without sharing orbits.", AMBER]], "DISTANCES COMPUTED ON ENCRYPTED POSITIONS", T_PRIV + 0.1, T_END - 0.4, t, 84);
    }
    paperLockup({ eyebrow: "WHITEPAPER · EDITION 1.8", title: ["Fast All-vs-All", "Conjunction Screening"], byline: "Anthony “TJ” Koury III", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
