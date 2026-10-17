// Intro reel for "Provenance and Security in Space Data Network" (Koury, 1.0).
// 42 seconds, in plain words and the paper's own numbers: every input keeps
// its source and the hash of its bytes, a snapshot is published whole and
// signed and re-runs in a browser, screening work units are checked by
// replicas byte for byte (a worker that hid conjunctions was caught), and the
// keys behind the signatures carry a public security bond.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO,
  clamp, lerp, seg, easeInOutCubic, easeInCubic, easeOutBack,
  limbCamera, createStudio,
} from "../showreel/kit.js";
import { prepareMath, createPaperKit } from "./paper-kit.js";

export const meta = { name: "reel", media: "docs/media/papers/provenance-and-security", poster: 760, crf: [29, 40], audio: { track: "delta-v", start: 12 } };

const DURATION = 42;
const FPS = 30;
const T_REC = 5.5; // every input keeps its source
const T_PUB = 13.0; // published whole, re-run anywhere
const T_REP = 20.5; // replicas compare byte for byte
const T_KEY = 29.0; // keys behind the signatures
const T_END = 37.5; // lockup

// The kinds of input a catalog solution rests on (section 2), each with a
// sample of the record a source carries: provider and the SHA-256 of its bytes.
const SOURCES = [
  ["IGS ORBITS", "sha256 4a1f 07c2 … 9c2e"],
  ["ILRS LASER RANGES", "sha256 b83e 51d0 … 07a4"],
  ["OPERATOR EPHEMERIS", "sha256 2d9c e6f1 … 5b13"],
  ["PUBLIC OMM", "sha256 f064 3a8b … c4d9"],
];
const SOL = [1560, 380];
const src = (i) => [860, 200 + i * 118];

