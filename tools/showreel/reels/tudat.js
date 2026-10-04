// Tudat WASM reel for digitalarsenal.github.io/tudat-wasm, in plain words. 30
// seconds: the TU Delft Astrodynamics Toolbox running in a web page, a simple
// model against the full physics, the Earth–Moon balance points, and a test
// suite that checks every result in the page.

import {
  W, H, AMBER, CYAN, INK, MUTED, SANS, MONO, D, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack,
  add, mul, norm, cross, makeTau, OMEGA_K, orbitBasis, orbitPos, makeSky, limbCamera, project, occluded, createStudio,
} from "../kit.js";

export const meta = { name: "tudat-reel", media: "docs/media", poster: 300, audio: { track: "orbital-insertion", start: 0 } };

const DURATION = 30;
const FPS = 30;
const T_MODEL = 6.5; // a simple model or the full physics
const T_MOON = 13.5; // Earth–Moon balance points
const T_TEST = 19.5; // checked in the page
const T_END = 25.5;

const SATS = makeSky();
const ORBIT = { ...orbitBasis(28 * D, 200 * D), r: 1.42 };
ORBIT.w = OMEGA_K * Math.pow(ORBIT.r, -1.5);
const N = norm(cross(ORBIT.e1, ORBIT.e2));
// Scene time slows once the comparison starts, so both tracks stay in frame.
const tau = makeTau((t) => 1 - 0.65 * seg(t, T_MODEL, T_MODEL + 0.8), DURATION);
const TESTS = [
  "Kepler orbital mechanics", "Spherical harmonics gravity", "Lambert targeting (Izzo)", "Earth–Moon three-body orbits",
  "NRLMSISE-00 atmosphere", "SPICE time conversions", "RK4 and RK78 integrators", "Orbital element conversions",
];

