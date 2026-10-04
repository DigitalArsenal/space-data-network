// Models reel for digitalarsenal.github.io/asset-models, in plain words. 30
// seconds: a 3D model for what is in orbit, each checked against the real
// spacecraft with its source and license recorded, light enough for a
// browser, and shown in the right shape on the map.

import {
  W, H, AMBER, CYAN, INK, MUTED, SANS, MONO, D, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeInCubic, easeOutBack,
  add, mul, norm, cross, OMEGA_K, orbitBasis, orbitPos, makeSky, lookAt, limbCamera, project, occluded, createStudio,
} from "../kit.js";

export const meta = { name: "asset-models-reel", media: "docs/media", poster: 330, audio: { track: "covariance", start: 0 } };

const DURATION = 30;
const FPS = 30;
const T_CHECK = 6.5; // checked against the real thing
const T_LIGHT = 12.5; // light enough for a browser
const T_MAP = 18.5; // in the right shape on the map
const T_END = 25.5;

const SATS = makeSky();
const ORBIT = { ...orbitBasis(51.6 * D, 300 * D), r: 1.18 };
ORBIT.w = OMEGA_K * Math.pow(ORBIT.r, -1.5);
ORBIT.u0 = 0.2;

// A satellite as edges: a box bus, two solar wings and a dish, in metres-ish units.
const SHAPE = (() => {
  const E = [];
  const box = (cx, cy, cz, sx, sy, sz) => {
    const v = [];
    for (const x of [-1, 1]) for (const y of [-1, 1]) for (const z of [-1, 1]) v.push([cx + x * sx, cy + y * sy, cz + z * sz]);
    [[0, 1], [2, 3], [4, 5], [6, 7], [0, 2], [1, 3], [4, 6], [5, 7], [0, 4], [1, 5], [2, 6], [3, 7]].forEach(([a, b]) => E.push([v[a], v[b], 0]));
  };
  box(0, 0, 0, 0.6, 0.6, 0.8);
  // wings with cell lines
  for (const side of [-1, 1]) {
    const x0 = side * 0.7;
    const x1 = side * 3.1;
    const z = 0.45;
    E.push([[x0, 0, -z], [x1, 0, -z], 1], [[x0, 0, z], [x1, 0, z], 1], [[x1, 0, -z], [x1, 0, z], 1], [[x0, 0, -z], [x0, 0, z], 1]);
    for (let k = 1; k < 6; k++) {
      const x = lerp(x0, x1, k / 6);
      E.push([[x, 0, -z], [x, 0, z], 2]);
    }
    E.push([[side * 0.6, 0, 0], [x0, 0, 0], 0]);
  }
  // the dish
  const N = 16;
  for (let i = 0; i < N; i++) {
    const a0 = (i / N) * Math.PI * 2;
    const a1 = ((i + 1) / N) * Math.PI * 2;
    E.push([[0.45 * Math.cos(a0), 0.95, 0.45 * Math.sin(a0)], [0.45 * Math.cos(a1), 0.95, 0.45 * Math.sin(a1)], 1]);
  }
  E.push([[0, 0.6, 0], [0, 0.95, 0], 0]);
  return E;
})();

