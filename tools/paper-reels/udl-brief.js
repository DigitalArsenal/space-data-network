// Intro reel for the UDL producer brief (spacedatanetwork.org/udl.html).
// 36 seconds: data a producer already delivers to the Unified Data Library
// goes to every other buyer as well, as Space Data Standards records published
// from the producer's own node, signed at the source, on the producer's terms,
// with accuracy measured against independent orbits.

import {
  W, H, AMBER, CYAN, INK, MUTED, SANS, MONO,
  clamp, seg, easeInOutCubic, easeInCubic, easeOutBack,
  limbCamera, createStudio,
} from "../showreel/kit.js";
import { prepareMath, createPaperKit } from "./paper-kit.js";

export const meta = { name: "reel", media: "docs/media/papers/udl-brief", poster: 330, crf: [29, 40], audio: { track: "delta-v", start: 40 } };

const DURATION = 36;
const FPS = 30;
const T_SDS = 5.5; // the same data, as SDS records
const T_NODE = 12.5; // from the producer's own node, signed
const T_TERMS = 19.5; // the producer's terms and keys
const T_MEAS = 26.0; // accuracy measured against independent orbits
const T_END = 32.0; // lockup

// What producers deliver today, and the SDS record each becomes.
const FEEDS = [
  ["ELEMENT SETS", "OMM"],
  ["STATE VECTORS", "OCM"],
  ["EPHEMERIDES", "OEM"],
  ["CONJUNCTIONS", "CDM"],
  ["EO OBSERVATIONS", "EOO"],
  ["RADAR OBSERVATIONS", "RDO"],
  ["RF OBSERVATIONS", "RFO"],
];
const row = (i) => [1000, 150 + i * 70];
const HUB = [1600, 360];