function cameraAt(t) {
  const k = easeInOutCubic(seg(t, 0, T_MODEL + 1));
  const dir = norm(add(mul(N, 0.95), mul(ORBIT.e1, lerp(0.42, 0.3, k))));
  const pos = mul(dir, lerp(4.6, 4.1, k));
  return { ...lookAtPos(pos), spin: 0.04 * t, off: [0.3, 0.0], zoom: 1, dist: lerp(4.6, 4.1, k) };
}
// Phase the orbit so the satellite rides the upper right of the frame through
// the comparison, clear of the type in the lower left.
ORBIT.u0 = (() => {
  const cam = cameraAt(T_MODEL + 3);
  let best = 0;
  let score = -Infinity;
  for (let u = 0; u < Math.PI * 2; u += 0.01) {
    let sc = 0;
    for (const du of [-0.4, 0, 0.5, 1.0, 1.4]) {
      const q = project(cam, orbitPos(ORBIT, u + du));
      if (!q) { sc -= 1e6; continue; }
      if (q.x < 1050 && q.y > 520) sc -= 2000 + (1050 - q.x) + (q.y - 520);
      sc -= 4 * Math.max(0, 120 - q.y) + 4 * Math.max(0, q.y - 980);
      sc += 0.3 * q.x;
    }
    if (sc > score) { score = sc; best = u; }
  }
  return best - ORBIT.w * tau(T_MODEL + 3);
})();
function lookAtPos(pos) {
  const f = norm(mul(pos, -1));
  const r = norm(cross(f, [0, 1, 0]));
  const u = cross(r, f);
  return { pos, f, r, u };
}

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "TUDAT WASM", hudUrl: "DIGITALARSENAL.GITHUB.IO/TUDAT-WASM" });
  const { glc, sx, font, maskText, dotGlow, scrim, pill, badge, renderEarth, drawSatellites, strokeOrbitArc, eyebrow, lockup, block, beginSubframe, bloom, hud } = studio;

  function head(cam, p, color, alpha, r = 6) {
    if (occluded(cam, p)) return null;
    const q = project(cam, p);
    if (!q) return null;
    sx.save();
    sx.globalAlpha = alpha;
    sx.fillStyle = color;
    sx.beginPath();
    sx.arc(q.x, q.y, r, 0, Math.PI * 2);
    sx.fill();
    sx.restore();
    dotGlow(q.x, q.y, r * 3.5, color, alpha * 0.7);
    return q;
  }
  function tag(q, text, color, dx, dy, alpha) {
    if (!q || alpha <= 0) return;
    sx.save();
    sx.globalAlpha = alpha;
    sx.font = font(600, 23, SANS);
    sx.letterSpacing = "3px";
    const tw = sx.measureText(text).width + 40;
    const lx = clamp(dx > 0 ? q.x + dx : q.x + dx - tw, 70, W - 70 - tw);
    const ly = clamp(q.y + dy - 22, 80, H - 220);
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

  // -------------------------------- an orbit, then a simple model against the full physics
  function drawOrbits(cam, t, alpha) {
    if (alpha <= 0) return;
    const u = ORBIT.u0 + ORBIT.w * tau(t);
    const draw = easeInOutCubic(seg(t, 0.8, 3.0));
    strokeOrbitArc(cam, ORBIT, u - 2.4 * draw, u, AMBER, alpha * 0.95, 2.6, 0.7);
    const split = seg(t, T_MODEL + 0.3, T_MODEL + 0.6);
    if (split <= 0) {
      head(cam, orbitPos(ORBIT, u), AMBER, alpha);
      return;
    }
    // From the split, the full-physics track falls lower and runs ahead of
    // the simple one, a little more every orbit.
    const gap = Math.max(0, t - T_MODEL - 0.3);
    const full = (v) => ({ ...ORBIT, r: ORBIT.r - 0.0035 * Math.max(0, v - uS) * Math.max(0, v - uS) });
    const uS = ORBIT.u0 + ORBIT.w * tau(T_MODEL + 0.3);
    const uSimple = u;
    const uFull = u + 0.009 * gap * gap;
    const simplePts = (v) => orbitPos(ORBIT, v);
    strokeOrbitArc(cam, ORBIT, uS, uSimple, "rgba(245,245,247,0.9)", alpha * split * 0.8, 1.8, 0, [7, 6]);
    // the full-physics path, drawn point by point (its radius changes)
    let prev = null;
    sx.save();
    sx.strokeStyle = CYAN;
    sx.globalAlpha = alpha * split;
    sx.lineWidth = 2.4;
    sx.beginPath();
    const steps = 60;
    for (let i = 0; i <= steps; i++) {
      const v = lerp(uS, uFull, i / steps);
      const p = orbitPos(full(v), v);
      const q = occluded(cam, p) ? null : project(cam, p);
      if (q && prev) {
        sx.moveTo(prev.x, prev.y);
        sx.lineTo(q.x, q.y);
      }
      prev = q;
    }
    sx.stroke();
    sx.restore();
    const qa = head(cam, simplePts(uSimple), INK, alpha);
    const qb = head(cam, orbitPos(full(uFull), uFull), CYAN, alpha);
    if (qa && qb) {
      sx.save();
      sx.globalAlpha = alpha * seg(t, T_MODEL + 1.5, T_MODEL + 2.0);
      sx.strokeStyle = AMBER;
      sx.setLineDash([4, 5]);
      sx.lineWidth = 2;
      sx.beginPath();
      sx.moveTo(qa.x, qa.y);
      sx.lineTo(qb.x, qb.y);
      sx.stroke();
      sx.restore();
    }
    const la = alpha * seg(t, T_MODEL + 0.8, T_MODEL + 1.2);
    tag(qa, "SIMPLE MODEL", INK, -60, -80, la);
    tag(qb, "FULL PHYSICS", CYAN, 60, 80, la);
  }

  // ------------------------------------------- the Earth–Moon balance points
  function drawMoon(t, alpha) {
    if (alpha <= 0) return;
    const cx = 1390;
    const cy = 400;
    const R = 280;
    const k = easeOutExpo(seg(t, T_MOON + 0.2, T_MOON + 0.9));
    const th = -0.28 + 0.05 * (t - T_MOON);
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.strokeStyle = "rgba(245,245,247,0.3)";
    sx.lineWidth = 1.5;
    sx.setLineDash([4, 7]);
    sx.beginPath();
    sx.arc(cx, cy, R * k, 0, Math.PI * 2);
    sx.stroke();
    sx.setLineDash([]);
    // Earth and Moon
    const earth = sx.createRadialGradient(cx - 10, cy - 10, 4, cx, cy, 44);
    earth.addColorStop(0, "#9fd0ff");
    earth.addColorStop(1, "#1d4f8f");
    sx.fillStyle = earth;
    sx.beginPath();
    sx.arc(cx, cy, 40, 0, Math.PI * 2);
    sx.fill();
    const mx = cx + Math.cos(th) * R * k;
    const my = cy + Math.sin(th) * R * k;
    sx.fillStyle = "#c9c9cf";
    sx.beginPath();
    sx.arc(mx, my, 16, 0, Math.PI * 2);
    sx.fill();
    sx.font = font(600, 21, SANS);
    sx.letterSpacing = "3px";
    sx.fillStyle = MUTED;
    sx.textAlign = "center";
    sx.fillText("EARTH", cx, cy + 74);
    sx.fillText("MOON", mx, my + 46);
    sx.restore();
    // L1-L5: where the pulls balance
    const pts = [
      ["L1", 0.84, 0], ["L2", 1.16, 0], ["L3", -1.0, 0], ["L4", 1, Math.PI / 3], ["L5", 1, -Math.PI / 3],
    ];
    pts.forEach(([name, f, a], i) => {
      const pk = easeOutBack(seg(t, T_MOON + 1.0 + i * 0.22, T_MOON + 1.4 + i * 0.22));
      if (pk <= 0) return;
      const ang = th + a + (f < 0 ? Math.PI : 0);
      const rr = Math.abs(f) * R * k;
      const x = cx + Math.cos(ang) * rr;
      const y = cy + Math.sin(ang) * rr;
      sx.save();
      sx.globalAlpha = alpha;
      sx.strokeStyle = AMBER;
      sx.lineWidth = 2;
      sx.beginPath();
      sx.moveTo(x - 9 * pk, y);
      sx.lineTo(x + 9 * pk, y);
      sx.moveTo(x, y - 9 * pk);
      sx.lineTo(x, y + 9 * pk);
      sx.stroke();
      sx.font = font(700, 24, SANS);
      sx.letterSpacing = "1px";
      sx.fillStyle = AMBER;
      sx.textAlign = "center";
      sx.fillText(name, x, y - 22);
      sx.restore();
      dotGlow(x, y, 22 * pk, AMBER, alpha * 0.5);
    });
    // a spacecraft parked near L2, tracing its loop
    const hk = seg(t, T_MOON + 2.4, T_MOON + 2.8);
    if (hk > 0) {
      const l2x = cx + Math.cos(th) * 1.16 * R * k;
      const l2y = cy + Math.sin(th) * 1.16 * R * k;
      sx.save();
      sx.globalAlpha = alpha * hk;
      sx.strokeStyle = CYAN;
      sx.lineWidth = 1.8;
      sx.beginPath();
      sx.ellipse(l2x, l2y, 26, 46, th, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      const a = (t - T_MOON) * 2.4;
      const px = l2x + Math.cos(th) * 26 * Math.cos(a) - Math.sin(th) * 46 * Math.sin(a);
      const py = l2y + Math.sin(th) * 26 * Math.cos(a) + Math.cos(th) * 46 * Math.sin(a);
      dotGlow(px, py, 14, CYAN, alpha * hk);
    }
  }

  // ------------------------------------------------- checked in the page
  function drawTests(t, alpha) {
    if (alpha <= 0) return;
    const px = 1040;
    const py = 130;
    const pw = 760;
    const k = easeOutExpo(seg(t, T_TEST + 0.2, T_TEST + 0.6));
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.beginPath();
    sx.roundRect(px, py, pw, 610, 18);
    sx.fillStyle = "rgba(22,22,24,0.9)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.28)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 22, SANS);
    sx.letterSpacing = "3.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("TEST SUITE", px + 32, py + 52);
    sx.restore();
    let passed = 0;
    TESTS.forEach((name, i) => {
      const t0 = T_TEST + 0.6 + i * 0.42;
      const run = seg(t, t0, t0 + 0.35);
      if (run <= 0) return;
      const done = run >= 1;
      if (done) passed++;
      const y = py + 112 + i * 60;
      sx.save();
      sx.globalAlpha = alpha * clamp(run * 3);
      sx.font = font(500, 26, SANS);
      sx.letterSpacing = "0px";
      sx.fillStyle = done ? INK : MUTED;
      sx.fillText(name, px + 84, y);
      if (!done) {
        sx.strokeStyle = INK;
        sx.lineWidth = 2.5;
        sx.lineCap = "round";
        const a0 = t * 8;
        sx.beginPath();
        sx.arc(px + 48, y - 9, 12, a0, a0 + 1.7);
        sx.stroke();
      }
      sx.restore();
      if (done) badge("check", px + 48, y - 9, 32, AMBER, alpha);
    });
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.font = font(500, 22, MONO);
    sx.fillStyle = AMBER;
    sx.textAlign = "right";
    sx.fillText(`${passed} / ${TESTS.length} PASSED`, px + pw - 32, py + 52);
    sx.restore();
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_MODEL, 0.8], [T_MOON, 1], [T_TEST, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const flat = seg(t, T_MOON - 0.3, T_MOON + 0.3);
    if (t < T_MOON + 0.3) {
      const cam = cameraAt(t);
      renderEarth(cam, t, seg(t, 0, 1.2) * (1 - flat), 0.2 * (1 - seg(t, 1.5, 3)), 55, 20);
      sx.drawImage(glc, 0, 0);
      const satA = 0.8 * seg(t, 0.3, 0.9) * (1 - 0.5 * seg(t, T_MODEL, T_MODEL + 0.4)) * (1 - flat);
      if (satA > 0.01) drawSatellites(cam, t + 2.0, tau(t), satA, true, SATS);
      drawOrbits(cam, t, seg(t, 0.6, 1.0) * (1 - flat));
    }
    if (t > T_MOON - 0.3) {
      renderEarth(limbCamera(t, 20, 25), t, 0.5 * flat * (1 - out), 0.2, 40, 18);
      sx.drawImage(glc, 0, 0);
    }
    if (t > T_MOON - 0.05 && t < T_TEST + 0.1) drawMoon(t, seg(t, T_MOON, T_MOON + 0.3) * (1 - seg(t, T_TEST - 0.4, T_TEST)));
    if (t > T_TEST - 0.05 && t < T_END + 0.1) drawTests(t, seg(t, T_TEST, T_TEST + 0.3) * (1 - seg(t, T_END - 0.3, T_END)));
    scrim(1 - out);

    if (t < T_MODEL + 0.1) {
      eyebrow("TUDAT WASM", 2, 0.4, T_MODEL - 0.45, t);
      block([["Spaceflight math", INK], ["in your browser.", AMBER]], "THE TU DELFT ASTRODYNAMICS TOOLBOX, NO INSTALL", 0.5, T_MODEL - 0.4, t, 88);
    }
    if (t > T_MODEL - 0.05 && t < T_MOON + 0.1) {
      block([["A simple model,", INK], ["or the full physics?", AMBER]], "RUN BOTH AND WATCH THE GAP GROW", T_MODEL + 0.1, T_MOON - 0.4, t, 84);
    }
    if (t > T_MOON - 0.05 && t < T_TEST + 0.1) {
      block([["Where the pulls", INK], ["of Earth and Moon balance.", AMBER]], "TRANSFERS, GRAVITY, ATMOSPHERE, TIME AND FRAMES", T_MOON + 0.1, T_TEST - 0.4, t, 80);
    }
    if (t > T_TEST - 0.05 && t < T_END + 0.1) {
      block([["Every result,", INK], ["checked in the page.", AMBER]], "A FULL TEST SUITE RUNS AS YOU WATCH", T_TEST + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "Tudat WASM", subtitle: "The TU Delft Astrodynamics Toolbox, in your browser", url: "DIGITALARSENAL.GITHUB.IO/TUDAT-WASM", t0: T_END + 0.3 }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
