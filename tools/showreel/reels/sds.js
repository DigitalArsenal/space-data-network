// Space Data Standards reel for spacedatastandards.org, in plain words. 34
// seconds: one shared language for space data, a format per program against
// one standard everyone reads, one record read from thirteen programming
// languages, and 255 standards (dist/manifest.json, 1.232.0).

import {
  W, H, AMBER, CYAN, INK, MUTED, SANS, MONO, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack, rng,
  limbCamera, createStudio,
} from "../kit.js";

export const meta = { name: "sds-reel", media: "media", poster: 450, audio: { track: "covariance", start: 73 } };

const DURATION = 34;
const FPS = 30;
const T_SHIFT = 6.5; // every program its own format → one standard everyone reads
const M_SHIFT = T_SHIFT + 3.0;
const T_LANG = 13.5; // write once, read in thirteen languages
const T_COUNT = 20.5; // 255 standards
const T_END = 29.5;

const KINDS = [
  ["ORBIT", "OMM"], ["CLOSE APPROACH", "CDM"], ["TRACKING", "TDM"], ["CATALOG ENTRY", "CAT"], ["EPHEMERIS", "OEM"],
  ["SPACE WEATHER", "SPW"], ["LAUNCH", "LCC"], ["SENSOR", "SEN"], ["MANEUVER", "MNV"],
];
const LANGS = ["JavaScript", "TypeScript", "Python", "Go", "Rust", "Java", "Kotlin", "C#", "Dart", "Swift", "PHP", "C++", "Lobster"];
const FIELDS = [["OBJECT", "ISS (ZARYA)"], ["EPOCH", "2026-10-04 12:00 UTC"], ["MEAN MOTION", "15.50 rev/day"], ["SOURCE", "Digitally signed"]];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "SPACE DATA STANDARDS", hudUrl: "SPACEDATASTANDARDS.ORG" });
  const { glc, sx, font, maskText, decodeText, dotGlow, scrim, pill, badge, bezierPts, strokeOn, renderEarth, eyebrow, lockup, block, shiftBlock, beginSubframe, bloom, hud } = studio;

  // A structured record card: a kind, its code, and the same four fields.
  function card(x, y, w, alpha, kind, code, rows = 3, hot = false) {
    if (alpha <= 0) return;
    const h = 64 + rows * 30;
    sx.save();
    sx.globalAlpha = alpha;
    sx.beginPath();
    sx.roundRect(x - w / 2, y - h / 2, w, h, 12);
    sx.fillStyle = "rgba(22,22,24,0.92)";
    sx.fill();
    sx.strokeStyle = hot ? "rgba(245,165,36,0.75)" : "rgba(245,245,247,0.3)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 19, SANS);
    sx.letterSpacing = "2px";
    sx.fillStyle = hot ? AMBER : INK;
    sx.textAlign = "left";
    sx.fillText(kind, x - w / 2 + 20, y - h / 2 + 36);
    sx.font = font(500, 16, MONO);
    sx.letterSpacing = "0px";
    sx.fillStyle = MUTED;
    sx.textAlign = "right";
    sx.fillText(code, x + w / 2 - 18, y - h / 2 + 36);
    for (let r = 0; r < rows; r++) {
      sx.fillStyle = "rgba(245,245,247,0.32)";
      sx.fillRect(x - w / 2 + 22, y - h / 2 + 62 + r * 30, 70, 6);
      sx.fillStyle = hot ? "rgba(245,165,36,0.55)" : "rgba(245,245,247,0.55)";
      sx.fillRect(x - w / 2 + 110, y - h / 2 + 62 + r * 30, (w - 150) * (0.5 + 0.4 * ((r * 7 + code.length) % 5) / 5), 6);
    }
    sx.restore();
  }

  // ------------------------------------------- the kinds of space data
  function drawKinds(t, alpha) {
    if (alpha <= 0) return;
    KINDS.forEach(([kind, code], i) => {
      const c = i % 3;
      const r = Math.floor(i / 3);
      const k = easeOutBack(seg(t, 0.5 + i * 0.12, 0.95 + i * 0.12));
      const drift = 6 * Math.sin(t * 1.3 + i);
      card(1080 + c * 320, 210 + r * 175 + drift, 300, alpha * clamp(k * 2), kind, code, 2, i === 0);
    });
  }

  // ------------------- every program its own format → one standard everyone reads
  const MESSY = [
    ["TEXT LINES", ["1 25544U 98067A   26277.50", "2 25544  51.6400 208.9163"]],
    ["SPREADSHEET", ["name,epoch,n,ecc,inc", "ISS,2026-10-04,15.50,..."]],
    ["MARKUP", ["<orbit obj=\"ISS\">", "  <n>15.50</n> ..."]],
  ];
  function drawShift(t, alpha) {
    if (alpha <= 0) return;
    const m = easeInOutExpo(seg(t, M_SHIFT + 0.2, M_SHIFT + 0.8));
    MESSY.forEach(([label, lines], i) => {
      const k = easeOutBack(seg(t, T_SHIFT + 0.15 + i * 0.12, T_SHIFT + 0.55 + i * 0.12));
      const x = 1150 + i * 300;
      const y = 330 + (i % 2) * 60;
      const rot = (1 - m) * (i - 1) * 0.06;
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2);
      sx.translate(x, y);
      sx.rotate(rot);
      if (m < 1) {
        sx.globalAlpha = alpha * clamp(k * 2) * (1 - m);
        sx.beginPath();
        sx.roundRect(-135, -80, 270, 160, 12);
        sx.fillStyle = "rgba(22,22,24,0.92)";
        sx.fill();
        sx.strokeStyle = "rgba(245,245,247,0.3)";
        sx.lineWidth = 1.5;
        sx.stroke();
        sx.font = font(600, 18, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillStyle = MUTED;
        sx.textAlign = "left";
        sx.fillText(label, -115, -44);
        sx.font = font(500, 15, MONO);
        sx.letterSpacing = "0px";
        sx.fillStyle = "rgba(245,245,247,0.7)";
        lines.forEach((ln, j) => sx.fillText(ln.slice(0, 26), -115, -8 + j * 28));
      }
      sx.restore();
      if (m > 0) card(x, 360, 270, alpha * m, "ORBIT", "OMM", 3, true);
      const ck = seg(t, M_SHIFT + 0.9 + i * 0.1, M_SHIFT + 1.1 + i * 0.1);
      if (ck > 0) badge("check", x + 120, 270, 34 * easeOutBack(ck), AMBER, alpha);
    });
    // the same fields, named the same way, in every copy
    const fk = seg(t, M_SHIFT + 1.3, M_SHIFT + 1.7);
    if (fk > 0) {
      sx.save();
      sx.globalAlpha = alpha * fk;
      sx.font = font(600, 22, SANS);
      sx.letterSpacing = "3px";
      sx.fillStyle = AMBER;
      sx.textAlign = "center";
      sx.fillText("SAME FIELDS · SAME NAMES · SAME UNITS", 1450, 540);
      sx.restore();
    }
  }

  // -------------------------------------- write once, read in thirteen languages
  function drawLangs(t, alpha) {
    if (alpha <= 0) return;
    const cx = 1400;
    const cy = 400;
    const k = easeOutBack(seg(t, T_LANG + 0.2, T_LANG + 0.6));
    // the record, its fields filled in
    sx.save();
    sx.globalAlpha = alpha * clamp(k * 2);
    sx.beginPath();
    sx.roundRect(cx - 190, cy - 110, 380, 220, 16);
    sx.fillStyle = "rgba(22,22,24,0.94)";
    sx.fill();
    sx.strokeStyle = "rgba(245,165,36,0.75)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 20, SANS);
    sx.letterSpacing = "2.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("ORBIT RECORD", cx - 166, cy - 72);
    FIELDS.forEach(([kk, v], i) => {
      sx.font = font(600, 14, SANS);
      sx.letterSpacing = "2px";
      sx.fillStyle = MUTED;
      sx.fillText(kk, cx - 166, cy - 28 + i * 36);
      sx.font = font(500, 19, SANS);
      sx.letterSpacing = "0px";
      sx.fillStyle = INK;
      sx.fillText(v, cx - 20, cy - 28 + i * 36);
    });
    sx.restore();
    // the languages around it, each wired in
    LANGS.forEach((name, i) => {
      const a = -Math.PI / 2 + (i / LANGS.length) * Math.PI * 2;
      const lx = cx + Math.cos(a) * 390;
      const ly = cy + Math.sin(a) * 270;
      const t0 = T_LANG + 0.7 + i * 0.13;
      const pk = easeOutBack(seg(t, t0, t0 + 0.35));
      if (pk <= 0) return;
      const from = [cx + Math.cos(a) * 200, cy + Math.sin(a) * 120];
      strokeOn([from, [lx, ly]], seg(t, t0, t0 + 0.3), "rgba(245,165,36,0.5)", 1.5, alpha, 0.3);
      sx.save();
      sx.font = font(600, 20, SANS);
      sx.letterSpacing = "1px";
      const w = sx.measureText(name).width + 36;
      sx.globalAlpha = alpha * clamp(pk * 2);
      sx.translate(lx, ly);
      sx.scale(pk, pk);
      pill(-w / 2, -21, w, 42, "rgba(245,245,247,0.5)", "rgba(0,0,0,0.75)");
      sx.fillStyle = INK;
      sx.textAlign = "center";
      sx.fillText(name, 0, 7);
      sx.restore();
    });
  }

  // ------------------------------------------------------- 255 standards
  function drawCount(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutExpo(seg(FRAME_T, T_COUNT + 0.3, T_COUNT + 2.6));
    const n = Math.round(255 * k);
    sx.save();
    sx.globalAlpha = alpha * seg(t, T_COUNT + 0.2, T_COUNT + 0.5);
    sx.font = font(700, 220, SANS);
    sx.letterSpacing = "-8px";
    sx.fillStyle = AMBER;
    sx.textAlign = "center";
    sx.fillText(String(n), 1420, 400);
    sx.font = font(600, 30, SANS);
    sx.letterSpacing = "6px";
    sx.fillStyle = INK;
    sx.fillText("STANDARDS", 1420, 470);
    sx.restore();
    const rows = [["ORBITS", "CLOSE APPROACHES", "TRACKING", "CATALOGS"], ["SPACE WEATHER", "LAUNCHES", "SENSORS", "GROUND STATIONS"]];
    rows.forEach((row, r) => {
      sx.save();
      sx.font = font(600, 18, SANS);
      sx.letterSpacing = "2.5px";
      const ws = row.map((tg) => sx.measureText(tg).width + 36);
      sx.restore();
      const total = ws.reduce((a, b) => a + b, 0) + 14 * (row.length - 1);
      let x = 1420 - total / 2;
      row.forEach((tg, j) => {
        const i = r * 4 + j;
        const pk = easeOutBack(seg(t, T_COUNT + 1.2 + i * 0.12, T_COUNT + 1.5 + i * 0.12));
        const w = ws[j];
        const cx = x + w / 2;
        x += w + 14;
        if (pk <= 0) return;
        const y = 560 + r * 64;
        sx.save();
        sx.font = font(600, 18, SANS);
        sx.letterSpacing = "2.5px";
        sx.globalAlpha = alpha * clamp(pk * 2);
        pill(cx - w / 2, y - 21, w, 42, "rgba(245,245,247,0.4)", "rgba(0,0,0,0.6)");
        sx.fillStyle = INK;
        sx.textAlign = "center";
        sx.fillText(tg, cx, y + 7);
        sx.restore();
      });
    });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_SHIFT, 1], [M_SHIFT + 0.3, 0.6], [T_LANG, 1], [T_COUNT, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;
  const SHIFT = {
    before: ["A format per program."],
    after: ["One standard for all."],
    why: "EACH SOURCE INVENTS ITS OWN FILE LAYOUT",
    fix: "ONE OPEN DEFINITION PER KIND OF RECORD",
    next: "WITH THE STANDARDS",
    tIn: T_SHIFT + 0.1,
    tMorph: M_SHIFT,
    tOut: T_LANG - 0.4,
  };

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    renderEarth(limbCamera(t, 60, 20), t, 0.55 * seg(t, 0, 1.2) * (1 - out), 0.2, 40, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_SHIFT + 0.2) drawKinds(t, 1 - seg(t, T_SHIFT - 0.4, T_SHIFT));
    if (t > T_SHIFT - 0.05 && t < T_LANG + 0.1) drawShift(t, seg(t, T_SHIFT, T_SHIFT + 0.3) * (1 - seg(t, T_LANG - 0.4, T_LANG)));
    if (t > T_LANG - 0.05 && t < T_COUNT + 0.1) drawLangs(t, seg(t, T_LANG, T_LANG + 0.3) * (1 - seg(t, T_COUNT - 0.4, T_COUNT)));
    if (t > T_COUNT - 0.05 && t < T_END + 0.1) drawCount(t, seg(t, T_COUNT, T_COUNT + 0.3) * (1 - seg(t, T_END - 0.3, T_END)));
    scrim(1 - out);

    if (t < T_SHIFT + 0.1) {
      eyebrow("SPACE DATA STANDARDS", 2, 0.4, T_SHIFT - 0.45, t);
      block([["One language", INK], ["for space data.", AMBER]], "OPEN AND FREE, FOR ANY SOFTWARE", 0.5, T_SHIFT - 0.4, t, 88);
    }
    if (t > SHIFT.tIn - 0.05 && t < SHIFT.tOut + 0.5) shiftBlock(SHIFT, t);
    if (t > T_LANG - 0.05 && t < T_COUNT + 0.1) {
      block([["Write it once.", INK], ["Read it in 13 languages.", AMBER]], "CODE FOR EVERY MAJOR LANGUAGE, GENERATED FOR YOU", T_LANG + 0.1, T_COUNT - 0.4, t, 84);
    }
    if (t > T_COUNT - 0.05 && t < T_END + 0.1) {
      block([["Every kind of", INK], ["space data.", AMBER]], "VERSIONED, FREE AND OPEN SOURCE", T_COUNT + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "Space Data Standards", subtitle: "The open language for space data", url: "SPACEDATASTANDARDS.ORG", t0: T_END + 0.3 }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
