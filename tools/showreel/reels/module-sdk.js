// Space Data Module SDK reel for digitalarsenal.github.io/space-data-module-sdk,
// in plain words. 34 seconds: build once and run on every node, one file
// instead of a rebuild per machine, digitally signed so you know who built it,
// nineteen kinds of modules with one interface each.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack, rng,
  limbCamera, createStudio,
} from "../kit.js";

export const meta = { name: "module-sdk-reel", media: "docs/media", poster: 450, audio: { track: "orbital-insertion", start: 127 } };

const DURATION = 34;
const FPS = 30;
const T_SHIFT = 6.5; // rebuilt for every computer → one file runs everywhere
const M_SHIFT = T_SHIFT + 3.0;
const T_SIGN = 13.5; // digitally signed
const T_KINDS = 20.5; // nineteen kinds
const T_END = 29.5;

const TARGETS = [["BROWSER", 1180], ["SERVER", 1480], ["NODE", 1780]];
const KINDS = [
  "PROPAGATOR", "MANEUVER", "PROPULSION", "ATTITUDE", "GNC", "SENSOR", "RADIO (RF)", "SIGNATURE", "ENVIRONMENT", "OBSTRUCTION",
  "BREAKUP", "REENTRY", "CONJUNCTION", "EFFECTS", "ESTIMATION", "SCHEDULER", "BEHAVIOR", "ANALYTICS", "DATA SOURCE",
];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "SPACE DATA MODULE SDK", hudUrl: "DIGITALARSENAL.GITHUB.IO/SPACE-DATA-MODULE-SDK" });
  const { glc, sx, font, maskText, dotGlow, scrim, pill, badge, stamp, bezierPts, pointOn, strokeOn, renderEarth, eyebrow, lockup, block, shiftBlock, beginSubframe, bloom, hud } = studio;

  // An isometric cube: the module.
  function cube(cx, cy, s, alpha, color = AMBER, fill = "rgba(245,165,36,0.12)") {
    if (alpha <= 0 || s <= 0) return;
    const a = s * 0.866;
    const top = [[cx, cy - s], [cx + a, cy - s / 2], [cx, cy], [cx - a, cy - s / 2]];
    const left = [[cx - a, cy - s / 2], [cx, cy], [cx, cy + s], [cx - a, cy + s / 2]];
    const right = [[cx, cy], [cx + a, cy - s / 2], [cx + a, cy + s / 2], [cx, cy + s]];
    sx.save();
    sx.globalAlpha = alpha;
    sx.lineJoin = "round";
    sx.lineWidth = Math.max(1.5, s * 0.045);
    sx.strokeStyle = color;
    [[top, 0.24], [left, 0.1], [right, 0.16]].forEach(([poly, f]) => {
      sx.beginPath();
      poly.forEach(([x, y], i) => (i ? sx.lineTo(x, y) : sx.moveTo(x, y)));
      sx.closePath();
      sx.fillStyle = fill.replace("0.12", String(f));
      sx.fill();
      sx.stroke();
    });
    sx.restore();
  }
  // Target devices: a browser window, a server rack, a network node.
  function device(kind, cx, cy, alpha, ok) {
    if (alpha <= 0) return;
    sx.save();
    sx.globalAlpha = alpha;
    sx.strokeStyle = ok ? AMBER : "rgba(245,245,247,0.7)";
    sx.lineWidth = 2;
    if (kind === "BROWSER") {
      sx.beginPath();
      sx.roundRect(cx - 70, cy - 48, 140, 96, 8);
      sx.moveTo(cx - 70, cy - 26);
      sx.lineTo(cx + 70, cy - 26);
      sx.stroke();
      [0, 1, 2].forEach((i) => {
        sx.beginPath();
        sx.arc(cx - 56 + i * 12, cy - 37, 3, 0, Math.PI * 2);
        sx.stroke();
      });
    } else if (kind === "SERVER") {
      [0, 1, 2].forEach((i) => {
        sx.beginPath();
        sx.roundRect(cx - 62, cy - 48 + i * 34, 124, 26, 5);
        sx.stroke();
        sx.beginPath();
        sx.arc(cx + 44, cy - 35 + i * 34, 3, 0, Math.PI * 2);
        sx.stroke();
      });
    } else {
      sx.beginPath();
      sx.arc(cx, cy, 46, 0, Math.PI * 2);
      sx.stroke();
      const pts = [[cx, cy - 22], [cx - 20, cy + 14], [cx + 20, cy + 14]];
      sx.beginPath();
      pts.forEach(([x, y], i) => (i ? sx.lineTo(x, y) : sx.moveTo(x, y)));
      sx.closePath();
      sx.stroke();
      pts.forEach(([x, y]) => {
        sx.beginPath();
        sx.arc(x, y, 5, 0, Math.PI * 2);
        sx.fillStyle = ok ? AMBER : INK;
        sx.fill();
      });
    }
    sx.font = font(600, 22, SANS);
    sx.letterSpacing = "3px";
    sx.fillStyle = ok ? AMBER : MUTED;
    sx.textAlign = "center";
    sx.fillText(kind, cx, cy + 92);
    sx.restore();
  }

  // ----------------------------------------- one module, every target
  function drawOpen(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, 0.4, 1.1));
    cube(1480, 300, 90 * k, alpha * clamp(k * 2));
    dotGlow(1480, 300, 160 * k, AMBER, 0.12 * alpha);
    TARGETS.forEach(([name, x], i) => {
      const t0 = 1.6 + i * 0.35;
      device(name, x, 650, alpha * seg(t, t0 - 0.4, t0), seg(t, t0 + 0.7, t0 + 0.8) > 0);
      const pts = bezierPts([1480, 400], [1480, 500], [x, 470], [x, 570], 32);
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.6)), "rgba(245,165,36,0.7)", 2, alpha, 0.4, [6, 7]);
      const fly = seg(t, t0, t0 + 0.7);
      if (fly > 0 && fly < 1) {
        const [px, py] = pointOn(pts, easeInOutCubic(fly));
        cube(px, py, 22, alpha);
        dotGlow(px, py, 30, AMBER, alpha * 0.5);
      }
      const ok = seg(t, t0 + 0.7, t0 + 0.95);
      if (ok > 0) badge("check", x + 64, 600, 36 * easeOutBack(ok), AMBER, alpha);
    });
  }

  // ----------------------- rebuilt for every computer → one file runs everywhere
  function drawShift(t, alpha) {
    if (alpha <= 0) return;
    const m = easeInOutExpo(seg(t, M_SHIFT + 0.2, M_SHIFT + 0.8));
    // before: one build per machine, each a different file
    const old = alpha * (1 - m);
    TARGETS.forEach(([name, x], i) => {
      const k = easeOutBack(seg(t, T_SHIFT + 0.2 + i * 0.12, T_SHIFT + 0.6 + i * 0.12));
      device(name, x, 650, alpha * clamp(k * 2), m > 0.5);
      if (old > 0) {
        const fx = x;
        const fy = 330;
        sx.save();
        sx.globalAlpha = old * clamp(k * 2);
        sx.beginPath();
        sx.roundRect(fx - 60, fy - 72, 120, 144, 10);
        sx.fillStyle = "rgba(24,24,27,0.92)";
        sx.fill();
        sx.strokeStyle = "rgba(245,245,247,0.45)";
        sx.lineWidth = 1.5;
        sx.stroke();
        sx.font = font(600, 20, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillStyle = MUTED;
        sx.textAlign = "center";
        sx.fillText(["BUILD A", "BUILD B", "BUILD C"][i], fx, fy - 32);
        sx.fillStyle = "rgba(245,245,247,0.4)";
        [0.7, 0.5, 0.8, 0.6].forEach((f, r) => sx.fillRect(fx - 40, fy - 6 + r * 18, 80 * f, 6));
        sx.strokeStyle = "rgba(245,245,247,0.35)";
        sx.setLineDash([5, 6]);
        sx.beginPath();
        sx.moveTo(fx, fy + 76);
        sx.lineTo(fx, 560);
        sx.stroke();
        sx.restore();
        // the rebuild spinner, forever turning
        sx.save();
        sx.globalAlpha = old * clamp(k * 2);
        sx.strokeStyle = "rgba(245,245,247,0.7)";
        sx.lineWidth = 2.5;
        sx.lineCap = "round";
        const a0 = t * 6 + i;
        sx.beginPath();
        sx.arc(fx + 54, fy - 66, 13, a0, a0 + 1.7);
        sx.stroke();
        sx.restore();
      }
    });
    // after: the one module, wired to all three
    if (m > 0) {
      const k = easeOutBack(seg(t, M_SHIFT + 0.4, M_SHIFT + 0.9));
      cube(1480, 300, 90 * k, alpha * clamp(k * 2));
      dotGlow(1480, 300, 150 * k, AMBER, 0.12 * alpha);
      TARGETS.forEach(([, x], i) => {
        const t0 = M_SHIFT + 0.7 + i * 0.12;
        const pts = bezierPts([1480, 400], [1480, 500], [x, 470], [x, 570], 32);
        strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.5)), "rgba(245,165,36,0.75)", 2, alpha, 0.4);
        const ok = seg(t, t0 + 0.5, t0 + 0.7);
        if (ok > 0) badge("check", x + 64, 600, 36 * easeOutBack(ok), AMBER, alpha);
      });
    }
  }

  // --------------------------------------------------- digitally signed
  function drawSign(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, T_SIGN + 0.2, T_SIGN + 0.7));
    cube(1480, 250, 80 * k, alpha * clamp(k * 2));
    const st = seg(t, T_SIGN + 0.9, T_SIGN + 1.07);
    if (st > 0) stamp(1480, 400, st, alpha);
    [[1240, false], [1720, true]].forEach(([nx, bad], j) => {
      const t0 = T_SIGN + 1.5 + j * 0.15;
      const ny = 690;
      const pts = bezierPts([1480, 440], [1480, 540], [nx, 560], [nx, 640], 32);
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.5)), "rgba(245,245,247,0.5)", 1.5, alpha, 0, [5, 7]);
      const p = easeInOutCubic(seg(t, t0 + 0.1, t0 + 0.7));
      const land = seg(t, t0 + 0.7, t0 + 0.85);
      if (p > 0 && land < 1) {
        const [px, py] = pointOn(pts, p);
        cube(px, py, 26, alpha * (1 - land), bad && p > 0.5 ? RED : AMBER, bad && p > 0.5 ? "rgba(255,59,48,0.12)" : "rgba(245,165,36,0.12)");
      }
      if (land > 0) {
        badge(bad ? "cross" : "check", nx, ny, 56 * easeOutBack(land), bad ? RED : AMBER, alpha);
        maskText(sx, bad ? "CHANGED · REFUSED" : "VERIFIED · RUNS", nx, ny + 74, 22, 600, bad ? RED : AMBER, t0 + 0.8, T_KINDS - 0.45, t, { tracking: 3, align: "center" });
      }
    });
  }

  // ------------------------------------------------ nineteen kinds
  function drawKinds(t, alpha) {
    if (alpha <= 0) return;
    const cols = 3;
    const x0 = 1010;
    const y0 = 150;
    KINDS.forEach((name, i) => {
      const c = i % cols;
      const r = Math.floor(i / cols);
      const k = easeOutBack(seg(t, T_KINDS + 0.3 + i * 0.07, T_KINDS + 0.65 + i * 0.07));
      if (k <= 0) return;
      const x = x0 + c * 280;
      const y = y0 + r * 84;
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2);
      sx.translate(x + 125, y + 26);
      sx.scale(k, k);
      sx.translate(-(x + 125), -(y + 26));
      pill(x, y, 250, 52, i < 1 ? AMBER : "rgba(245,245,247,0.4)", "rgba(0,0,0,0.6)");
      sx.font = font(600, 19, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = i < 1 ? AMBER : INK;
      sx.textAlign = "center";
      sx.fillText(name, x + 125, y + 33);
      sx.restore();
    });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_SHIFT, 1], [M_SHIFT + 0.3, 0.6], [T_SIGN, 1], [T_KINDS, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;
  const SHIFT = {
    before: ["Rebuilt for every machine."],
    after: ["One file runs everywhere."],
    why: "SOFTWARE USUALLY HAS TO BE REBUILT FOR EACH COMPUTER",
    fix: "THE SAME MODULE RUNS IN A BROWSER, ON A SERVER, ON A NODE",
    next: "WITH THE SDK",
    tIn: T_SHIFT + 0.1,
    tMorph: M_SHIFT,
    tOut: T_SIGN - 0.4,
  };

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    renderEarth(limbCamera(t, -100, 35), t, 0.55 * seg(t, 0, 1.2) * (1 - out), 0.2, 40, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_SHIFT + 0.2) drawOpen(t, 1 - seg(t, T_SHIFT - 0.4, T_SHIFT));
    if (t > T_SHIFT - 0.05 && t < T_SIGN + 0.1) drawShift(t, seg(t, T_SHIFT, T_SHIFT + 0.3) * (1 - seg(t, T_SIGN - 0.4, T_SIGN)));
    if (t > T_SIGN - 0.05 && t < T_KINDS + 0.1) drawSign(t, seg(t, T_SIGN, T_SIGN + 0.3) * (1 - seg(t, T_KINDS - 0.4, T_KINDS)));
    if (t > T_KINDS - 0.05 && t < T_END + 0.1) drawKinds(t, seg(t, T_KINDS, T_KINDS + 0.3) * (1 - seg(t, T_END - 0.3, T_END)));
    scrim(1 - out);

    if (t < T_SHIFT + 0.1) {
      eyebrow("SPACE DATA MODULE SDK", 2, 0.4, T_SHIFT - 0.45, t);
      block([["Build once.", INK], ["Run on every node.", AMBER]], "SPACE SOFTWARE, PACKAGED AS MODULES", 0.5, T_SHIFT - 0.4, t, 88);
    }
    if (t > SHIFT.tIn - 0.05 && t < SHIFT.tOut + 0.5) shiftBlock(SHIFT, t);
    if (t > T_SIGN - 0.05 && t < T_KINDS + 0.1) {
      block([["Digitally signed.", INK], ["You know who built it.", AMBER]], "AND THAT NOBODY CHANGED IT ON THE WAY", T_SIGN + 0.1, T_KINDS - 0.4, t, 84);
    }
    if (t > T_KINDS - 0.05 && t < T_END + 0.1) {
      block([["Nineteen kinds.", INK], ["One interface each.", AMBER]], "ORBITS, SENSORS, RADIO, WEATHER, ANALYTICS AND MORE", T_KINDS + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "Space Data Module SDK", subtitle: "Build once. Run on every node.", url: "DIGITALARSENAL.GITHUB.IO/SPACE-DATA-MODULE-SDK", t0: T_END + 0.3 }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
