// Edgesource FlatBuffers reel for digitalarsenal.github.io/flatbuffers, in
// plain words. 34 seconds: data ready the moment it arrives, reading in place
// instead of unpacking, locking just the fields that matter, and every
// language including the browser.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack, rng,
  limbCamera, createStudio,
} from "../kit.js";

export const meta = { name: "flatbuffers-reel", media: "docs/media", poster: 480, audio: { track: "orbital-insertion", start: 84 } };

const DURATION = 34;
const FPS = 30;
const T_SHIFT = 6.5; // unpack, then read → read it in place
const M_SHIFT = T_SHIFT + 3.0;
const T_LOCK = 13.5; // lock just the fields that matter
const T_LANG = 20.5; // every language, even the browser
const T_END = 29.5;

const FIELDS = [["OBJECT", "ISS (ZARYA)", false], ["EPOCH", "2026-10-04 12:00", false], ["POSITION", "6,771 km · 51.6°", true], ["UNCERTAINTY", "± 25 m", true]];
const LANGS = ["C++", "C#", "Go", "Java", "JavaScript", "TypeScript", "Python", "Rust", "Swift", "Kotlin", "Dart", "PHP"];
const HEXSEED = 17;

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "EDGESOURCE FLATBUFFERS", hudUrl: "DIGITALARSENAL.GITHUB.IO/FLATBUFFERS" });
  const { glc, sx, font, maskText, decodeText, GLYPHS, dotGlow, scrim, pill, badge, padlock, keyIcon, bezierPts, strokeOn, renderEarth, eyebrow, lockup, block, shiftBlock, beginSubframe, bloom, hud } = studio;

  // A row of bytes: the record as it arrives.
  const BYTES = (() => {
    const R = rng(HEXSEED);
    return Array.from({ length: 48 }, () => Math.floor(R() * 256).toString(16).padStart(2, "0").toUpperCase());
  })();
  function bytes(x, y, alpha, hi = -1, hiLen = 0, cols = 16) {
    if (alpha <= 0) return;
    sx.save();
    sx.globalAlpha = alpha;
    sx.font = font(500, 24, MONO);
    sx.letterSpacing = "1px";
    sx.textAlign = "left";
    BYTES.forEach((b, i) => {
      const c = i % cols;
      const r = Math.floor(i / cols);
      const lit = i >= hi && i < hi + hiLen;
      if (lit) {
        sx.fillStyle = "rgba(245,165,36,0.18)";
        sx.fillRect(x + c * 48 - 6, y + r * 44 - 26, 44, 36);
      }
      sx.fillStyle = lit ? AMBER : "rgba(245,245,247,0.55)";
      sx.fillText(b, x + c * 48, y + r * 44);
    });
    sx.restore();
  }

  // ------------------------------------------- data arrives, ready to read
  function drawArrive(t, alpha) {
    if (alpha <= 0) return;
    const k = seg(t, 0.4, 2.2);
    const n = Math.floor(BYTES.length * easeOutExpo(k));
    sx.save();
    sx.globalAlpha = alpha;
    sx.font = font(500, 24, MONO);
    sx.letterSpacing = "1px";
    BYTES.slice(0, n).forEach((b, i) => {
      const c = i % 16;
      const r = Math.floor(i / 16);
      sx.fillStyle = "rgba(245,245,247,0.6)";
      sx.fillText(b, 1030 + c * 48, 230 + r * 44);
    });
    sx.restore();
    // a reader lands straight on a field, again and again
    const ph = ((t - 2.4) / 1.1) % 1;
    if (t > 2.4) {
      const slots = [[3, 4], [21, 6], [37, 5]];
      const [hi, len] = slots[Math.floor((t - 2.4) / 1.1) % slots.length];
      bytes(1030, 230, alpha, hi, len);
      const c = hi % 16;
      const r = Math.floor(hi / 16);
      const x = 1030 + c * 48 + (len * 48) / 2 - 24;
      const y = 230 + r * 44 - 70;
      sx.save();
      sx.globalAlpha = alpha * (1 - ph * 0.5);
      sx.strokeStyle = AMBER;
      sx.lineWidth = 2;
      sx.beginPath();
      sx.moveTo(x, y - 36);
      sx.lineTo(x, y + 18);
      sx.moveTo(x - 9, y + 8);
      sx.lineTo(x, y + 20);
      sx.lineTo(x + 9, y + 8);
      sx.stroke();
      sx.restore();
      dotGlow(x, y + 46, 40, AMBER, 0.25 * alpha);
    }
  }

  // --------------------------------- unpack, then read → read it in place
  function drawShift(t, alpha) {
    if (alpha <= 0) return;
    const m = easeInOutExpo(seg(t, M_SHIFT + 0.2, M_SHIFT + 0.8));
    const k = seg(t, T_SHIFT + 0.2, T_SHIFT + 0.6);
    bytes(1030, 200, alpha * k * (m > 0 ? 1 : 0.8), m > 0.5 ? 21 : -1, m > 0.5 ? 6 : 0);
    const old = alpha * (1 - m) * k;
    if (old > 0) {
      // the parse step: everything copied into objects before a single read
      const prog = clamp(0.05 + 0.05 * Math.max(0, FRAME_T - T_SHIFT - 0.6));
      sx.save();
      sx.globalAlpha = old;
      sx.strokeStyle = "rgba(245,245,247,0.4)";
      sx.setLineDash([5, 7]);
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.moveTo(1400, 340);
      sx.lineTo(1400, 410);
      sx.stroke();
      sx.setLineDash([]);
      sx.beginPath();
      sx.roundRect(1250, 420, 300, 150, 16);
      sx.fillStyle = "rgba(24,24,27,0.92)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.45)";
      sx.stroke();
      sx.font = font(600, 22, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "center";
      sx.fillText("UNPACK EVERYTHING", 1400, 474);
      sx.fillStyle = "rgba(245,245,247,0.15)";
      sx.fillRect(1290, 502, 220, 12);
      sx.fillStyle = "rgba(245,245,247,0.7)";
      sx.fillRect(1290, 502, 220 * prog, 12);
      sx.font = font(500, 22, MONO);
      sx.fillStyle = MUTED;
      sx.fillText(`${Math.floor(prog * 100)}%`, 1400, 548);
      sx.restore();
    }
    if (m > 0) {
      // one field, read directly where it sits
      const fk = easeOutBack(seg(t, M_SHIFT + 0.6, M_SHIFT + 1.0));
      const x = 1030 + 5 * 48 + 120;
      sx.save();
      sx.globalAlpha = alpha * clamp(fk * 2);
      sx.strokeStyle = AMBER;
      sx.lineWidth = 2;
      sx.beginPath();
      sx.moveTo(x, 290);
      sx.lineTo(x, 380);
      sx.stroke();
      pill(x - 170, 390, 340, 64, AMBER, "rgba(0,0,0,0.7)");
      sx.font = font(600, 26, SANS);
      sx.letterSpacing = "1px";
      sx.fillStyle = INK;
      sx.textAlign = "center";
      sx.fillText("POSITION · 6,771 km", x, 431);
      sx.restore();
      const ck = seg(t, M_SHIFT + 1.1, M_SHIFT + 1.3);
      if (ck > 0) badge("check", x + 200, 422, 40 * easeOutBack(ck), AMBER, alpha);
    }
  }

  // ------------------------------------------- lock just the fields that matter
  function drawLock(t, alpha) {
    if (alpha <= 0) return;
    const px = 1080;
    const py = 160;
    const pw = 700;
    const k = easeOutExpo(seg(t, T_LOCK + 0.2, T_LOCK + 0.6));
    const lockK = easeInExpo(seg(t, T_LOCK + 1.2, T_LOCK + 1.5));
    const openK = seg(t, T_LOCK + 3.6, T_LOCK + 4.0);
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.beginPath();
    sx.roundRect(px, py, pw, 420, 18);
    sx.fillStyle = "rgba(22,22,24,0.92)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.28)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 22, SANS);
    sx.letterSpacing = "3.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("ORBIT RECORD", px + 32, py + 52);
    const R = rng(900 + Math.round(FRAME_T * 30));
    FIELDS.forEach(([name, value, secret], i) => {
      const y = py + 120 + i * 76;
      sx.font = font(600, 18, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.fillText(name, px + 32, y);
      let v = value;
      if (secret && lockK > 0 && openK < 1) {
        v = "";
        for (let c = 0; c < value.length; c++) v += value[c] === " " ? " " : c / value.length < lockK * 1.2 && c / value.length >= openK * 1.2 ? GLYPHS[Math.floor(R() * GLYPHS.length)] : value[c];
      }
      sx.font = font(500, 30, secret && lockK > 0 && openK < 1 ? MONO : SANS);
      sx.letterSpacing = "0px";
      sx.fillStyle = secret && lockK > 0 ? (openK >= 1 ? AMBER : "rgba(89,217,255,0.8)") : INK;
      sx.fillText(v, px + 260, y + 2);
    });
    sx.restore();
    FIELDS.forEach(([, , secret], i) => {
      if (!secret) return;
      const y = py + 120 + i * 76 - 10;
      padlock(px + pw - 60, y, 40 * easeOutBack(seg(t, T_LOCK + 0.9, T_LOCK + 1.2)), openK, openK > 0 ? AMBER : INK, alpha);
    });
    // the key holder opens them
    const kk = easeInOutCubic(seg(t, T_LOCK + 2.6, T_LOCK + 3.5));
    const ka = alpha * seg(t, T_LOCK + 2.5, T_LOCK + 2.7) * (1 - seg(t, T_LOCK + 3.5, T_LOCK + 3.8));
    keyIcon(lerp(px + pw + 140, px + pw - 110, kk), py + 274, 56, AMBER, ka);
    maskText(sx, openK > 0 ? "KEY HOLDER · OPENED" : "EVERYONE ELSE · LOCKED", px + pw / 2, py + 470, 22, 600, openK > 0 ? AMBER : MUTED, T_LOCK + 1.6, T_LANG - 0.45, t, { tracking: 3, align: "center" });
  }

  // ------------------------------------------- every language, even the browser
  function drawLangs(t, alpha) {
    if (alpha <= 0) return;
    const cx = 1420;
    const cy = 380;
    const k = easeOutBack(seg(t, T_LANG + 0.2, T_LANG + 0.6));
    sx.save();
    sx.globalAlpha = alpha * clamp(k * 2);
    pill(cx - 150, cy - 34, 300, 68, AMBER, "rgba(0,0,0,0.75)");
    sx.font = font(600, 26, SANS);
    sx.letterSpacing = "3px";
    sx.fillStyle = AMBER;
    sx.textAlign = "center";
    sx.fillText("ONE SCHEMA", cx, cy + 9);
    sx.restore();
    LANGS.forEach((name, i) => {
      const a = -Math.PI / 2 + (i / LANGS.length) * Math.PI * 2;
      const lx = cx + Math.cos(a) * 380;
      const ly = cy + Math.sin(a) * 250;
      const t0 = T_LANG + 0.6 + i * 0.12;
      const pk = easeOutBack(seg(t, t0, t0 + 0.35));
      if (pk <= 0) return;
      strokeOn([[cx + Math.cos(a) * 160, cy + Math.sin(a) * 40], [lx, ly]], seg(t, t0, t0 + 0.3), "rgba(245,165,36,0.5)", 1.5, alpha, 0.3);
      sx.save();
      sx.font = font(600, 21, SANS);
      sx.letterSpacing = "1px";
      const w = sx.measureText(name).width + 36;
      sx.globalAlpha = alpha * clamp(pk * 2);
      sx.translate(lx, ly);
      sx.scale(pk, pk);
      pill(-w / 2, -22, w, 44, name === "JavaScript" ? AMBER : "rgba(245,245,247,0.5)", "rgba(0,0,0,0.75)");
      sx.fillStyle = name === "JavaScript" ? AMBER : INK;
      sx.textAlign = "center";
      sx.fillText(name, 0, 8);
      sx.restore();
    });
    maskText(sx, "THE COMPILER ITSELF RUNS IN THE BROWSER", cx, 720, 22, 600, MUTED, T_LANG + 2.4, T_END - 0.45, t, { tracking: 3, align: "center" });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_SHIFT, 1], [M_SHIFT + 0.3, 0.6], [T_LOCK, 1], [T_LANG, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;
  const SHIFT = {
    before: ["Unpack, then read."],
    after: ["Read it in place."],
    why: "MOST FORMATS MUST BE UNPACKED BEFORE YOU CAN USE THEM",
    fix: "FLATBUFFERS READS ANY FIELD STRAIGHT FROM THE BYTES",
    next: "WITH FLATBUFFERS",
    tIn: T_SHIFT + 0.1,
    tMorph: M_SHIFT,
    tOut: T_LOCK - 0.4,
  };

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    renderEarth(limbCamera(t, 140, 25), t, 0.55 * seg(t, 0, 1.2) * (1 - out), 0.2, 40, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_SHIFT + 0.2) drawArrive(t, 1 - seg(t, T_SHIFT - 0.4, T_SHIFT));
    if (t > T_SHIFT - 0.05 && t < T_LOCK + 0.1) drawShift(t, seg(t, T_SHIFT, T_SHIFT + 0.3) * (1 - seg(t, T_LOCK - 0.4, T_LOCK)));
    if (t > T_LOCK - 0.05 && t < T_LANG + 0.1) drawLock(t, seg(t, T_LOCK, T_LOCK + 0.3) * (1 - seg(t, T_LANG - 0.4, T_LANG)));
    if (t > T_LANG - 0.05 && t < T_END + 0.1) drawLangs(t, seg(t, T_LANG, T_LANG + 0.3) * (1 - seg(t, T_END - 0.3, T_END)));
    scrim(1 - out);

    if (t < T_SHIFT + 0.1) {
      eyebrow("EDGESOURCE FLATBUFFERS", 2, 0.4, T_SHIFT - 0.45, t);
      block([["Data that's ready", INK], ["the moment it arrives.", AMBER]], "NO UNPACKING STEP BEFORE YOU CAN READ IT", 0.5, T_SHIFT - 0.4, t, 84);
    }
    if (t > SHIFT.tIn - 0.05 && t < SHIFT.tOut + 0.5) shiftBlock(SHIFT, t);
    if (t > T_LOCK - 0.05 && t < T_LANG + 0.1) {
      block([["Lock just the", INK], ["fields that matter.", AMBER]], "SHARE THE RECORD; ONLY THE KEY HOLDER READS THE SECRETS", T_LOCK + 0.1, T_LANG - 0.4, t, 88);
    }
    if (t > T_LANG - 0.05 && t < T_END + 0.1) {
      block([["Every language.", INK], ["Even the browser.", AMBER]], "ONE SCHEMA, CODE GENERATED FOR EACH", T_LANG + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "Edgesource FlatBuffers", subtitle: "Fast data with field-level encryption", url: "DIGITALARSENAL.GITHUB.IO/FLATBUFFERS", t0: T_END + 0.3 }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