// Work units of the 3-day screen (section 4): 96 units as 8 × 12 cells.
const COLS = 12;
const ROWS = 8;
const CELL = 46;
const GRID0 = [1010, 250];
const cellAt = (i) => [GRID0[0] + (i % COLS) * (CELL + 8), GRID0[1] + Math.floor(i / COLS) * (CELL + 8)];
// A deterministic shuffle: the order units finish, which ones were replicated,
// and the cheater's units that were caught.
const rand = (i) => { const x = Math.sin(i * 12.9898 + 4.1414) * 43758.5453; return x - Math.floor(x); };
const ORDER = Array.from({ length: 96 }, (_, i) => i).sort((a, b) => rand(a) - rand(b));
const REPLICATED = new Set(ORDER.filter((i) => rand(i + 97) < 0.3));
const CAUGHT = new Set([...REPLICATED].slice(0, 5));

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "WHITEPAPER · PROVENANCE AND SECURITY", hudUrl: "SPACEDATANETWORK.ORG" });
  const { glc, sx, font, dotGlow, scrim, renderEarth, eyebrow, block, beginSubframe, bloom, hud, keyIcon, bezierPts, strokeOn, stamp, badge } = studio;
  const math = await prepareMath({});
  const { panel, label, stat, chip, paperLockup } = createPaperKit(studio, math);

  // ------------------------------------------------------- the question
  function drawQuestion(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, 1.0, 1.5));
    panel(1180, 330, 520, 230, alpha * clamp(k * 2), { r: 22 });
    sx.save();
    sx.globalAlpha = alpha * clamp(k * 2);
    sx.font = font(650, 26, SANS);
    sx.letterSpacing = "3px";
    sx.fillStyle = INK;
    sx.textAlign = "left";
    sx.fillText("ORBIT · NORAD 25544", 1214, 386);
    sx.font = font(500, 22, MONO);
    sx.letterSpacing = "0px";
    sx.fillStyle = MUTED;
    ["EPOCH   2026-10-11 00:00:00 UTC", "X  -4213.8 km   Y  5103.2 km", "Z   1872.5 km   σ  0.82 km"].forEach((s, i) => sx.fillText(s, 1214, 432 + i * 36));
    sx.restore();
    const q = easeOutBack(seg(t, 1.8, 2.2));
    if (q > 0) {
      dotGlow(1700, 330, 120 * q, AMBER, 0.18 * alpha);
      chip("SOURCE?", 1700, 330, q, alpha, { color: AMBER, px: 24, stroke: AMBER });
    }
  }

  // ------------------------------------------- every input keeps its source
  function drawRecords(t, alpha) {
    if (alpha <= 0) return;
    SOURCES.forEach(([name, hash], i) => {
      const [x, y] = src(i);
      const t0 = T_REC + 0.3 + i * 0.22;
      const k = easeOutBack(seg(t, t0, t0 + 0.4));
      if (k <= 0) return;
      panel(x - 230, y - 50, 460, 100, alpha * clamp(k * 2), { r: 20 });
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2);
      sx.font = font(650, 22, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = INK;
      sx.textAlign = "left";
      sx.fillText(name, x - 200, y - 8);
      sx.font = font(500, 19, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = CYAN;
      sx.fillText(hash, x - 200, y + 26);
      sx.restore();
      const pts = bezierPts([x + 232, y], [x + 360, y], [SOL[0] - 330, SOL[1]], [SOL[0] - 200, SOL[1]], 40);
      strokeOn(pts, easeInOutCubic(seg(t, t0 + 0.5, t0 + 1.1)), "rgba(89,217,255,0.55)", 1.8, alpha, 0.35);
    });
    const s = easeOutBack(seg(t, T_REC + 1.8, T_REC + 2.3));
    if (s > 0) {
      panel(SOL[0] - 200, SOL[1] - 120, 440, 240, alpha * clamp(s * 2), { r: 24, stroke: "rgba(245,165,36,0.6)" });
      sx.save();
      sx.globalAlpha = alpha * clamp(s * 2);
      sx.font = font(650, 24, SANS);
      sx.letterSpacing = "3px";
      sx.fillStyle = AMBER;
      sx.textAlign = "left";
      sx.fillText("SOLUTION", SOL[0] - 170, SOL[1] - 66);
      sx.font = font(500, 20, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = INK;
      ["inputs   4 records, levels 2–3", "dynamics FFM (full force)", "uncertainty  calibrated"].forEach((line, i) => sx.fillText(line, SOL[0] - 170, SOL[1] - 18 + i * 36));
      sx.restore();
      label("LINEAGE: EVERY SOURCE RECORD, NAMED", SOL[0] + 20, SOL[1] + 170, T_REC + 2.4, T_PUB - 0.45, t, { px: 18, color: CYAN, align: "center" });
    }
  }

  // ------------------------------------- published whole, re-run anywhere
  function drawPublish(t, alpha) {
    if (alpha <= 0) return;
    for (let j = 2; j >= 0; j--) {
      const k = easeOutBack(seg(t, T_PUB + 0.3 + j * 0.12, T_PUB + 0.7 + j * 0.12));
      panel(1060 + j * 22, 250 + j * 22, 520, 300, alpha * clamp(k * 2) * (j === 0 ? 1 : 0.55), { r: 24 });
    }
    const k = seg(t, T_PUB + 0.6, T_PUB + 1.0);
    if (k > 0) {
      sx.save();
      sx.globalAlpha = alpha * k;
      sx.font = font(650, 24, SANS);
      sx.letterSpacing = "3px";
      sx.fillStyle = INK;
      sx.textAlign = "left";
      sx.fillText("CATALOG SNAPSHOT", 1096, 306);
      sx.font = font(500, 19, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = MUTED;
      ["manifest   every record by SHA-256", "head       bafkrei…q7x4", "objects    one complete snapshot"].forEach((line, i) => sx.fillText(line, 1096, 352 + i * 34));
      sx.restore();
    }
    const st = seg(t, T_PUB + 1.5, T_PUB + 1.67);
    if (st > 0) stamp(1320, 500, st, alpha);
    // The browser re-run: a window that reproduces the published number.
    const b = easeOutBack(seg(t, T_PUB + 2.4, T_PUB + 2.9));
    if (b > 0) {
      const x = 1180;
      const y = 650;
      panel(x, y, 620, 170, alpha * clamp(b * 2), { r: 18, stroke: "rgba(245,245,247,0.35)" });
      sx.save();
      sx.globalAlpha = alpha * clamp(b * 2);
      [0, 1, 2].forEach((i) => { sx.beginPath(); sx.arc(x + 30 + i * 22, y + 28, 7, 0, Math.PI * 2); sx.fillStyle = "rgba(245,245,247,0.35)"; sx.fill(); });
      sx.font = font(600, 22, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = INK;
      sx.textAlign = "left";
      sx.fillText("IN A WEB BROWSER", x + 110, y + 36);
      sx.font = font(500, 20, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = MUTED;
      sx.fillText("V1 seed, the same modules", x + 34, y + 92);
      sx.restore();
      chip("REPRODUCED EXACTLY", x + 430, y + 128, easeOutBack(seg(t, T_PUB + 3.4, T_PUB + 3.8)), alpha, { color: AMBER, px: 20, stroke: AMBER });
    }
  }

  // ------------------------------------------- replicas compare byte for byte
  function drawReplicas(t, alpha) {
    if (alpha <= 0) return;
    const run = seg(t, T_REP + 0.4, T_REP + 5.2);
    const done = Math.floor(run * 96);
    sx.save();
    sx.globalAlpha = alpha;
    ORDER.forEach((u, n) => {
      const [x, y] = cellAt(u);
      const finished = n < done;
      const caught = CAUGHT.has(u);
      const flagged = caught && finished && t < T_REP + 6.4;
      sx.beginPath();
      sx.roundRect(x, y, CELL, CELL, 8);
      sx.fillStyle = !finished ? "rgba(245,245,247,0.06)" : flagged ? "rgba(255,59,48,0.75)" : REPLICATED.has(u) ? "rgba(89,217,255,0.55)" : "rgba(245,165,36,0.55)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.16)";
      sx.lineWidth = 1;
      sx.stroke();
    });
    sx.restore();
    // a replicated pair, side by side, with matching hashes
    const p = easeOutBack(seg(t, T_REP + 2.6, T_REP + 3.0));
    if (p > 0) {
      chip("REPLICA A   sha256 7e41…d2c0", 1330, 760, p, alpha, { color: CYAN, px: 19, family: MONO, tracking: 0 });
      chip("REPLICA B   sha256 7e41…d2c0", 1330, 820, easeOutBack(seg(t, T_REP + 2.8, T_REP + 3.2)), alpha, { color: CYAN, px: 19, family: MONO, tracking: 0 });
      const c = seg(t, T_REP + 3.3, T_REP + 3.5);
      if (c > 0) badge("check", 1620, 790, 46, CYAN, alpha * c);
    }
    stat("27", "REPLICA PAIRS, ALL BYTE-IDENTICAL", 150, 300, T_REP + 1.2, T_KEY - 0.45, t, { color: CYAN, px: 92 });
    stat("5", "MISMATCHES: THE CHEATING WORKER CAUGHT", 150, 470, T_REP + 3.8, T_KEY - 0.45, t, { color: RED, px: 92 });
    label("ITS 9 ALTERED OUTPUTS DISCARDED, 13 UNITS RUN AGAIN", 152, 568, T_REP + 4.4, T_KEY - 0.45, t, { px: 17 });
  }

  // ------------------------------------------------ keys behind the signatures
  function drawKey(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, T_KEY + 0.3, T_KEY + 0.7));
    const cx = 1380;
    const cy = 470;
    // the bond: value at the key's derived addresses, as rings of coins
    for (let r = 0; r < 3; r++) {
      const kr = easeInOutCubic(seg(t, T_KEY + 1.0 + r * 0.35, T_KEY + 1.6 + r * 0.35));
      if (kr <= 0) continue;
      const R = 150 + r * 70;
      const n = 10 + r * 6;
      for (let i = 0; i < n; i++) {
        const a = (i / n) * Math.PI * 2 + t * 0.15 * (r % 2 ? -1 : 1);
        const x = cx + Math.cos(a) * R * kr;
        const y = cy + Math.sin(a) * R * kr * 0.62;
        sx.save();
        sx.globalAlpha = alpha * kr * 0.9;
        sx.beginPath();
        sx.arc(x, y, 9, 0, Math.PI * 2);
        sx.fillStyle = AMBER;
        sx.fill();
        sx.restore();
      }
    }
    dotGlow(cx, cy, 200 * k, AMBER, 0.14 * alpha);
    keyIcon(cx, cy, 130 * k, AMBER, alpha);
    label("NODE KEY", cx, cy + 120, T_KEY + 0.5, T_END - 0.45, t, { px: 20, align: "center", color: AMBER });
    label("VALUE AT ITS ADDRESSES ON SEVERAL CHAINS", cx, cy + 330, T_KEY + 2.0, T_END - 0.45, t, { px: 18, align: "center" });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_REC, 0.8], [T_PUB, 1], [T_REP, 1], [T_KEY, 1], [T_END + 0.2, 1]]);
  const flash = (t) => 0.08 * Math.exp(-Math.pow((t - (T_PUB + 1.6)) / 0.05, 2));
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const dim = t < T_REC ? 1 : 0.35;
    renderEarth(limbCamera(t, 20, 30), t, 0.6 * dim * seg(t, 0, 1.2) * (1 - out), 0.2 * (1 - seg(t, 1.5, 3)), 60, 16);
    sx.drawImage(glc, 0, 0);
    if (t < T_REC + 0.1) drawQuestion(t, 1 - seg(t, T_REC - 0.45, T_REC - 0.05));
    if (t > T_REC - 0.05 && t < T_PUB + 0.1) drawRecords(t, seg(t, T_REC, T_REC + 0.3) * (1 - seg(t, T_PUB - 0.4, T_PUB)));
    if (t > T_PUB - 0.05 && t < T_REP + 0.1) drawPublish(t, seg(t, T_PUB, T_PUB + 0.3) * (1 - seg(t, T_REP - 0.4, T_REP)));
    if (t > T_REP - 0.05 && t < T_KEY + 0.1) drawReplicas(t, seg(t, T_REP, T_REP + 0.3) * (1 - seg(t, T_KEY - 0.4, T_KEY)));
    if (t > T_KEY - 0.05 && t < T_END + 0.1) drawKey(t, seg(t, T_KEY, T_KEY + 0.3) * (1 - seg(t, T_END - 0.4, T_END)));
    scrim((1 - out) * 0.9);

    // -------------------------------------------------------- the type
    if (t < T_REC + 0.1) {
      eyebrow("WHITEPAPER", 2, 0.5, T_REC - 0.45, t);
      block([["Where did this", INK], ["orbit come from?", AMBER]], "TRUST NEEDS RECORDS ANYONE CAN CHECK", 0.6, T_REC - 0.4, t, 88);
    }
    if (t > T_REC - 0.05 && t < T_PUB + 0.1) {
      block([["Every input", INK], ["keeps its source.", AMBER]], "PROVIDER, EDITION AND THE SHA-256 OF ITS BYTES", T_REC + 0.1, T_PUB - 0.4, t, 84);
    }
    if (t > T_PUB - 0.05 && t < T_REP + 0.1) {
      block([["Published whole.", INK], ["Re-run anywhere.", AMBER]], "SIGNED, CONTENT-ADDRESSED; EVERY NUMBER FROM A RUN MANIFEST", T_PUB + 0.1, T_REP - 0.4, t, 84);
    }
    if (t > T_REP - 0.05 && t < T_KEY + 0.1) {
      block([["Screening that", INK], ["checks itself.", AMBER]], "WORK UNITS COMPARED BYTE FOR BYTE ACROSS RUNTIMES", T_REP + 0.1, T_KEY - 0.4, t, 84);
    }
    if (t > T_KEY - 0.05 && t < T_END + 0.1) {
      block([["A signature is only", INK], ["as good as its key.", AMBER]], "A PUBLIC SECURITY BOND SHOWS THE KEY IS INTACT", T_KEY + 0.1, T_END - 0.4, t, 80);
    }
    paperLockup({ eyebrow: "WHITEPAPER · EDITION 1.0", title: ["Provenance and Security", "in Space Data Network"], byline: "Anthony “TJ” Koury III and Claude Opus 5.5", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
