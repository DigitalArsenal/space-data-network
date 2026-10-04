// FlatSQL reel for digitalarsenal.github.io/flatsql, in plain words. 34
// seconds: ask questions of the data directly, no copy-and-convert step,
// records searchable the moment they arrive, one question and every match.

import {
  W, H, AMBER, CYAN, INK, MUTED, SANS, MONO, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack, rng,
  limbCamera, createStudio,
} from "../kit.js";

export const meta = { name: "flatsql-reel", media: "docs/media", poster: 540, audio: { track: "covariance", start: 128 } };

const DURATION = 34;
const FPS = 30;
const T_COPY = 6.5; // copy, convert, then ask → just ask
const M_COPY = T_COPY + 3.2;
const T_LIVE = 14.0; // searchable the moment it arrives
const T_QUERY = 21.0; // one question, every match
const T_END = 29.5;

const NAMES = ["ISS (ZARYA) · ORBIT", "GOES 16 · WEATHER", "NOAA 20 · ORBIT", "HUBBLE · ORBIT", "SENTINEL-2A · IMAGE", "STARLINK-1007 · ORBIT", "TIANGONG · ORBIT", "LANDSAT 9 · IMAGE", "SL-16 R/B · TRACKING", "AQUA · WEATHER"];
const RESULTS = [["ISS (ZARYA)", "418 km"], ["TIANGONG", "385 km"], ["HUBBLE", "515 km"], ["STARLINK-1007", "550 km"]];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "EDGESOURCE FLATSQL", hudUrl: "DIGITALARSENAL.GITHUB.IO/FLATSQL" });
  const { glc, sx, font, maskText, decodeText, dotGlow, scrim, pill, badge, renderEarth, eyebrow, lockup, block, shiftBlock, beginSubframe, bloom, hud } = studio;

  // A record: a small card with its name and three data bars.
  function record(x, y, w, alpha, label, hot = 0) {
    if (alpha <= 0) return;
    const h = w * 0.36;
    sx.save();
    sx.globalAlpha = alpha;
    sx.beginPath();
    sx.roundRect(x - w / 2, y - h / 2, w, h, 8);
    sx.fillStyle = "rgba(24,24,27,0.92)";
    sx.fill();
    sx.strokeStyle = hot > 0 ? `rgba(245,165,36,${0.35 + 0.6 * hot})` : "rgba(245,245,247,0.35)";
    sx.lineWidth = 1.5;
    sx.stroke();
    if (label && w > 150) {
      sx.font = font(600, Math.round(w * 0.075), SANS);
      sx.letterSpacing = "1.5px";
      sx.fillStyle = hot > 0.5 ? AMBER : INK;
      sx.textAlign = "left";
      sx.fillText(label, x - w / 2 + 14, y - h / 2 + w * 0.1);
    }
    sx.fillStyle = "rgba(245,245,247,0.45)";
    [0.62, 0.44, 0.7].forEach((f, i) => sx.fillRect(x - w / 2 + 14, y - h / 2 + h * (label && w > 150 ? 0.45 : 0.25) + i * h * 0.17, (w - 28) * f, Math.max(3, h * 0.07)));
    sx.restore();
  }

  // ------------------------------------------------ records stream past
  function drawStream(t, alpha) {
    if (alpha <= 0) return;
    for (let lane = 0; lane < 3; lane++) {
      for (let k = 0; k < 6; k++) {
        const speed = 120 + lane * 35;
        const x = W + 200 - ((t * speed + k * 360 + lane * 120) % 2160);
        const y = 190 + lane * 150;
        const fadeIn = seg(t, 0.2 + lane * 0.15, 0.8 + lane * 0.15);
        record(x, y, 270, alpha * fadeIn * clamp((x - 900) / 200), NAMES[(k + lane * 3) % NAMES.length]);
      }
    }
  }

  // ------------------------------- copy, convert, then ask → just ask
  function drawCopy(t, alpha) {
    if (alpha <= 0) return;
    const m = easeInOutExpo(seg(t, M_COPY + 0.2, M_COPY + 0.8));
    const sx0 = 1020;
    const cy = 400;
    // the records
    for (let i = 0; i < 4; i++) {
      const k = easeOutBack(seg(t, T_COPY + 0.15 + i * 0.08, T_COPY + 0.5 + i * 0.08));
      record(sx0, cy - 150 + i * 100, 290, alpha * clamp(k * 2), i === 0 ? "RECORDS" : null, m * seg(t, M_COPY + 1.2 + i * 0.1, M_COPY + 1.4 + i * 0.1));
    }
    // the copy-and-convert path, fading out when the words turn
    const old = alpha * (1 - m);
    if (old > 0) {
      const bx = 1420;
      const prog = clamp(0.06 + 0.04 * Math.max(0, FRAME_T - T_COPY - 0.6));
      sx.save();
      sx.globalAlpha = old * seg(t, T_COPY + 0.4, T_COPY + 0.8);
      sx.strokeStyle = "rgba(245,245,247,0.45)";
      sx.setLineDash([5, 7]);
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.moveTo(sx0 + 150, cy);
      sx.lineTo(bx - 140, cy);
      sx.moveTo(bx + 140, cy);
      sx.lineTo(1630, cy);
      sx.stroke();
      sx.setLineDash([]);
      sx.beginPath();
      sx.roundRect(bx - 135, cy - 85, 270, 170, 16);
      sx.fillStyle = "rgba(24,24,27,0.92)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.45)";
      sx.stroke();
      sx.font = font(600, 26, SANS);
      sx.letterSpacing = "3px";
      sx.fillStyle = MUTED;
      sx.textAlign = "center";
      sx.fillText("COPY + CONVERT", bx, cy - 22);
      sx.fillStyle = "rgba(245,245,247,0.15)";
      sx.fillRect(bx - 100, cy + 14, 200, 12);
      sx.fillStyle = "rgba(245,245,247,0.7)";
      sx.fillRect(bx - 100, cy + 14, 200 * prog, 12);
      sx.font = font(500, 22, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = MUTED;
      sx.fillText(`${Math.floor(prog * 100)}%`, bx, cy + 60);
      // the database at the end
      const dx = 1720;
      sx.strokeStyle = "rgba(245,245,247,0.55)";
      sx.lineWidth = 2;
      sx.beginPath();
      sx.ellipse(dx, cy - 75, 85, 22, 0, 0, Math.PI * 2);
      sx.moveTo(dx - 85, cy - 75);
      sx.lineTo(dx - 85, cy + 75);
      sx.ellipse(dx, cy + 75, 85, 22, 0, Math.PI, 0, true);
      sx.lineTo(dx + 85, cy - 75);
      sx.stroke();
      sx.font = font(600, 22, SANS);
      sx.letterSpacing = "3px";
      sx.fillStyle = MUTED;
      sx.fillText("DATABASE", dx, cy + 140);
      sx.restore();
    }
    // after: a question goes straight to the records, answers come back
    if (m > 0) {
      const qx = 1600;
      const qk = easeOutBack(seg(t, M_COPY + 0.5, M_COPY + 0.9));
      sx.save();
      sx.globalAlpha = alpha * clamp(qk * 2);
      pill(qx - 120, cy - 30, 240, 60, AMBER, "rgba(0,0,0,0.66)");
      sx.font = font(600, 24, SANS);
      sx.letterSpacing = "3.5px";
      sx.fillStyle = AMBER;
      sx.textAlign = "center";
      sx.fillText("QUESTION", qx, cy + 9);
      sx.restore();
      const fly = seg(t, M_COPY + 0.9, M_COPY + 1.25);
      if (fly > 0 && fly < 1) {
        const x = lerp(qx - 125, sx0 + 150, easeInOutCubic(fly));
        dotGlow(x, cy, 26, AMBER, alpha * 0.8);
        dotGlow(x, cy, 8, "#ffffff", alpha * 0.8);
      }
      const back = seg(t, M_COPY + 1.5, M_COPY + 1.9);
      if (back > 0) {
        sx.save();
        sx.globalAlpha = alpha * back;
        sx.strokeStyle = AMBER;
        sx.lineWidth = 2;
        sx.beginPath();
        sx.moveTo(sx0 + 150, cy);
        sx.lineTo(qx - 125, cy);
        sx.stroke();
        sx.restore();
        badge("check", qx + 150, cy, 40 * easeOutBack(back), AMBER, alpha);
      }
    }
  }

  // ----------------------------------- searchable the moment it arrives
  function drawLive(t, alpha) {
    if (alpha <= 0) return;
    const colX = 1180;
    const baseY = 640;
    const n = Math.floor(clamp((t - T_LIVE - 0.4) / 0.42, 0, 14));
    // the stored stack
    for (let i = 0; i < Math.min(n, 6); i++) record(colX, baseY - i * 62, 300, alpha * 0.9, null, 0);
    // the record falling in now
    const ph = ((t - T_LIVE - 0.4) / 0.42) % 1;
    if (t > T_LIVE + 0.4 && n < 14) {
      const y = lerp(140, baseY - Math.min(n, 6) * 62, easeInCubic(ph));
      record(colX, y, 300, alpha, NAMES[n % NAMES.length], 1);
    }
    // the index beside it, a row per record, lit as it lands
    const ix = 1440;
    sx.save();
    sx.globalAlpha = alpha * seg(t, T_LIVE + 0.2, T_LIVE + 0.6);
    sx.beginPath();
    sx.roundRect(ix, 150, 400, 530, 16);
    sx.fillStyle = "rgba(22,22,24,0.88)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.28)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 24, SANS);
    sx.letterSpacing = "3.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("INDEX", ix + 28, 198);
    for (let i = 0; i < Math.min(n, 9); i++) {
      const name = NAMES[(n - 1 - i + NAMES.length * 3) % NAMES.length];
      const fresh = i === 0 ? 1 - seg(t, T_LIVE + 0.4 + (n - 1) * 0.42 + 0.42, T_LIVE + 0.4 + (n - 1) * 0.42 + 0.9) : 0;
      sx.font = font(600, 20, SANS);
      sx.letterSpacing = "1.5px";
      sx.fillStyle = fresh > 0 ? AMBER : "rgba(245,245,247,0.8)";
      sx.fillText(name, ix + 28, 250 + i * 48);
    }
    sx.restore();
    // a count that never stops
    const count = Math.floor(1204000 + 3100 * Math.max(0, FRAME_T - T_LIVE));
    maskText(sx, "RECORDS, ALL SEARCHABLE", ix, 742, 20, 600, MUTED, T_LIVE + 0.5, T_QUERY - 0.45, t, { tracking: 3.5 });
    sx.save();
    sx.globalAlpha = alpha * seg(t, T_LIVE + 0.5, T_LIVE + 0.9);
    sx.font = font(700, 56, SANS);
    sx.letterSpacing = "-1.5px";
    sx.fillStyle = INK;
    sx.textAlign = "left";
    sx.fillText(count.toLocaleString("en-US"), ix, 808);
    sx.restore();
  }

  // ------------------------------------------- one question, every match
  function drawQuery(t, alpha) {
    if (alpha <= 0) return;
    const px = 1010;
    const py = 130;
    const pw = 800;
    const k = easeOutExpo(seg(t, T_QUERY + 0.2, T_QUERY + 0.6));
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.translate(0, (1 - k) * 30);
    sx.beginPath();
    sx.roundRect(px, py, pw, 590, 18);
    sx.fillStyle = "rgba(22,22,24,0.9)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.28)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 22, SANS);
    sx.letterSpacing = "3.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("THE QUESTION", px + 32, py + 52);
    // typed out, a character at a time
    const q = ["SELECT name, altitude", "FROM orbits", "WHERE altitude < 600 km"];
    const typed = Math.floor(Math.max(0, FRAME_T - T_QUERY - 0.6) * 28);
    let used = 0;
    sx.font = font(500, 30, MONO);
    sx.letterSpacing = "0px";
    q.forEach((line, i) => {
      const shown = line.slice(0, clamp(typed - used, 0, line.length));
      used += line.length;
      sx.fillStyle = INK;
      sx.fillText(shown, px + 32, py + 106 + i * 44);
    });
    sx.strokeStyle = "rgba(245,245,247,0.14)";
    sx.beginPath();
    sx.moveTo(px + 24, py + 262);
    sx.lineTo(px + pw - 24, py + 262);
    sx.stroke();
    sx.restore();
    const r0 = T_QUERY + 0.6 + q.join("").length / 28 + 0.3;
    RESULTS.forEach(([name, alt], i) => {
      const rk = seg(t, r0 + i * 0.1, r0 + 0.25 + i * 0.1);
      if (rk <= 0) return;
      const y = py + 318 + i * 62;
      sx.save();
      sx.globalAlpha = alpha * rk;
      sx.font = font(600, 28, SANS);
      sx.letterSpacing = "0.5px";
      sx.fillStyle = INK;
      sx.textAlign = "left";
      decodeText(sx, name, px + 32, y, r0 + i * 0.1, 0.3, t, 80 + i);
      sx.textAlign = "right";
      sx.fillStyle = AMBER;
      sx.font = font(500, 28, MONO);
      sx.fillText(alt, px + pw - 32, y);
      sx.restore();
    });
  }

  // The FlatSQL symbol: a database whose top is a FlatBuffer, struck by a bolt.
  function icon(cx, cy, R, k) {
    if (k <= 0) return;
    const s = (R / 50) * k;
    sx.save();
    sx.translate(cx, cy);
    sx.scale(s, s);
    sx.strokeStyle = INK;
    sx.lineWidth = 5;
    sx.beginPath();
    sx.ellipse(0, -28, 38, 13, 0, 0, Math.PI * 2);
    sx.moveTo(-38, -28);
    sx.lineTo(-38, 28);
    sx.ellipse(0, 28, 38, 13, 0, Math.PI, 0, true);
    sx.lineTo(38, -28);
    sx.stroke();
    sx.fillStyle = AMBER;
    sx.beginPath();
    sx.moveTo(6, -14);
    sx.lineTo(-12, 10);
    sx.lineTo(4, 10);
    sx.lineTo(-8, 36);
    sx.lineTo(20, 2);
    sx.lineTo(4, 2);
    sx.lineTo(14, -14);
    sx.closePath();
    sx.fill();
    sx.restore();
    dotGlow(cx, cy, R * 0.8, AMBER, 0.15 * k);
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_COPY, 1], [M_COPY + 0.3, 0.6], [T_LIVE, 1], [T_QUERY, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;
  const SHIFT = {
    before: ["Copy. Convert. Ask."],
    after: ["Just ask."],
    why: "MOST DATABASES COPY YOUR DATA INTO THEIR OWN FORMAT FIRST",
    fix: "FLATSQL READS THE RECORDS AS THEY ARE",
    next: "WITH FLATSQL",
    tIn: T_COPY + 0.1,
    tMorph: M_COPY,
    tOut: T_LIVE - 0.4,
  };

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    renderEarth(limbCamera(t), t, 0.55 * seg(t, 0, 1.2) * (1 - out), 0.2, 40, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_COPY + 0.2) drawStream(t, 1 - seg(t, T_COPY - 0.4, T_COPY));
    if (t > T_COPY - 0.05 && t < T_LIVE + 0.1) drawCopy(t, seg(t, T_COPY, T_COPY + 0.3) * (1 - seg(t, T_LIVE - 0.4, T_LIVE)));
    if (t > T_LIVE - 0.05 && t < T_QUERY + 0.1) drawLive(t, seg(t, T_LIVE, T_LIVE + 0.3) * (1 - seg(t, T_QUERY - 0.4, T_QUERY)));
    if (t > T_QUERY - 0.05 && t < T_END + 0.1) drawQuery(t, seg(t, T_QUERY, T_QUERY + 0.3) * (1 - seg(t, T_END - 0.3, T_END)));
    scrim(1 - out);

    if (t < T_COPY + 0.1) {
      eyebrow("FLATSQL", 2, 0.4, T_COPY - 0.45, t);
      block([["Ask questions of", INK], ["your data, directly.", AMBER]], "IN SQL, THE LANGUAGE DATABASES SPEAK", 0.5, T_COPY - 0.4, t, 88);
    }
    if (t > SHIFT.tIn - 0.05 && t < SHIFT.tOut + 0.5) shiftBlock(SHIFT, t);
    if (t > T_LIVE - 0.05 && t < T_QUERY + 0.1) {
      block([["Searchable the moment", INK], ["it arrives.", AMBER]], "INDEXES BUILD AS THE RECORDS STREAM IN", T_LIVE + 0.1, T_QUERY - 0.4, t, 84);
    }
    if (t > T_QUERY - 0.05 && t < T_END + 0.1) {
      block([["One question.", INK], ["Every match.", AMBER]], "IN A BROWSER, ON A SERVER OR ON A NODE", T_QUERY + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "FlatSQL", subtitle: "SQL over raw FlatBuffer storage", url: "DIGITALARSENAL.GITHUB.IO/FLATSQL", t0: T_END + 0.3, mark: false, icon }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
