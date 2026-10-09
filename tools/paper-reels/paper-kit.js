// What the whitepaper reels share on top of the showreel kit: typeset math
// drawn from pre-rendered SVG paths, dark panels, stat tiles, a keyed camera
// and the closing card for a paper.

import {
  W, H, AMBER, INK, MUTED, SANS,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutCubic,
  add, sub, mul, dot, cross, norm, lookAt, geoDir, canvas, project, occluded, orbitPos, TAN,
} from "../showreel/kit.js";
import { SVG } from "./math.gen.js";

function loadSvg(src) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("math svg"));
    img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(src)}`;
  });
}

// specs: { id: { key, px, color } }. Each equation is rasterized once at its
// drawn size, so the glyphs stay crisp; px is the em size in frame pixels.
export async function prepareMath(specs) {
  const out = {};
  await Promise.all(
    Object.entries(specs).map(async ([id, { key, px, color = INK }]) => {
      const src = SVG[key];
      if (!src) throw new Error(`no typeset math for ${key}`);
      const [, vy, vw, vh] = src.match(/viewBox="([^"]+)"/)[1].split(" ").map(Number);
      const s = px / 1000;
      const w = vw * s;
      const h = vh * s;
      const img = await loadSvg(src.replace("<svg ", `<svg width="${w}" height="${h}" `).replaceAll("currentColor", color));
      const c = canvas(Math.ceil(w) + 2, Math.ceil(h) + 2);
      c.getContext("2d").drawImage(img, 1, 1, w, h);
      out[id] = { c, w, h, base: -vy * s, color };
    }),
  );
  return out;
}

export function createPaperKit(studio, math) {
  const { sx, gx, font, maskText, pill } = studio;

  // An equation wiped in from the left and lifted out; y is its baseline.
  function eq(id, x, y, tIn, tOut, t, { align = "left", alpha = 1, glow = 0.12, dur = 0.7 } = {}) {
    const m = math[id];
    const inK = easeOutExpo(seg(t, tIn, tIn + dur));
    const outK = tOut == null ? 0 : easeInExpo(seg(t, tOut, tOut + 0.35));
    const a = alpha * clamp(inK * 1.6) * (1 - outK);
    if (a <= 0) return null;
    const left = align === "center" ? x - m.w / 2 : align === "right" ? x - m.w : x;
    const top = y - m.base + (1 - inK) * 18 - outK * 18;
    sx.save();
    sx.globalAlpha = a;
    sx.beginPath();
    sx.rect(left - 6, top - 20, (m.w + 12) * inK, m.h + 40);
    sx.clip();
    sx.drawImage(m.c, left - 1, top - 1);
    sx.restore();
    if (glow > 0) {
      gx.globalAlpha = a * glow;
      gx.drawImage(m.c, (left - 1) / 2, (top - 1) / 2, m.c.width / 2, m.c.height / 2);
    }
    return { left, top, w: m.w, h: m.h };
  }

  // A dark card behind a group; k scales it in.
  function panel(x, y, w, h, alpha, { stroke = "rgba(245,245,247,0.22)", fill = "rgba(10,10,12,0.78)", r = 18 } = {}) {
    if (alpha <= 0) return;
    sx.save();
    sx.globalAlpha = alpha;
    sx.beginPath();
    sx.roundRect(x, y, w, h, r);
    sx.fillStyle = fill;
    sx.fill();
    if (stroke) {
      sx.strokeStyle = stroke;
      sx.lineWidth = 1.5;
      sx.stroke();
    }
    sx.restore();
  }

  // Small tracked capitals, the label voice of the reels.
  function label(text, x, y, tIn, tOut, t, { px = 24, color = MUTED, align = "left", weight = 600, tracking = 3.5, family = SANS } = {}) {
    maskText(sx, text, x, y, px, weight, color, tIn, tOut, t, { tracking, align, family, outDur: 0.28 });
  }

  // A big number over a caption, scaled in.
  function stat(value, caption, x, y, tIn, tOut, t, { color = AMBER, px = 96, align = "left" } = {}) {
    maskText(sx, value, x, y, px, 700, color, tIn, tOut, t, { tracking: -2.5, align, outDur: 0.28 });
    maskText(sx, caption, x + (align === "left" ? 4 : 0), y + 48, 22, 600, MUTED, tIn + 0.12, tOut, t, { tracking: 3, align, outDur: 0.28 });
  }

  // A pill tag with text, scaled in from its center.
  function chip(text, cx, cy, k, alpha, { color = INK, px = 22, fill = "rgba(0,0,0,0.72)", stroke = null, family = SANS, tracking = 2.5 } = {}) {
    if (k <= 0 || alpha <= 0) return 0;
    sx.save();
    sx.font = font(600, px, family);
    sx.letterSpacing = `${tracking}px`;
    const w = sx.measureText(text).width + px * 1.8;
    const h = px * 2.1;
    sx.globalAlpha = alpha * clamp(k * 2);
    sx.translate(clamp(cx, w / 2 + 70, W - w / 2 - 70), cy);
    sx.scale(k, k);
    pill(-w / 2, -h / 2, w, h, stroke ?? color, fill);
    sx.fillStyle = color;
    sx.textAlign = "center";
    sx.fillText(text, 0, px * 0.36);
    sx.restore();
    return w;
  }

  // Closing card: the network's mark, the paper's title (one or two lines),
  // its authors and edition, revealed from t0 and held.
  function paperLockup({ eyebrow, title, byline, t0 }, t) {
    if (t < t0) return;
    const TP = 70;
    const lines = Array.isArray(title) ? title : [title];
    sx.save();
    sx.font = font(650, TP);
    sx.letterSpacing = "-1.5px";
    const tw = Math.max(...lines.map((l) => sx.measureText(l).width));
    sx.font = font(400, 28);
    sx.letterSpacing = "0px";
    const bw = sx.measureText(byline).width;
    sx.restore();
    const R = 104;
    const gap = 90;
    const total = 2 * R + gap + Math.max(tw, bw);
    const x0 = (W - total) / 2;
    const tx = x0 + 2 * R + gap;
    const lh = 82;
    const top = H / 2 - ((lines.length - 1) * lh) / 2 + 4;
    studio.drawMark(x0 + R, H / 2, R, t, {
      head: easeInOutCubic(seg(t, t0, t0 + 0.5)),
      ring: seg(t, t0 - 0.1, t0 + 0.2),
      nodes: seg(t, t0 + 0.52, t0 + 0.76),
      links: easeOutExpo(seg(t, t0 + 0.6, t0 + 0.82)),
      fade: seg(t, t0 - 0.1, t0 + 0.1),
    });
    maskText(sx, eyebrow, tx + 2, top - 92, 20, 600, AMBER, t0 + 0.84, null, t, { dur: 0.4, tracking: 4 });
    lines.forEach((l, i) => maskText(sx, l, tx, top + i * lh, TP, 650, INK, t0 + 0.72 + i * 0.08, null, t, { dur: 0.45, tracking: -1.5 }));
    maskText(sx, byline, tx + 2, top + (lines.length - 1) * lh + 60, 28, 400, MUTED, t0 + 0.8 + lines.length * 0.05, null, t, { dur: 0.4 });
  }

  // A world-space path pos(u) for u0..u1, hidden behind the planet.
  function strokePath(cam, pos, u0, u1, color, alpha, width, { dash = null, glowA = 0.5, n = 50 } = {}) {
    if (alpha <= 0 || u1 === u0) return;
    let prev = null;
    sx.save();
    sx.strokeStyle = color;
    sx.globalAlpha = alpha;
    sx.lineWidth = width;
    sx.lineCap = "round";
    if (dash) sx.setLineDash(dash);
    sx.beginPath();
    gx.strokeStyle = color;
    gx.globalAlpha = alpha * glowA;
    gx.lineWidth = width * 1.5;
    gx.beginPath();
    const N = Math.max(8, Math.ceil(Math.abs(u1 - u0) * n));
    for (let i = 0; i <= N; i++) {
      const p = pos(lerp(u0, u1, i / N));
      const q = project(cam, p);
      const hid = occluded(cam, p);
      if (q && prev && !hid && !prev.hid) {
        sx.moveTo(prev.x, prev.y);
        sx.lineTo(q.x, q.y);
        gx.moveTo(prev.x / 2, prev.y / 2);
        gx.lineTo(q.x / 2, q.y / 2);
      }
      prev = q ? { ...q, hid } : null;
    }
    sx.stroke();
    if (glowA > 0) gx.stroke();
    sx.restore();
  }
  // A satellite dot with a glow, where it is visible.
  function head(cam, p, color, alpha, r = 6) {
    if (alpha <= 0 || occluded(cam, p)) return null;
    const q = project(cam, p);
    if (!q) return null;
    sx.save();
    sx.globalAlpha = alpha;
    sx.fillStyle = color;
    sx.beginPath();
    sx.arc(q.x, q.y, r, 0, Math.PI * 2);
    sx.fill();
    sx.restore();
    studio.dotGlow(q.x, q.y, r * 4, color, alpha * 0.7);
    studio.dotGlow(q.x, q.y, r * 1.3, "#ffffff", alpha * 0.6);
    return q;
  }

  return { eq, panel, label, stat, chip, paperLockup, strokePath, head };
}

// The argument of latitude u on orbit o that puts the satellite nearest the
// screen point [x, y] under camera cam, with the path from u to u + ahead
// in front of the planet and moving rightward (dir 1) or leftward (dir -1).
export function phaseFor(o, cam, [x, y], ahead = 1, dir = 1) {
  let best = 0;
  let score = -Infinity;
  for (let u = 0; u < Math.PI * 2; u += 0.005) {
    let sc = 0;
    for (let f = 0; f <= 1; f += 0.125) {
      const p = orbitPos(o, u + f * ahead);
      const q = project(cam, p);
      if (!q || occluded(cam, p) || q.x < 60 || q.x > W - 60 || q.y < 100 || q.y > H - 120) sc -= 5000;
    }
    const q = project(cam, orbitPos(o, u));
    const q2 = project(cam, orbitPos(o, u + 0.05));
    if (!q || !q2) continue;
    sc -= Math.hypot(q.x - x, q.y - y);
    if ((q2.x - q.x) * dir < 0) sc -= 3000;
    if (sc > score) {
      score = sc;
      best = u;
    }
  }
  return best;
}

// An orbit of radius r whose satellite sits at screen point [x, y] (on the
// near side of the planet) at u = 0 and moves along screen direction
// [dx, dy] from there; tilt opens the plane away from edge-on.
export function orbitThrough(cam, [x, y], r, [dx, dy], tilt = 0.8) {
  const tan = TAN / cam.zoom;
  const vx = ((x / W) * 2 - 1 - cam.off[0]) * tan * (W / H);
  const vy = (1 - (y / H) * 2 - cam.off[1]) * tan;
  const d = norm(add(add(cam.f, mul(cam.r, vx)), mul(cam.u, vy)));
  const b = dot(cam.pos, d);
  const c = dot(cam.pos, cam.pos) - r * r;
  const s = -b - Math.sqrt(Math.max(0, b * b - c));
  const P = add(cam.pos, mul(d, s));
  const T = add(mul(cam.r, dx), mul(cam.u, -dy));
  // Tilt the plane off the line of sight so the orbit reads as an ellipse.
  const e1 = norm(P);
  const fp = norm(sub(cam.f, mul(e1, dot(cam.f, e1))));
  const N = norm(add(norm(cross(P, T)), mul(fp, tilt)));
  return { e1, e2: norm(cross(N, e1)), r };
}

// Camera keys [t, lat, lon, dist, offX, offY], eased between; the camera
// looks at the Earth's center from (lat, lon), dist Earth radii out.
export function keyedCamera(keys, spinRate = 0.03) {
  return (t) => {
    let i = 0;
    while (i < keys.length - 2 && t >= keys[i + 1][0]) i++;
    const [t0, la0, lo0, r0, ox0, oy0] = keys[i];
    const [t1, la1, lo1, r1, ox1, oy1] = keys[i + 1];
    const k = easeInOutCubic(seg(t, t0, t1));
    const dir = norm(add(mul(geoDir(la0, lo0, 0), 1 - k), mul(geoDir(la1, lo1, 0), k)));
    const dist = lerp(r0, r1, k);
    return { ...lookAt(mul(dir, dist), [0, 0, 0], [0, 1, 0]), spin: spinRate * t, off: [lerp(ox0, ox1, k), lerp(oy0, oy1, k)], zoom: 1, dist };
  };
}
