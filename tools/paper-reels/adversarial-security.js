// Intro reel for "Persistent Adversarial Security" (Koury). 47 seconds, in
// plain words with the paper's own math: one key derives addresses on many
// chains, value at those addresses is a public bond, a thief drains it at
// once so untouched funds show an intact key, the bond separates honest from
// dishonest actors, and trust decays as keys age.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO,
  clamp, lerp, seg, easeOutExpo, easeInOutCubic, easeInCubic, easeOutBack,
  limbCamera, createStudio,
} from "../showreel/kit.js";
import { prepareMath, createPaperKit } from "./paper-kit.js";

export const meta = { name: "reel", media: "docs/media/papers/adversarial-security", poster: 400, crf: [29, 40], audio: { track: "delta-v", start: 30 } };

const DURATION = 47;
const FPS = 30;
const T_HD = 5.5; // one key, addresses on many chains
const T_BOND = 12.5; // value at those addresses is a bond
const T_DRAIN = 20.0; // a thief drains it at once
const T_GAME = 27.5; // honest actors bond, dishonest ones don't
const T_DECAY = 35.5; // trust decays with key age
const T_END = 42.5; // lockup

// Derivation paths from the paper's implementation table (section 7).
const CHAINS = [
  ["BITCOIN", "m/44'/0'/0'/0/0"],
  ["ETHEREUM", "m/44'/60'/0'/0/0"],
  ["SOLANA", "m/44'/501'/0'/0'"],
  ["SUI", "m/44'/784'/0'/0'/0'"],
  ["COSMOS", "m/44'/118'/0'/0/0"],
  ["CARDANO", "m/1852'/1815'/0'/0/0"],
];
const KEY = [1080, 420];
const slot = (i) => [1500, 170 + i * 92];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "WHITEPAPER · PERSISTENT ADVERSARIAL SECURITY", hudUrl: "SPACEDATANETWORK.ORG" });
  const { glc, sx, font, maskText, dotGlow, scrim, renderEarth, eyebrow, block, beginSubframe, bloom, hud, keyIcon, padlock, bezierPts, pointOn, strokeOn, badge } = studio;
  const math = await prepareMath({
    bond: { key: "adv_bond", px: 84, color: AMBER },
    trust: { key: "adv_trust", px: 50, color: INK },
    cred: { key: "adv_cred", px: 40, color: INK },
    decay: { key: "adv_decay", px: 56, color: AMBER },
  });
  const { eq, panel, label, chip, paperLockup } = createPaperKit(studio, math);

  // ------------------------------------- one key, addresses on many chains
  function drawTree(t, alpha, coins) {
    if (alpha <= 0) return;
    const k = easeOutBack(seg(t, T_HD + 0.2, T_HD + 0.6));
    keyIcon(KEY[0], KEY[1], 110 * k, AMBER, alpha);
    dotGlow(KEY[0], KEY[1], 140 * k, AMBER, 0.12 * alpha);
    label("ONE KEY PAIR", KEY[0], KEY[1] + 100, T_HD + 0.4, T_DRAIN - 0.45, t, { px: 20, align: "center", color: AMBER });
    CHAINS.forEach(([name, path], i) => {
      const [x, y] = slot(i);
      const t0 = T_HD + 0.7 + i * 0.16;
      const pts = bezierPts([KEY[0] + 70, KEY[1]], [KEY[0] + 200, KEY[1]], [x - 260, y], [x - 120, y], 40);
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.4)), "rgba(245,165,36,0.55)", 1.6, alpha, 0.3);
      const pk = easeOutBack(seg(t, t0 + 0.3, t0 + 0.65));
      if (pk <= 0) return;
      sx.save();
      sx.globalAlpha = alpha * clamp(pk * 2);
      panel(x - 120, y - 36, 440, 72, 1, { r: 36, stroke: "rgba(245,245,247,0.35)" });
      sx.font = font(650, 22, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = INK;
      sx.textAlign = "left";
      sx.fillText(name, x - 92, y - 4);
      sx.font = font(500, 18, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = MUTED;
      sx.fillText(path, x - 92, y + 22);
      sx.restore();
      // the bond: value arriving at each address, from anyone
      if (coins > 0) {
        const c0 = T_BOND + 0.8 + i * 0.25;
        for (let j = 0; j < 3; j++) {
          const ck = seg(t, c0 + j * 0.5, c0 + j * 0.5 + 0.6);
          if (ck <= 0 || ck >= 1) continue;
          const cx = lerp(x + 520, x + 280, easeInOutCubic(ck));
          sx.save();
          sx.globalAlpha = alpha * coins * (1 - ck * 0.3);
          sx.fillStyle = AMBER;
          sx.beginPath();
          sx.arc(cx, y, 9, 0, Math.PI * 2);
          sx.fill();
          sx.restore();
          dotGlow(cx, y, 22, AMBER, alpha * coins * 0.5);
        }
        const fill = seg(t, c0, c0 + 1.8);
        if (fill > 0) {
          sx.save();
          sx.globalAlpha = alpha * coins;
          sx.fillStyle = "rgba(245,165,36,0.18)";
          sx.beginPath();
          sx.roundRect(x + 160, y - 22, 130, 44, 22);
          sx.fill();
          sx.fillStyle = AMBER;
          sx.beginPath();
          sx.roundRect(x + 160, y - 22, 130 * fill, 44, 22);
          sx.fill();
          sx.restore();
        }
      }
    });
  }

  // -------------------------------------------------- the drain chart
  function drawDrain(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 1000;
    const x1 = 1790;
    const yTop = 200;
    const yb = 620;
    const ak = seg(t, T_DRAIN + 0.3, T_DRAIN + 0.7);
    sx.save();
    sx.globalAlpha = alpha * ak;
    sx.strokeStyle = "rgba(245,245,247,0.5)";
    sx.lineWidth = 1.5;
    sx.beginPath();
    sx.moveTo(x0, yTop - 30);
    sx.lineTo(x0, yb);
    sx.lineTo(x1, yb);
    sx.stroke();
    sx.restore();
    label("BALANCE AT A KEY'S ADDRESSES", x0 - 2, yTop - 50, T_DRAIN + 0.4, T_GAME - 0.45, t, { px: 18 });
    label("TIME · EVERY BLOCK", x1, yb + 40, T_DRAIN + 0.4, T_GAME - 0.45, t, { px: 18, align: "right" });
    const prog = easeInOutCubic(seg(t, T_DRAIN + 0.6, T_DRAIN + 4.2));
    const xc = lerp(x0, x1, 0.55);
    const intact = [];
    const drained = [];
    for (let i = 0; i <= 100; i++) {
      const f = i / 100;
      const x = lerp(x0, x1, f);
      intact.push([x, yTop + 20 - 10 * Math.sin(f * 3)]);
      drained.push([x, x < xc ? yTop + 130 + 8 * Math.sin(f * 5) : yb - 4]);
    }
    drained.splice(56, 0, [xc, yb - 4]);
    strokeOn(intact, prog, AMBER, 3, alpha, 0.5);
    strokeOn(drained, prog, RED, 3, alpha, 0.5);
    const hit = seg(t, T_DRAIN + 2.4, T_DRAIN + 2.7);
    if (hit > 0) {
      sx.save();
      sx.globalAlpha = alpha * (1 - seg(t, T_DRAIN + 2.7, T_DRAIN + 3.6));
      sx.strokeStyle = RED;
      sx.lineWidth = 2.5;
      sx.beginPath();
      sx.arc(xc, yTop + 140, 12 + easeOutExpo(hit) * 70, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      chip("KEY STOLEN · DRAINED AT ONCE", xc + 40, yTop + 290, easeOutBack(seg(t, T_DRAIN + 2.6, T_DRAIN + 3.0)), alpha, { color: RED, px: 20 });
    }
    chip("UNTOUCHED · KEY INTACT", x1 - 170, yTop + 80, easeOutBack(seg(t, T_DRAIN + 4.2, T_DRAIN + 4.6)), alpha, { color: AMBER, px: 20 });
  }

  // ------------------------------------------------- the payoff matrix
  function drawMatrix(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 1000;
    const y0 = 340;
    const rh = 112;
    const c0 = 230;
    const cw = 290;
    const cells = [
      [0, 0, "+ TRUST, + ACCESS *", AMBER],
      [1, 0, "COST > GAIN", INK],
      [0, 1, "NO TRUST SIGNAL", MUTED],
      [1, 1, "UNDETECTABLE", MUTED],
    ];
    const k = easeOutExpo(seg(t, T_GAME + 0.3, T_GAME + 0.8));
    sx.save();
    sx.globalAlpha = alpha * k;
    panel(x0, y0, c0 + 2 * cw, rh * 3, 1, { r: 14 });
    sx.fillStyle = "rgba(245,165,36,0.12)";
    sx.fillRect(x0 + c0, y0 + rh, cw, rh);
    sx.strokeStyle = "rgba(245,245,247,0.25)";
    sx.lineWidth = 1.2;
    sx.beginPath();
    [1, 2].forEach((r) => {
      sx.moveTo(x0, y0 + r * rh);
      sx.lineTo(x0 + c0 + 2 * cw, y0 + r * rh);
    });
    [c0, c0 + cw].forEach((c) => {
      sx.moveTo(x0 + c, y0);
      sx.lineTo(x0 + c, y0 + 3 * rh);
    });
    sx.stroke();
    sx.font = font(650, 21, SANS);
    sx.letterSpacing = "2.5px";
    sx.fillStyle = INK;
    sx.textAlign = "center";
    sx.fillText("HONEST", x0 + c0 + cw / 2, y0 + rh / 2 - 4);
    sx.fillText("DISHONEST", x0 + c0 + cw * 1.5, y0 + rh / 2 - 4);
    sx.fillStyle = MUTED;
    sx.font = font(600, 17, SANS);
    sx.fillText("CLAIMANT", x0 + c0 + cw / 2, y0 + rh / 2 + 22);
    sx.fillText("CLAIMANT", x0 + c0 + cw * 1.5, y0 + rh / 2 + 22);
    sx.font = font(650, 21, SANS);
    sx.fillStyle = INK;
    sx.fillText("FUND BOND", x0 + c0 / 2, y0 + rh * 1.5 + 7);
    sx.fillText("NO BOND", x0 + c0 / 2, y0 + rh * 2.5 + 7);
    sx.restore();
    cells.forEach(([c, r, text, col], i) => {
      const t0 = T_GAME + 0.9 + i * 0.3;
      label(text, x0 + c0 + cw * (c + 0.5), y0 + rh * (r + 1.5) + 8, t0, T_DECAY - 0.45, t, { px: 22, color: col, align: "center", weight: 700, tracking: 2 });
    });
    label("VERIFIER VERSUS CLAIMANT", x0 + 2, y0 - 26, T_GAME + 0.4, T_DECAY - 0.45, t, { px: 18 });
    label("* DOMINANT STRATEGY FOR HONEST ACTORS", x0 + 2, y0 + rh * 3 + 40, T_GAME + 2.2, T_DECAY - 0.45, t, { px: 18 });
  }

  // ---------------------------------------------------- trust decay
  function drawDecay(t, alpha) {
    if (alpha <= 0) return;
    const x0 = 1060;
    const x1 = 1760;
    const yb = 620;
    const yt = 200;
    const X = (f) => lerp(x0, x1, f);
    const Y = (v) => lerp(yb, yt, v);
    const ak = seg(t, T_DECAY + 0.3, T_DECAY + 0.7);
    sx.save();
    sx.globalAlpha = alpha * ak;
    sx.fillStyle = "rgba(245,165,36,0.1)";
    sx.fillRect(X(0.8), yt - 20, X(1) - X(0.8), yb - yt + 20);
    sx.strokeStyle = "rgba(245,245,247,0.5)";
    sx.lineWidth = 1.5;
    sx.beginPath();
    sx.moveTo(x0, yt - 30);
    sx.lineTo(x0, yb);
    sx.lineTo(x1 + 30, yb);
    sx.stroke();
    sx.setLineDash([4, 6]);
    sx.strokeStyle = "rgba(245,245,247,0.3)";
    sx.beginPath();
    sx.moveTo(x0, Y(0.5));
    sx.lineTo(x1, Y(0.5));
    sx.stroke();
    sx.restore();
    [["0", 0], ["T/2", 0.5], ["T", 1]].forEach(([s, f]) => label(s, X(f), yb + 38, T_DECAY + 0.5, T_END - 0.45, t, { px: 22, align: "center", tracking: 0, family: "Times New Roman, serif", weight: "italic 500" }));
    [["1", 1], ["0.5", 0.5]].forEach(([s, v]) => label(s, x0 - 16, Y(v) + 8, T_DECAY + 0.5, T_END - 0.45, t, { px: 20, align: "right", tracking: 0 }));
    label("TRUST", x0, yt - 50, T_DECAY + 0.4, T_END - 0.45, t, { px: 18 });
    label("KEY AGE", x1 + 30, yb + 80, T_DECAY + 0.4, T_END - 0.45, t, { px: 18, align: "right" });
    label("ROTATION URGENT", X(0.9), yt + 10, T_DECAY + 2.6, T_END - 0.45, t, { px: 16, align: "center", color: AMBER });
    const pts = [];
    for (let i = 0; i <= 100; i++) {
      const f = i / 100;
      pts.push([X(f), Y(Math.max(0, 1 - f * f))]);
    }
    strokeOn(pts, easeInOutCubic(seg(t, T_DECAY + 0.8, T_DECAY + 2.6)), AMBER, 3.5, alpha, 0.6);
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_HD, 0.8], [T_BOND, 0.6], [T_DRAIN, 1], [T_GAME, 1], [T_DECAY, 1], [T_END + 0.2, 1]]);
  const flash = (t) => 0.1 * Math.exp(-Math.pow((t - (T_DRAIN + 2.45)) / 0.05, 2));
  const fade = (t) => seg(t, 0, 0.4);

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const dim = t < T_HD ? 1 : 0.4;
    renderEarth(limbCamera(t, -10, 40), t, 0.6 * dim * seg(t, 0, 1.2) * (1 - out), 0.2 * (1 - seg(t, 1.5, 3)), 40, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_HD + 0.1) {
      const k = easeOutBack(seg(t, 1.2, 1.7));
      padlock(1440, 380, 150 * k, 0, INK, 1 - seg(t, T_HD - 0.45, T_HD - 0.05));
      chip("TRUST TODAY: AN AUTHORITY'S CERTIFICATE", 1440, 560, easeOutBack(seg(t, 1.8, 2.2)), 1 - seg(t, T_HD - 0.45, T_HD - 0.05), { color: MUTED, px: 20 });
    }
    if (t > T_HD - 0.05 && t < T_DRAIN + 0.1) drawTree(t, seg(t, T_HD, T_HD + 0.3) * (1 - seg(t, T_DRAIN - 0.4, T_DRAIN)), seg(t, T_BOND, T_BOND + 0.3));
    if (t > T_DRAIN - 0.05 && t < T_GAME + 0.1) drawDrain(t, seg(t, T_DRAIN, T_DRAIN + 0.3) * (1 - seg(t, T_GAME - 0.4, T_GAME)));
    if (t > T_GAME - 0.05 && t < T_DECAY + 0.1) drawMatrix(t, seg(t, T_GAME, T_GAME + 0.3) * (1 - seg(t, T_DECAY - 0.4, T_DECAY)));
    if (t > T_DECAY - 0.05 && t < T_END + 0.1) drawDecay(t, seg(t, T_DECAY, T_DECAY + 0.3) * (1 - seg(t, T_END - 0.4, T_END)));
    scrim((1 - out) * 0.9);

    // -------------------------------------------------------- the type
    if (t < T_HD + 0.1) {
      eyebrow("WHITEPAPER", 2, 0.5, T_HD - 0.45, t);
      block([["Is this key", INK], ["still safe?", AMBER]], "TRUST NEEDS PROOF THAT IS LIVE AND PUBLIC", 0.6, T_HD - 0.4, t, 88);
    }
    if (t > T_HD - 0.05 && t < T_BOND + 0.1) {
      block([["One key.", INK], ["Addresses on many chains.", AMBER]], "DERIVED DETERMINISTICALLY: BIP-32, BIP-44, SLIP-10", T_HD + 0.1, T_BOND - 0.4, t, 84);
    }
    if (t > T_BOND - 0.05 && t < T_DRAIN + 0.1) {
      eq("bond", 150, 280, T_BOND + 0.4, T_DRAIN - 0.45, t);
      label("S(k): SECURITY OF THE DATA THE KEY SIGNS", 152, 360, T_BOND + 0.8, T_DRAIN - 0.45, t, { px: 18 });
      label("F(k): FUNDS THE KEY CAN REACH", 152, 392, T_BOND + 0.95, T_DRAIN - 0.45, t, { px: 18 });
      block([["Put value behind it.", INK], ["Anyone can add more.", AMBER]], "FUNDS AT A KEY'S ADDRESSES: A PUBLIC SECURITY BOND", T_BOND + 0.1, T_DRAIN - 0.4, t, 84);
    }
    if (t > T_DRAIN - 0.05 && t < T_GAME + 0.1) {
      eq("trust", 150, 260, T_DRAIN + 0.5, T_GAME - 0.45, t);
      label("TRUST FROM WEB OF TRUST, VALUE AND DURATION", 152, 330, T_DRAIN + 0.8, T_GAME - 0.45, t, { px: 18 });
      block([["A thief drains it at once.", INK], ["Untouched funds, intact key.", AMBER]], "INSTANT, IRREVERSIBLE, RISK-FREE FOR WHOEVER STOLE THE KEY", T_DRAIN + 0.1, T_GAME - 0.4, t, 80);
    }
    if (t > T_GAME - 0.05 && t < T_DECAY + 0.1) {
      eq("cred", 150, 196, T_GAME + 0.4, T_DECAY - 0.45, t);
      label("A BOND IS CREDIBLE WHEN IT EXCEEDS THE EXPECTED VALUE OF CHEATING", 152, 252, T_GAME + 0.8, T_DECAY - 0.45, t, { px: 18 });
      block([["Honest actors bond.", INK], ["Dishonest ones don't.", AMBER]], "A SEPARATING EQUILIBRIUM: THE TWO BECOME DISTINGUISHABLE", T_GAME + 0.1, T_DECAY - 0.4, t, 84);
    }
    if (t > T_DECAY - 0.05 && t < T_END + 0.1) {
      eq("decay", 150, 270, T_DECAY + 0.4, T_END - 0.45, t);
      label("T: THE KEY'S EXPIRATION AGE", 152, 340, T_DECAY + 0.8, T_END - 0.45, t, { px: 18 });
      block([["Old keys earn less trust.", INK], ["Rotate them.", AMBER]], "TRUST DECAYS QUADRATICALLY AS A KEY AGES", T_DECAY + 0.1, T_END - 0.4, t, 84);
    }
    paperLockup({ eyebrow: "WHITEPAPER", title: ["Persistent", "Adversarial Security"], byline: "Anthony “TJ” Koury III", t0: T_END + 0.3 }, t);

    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
