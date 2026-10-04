// HD Wallet reel for wallet.spacedatanetwork.org, in plain words. 30 seconds:
// one recovery phrase that makes every key, signing so people know it is you,
// locking data so only the recipient opens it, and one wallet for many
// networks. The recovery phrase is always shown masked.

import {
  W, H, AMBER, CYAN, RED, INK, MUTED, SANS, MONO, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack,
  limbCamera, createStudio,
} from "../kit.js";

export const meta = { name: "hd-wallet-reel", media: "docs/media", poster: 300, audio: { track: "covariance", start: 170 } };

const DURATION = 30;
const FPS = 30;
const T_SIGN = 6.5; // sign: people know it's you
const T_LOCK = 12.5; // lock: only they can open it
const T_NETS = 18.5; // one wallet, many networks
const T_END = 25.5;

const NETS = ["BITCOIN", "ETHEREUM", "SOLANA", "COSMOS", "POLKADOT", "LITECOIN", "DOGECOIN", "WEB CERTIFICATES"];

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "HD WALLET", hudUrl: "WALLET.SPACEDATANETWORK.ORG" });
  const { glc, sx, font, maskText, dotGlow, scrim, pill, badge, padlock, keyIcon, stamp, bezierPts, pointOn, strokeOn, renderEarth, eyebrow, lockup, block, beginSubframe, bloom, hud } = studio;

  // ------------------------------------- one phrase, every key
  function drawPhrase(t, alpha) {
    if (alpha <= 0) return;
    // twelve masked words
    for (let i = 0; i < 12; i++) {
      const c = i % 4;
      const r = Math.floor(i / 4);
      const k = easeOutBack(seg(t, 0.4 + i * 0.06, 0.75 + i * 0.06));
      if (k <= 0) continue;
      const x = 1000 + c * 205;
      const y = 130 + r * 70;
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2);
      pill(x, y, 190, 54, "rgba(245,245,247,0.4)", "rgba(0,0,0,0.7)");
      sx.font = font(500, 19, MONO);
      sx.fillStyle = MUTED;
      sx.textAlign = "left";
      sx.fillText(String(i + 1).padStart(2, "0"), x + 18, y + 34);
      sx.fillStyle = INK;
      sx.font = font(700, 26, SANS);
      sx.fillText("• • • • •", x + 60, y + 36);
      sx.restore();
    }
    // the tree of keys it makes
    const root = [1400, 420];
    const kids = [[1150, 560], [1400, 560], [1650, 560]];
    const leaves = [[1060, 690], [1240, 690], [1330, 690], [1470, 690], [1560, 690], [1740, 690]];
    const rk = easeOutBack(seg(t, 1.6, 2.0));
    if (rk > 0) keyIcon(root[0], root[1], 80 * rk, AMBER, alpha);
    kids.forEach((p, i) => {
      const t0 = 2.1 + i * 0.12;
      strokeOn([[root[0], root[1] + 26], p], easeInOutCubic(seg(t, t0, t0 + 0.3)), "rgba(245,165,36,0.6)", 1.8, alpha, 0.3);
      const k = easeOutBack(seg(t, t0 + 0.3, t0 + 0.6));
      if (k > 0) keyIcon(p[0], p[1], 62 * k, INK, alpha);
    });
    leaves.forEach((p, i) => {
      const parent = kids[Math.floor(i / 2)];
      const t0 = 2.8 + i * 0.08;
      strokeOn([[parent[0], parent[1] + 20], p], easeInOutCubic(seg(t, t0, t0 + 0.3)), "rgba(245,245,247,0.4)", 1.4, alpha, 0);
      const k = easeOutBack(seg(t, t0 + 0.3, t0 + 0.6));
      if (k > 0) keyIcon(p[0], p[1], 48 * k, "rgba(245,245,247,0.8)", alpha);
    });
  }

  // ------------------------------------------- sign: people know it's you
  function drawSign(t, alpha) {
    if (alpha <= 0) return;
    const cx = 1420;
    const k = easeOutExpo(seg(t, T_SIGN + 0.2, T_SIGN + 0.6));
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.beginPath();
    sx.roundRect(cx - 230, 140, 460, 260, 16);
    sx.fillStyle = "rgba(22,22,24,0.92)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.3)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 22, SANS);
    sx.letterSpacing = "3px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("MESSAGE", cx - 200, 190);
    sx.fillStyle = "rgba(245,245,247,0.45)";
    [0.9, 0.75, 0.85, 0.6].forEach((f, r) => sx.fillRect(cx - 200, 222 + r * 34, 400 * f, 8));
    sx.restore();
    const st = seg(t, T_SIGN + 0.9, T_SIGN + 1.07);
    if (st > 0) stamp(cx, 460, st, alpha);
    const ck = seg(t, T_SIGN + 1.9, T_SIGN + 2.2);
    if (ck > 0) {
      badge("check", cx, 600, 64 * easeOutBack(ck), AMBER, alpha);
      maskText(sx, "ANYONE CAN CHECK IT CAME FROM YOU", cx, 690, 22, 600, AMBER, T_SIGN + 2.1, T_LOCK - 0.45, t, { tracking: 3, align: "center" });
    }
  }

  // ------------------------------------------ lock: only they can open it
  function drawLock(t, alpha) {
    if (alpha <= 0) return;
    const src = [1400, 230];
    const k = easeOutBack(seg(t, T_LOCK + 0.2, T_LOCK + 0.5));
    const lockK = easeInExpo(seg(t, T_LOCK + 0.6, T_LOCK + 0.8));
    padlock(src[0], src[1], 120 * k, 1 - lockK, lockK >= 1 ? AMBER : INK, alpha);
    [[1180, 640, true], [1620, 640, false]].forEach(([nx, ny, to], j) => {
      const t0 = T_LOCK + 1.0 + j * 0.08;
      const pts = bezierPts([src[0], src[1] + 80], [src[0], src[1] + 200], [nx, ny - 220], [nx, ny - 50], 32);
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.35)), "rgba(245,245,247,0.5)", 1.5, alpha, 0, [5, 7]);
      const p = easeInOutCubic(seg(t, t0 + 0.15, t0 + 0.6));
      if (p > 0 && p < 1) {
        const [px, py] = pointOn(pts, p);
        padlock(px, py, 32, 0, AMBER, alpha);
      }
      const arrive = seg(t, t0 + 0.6, t0 + 0.7);
      if (arrive <= 0) return;
      const open = to ? easeOutBack(seg(t, T_LOCK + 2.5, T_LOCK + 2.7)) : 0;
      if (to) {
        const kp = easeInOutCubic(seg(t, T_LOCK + 2.0, T_LOCK + 2.45));
        const ka = alpha * seg(t, T_LOCK + 1.9, T_LOCK + 2.0) * (1 - seg(t, T_LOCK + 2.45, T_LOCK + 2.55));
        keyIcon(lerp(nx - 170, nx - 30, kp), ny + 4, 50, AMBER, ka);
      }
      padlock(nx, ny, 76 * easeOutBack(arrive), open, to && open > 0 ? AMBER : "rgba(245,245,247,0.7)", alpha);
      maskText(sx, to ? "RECIPIENT · OPENED" : "EVERYONE ELSE · LOCKED", nx, ny + 84, 22, 600, to && open > 0 ? AMBER : MUTED, T_LOCK + 1.8, T_NETS - 0.45, t, { tracking: 3, align: "center" });
    });
  }

  // ----------------------------------------- one wallet, many networks
  function drawNets(t, alpha) {
    if (alpha <= 0) return;
    const cx = 1420;
    const cy = 400;
    const k = easeOutBack(seg(t, T_NETS + 0.2, T_NETS + 0.6));
    keyIcon(cx, cy, 90 * k, AMBER, alpha);
    dotGlow(cx, cy, 120 * k, AMBER, 0.15 * alpha);
    NETS.forEach((name, i) => {
      const a = -Math.PI / 2 + (i / NETS.length) * Math.PI * 2;
      const lx = cx + Math.cos(a) * 360;
      const ly = cy + Math.sin(a) * 250;
      const t0 = T_NETS + 0.6 + i * 0.14;
      const pk = easeOutBack(seg(t, t0, t0 + 0.35));
      if (pk <= 0) return;
      strokeOn([[cx + Math.cos(a) * 80, cy + Math.sin(a) * 60], [lx, ly]], seg(t, t0, t0 + 0.3), "rgba(245,165,36,0.5)", 1.5, alpha, 0.3);
      sx.save();
      sx.font = font(600, 20, SANS);
      sx.letterSpacing = "2.5px";
      const w = sx.measureText(name).width + 40;
      sx.globalAlpha = alpha * clamp(pk * 2);
      sx.translate(lx, ly);
      sx.scale(pk, pk);
      pill(-w / 2, -23, w, 46, "rgba(245,245,247,0.5)", "rgba(0,0,0,0.75)");
      sx.fillStyle = INK;
      sx.textAlign = "center";
      sx.fillText(name, 0, 8);
      sx.restore();
    });
  }

  function icon(cx, cy, R, k) {
    keyIcon(cx, cy, R * 1.2 * k, AMBER, clamp(k * 2));
    dotGlow(cx, cy, R * 0.8, AMBER, 0.15 * k);
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_SIGN, 1], [T_LOCK, 1], [T_NETS, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    renderEarth(limbCamera(t, -10, 40), t, 0.55 * seg(t, 0, 1.2) * (1 - out), 0.2, 40, 18);
    sx.drawImage(glc, 0, 0);
    if (t < T_SIGN + 0.2) drawPhrase(t, 1 - seg(t, T_SIGN - 0.4, T_SIGN));
    if (t > T_SIGN - 0.05 && t < T_LOCK + 0.1) drawSign(t, seg(t, T_SIGN, T_SIGN + 0.3) * (1 - seg(t, T_LOCK - 0.4, T_LOCK)));
    if (t > T_LOCK - 0.05 && t < T_NETS + 0.1) drawLock(t, seg(t, T_LOCK, T_LOCK + 0.3) * (1 - seg(t, T_NETS - 0.4, T_NETS)));
    if (t > T_NETS - 0.05 && t < T_END + 0.1) drawNets(t, seg(t, T_NETS, T_NETS + 0.3) * (1 - seg(t, T_END - 0.3, T_END)));
    scrim(1 - out);

    if (t < T_SIGN + 0.1) {
      eyebrow("HD WALLET", 2, 0.4, T_SIGN - 0.45, t);
      block([["One recovery phrase.", INK], ["Every key you need.", AMBER]], "MADE AND KEPT IN YOUR BROWSER", 0.5, T_SIGN - 0.4, t, 84);
    }
    if (t > T_SIGN - 0.05 && t < T_LOCK + 0.1) {
      block([["Sign it.", INK], ["People know it's you.", AMBER]], "A DIGITAL SIGNATURE ANYONE CAN CHECK", T_SIGN + 0.1, T_LOCK - 0.4, t, 88);
    }
    if (t > T_LOCK - 0.05 && t < T_NETS + 0.1) {
      block([["Lock it.", INK], ["Only they can open it.", AMBER]], "ENCRYPTED TO THE RECIPIENT'S KEY", T_LOCK + 0.1, T_NETS - 0.4, t, 88);
    }
    if (t > T_NETS - 0.05 && t < T_END + 0.1) {
      block([["One wallet,", INK], ["many networks.", AMBER]], "THE SAME KEYS SIGN FOR EACH, AND FOR WEB CERTIFICATES", T_NETS + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "HD Wallet", subtitle: "Your keys, in your browser", url: "WALLET.SPACEDATANETWORK.ORG", t0: T_END + 0.3, mark: false, icon }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