// The catalog paper's measured medians (its summary table).
const BARS = [
  ["GNSS ANALYSIS-CENTER ORBIT", "3.5 cm", 0.06],
  ["LASER-RANGING FIT", "7 m", 0.22],
  ["PUBLIC OMM, AT ISSUE", "0.3–1.6 km", 0.92],
];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "PRODUCER BRIEF · SPACE DATA NETWORK", hudUrl: "SPACEDATANETWORK.ORG" });
  const { glc, sx, font, dotGlow, scrim, renderEarth, eyebrow, block, beginSubframe, bloom, hud, keyIcon, padlock, bezierPts, strokeOn, stamp } = studio;
  const math = await prepareMath({});
  const { panel, label, chip, paperLockup } = createPaperKit(studio, math);

  function feedCard(name, rec, x, y, k, alpha, sds) {
    if (k <= 0) return;
    panel(x - 220, y - 29, 440, 58, alpha * clamp(k * 2), { r: 29, stroke: sds > 0 ? "rgba(245,165,36,0.6)" : "rgba(245,245,247,0.3)" });
    sx.save();
    sx.globalAlpha = alpha * clamp(k * 2);
    sx.font = font(650, 21, SANS);
    sx.letterSpacing = "2.5px";
    sx.fillStyle = INK;
    sx.textAlign = "left";
    sx.fillText(name, x - 190, y + 8);
    if (sds > 0) {
      sx.globalAlpha = alpha * sds;
      sx.font = font(700, 22, MONO);
      sx.letterSpacing = "1px";
      sx.fillStyle = AMBER;
      sx.textAlign = "right";
      sx.fillText(`$${rec}`, x + 196, y + 8);
    }
    sx.restore();
  }

  // ------------------------------------------- what producers deliver today
  function drawFeeds(t, alpha, sds) {
    if (alpha <= 0) return;
    FEEDS.forEach(([name, rec], i) => {
      const [x, y] = row(i);
      const t0 = 0.8 + i * 0.16;
      feedCard(name, rec, x, y, easeOutBack(seg(t, t0, t0 + 0.4)), alpha, sds * seg(t, T_SDS + 0.6 + i * 0.12, T_SDS + 0.9 + i * 0.12));
      if (sds <= 0) {
        const pts = bezierPts([x + 222, y], [x + 360, y], [HUB[0] - 260, HUB[1]], [HUB[0] - 110, HUB[1]], 40);
        strokeOn(pts, easeInOutCubic(seg(t, t0 + 0.5, t0 + 1.0)), "rgba(245,245,247,0.35)", 1.4, alpha * (1 - seg(t, T_SDS - 0.4, T_SDS)), 0.2);
      }
    });
    const hk = easeOutBack(seg(t, 1.8, 2.2)) * (1 - seg(t, T_SDS - 0.4, T_SDS));
    if (hk > 0) {
      dotGlow(HUB[0], HUB[1], 130 * hk, CYAN, 0.12 * alpha);
      chip("THE UDL", HUB[0], HUB[1], hk, alpha, { color: CYAN, px: 24, stroke: CYAN });
    }
    if (sds > 0) label("SPACE DATA STANDARDS RECORDS", 1216, 640, T_SDS + 1.4, T_NODE - 0.45, t, { px: 18, color: AMBER, align: "right" });
  }

  // ---------------------------------------- from the producer's own node
  function drawNode(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, T_NODE + 0.3, T_NODE + 0.7));
    const cx = 1180;
    const cy = 420;
    panel(cx - 260, cy - 170, 520, 340, alpha * clamp(k * 2), { r: 28 });
    sx.save();
    sx.globalAlpha = alpha * clamp(k * 2);
    sx.font = font(650, 24, SANS);
    sx.letterSpacing = "3px";
    sx.fillStyle = INK;
    sx.textAlign = "left";
    sx.fillText("YOUR NODE", cx - 226, cy - 112);
    sx.font = font(500, 20, MONO);
    sx.letterSpacing = "0px";
    sx.fillStyle = MUTED;
    ["$OMM  $OEM  $CDM  $RDO", "content address  bafkrei…", "key  your node's identity key"].forEach((s, i) => sx.fillText(s, cx - 226, cy - 56 + i * 40));
    sx.restore();
    keyIcon(cx + 170, cy - 112, 60 * k, AMBER, alpha);
    const st = seg(t, T_NODE + 1.6, T_NODE + 1.77);
    if (st > 0) stamp(cx, cy + 108, st, alpha);
    // buyers anywhere, one protocol
    ["OPERATORS", "AGENCIES", "INSURERS", "RESEARCHERS"].forEach((name, i) => {
      const t0 = T_NODE + 2.4 + i * 0.2;
      const y = 250 + i * 120;
      const kk = easeOutBack(seg(t, t0, t0 + 0.4));
      const pts = bezierPts([cx + 262, cy], [cx + 360, cy], [1640, y], [1680, y], 30);
      strokeOn(pts, easeInOutCubic(seg(t, t0 - 0.2, t0 + 0.3)), "rgba(245,165,36,0.5)", 1.6, alpha, 0.3);
      chip(name, 1740, y, kk, alpha, { color: INK, px: 18 });
    });
  }

  // ------------------------------------------------ your terms, your keys
  function drawTerms(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, T_TERMS + 0.3, T_TERMS + 0.7));
    padlock(1180, 400, 150 * k, 0, AMBER, alpha);
    dotGlow(1180, 400, 170 * k, AMBER, 0.12 * alpha);
    [["FREE", INK], ["DELAYED", INK], ["SUBSCRIPTION", AMBER], ["ONE-TIME", AMBER]].forEach(([name, color], i) => {
      const kk = easeOutBack(seg(t, T_TERMS + 1.0 + i * 0.2, T_TERMS + 1.4 + i * 0.2));
      chip(name, 980 + i * 135 * (i < 2 ? 1 : 1.18), 640, kk, alpha, { color, px: 18, stroke: color === AMBER ? AMBER : null });
    });
    label("THE CONTENT KEY STAYS WITH YOUR NODE", 1180, 760, T_TERMS + 2.0, T_MEAS - 0.45, t, { px: 18, align: "center" });
  }

  // ------------------------------------------- accuracy you can show
  function drawMeasured(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 900;
    BARS.forEach(([name, value, f], i) => {
      const y = 300 + i * 150;
      const kk = easeInOutCubic(seg(t, T_MEAS + 0.5 + i * 0.3, T_MEAS + 1.3 + i * 0.3));
      label(name, x0, y - 22, T_MEAS + 0.4 + i * 0.3, T_END - 0.45, t, { px: 18 });
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = "rgba(245,245,247,0.08)";
      sx.beginPath();
      sx.roundRect(x0, y, 820, 36, 18);
      sx.fill();
      sx.fillStyle = i === 2 ? "rgba(245,245,247,0.45)" : i === 0 ? CYAN : AMBER;
      sx.beginPath();
      sx.roundRect(x0, y, Math.max(36, 820 * f * kk), 36, 18);
      sx.fill();
      sx.restore();
      label(value, x0 + 830, y + 26, T_MEAS + 1.0 + i * 0.3, T_END - 0.45, t, { px: 24, color: INK, align: "right", tracking: 0 });
    });
    label("MEDIAN 3D ERROR AT ISSUE, AGAINST INDEPENDENT PRECISE ORBITS", x0, 760, T_MEAS + 1.6, T_END - 0.45, t, { px: 17 });
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_SDS, 0.8], [T_NODE, 1], [T_TERMS, 1], [T_MEAS, 1], [T_END + 0.2, 1]]);
  const flash = (t) => 0.08 * Math.exp(-Math.pow((t - (T_NODE + 1.7)) / 0.05, 2));
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const dim = t < T_SDS ? 0.8 : 0.35;
    renderEarth(limbCamera(t, -30, 35), t, 0.6 * dim * seg(t, 0, 1.2) * (1 - out), 0.2 * (1 - seg(t, 1.5, 3)), 50, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_NODE + 0.1) drawFeeds(t, 1 - seg(t, T_NODE - 0.4, T_NODE), seg(t, T_SDS, T_SDS + 0.3));
    if (t > T_NODE - 0.05 && t < T_TERMS + 0.1) drawNode(t, seg(t, T_NODE, T_NODE + 0.3) * (1 - seg(t, T_TERMS - 0.4, T_TERMS)));
    if (t > T_TERMS - 0.05 && t < T_MEAS + 0.1) drawTerms(t, seg(t, T_TERMS, T_TERMS + 0.3) * (1 - seg(t, T_MEAS - 0.4, T_MEAS)));
    if (t > T_MEAS - 0.05 && t < T_END + 0.1) drawMeasured(t, seg(t, T_MEAS, T_MEAS + 0.3) * (1 - seg(t, T_END - 0.4, T_END)));
    scrim((1 - out) * 0.9);

    // -------------------------------------------------------- the type
    if (t < T_SDS + 0.1) {
      eyebrow("PRODUCER BRIEF", 2, 0.5, T_SDS - 0.45, t);
      block([["Your data already", INK], ["serves the UDL.", AMBER]], "ELEMENT SETS, STATE VECTORS, EPHEMERIDES, OBSERVATIONS", 0.6, T_SDS - 0.4, t, 86);
    }
    if (t > T_SDS - 0.05 && t < T_NODE + 0.1) {
      block([["Offer it to every", INK], ["other buyer too.", AMBER]], "ONE OPEN STANDARD: SPACE DATA STANDARDS", T_SDS + 0.1, T_NODE - 0.4, t, 86);
    }
    if (t > T_NODE - 0.05 && t < T_TERMS + 0.1) {
      block([["From your own node,", INK], ["signed at the source.", AMBER]], "ANY BUYER CAN CHECK WHO PUBLISHED EVERY RECORD", T_NODE + 0.1, T_TERMS - 0.4, t, 80);
    }
    if (t > T_TERMS - 0.05 && t < T_MEAS + 0.1) {
      block([["Your terms.", INK], ["Your keys.", AMBER]], "PAID PRODUCTS SHIP ENCRYPTED", T_TERMS + 0.1, T_MEAS - 0.4, t, 88);
    }
    if (t > T_MEAS - 0.05 && t < T_END + 0.1) {
      block([["Accuracy", INK], ["you can show.", AMBER]], "MEASURED AGAINST INDEPENDENT PRECISE ORBITS", T_MEAS + 0.1, T_END - 0.4, t, 88);
    }
    paperLockup({ eyebrow: "PRODUCER BRIEF", title: ["Bring your data", "to Space Data Network"], byline: "tj@edgesource.com", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