export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "SPACE DATA NETWORK · MODELS", hudUrl: "DIGITALARSENAL.GITHUB.IO/ASSET-MODELS" });
  const { glc, sx, gx, font, maskText, decodeText, dotGlow, scrim, pill, badge, renderEarth, drawSatellites, strokeOrbitArc, eyebrow, lockup, block, beginSubframe, bloom, hud } = studio;

  // The wireframe model, turning: yaw a, a fixed tilt, a perspective camera.
  function model(cx, cy, scale, yaw, alpha, tilt = 0.42) {
    if (alpha <= 0 || scale <= 0) return;
    const cy0 = Math.cos(yaw), sy0 = Math.sin(yaw), ct = Math.cos(tilt), st = Math.sin(tilt);
    const P = ([x, y, z]) => {
      const x1 = x * cy0 + z * sy0;
      const z1 = -x * sy0 + z * cy0;
      const y2 = y * ct - z1 * st;
      const z2 = y * st + z1 * ct;
      const f = 9 / (9 + z2);
      return [cx + x1 * scale * f, cy - y2 * scale * f];
    };
    sx.save();
    sx.lineCap = "round";
    SHAPE.forEach(([a, b, kind]) => {
      const [x0, y0] = P(a);
      const [x1, y1] = P(b);
      sx.globalAlpha = alpha * (kind === 2 ? 0.45 : 0.95);
      sx.strokeStyle = kind === 0 ? INK : AMBER;
      sx.lineWidth = kind === 2 ? 1.2 : 2;
      sx.beginPath();
      sx.moveTo(x0, y0);
      sx.lineTo(x1, y1);
      sx.stroke();
      if (kind === 1) {
        gx.globalAlpha = alpha * 0.4;
        gx.strokeStyle = AMBER;
        gx.lineWidth = 3;
        gx.beginPath();
        gx.moveTo(x0 / 2, y0 / 2);
        gx.lineTo(x1 / 2, y1 / 2);
        gx.stroke();
      }
    });
    sx.restore();
  }

  // ------------------------------------------- a model for what's in orbit
  function drawOpen(t, alpha) {
    if (alpha <= 0) return;
    const k = easeOutExpo(seg(t, 0.3, 1.4));
    model(1420, 380, 120 * k, 0.6 * t, alpha);
    // a measuring line under it
    const mk = seg(t, 2.0, 2.6);
    if (mk > 0) {
      sx.save();
      sx.globalAlpha = alpha * mk;
      sx.strokeStyle = "rgba(245,245,247,0.5)";
      sx.lineWidth = 1.2;
      sx.beginPath();
      sx.moveTo(1050, 640);
      sx.lineTo(1790, 640);
      sx.moveTo(1050, 630);
      sx.lineTo(1050, 650);
      sx.moveTo(1790, 630);
      sx.lineTo(1790, 650);
      sx.stroke();
      sx.font = font(600, 22, SANS);
      sx.letterSpacing = "3px";
      sx.fillStyle = INK;
      sx.textAlign = "center";
      sx.fillText("WINGSPAN TO PUBLISHED DIMENSIONS", 1420, 690);
      sx.restore();
    }
  }

  // --------------------------------------- checked against the real thing
  const FACTS = [["SPACECRAFT", "Earth-observation satellite"], ["SIZE", "6.2 m wingspan"], ["SOURCE", "Published drawings"], ["LICENSE", "Recorded"], ["RENDER CHECK", "Verified"]];
  function drawCheck(t, alpha) {
    if (alpha <= 0) return;
    model(1090, 360, 70, 0.6 * t, alpha * 0.9);
    const px = 1300;
    const py = 130;
    const k = easeOutExpo(seg(t, T_CHECK + 0.3, T_CHECK + 0.7));
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.beginPath();
    sx.roundRect(px, py, 540, 520, 18);
    sx.fillStyle = "rgba(22,22,24,0.92)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.28)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.font = font(600, 22, SANS);
    sx.letterSpacing = "3.5px";
    sx.fillStyle = AMBER;
    sx.textAlign = "left";
    sx.fillText("MODEL RECORD", px + 30, py + 50);
    sx.restore();
    FACTS.forEach(([kk, v], i) => {
      const t0 = T_CHECK + 0.8 + i * 0.3;
      const fk = seg(t, t0, t0 + 0.3);
      if (fk <= 0) return;
      const y = py + 116 + i * 82;
      sx.save();
      sx.globalAlpha = alpha * fk;
      sx.font = font(600, 18, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.fillText(kk, px + 30, y - 20);
      sx.font = font(500, 28, SANS);
      sx.letterSpacing = "0px";
      sx.fillStyle = i >= 3 ? AMBER : INK;
      decodeText(sx, v, px + 30, y + 14, t0, 0.3, t, 60 + i);
      sx.restore();
      if (i >= 3) badge("check", px + 490, y, 36 * easeOutBack(seg(t, t0 + 0.3, t0 + 0.5)), AMBER, alpha);
    });
  }

  // ------------------------------------------ light enough for a browser
  function drawLight(t, alpha) {
    if (alpha <= 0) return;
    // a browser window holding the model
    const bx = 1080;
    const by = 140;
    const bw = 720;
    const bh = 470;
    const k = easeOutExpo(seg(t, T_LIGHT + 0.2, T_LIGHT + 0.6));
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.beginPath();
    sx.roundRect(bx, by, bw, bh, 14);
    sx.fillStyle = "rgba(14,14,16,0.92)";
    sx.fill();
    sx.strokeStyle = "rgba(245,245,247,0.35)";
    sx.lineWidth = 1.5;
    sx.stroke();
    sx.beginPath();
    sx.moveTo(bx, by + 44);
    sx.lineTo(bx + bw, by + 44);
    sx.stroke();
    [0, 1, 2].forEach((i) => {
      sx.beginPath();
      sx.arc(bx + 24 + i * 18, by + 22, 5, 0, Math.PI * 2);
      sx.stroke();
    });
    sx.restore();
    const load = easeOutExpo(seg(t, T_LIGHT + 0.8, T_LIGHT + 1.4));
    model(bx + bw / 2, by + 260, 95 * load, 0.6 * t, alpha);
    // the load bar fills almost at once
    sx.save();
    sx.globalAlpha = alpha * k;
    sx.fillStyle = "rgba(245,245,247,0.15)";
    sx.fillRect(bx + 40, by + bh - 40, bw - 80, 6);
    sx.fillStyle = AMBER;
    sx.fillRect(bx + 40, by + bh - 40, (bw - 80) * load, 6);
    sx.restore();
    maskText(sx, "HUNDREDS OF MODELS, AND GROWING", bx + bw / 2, by + bh + 64, 22, 600, MUTED, T_LIGHT + 1.6, T_MAP - 0.45, t, { tracking: 3, align: "center" });
  }

  // ---------------------------------------------- in the right shape on the map
  function drawMap(cam, t, alpha) {
    if (alpha <= 0) return;
    const u = ORBIT.u0 + ORBIT.w * t;
    strokeOrbitArc(cam, ORBIT, u - 1.2, u, AMBER, alpha * 0.9, 2.2, 0.6);
    const p = orbitPos(ORBIT, u);
    if (occluded(cam, p)) return;
    const q = project(cam, p);
    if (!q) return;
    dotGlow(q.x, q.y, 20, AMBER, alpha * 0.7);
    // the model grows out of the dot
    const g = easeOutBack(seg(t, T_MAP + 1.0, T_MAP + 1.6));
    const mx = clamp(q.x - 360, 330, 900);
    const my = clamp(q.y - 300, 190, 400);
    if (g > 0) {
      sx.save();
      sx.globalAlpha = alpha * clamp(g * 2);
      sx.strokeStyle = "rgba(245,245,247,0.5)";
      sx.lineWidth = 1.2;
      sx.beginPath();
      sx.moveTo(q.x, q.y);
      sx.lineTo(mx + 120, my + 120);
      sx.stroke();
      sx.beginPath();
      sx.roundRect(mx - 180, my - 120, 360, 240, 16);
      sx.fillStyle = "rgba(0,0,0,0.6)";
      sx.fill();
      sx.strokeStyle = "rgba(245,165,36,0.6)";
      sx.stroke();
      sx.restore();
      model(mx, my, 48 * g, 0.8 * t, alpha);
      maskText(sx, "SATELLITE · ITS 3D MODEL", mx, my + 150, 22, 600, AMBER, T_MAP + 1.4, T_END - 0.45, t, { tracking: 3, align: "center" });
    }
  }

  function icon(cx, cy, R, k) {
    if (k <= 0) return;
    sx.save();
    sx.translate(cx, cy);
    sx.rotate(Math.PI / 4);
    sx.scale(k, k);
    sx.strokeStyle = "rgba(245,245,247,0.6)";
    sx.lineWidth = 3;
    sx.strokeRect(-48, -48, 96, 96);
    sx.fillStyle = AMBER;
    [[-30, 20], [-6, 36], [18, 28]].forEach(([x, h]) => sx.fillRect(x, 34 - h, 16, h));
    sx.restore();
    dotGlow(cx, cy, R * 0.8, AMBER, 0.15 * k);
  }

  // ------------------------------------------------------------ subframe
  const ca = studio.cutPulses([[T_CHECK, 1], [T_LIGHT, 1], [T_MAP, 1], [T_END + 0.2, 1]]);
  const fade = (t) => seg(t, 0, 0.4);
  const flash = () => 0;

  function mapCamera(t) {
    const p = orbitPos(ORBIT, ORBIT.u0 + ORBIT.w * (T_MAP + 2));
    const dir = norm(add(norm(p), mul(norm(cross(p, [0, 1, 0])), -0.6)));
    const pos = mul(norm(add(dir, [0, 0.25, 0])), 3.2);
    return { ...lookAt(pos, [0, 0, 0], [0, 1, 0]), spin: 0.04 * t, off: [0.22, -0.05], zoom: 1, dist: 3.2 };
  }

  function drawSubframe(t, frameIndex) {
    beginSubframe();
    const out = easeInCubic(seg(t, T_END - 0.2, T_END + 0.4));
    const onMap = seg(t, T_MAP - 0.3, T_MAP + 0.3);
    if (onMap < 1) {
      renderEarth(limbCamera(t, 30, 20), t, 0.55 * seg(t, 0, 1.2) * (1 - onMap), 0.2, 40, 18);
      sx.drawImage(glc, 0, 0);
    }
    if (onMap > 0) {
      const cam = mapCamera(t);
      renderEarth(cam, t, onMap * (1 - out), 0.2, 55, 20);
      sx.save();
      sx.globalAlpha = onMap;
      sx.drawImage(glc, 0, 0);
      sx.restore();
      drawSatellites(cam, t + 4, t, 0.7 * onMap * (1 - out), false, SATS);
      drawMap(cam, t, onMap * (1 - seg(t, T_END - 0.3, T_END)));
    }
    if (t < T_CHECK + 0.2) drawOpen(t, 1 - seg(t, T_CHECK - 0.4, T_CHECK));
    if (t > T_CHECK - 0.05 && t < T_LIGHT + 0.1) drawCheck(t, seg(t, T_CHECK, T_CHECK + 0.3) * (1 - seg(t, T_LIGHT - 0.4, T_LIGHT)));
    if (t > T_LIGHT - 0.05 && t < T_MAP + 0.1) drawLight(t, seg(t, T_LIGHT, T_LIGHT + 0.3) * (1 - seg(t, T_MAP - 0.4, T_MAP)));
    scrim(1 - out);

    if (t < T_CHECK + 0.1) {
      eyebrow("SPACE DATA NETWORK · MODELS", 2, 0.4, T_CHECK - 0.45, t);
      block([["A 3D model", INK], ["for what's in orbit.", AMBER]], "SATELLITES, ROCKETS AND STATIONS", 0.5, T_CHECK - 0.4, t, 88);
    }
    if (t > T_CHECK - 0.05 && t < T_LIGHT + 0.1) {
      block([["Checked against", INK], ["the real spacecraft.", AMBER]], "SIZE, SOURCE AND LICENSE RECORDED FOR EACH", T_CHECK + 0.1, T_LIGHT - 0.4, t, 88);
    }
    if (t > T_LIGHT - 0.05 && t < T_MAP + 0.1) {
      block([["Light enough", INK], ["for a browser.", AMBER]], "LOW-POLYGON MODELS THAT LOAD IN AN INSTANT", T_LIGHT + 0.1, T_MAP - 0.4, t, 88);
    }
    if (t > T_MAP - 0.05 && t < T_END + 0.1) {
      block([["On the map,", INK], ["in the right shape.", AMBER]], "THE NETWORK'S 3D VIEWS SHOW THE REAL SPACECRAFT", T_MAP + 0.1, T_END - 0.4, t, 88);
    }
    lockup({ title: "Space Data Network Models", subtitle: "3D models of what is in orbit", url: "DIGITALARSENAL.GITHUB.IO/ASSET-MODELS", t0: T_END + 0.3, mark: false, icon }, t);
    bloom();
    hud(t, frameIndex, T_END + 0.1);
  }

  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });
  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
