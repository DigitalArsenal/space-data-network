// Space Data Network showreel: a deterministic 45-second motion-graphics piece
// for the spacedatanetwork.org hero. The shared studio (Earth, drawing kit,
// motion blur, bloom, finishing pass) is ../kit.js.
// Open showreel.html?reel=sdn&t=<seconds> to inspect a single frame.

import {
  W, H, FPS, AMBER, CYAN, SAT, RED, INK, MUTED, SANS, MONO, D, FRAME_T,
  clamp, lerp, seg, easeOutExpo, easeInExpo, easeInOutExpo, easeInOutCubic, easeOutCubic, easeInCubic, easeOutBack,
  v3, add, sub, mul, dot, cross, len, norm, mix3, rng, makeTau,
  OMEGA_K, orbitBasis, orbitPos, makeSky, lookAt, geoDir, TAN, distForRadius, project, occluded, createStudio,
} from "../kit.js";

export const meta = { name: "sdn-showreel", media: "docs/media/showreel", poster: 645, audio: { track: "orbital-insertion", start: 40 } };

const DURATION = 45; // 41 s of motion, then a hold on the lockup

// The through line: three shifts, each a problem struck through and rewritten
// as the network's answer. Chapter starts and the moments each word turns, in
// seconds; ignition and the crowded sky run from 0.
const T_ORG = 7.0; // disorganized → organized
const M1 = T_ORG + 2.5;
const T_SLOW = 13.4; // slow → fast
const M2 = 15.45;
const T_NET = 16.0; // …fast, on the network
const T_STORE = T_NET + 3.9; // the storefront
const T_SEC = 23.0; // insecure, unattributed → secure, attributed
const M3 = T_SEC + 2.2;
const T_SEAL = T_SEC + 5.0;
const T_CONJ = 30.4; // the payoff: a close approach everyone can see
const T_TCA = T_CONJ + 4.6;
const T_END = 39.0; // pull back to the lockup

// --------------------------------------------------------------- time ramp
// Scene time τ runs at a variable rate: the conjunction pair creeps into
// frame, all but stops at closest approach, and the finale snaps forward.
// τ(t) is integrated once.
function rate(t) {
  const conj = seg(t, T_CONJ - 0.05, T_CONJ + 0.05) * (1 - seg(t, T_END - 0.3, T_END + 0.15));
  let r = lerp(1, 0.24, conj);
  r -= 0.2 * seg(t, T_TCA - 0.8, T_TCA + 0.4) * (1 - seg(t, T_END - 0.3, T_END)); // near-freeze through TCA
  r += 3.0 * seg(t, T_END - 0.3, T_END + 0.15) * (1 - seg(t, T_END + 0.75, T_END + 1.15)); // snap out
  return r;
}
const tau = makeTau(rate, DURATION);

const SATS = makeSky();

// The amber satellite from the mark keeps flying through the whole piece.
const HERO = { ...orbitBasis(58 * D, 300 * D), r: 1.22, u0: 0.4, w: OMEGA_K * Math.pow(1.22, -1.5) };

// Conjunction: two orbits at nearly the same altitude, phased to cross at τ_TCA.
const CONJ = (() => {
  const A = { ...orbitBasis(51.6 * D, 25 * D), r: 1.1 };
  const B = { ...orbitBasis(97.6 * D, 105 * D), r: 1.1025 };
  const nA = cross(A.e1, A.e2);
  const nB = cross(B.e1, B.e2);
  let X = norm(cross(nA, nB));
  const uAt = (o, p) => Math.atan2(dot(p, o.e2), dot(p, o.e1));
  A.w = OMEGA_K * Math.pow(A.r, -1.5);
  B.w = OMEGA_K * Math.pow(B.r, -1.5);
  const tTCA = tau(T_TCA);
  A.u0 = uAt(A, X) - A.w * tTCA;
  B.u0 = uAt(B, X) - B.w * tTCA + 0.0035;
  return { A, B, X, tTCA };
})();

// Ground nodes (latitude, longitude) and the peer links between them.
const NODES = [
  [30.27, -97.74], [40.01, -105.27], [52.01, 4.36], [50.11, 8.68], [40.71, -74.0],
  [51.5, -0.12], [-23.55, -46.63], [-1.29, 36.82], [35.68, 139.69], [1.35, 103.82],
  [-33.87, 151.21], [38.83, -104.82], [43.6, 1.44], [12.97, 77.59], [28.39, -80.6],
  [64.14, -21.94], [19.43, -99.13], [45.5, -73.57], [59.33, 18.07], [37.57, 126.98],
];
const LINKS = [
  [0, 1], [0, 4], [1, 11], [4, 5], [5, 2], [2, 3], [3, 12], [5, 15], [4, 17], [0, 16],
  [14, 4], [6, 14], [6, 7], [3, 7], [7, 13], [13, 9], [9, 10], [9, 8], [8, 19], [2, 18],
  [3, 13], [16, 6], [15, 17], [11, 14],
];
// Hops from the publishing node (New York): a record fans out one hop at a time.
const SOURCE = 4;
const HOPS = (() => {
  const h = NODES.map(() => Infinity);
  h[SOURCE] = 0;
  const queue = [SOURCE];
  while (queue.length) {
    const i = queue.shift();
    for (const [a, b] of LINKS) {
      const j = a === i ? b : b === i ? a : -1;
      if (j >= 0 && h[j] === Infinity) {
        h[j] = h[i] + 1;
        queue.push(j);
      }
    }
  }
  return h;
})();
// Listings on the storefront beat: node, what it offers, free or for sale,
// and where the tag sits relative to the node (negative dx: tag to the left).
const STORE = [
  [17, "ORBIT SOFTWARE", "FOR SALE", 34, -78],
  [5, "SATELLITE ORBITS", "FREE", -40, -76],
  [16, "RADAR TRACKING", "FOR SALE", 34, 52],
  [3, "COLLISION ALERTS", "FOR SALE", -40, 72],
  [15, "SPACE WEATHER", "FREE", 34, -56],
];

// ------------------------------------------------------------------ camera
function ringRadius(t) {
  // The mark's ring: 150 px at rest, swelling into the planet's limb.
  const grow = easeInExpo(seg(t, 1.5, 2.25));
  return lerp(150, 430, grow);
}

// Where the camera rests from the crowded sky through the slow half.
function catalogCamera(t) {
  const k = easeInOutCubic(seg(t, 1.6, 5.0));
  const lon = lerp(-62, -30, k) + 12 * seg(t, 5.0, T_NET);
  const lat = lerp(18, 26, k);
  const d0 = distForRadius(ringRadius(Math.min(t, 2.25)));
  const dist = lerp(d0, 7.4, easeInOutCubic(seg(t, 2.35, 4.6))) - 0.7 * easeInOutCubic(seg(t, 4.9, T_ORG));
  const s = easeInOutCubic(seg(t, 4.85, 5.55));
  // A zoom kick on the hard cut into the slow half.
  const zoom = t >= T_SLOW ? 1 + 0.18 * (1 - easeOutExpo(seg(t, T_SLOW, T_SLOW + 0.5))) : 1;
  return { dir: geoDir(lat, lon, 0), dist, off: [0.36 * s, -0.03 * s], zoom };
}

function cameraAt(t) {
  const spin = 0.045 * t;
  let dir;
  let dist;
  let off;
  let zoom = 1;
  if (t < T_NET) {
    ({ dir, dist, off, zoom } = catalogCamera(t));
  } else if (t < T_CONJ) {
    // A whip down to a hemisphere of nodes, then a slow pan east; it rests
    // there, blurred, under the third shift.
    const from = catalogCamera(T_NET);
    const k = easeOutExpo(seg(t, T_NET, T_NET + 1.1));
    const p = seg(t, T_NET, T_SEC);
    if (t >= T_SEC) zoom = 1 + 0.18 * (1 - easeOutExpo(seg(t, T_SEC, T_SEC + 0.5)));
    dir = norm(mix3(from.dir, geoDir(lerp(30, 36, p), lerp(-58, -34, p), spin), k));
    dist = lerp(from.dist, 2.75, k);
    off = [lerp(from.off[0], 0.42, k), lerp(from.off[1], -0.1, k)];
  } else {
    // Conjunction close-up, then the pull back to the whole network.
    const X = CONJ.X;
    const side = norm(cross(X, [0, 1, 0]));
    const push = lerp(1.56, 1.5, easeInOutCubic(seg(t, T_CONJ, T_TCA)));
    const camNear = add(add(mul(X, push), mul(side, 0.34)), mul(norm(cross(side, X)), 0.12));
    const targetNear = mul(X, 1.08);
    const drift = seg(t, T_CONJ, T_END + 0.15);
    const near = add(camNear, mul(side, -0.12 * drift));
    const pull = easeInOutExpo(seg(t, T_END, T_END + 1.1));
    const farDir = norm(add(mul(X, 1), [0, 0.35, 0]));
    const shrink = easeInExpo(seg(t, T_END + 0.85, T_END + 1.5));
    const farDist = lerp(6.2, distForRadius(150), shrink);
    const pos = mix3(near, mul(farDir, farDist), pull);
    const target = mix3(targetNear, [0, 0, 0], pull);
    const cam = lookAt(pos, target, [0, 1, 0]);
    return { ...cam, spin, off: [lerp(0.14, 0, pull), lerp(0.1, 0, pull)], zoom: 1, dist: len(pos) };
  }
  const pos = mul(dir, dist);
  const cam = lookAt(pos, [0, 0, 0], [0, 1, 0]);
  return { ...cam, spin, off, zoom, dist };
}

// ------------------------------------------------------------- the renderer
export async function createReel(base = "") {
  const studio = await createStudio({ base, hudTitle: "SPACE DATA NETWORK", hudUrl: "SPACEDATANETWORK.ORG" });
  const {
    glc, sx, gx, font, maskText, GLYPHS, decodeText, dotGlow, drawMark, hud, scrim, pill, icon, badge, padlock, keyIcon,
    bezierPts, pointOn, strokeOn, stamp, renderEarth, drawOrbitPath, arcPoint, strokeArc,
    LX, CAP_Y, BIG, EYE, CAP, LETTERS, lineYs, caption, block, shiftBlock, questionBadge, beginSubframe, bloom,
  } = studio;
  const drawSatellites = (cam, t, T, alpha, spawn) => studio.drawSatellites(cam, t, T, alpha, spawn, SATS);


  // Hex bytes for the record scene: a stable pseudo-FlatBuffer.
  const HEX = (() => {
    const R = rng(42);
    const rows = [];
    for (let i = 0; i < 64; i++) {
      const bytes = [];
      for (let j = 0; j < 8; j++) bytes.push(Math.floor(R() * 65536).toString(16).padStart(4, "0"));
      rows.push({ off: (i * 16).toString(16).padStart(6, "0"), bytes });
    }
    return rows;
  })();


  function drawNetwork(cam, t, alpha) {
    const spin = cam.spin;
    const n0 = T_NET;
    const P = NODES.map(([lat, lon]) => geoDir(lat, lon, spin));
    const screen = (p) => {
      const s = mul(p, 1.004);
      return occluded(cam, s) ? null : project(cam, s);
    };
    // links: the whole mesh lights up fast
    LINKS.forEach(([i, j], k) => {
      const t0 = n0 + 0.25 + k * 0.02;
      const draw = easeInOutCubic(seg(t, t0, t0 + 0.35));
      if (draw <= 0) return;
      strokeArc(cam, P[i], P[j], draw, AMBER, 1.4, alpha * 0.55, AMBER, alpha * 0.35);
      // packets riding the link once it exists
      if (draw >= 1) {
        for (let n = 0; n < 2; n++) {
          const ph = (tau(t) * 0.55 + k * 0.137 + n * 0.5) % 1;
          const dir = (k + n) % 2 ? ph : 1 - ph;
          const p = arcPoint(P[i], P[j], dir);
          const q = project(cam, p);
          if (!q || occluded(cam, p)) continue;
          sx.save();
          sx.globalAlpha = alpha;
          sx.fillStyle = "#fff4dc";
          sx.fillRect(q.x - 2.5, q.y - 2.5, 5, 5);
          sx.restore();
          dotGlow(q.x, q.y, 12, AMBER, alpha * 0.6);
        }
      }
    });
    // nodes
    P.forEach((p, i) => {
      const t0 = n0 + 0.05 + i * 0.025;
      const k = seg(t, t0, t0 + 0.4);
      if (k <= 0) return;
      const q = screen(p);
      if (!q) return;
      const sc = easeOutBack(k);
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = AMBER;
      sx.beginPath();
      sx.arc(q.x, q.y, 4 * sc, 0, Math.PI * 2);
      sx.fill();
      const pulse = (tau(t) * 0.8 + i * 0.21) % 1;
      sx.strokeStyle = AMBER;
      sx.globalAlpha = alpha * (1 - pulse) * 0.8;
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.arc(q.x, q.y, 4 + pulse * 26, 0, Math.PI * 2);
      sx.stroke();
      sx.restore();
      dotGlow(q.x, q.y, 16 * sc, AMBER, alpha * 0.45);
    });

    // New orbit data, published once, races out hop by hop.
    const P0 = n0 + 0.9;
    const HOP = 0.22;
    const wave = alpha * seg(t, P0 - 0.3, P0) * (1 - seg(t, n0 + 3.4, n0 + 3.75));
    if (wave > 0) {
      LINKS.forEach(([i, j]) => {
        if (HOPS[i] === HOPS[j]) return;
        const [a, b] = HOPS[i] < HOPS[j] ? [i, j] : [j, i];
        const t0 = P0 + HOPS[a] * HOP;
        const f = easeInOutCubic(seg(t, t0, t0 + 0.3));
        if (f <= 0) return;
        strokeArc(cam, P[a], P[b], f, "#fff1d0", 2.4, wave * 0.9, AMBER, wave * 0.7, 40);
        if (f < 1) {
          const p = arcPoint(P[a], P[b], f);
          const q = project(cam, p);
          if (q && !occluded(cam, p)) {
            dotGlow(q.x, q.y, 20, AMBER, wave);
            dotGlow(q.x, q.y, 6, "#ffffff", wave);
          }
        }
      });
      P.forEach((p, i) => {
        const landed = P0 + HOPS[i] * HOP + (HOPS[i] ? 0.3 : 0);
        const k = seg(t, landed, landed + 0.6);
        if (k <= 0 || k >= 1) return;
        const q = screen(p);
        if (!q) return;
        sx.save();
        sx.globalAlpha = wave * (1 - k);
        sx.strokeStyle = "#fff1d0";
        sx.lineWidth = 2;
        sx.beginPath();
        sx.arc(q.x, q.y, 6 + easeOutExpo(k) * 44, 0, Math.PI * 2);
        sx.stroke();
        sx.restore();
        dotGlow(q.x, q.y, 30 * (1 - k), AMBER, wave * (1 - k));
      });
      const q = screen(P[SOURCE]);
      const ta = wave * seg(t, P0 - 0.25, P0);
      if (q && ta > 0) {
        const label = "NEW ORBIT DATA";
        sx.save();
        sx.globalAlpha = ta;
        sx.font = font(600, 14, SANS);
        sx.letterSpacing = "2.5px";
        const w = sx.measureText(label).width + 40;
        const lx = q.x + 34;
        const ly = q.y - 76;
        sx.strokeStyle = "rgba(245,245,247,0.6)";
        sx.lineWidth = 1.2;
        sx.beginPath();
        sx.moveTo(q.x, q.y);
        sx.lineTo(lx + 16, ly + 34);
        sx.stroke();
        pill(lx, ly, w, 34, AMBER, "rgba(0,0,0,0.62)");
        sx.fillStyle = AMBER;
        sx.textAlign = "left";
        sx.fillText(label, lx + 20, ly + 22.5);
        sx.restore();
      }
    }

    // The storefront: listings, free or for sale, straight from each node.
    const S0 = T_STORE + 0.25;
    STORE.forEach(([i, name, price, dx, dy], k) => {
      const t0 = S0 + k * 0.12;
      const pop = seg(t, t0, t0 + 0.35);
      const a = alpha * clamp(pop * 3) * (1 - seg(t, T_SEC - 0.45, T_SEC - 0.2));
      if (a <= 0) return;
      const q = screen(P[i]);
      if (!q) return;
      const sale = price !== "FREE";
      sx.save();
      sx.font = font(600, 14, SANS);
      sx.letterSpacing = "2px";
      const w1 = sx.measureText(name).width;
      const w2 = sx.measureText(price).width;
      const w = 22 + w1 + 26 + w2 + 20;
      const h = 34;
      const right = dx > 0;
      const lx = right ? q.x + dx : q.x + dx - w;
      const ly = q.y + dy - h / 2;
      sx.globalAlpha = a;
      sx.strokeStyle = "rgba(245,245,247,0.6)";
      sx.lineWidth = 1.2;
      sx.beginPath();
      sx.moveTo(q.x, q.y);
      sx.lineTo(right ? lx : lx + w, ly + h / 2);
      sx.stroke();
      const sc = easeOutBack(pop);
      const ax = right ? lx : lx + w;
      sx.translate(ax, ly + h / 2);
      sx.scale(sc, sc);
      sx.translate(-ax, -(ly + h / 2));
      pill(lx, ly, w, h, sale ? AMBER : "rgba(245,245,247,0.55)", "rgba(0,0,0,0.62)");
      sx.textAlign = "left";
      sx.fillStyle = INK;
      sx.fillText(name, lx + 20, ly + 22.5);
      sx.strokeStyle = "rgba(245,245,247,0.3)";
      sx.beginPath();
      sx.moveTo(lx + 20 + w1 + 12, ly + 9);
      sx.lineTo(lx + 20 + w1 + 12, ly + h - 9);
      sx.stroke();
      sx.fillStyle = sale ? AMBER : INK;
      sx.fillText(price, lx + 20 + w1 + 26, ly + 22.5);
      sx.restore();
    });
  }

  const SHIFTS = [
    {
      before: ["Disorganized."],
      after: ["Organized."],
      why: "EVERY SOURCE HAS ITS OWN WEBSITE, LOGIN AND FILE TYPE",
      fix: "ONE OPEN STANDARD ANY SOFTWARE CAN READ",
      tIn: T_ORG + 0.1,
      tMorph: M1,
      tOut: T_SLOW - 0.4,
    },
    {
      before: ["Slow."],
      after: ["Fast."],
      why: "EVERYONE DOWNLOADS THE SAME BIG FILES, AGAIN AND AGAIN",
      fix: "PUBLISHED ONCE, PASSED FROM NODE TO NODE",
      tIn: T_SLOW + 0.1,
      tMorph: M2,
      tOut: T_STORE - 0.35,
    },
    {
      before: ["Insecure.", "Unattributed."],
      after: ["Secure.", "Attributed."],
      why: "NO PROOF OF WHO MADE A FILE, OR THAT NOBODY CHANGED IT",
      fix: "DIGITALLY SIGNED AT THE SOURCE, CHECKED BY EVERY NODE",
      tIn: T_SEC + 0.1,
      tMorph: M3,
      tOut: T_CONJ - 0.4,
      fixOut: T_SEAL - 0.3,
    },
  ];

  // --------------------------------------- shift 1: disorganized → organized
  // Where space data lives today: separate sites, logins and file types.
  const SOURCES = [
    [1230, 300, 250, 150, -6, "login", "WEB PORTAL", false],
    [1540, 250, 220, 160, 4, "text", "TEXT FILE", true],
    [1780, 380, 170, 130, -3, "doc", "PDF", false],
    [1190, 530, 230, 150, 3, "sheet", "SPREADSHEET", true],
    [1480, 500, 210, 140, -8, "mail", "EMAIL", false],
    [1760, 620, 180, 150, 6, "login", "LOGIN", false],
    [1250, 760, 240, 150, -4, "text", "TEXT FILE", false],
    [1520, 770, 230, 160, 7, "sheet", "DOWNLOAD", true],
    [1780, 840, 160, 120, -5, "doc", "ARCHIVE", false],
  ];
  const GRID_NAMES = ["HUBBLE", "TIANGONG", "NOAA 20", "AQUA", "ISS", "SENTINEL-2A", "GOES-16", "TERRA", "LANDSAT 9"];
  const JIT = SOURCES.map((_, i) => {
    const R = rng(500 + i);
    return [(R() - 0.5) * 320, (R() - 0.5) * 220, R() * 6, R()];
  });
  function kindBody(kind, L, T, w, h, seed) {
    const R = rng(seed);
    sx.strokeStyle = "rgba(245,245,247,0.3)";
    sx.fillStyle = "rgba(245,245,247,0.35)";
    sx.lineWidth = 1.2;
    if (kind === "login") {
      for (let j = 0; j < 2; j++) {
        sx.beginPath();
        sx.roundRect(L + 16, T + 14 + j * 28, w - 32, 20, 4);
        sx.stroke();
      }
      sx.fillStyle = "rgba(245,245,247,0.28)";
      sx.beginPath();
      sx.roundRect(L + 16, T + 72, 74, 22, 11);
      sx.fill();
    } else if (kind === "text") {
      for (let y = T + 16; y < T + h - 8; y += 14) sx.fillRect(L + 16, y, (w - 32) * (0.45 + 0.55 * R()), 5);
    } else if (kind === "sheet") {
      const gw = w - 28;
      const gh = h - 20;
      for (let c = 0; c <= 4; c++) {
        sx.beginPath();
        sx.moveTo(L + 14 + (gw * c) / 4, T + 10);
        sx.lineTo(L + 14 + (gw * c) / 4, T + 10 + gh);
        sx.stroke();
      }
      for (let r = 0; r <= 4; r++) {
        sx.beginPath();
        sx.moveTo(L + 14, T + 10 + (gh * r) / 4);
        sx.lineTo(L + 14 + gw, T + 10 + (gh * r) / 4);
        sx.stroke();
      }
    } else if (kind === "doc") {
      const x = L + 16;
      const y = T + 12;
      sx.beginPath();
      sx.moveTo(x, y);
      sx.lineTo(x + 32, y);
      sx.lineTo(x + 44, y + 12);
      sx.lineTo(x + 44, y + 56);
      sx.lineTo(x, y + 56);
      sx.closePath();
      sx.moveTo(x + 32, y);
      sx.lineTo(x + 32, y + 12);
      sx.lineTo(x + 44, y + 12);
      sx.stroke();
      for (let j = 0; j < 4; j++) sx.fillRect(x + 58, y + 6 + j * 13, (w - 90) * (0.5 + 0.5 * R()), 5);
    } else if (kind === "mail") {
      const x = L + w / 2 - 45;
      const y = T + h / 2 - 30;
      sx.beginPath();
      sx.rect(x, y, 90, 56);
      sx.moveTo(x, y);
      sx.lineTo(x + 45, y + 32);
      sx.lineTo(x + 90, y);
      sx.stroke();
    }
  }
  function drawSources(t) {
    const s0 = T_ORG;
    const s1 = T_SLOW;
    if (t < s0 || t > s1 + 0.02) return;
    const out = easeInExpo(seg(t, s1 - 0.35, s1 - 0.05));
    const focus = easeInOutExpo(seg(t, s0 + 4.4, s0 + 4.9));
    const order = [0, 1, 2, 3, 5, 6, 7, 8, 4]; // the ISS card last, so it can grow over the rest
    order.forEach((i) => {
      const [cx, cy, w0, h0, rot0, kind, label, unknown] = SOURCES[i];
      const tin = s0 + 0.12 + i * 0.05;
      const e = easeOutBack(seg(t, tin, tin + 0.4));
      if (e <= 0) return;
      const m = easeInOutExpo(seg(t, M1 + 0.15 + i * 0.03, M1 + 0.75 + i * 0.03));
      const gx0 = 1245 + (i % 3) * 252;
      const gy0 = 348 + Math.floor(i / 3) * 172;
      const [jx, jy, ph] = JIT[i];
      const wob = 1 - m;
      let x = lerp(cx + jx * (1 - e), gx0, m) + wob * 7 * Math.sin(t * 2.3 + ph);
      let y = lerp(cy + jy * (1 - e), gy0, m) + wob * 5 * Math.cos(t * 1.9 + ph * 1.3);
      const r = lerp(rot0 + 2.5 * Math.sin(t * 1.7 + ph), 0, m) * D;
      let w = lerp(w0, 230, m);
      let h = lerp(h0, 150, m);
      const center = i === 4;
      const detail = center ? focus : 0;
      if (center) {
        w = lerp(w, 560, focus);
        h = lerp(h, 360, focus);
        x = lerp(x, 1497, focus);
        y = lerp(y, 520, focus);
      }
      const a = clamp(e * 2) * (1 - out) * (center ? 1 : 1 - 0.78 * focus);
      if (a <= 0) return;
      const L = -w / 2;
      const T = -h / 2;
      sx.save();
      sx.globalAlpha = a;
      sx.translate(x, y - out * 40);
      sx.rotate(r);
      sx.beginPath();
      sx.roundRect(L, T, w, h, 10);
      sx.fillStyle = "rgba(16,16,20,0.92)";
      sx.fill();
      sx.strokeStyle = m > 0.5 ? `rgba(245,165,36,${0.3 + 0.4 * m})` : "rgba(245,245,247,0.35)";
      sx.lineWidth = 1.5;
      sx.stroke();
      sx.fillStyle = "rgba(245,245,247,0.07)";
      sx.fillRect(L + 1, T + 1, w - 2, 28);
      sx.font = font(600, 12, SANS);
      sx.letterSpacing = "2px";
      sx.textAlign = "left";
      if (m < 1) {
        sx.globalAlpha = a * (1 - m);
        sx.fillStyle = MUTED;
        sx.fillText(label, L + 12, T + 19);
        kindBody(kind, L, T + 28, w, h - 28, 40 + i);
      }
      if (m > 0) {
        sx.globalAlpha = a * m;
        sx.font = font(600, 12, SANS);
        sx.letterSpacing = "2px";
        sx.fillStyle = AMBER;
        sx.fillText(center && detail > 0.5 ? "INTERNATIONAL SPACE STATION" : GRID_NAMES[i], L + 12, T + 19);
        // every card the same three rows: where it is, how fast, how sure
        sx.globalAlpha = a * m * (1 - detail);
        ["POSITION", "SPEED", "UNCERTAINTY"].forEach((row, j) => {
          const ry = T + 28 + 30 + j * 30;
          sx.font = font(600, 11, SANS);
          sx.letterSpacing = "1.5px";
          sx.fillStyle = MUTED;
          sx.fillText(row, L + 14, ry);
          sx.fillStyle = j === 2 ? "rgba(245,165,36,0.8)" : "rgba(245,245,247,0.55)";
          sx.fillRect(L + w * 0.52, ry - 7, w * (j === 2 ? 0.22 : 0.36), 6);
        });
      }
      if (detail > 0) {
        sx.globalAlpha = a * seg(detail, 0.5, 1);
        [
          ["AS OF", "25 Sep 2026 · 12:00 UTC"],
          ["ALTITUDE", "418 km"],
          ["SPEED", "7.66 km/s"],
          ["UNCERTAINTY", "± 25 m"],
        ].forEach(([k, v], j) => {
          const ry = T + 28 + 72 + j * 70;
          sx.font = font(600, 15, SANS);
          sx.letterSpacing = "2.5px";
          sx.fillStyle = MUTED;
          sx.fillText(k, L + 28, ry);
          sx.font = font(600, 28, SANS);
          sx.letterSpacing = "0px";
          sx.fillStyle = j === 3 ? AMBER : INK;
          decodeText(sx, v, L + 200, ry + 2, s0 + 4.75 + j * 0.08, 0.35, t, 70 + j);
        });
      }
      sx.restore();
      // corner marks: unknown origin before, a check once organized
      const cxr = x + Math.cos(r) * (w / 2 - 4) - Math.sin(r) * (-h / 2 + 4);
      const cyr = y - out * 40 + Math.sin(r) * (w / 2 - 4) + Math.cos(r) * (-h / 2 + 4);
      if (unknown) questionBadge(cxr, cyr, 30, a * (1 - m));
      const ck = seg(t, M1 + 0.95 + i * 0.04, M1 + 1.25 + i * 0.04);
      if (ck > 0) badge("check", cxr - 10, cyr + 24, 22 * easeOutBack(ck), AMBER, a * (1 - detail));
    });
  }

  // ------------------------------------------------ shift 2: slow → fast
  // One website, everyone pulling the same big files, all of it crawling.
  const USERS = Array.from({ length: 10 }, (_, i) => {
    const R = rng(80 + i);
    return { ang: -Math.PI / 2 + (i / 10) * Math.PI * 2 + (R() - 0.5) * 0.3, sp: 0.5 + R() * 0.9, p0: 0.04 + R() * 0.22, ph: R() * 6 };
  });
  function drawHub(t) {
    const h0 = T_SLOW;
    const h1 = T_NET;
    if (t < h0 || t > h1 + 0.02) return;
    const alpha = 1 - easeInExpo(seg(t, h1 - 0.3, h1 - 0.02));
    const cx = 1480;
    const cy = 470;
    USERS.forEach((u, i) => {
      const ux = cx + Math.cos(u.ang) * 370;
      const uy = cy + Math.sin(u.ang) * 250;
      const t0 = h0 + 0.15 + i * 0.04;
      const k = easeOutExpo(seg(t, t0, t0 + 0.35));
      if (k <= 0) return;
      sx.save();
      sx.globalAlpha = alpha * k * 0.6;
      sx.strokeStyle = "rgba(245,245,247,0.5)";
      sx.lineWidth = 1.2;
      sx.setLineDash([4, 6]);
      sx.beginPath();
      sx.moveTo(cx, cy);
      sx.lineTo(lerp(cx, ux, k), lerp(cy, uy, k));
      sx.stroke();
      sx.restore();
      const prog = (tt) => clamp(u.p0 + 0.1 * u.sp * Math.max(0, tt - t0));
      const p = prog(t);
      // the same heavy file, inching out to each user
      const fx = lerp(cx, ux, 0.3 + 0.45 * p);
      const fy = lerp(cy, uy, 0.3 + 0.45 * p);
      sx.save();
      sx.globalAlpha = alpha * k;
      sx.fillStyle = "rgba(245,245,247,0.75)";
      sx.fillRect(fx - 7, fy - 9, 14, 18);
      sx.fillStyle = "#101014";
      sx.fillRect(fx - 4, fy - 5, 8, 2);
      sx.fillRect(fx - 4, fy - 1, 8, 2);
      // the user, a spinner, a progress bar that barely moves
      sx.fillStyle = "rgba(245,245,247,0.85)";
      sx.beginPath();
      sx.arc(ux, uy, 8 * k, 0, Math.PI * 2);
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.55)";
      sx.lineWidth = 2;
      sx.lineCap = "round";
      const a0 = t * 5 + u.ph;
      sx.beginPath();
      sx.arc(ux, uy, 17, a0, a0 + 1.6);
      sx.stroke();
      sx.fillStyle = "rgba(245,245,247,0.15)";
      sx.fillRect(ux - 48, uy + 28, 96, 6);
      sx.fillStyle = "rgba(245,245,247,0.7)";
      sx.fillRect(ux - 48, uy + 28, 96 * p, 6);
      sx.font = font(500, 13, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = MUTED;
      sx.textAlign = "center";
      sx.fillText(`${Math.floor(prog(FRAME_T) * 100)}%`, ux, uy + 54);
      sx.restore();
    });
    const hk = easeOutBack(seg(t, h0 + 0.05, h0 + 0.4));
    if (hk > 0) {
      sx.save();
      sx.globalAlpha = alpha * clamp(hk * 2);
      sx.translate(cx, cy);
      sx.scale(hk, hk);
      sx.beginPath();
      sx.roundRect(-100, -58, 200, 116, 14);
      sx.fillStyle = "rgba(16,16,20,0.95)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.5)";
      sx.lineWidth = 1.5;
      sx.stroke();
      for (let j = 2; j >= 0; j--) {
        sx.fillStyle = j ? "rgba(245,245,247,0.25)" : "rgba(245,245,247,0.75)";
        sx.fillRect(-18 + j * 6, -38 - j * 6, 34, 42);
      }
      sx.font = font(600, 13, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "center";
      sx.fillText("ONE WEBSITE", 0, 36);
      sx.restore();
    }
  }

  // ------------------- shift 3: insecure, unattributed → secure, attributed
  // A file of unknown origin passes along, is changed on the way, and is
  // accepted anyway.
  const CHAIN = [
    [1160, "SOURCE"],
    [1380, "WEBSITE"],
    [1600, "EMAIL"],
    [1820, "YOUR SYSTEM"],
  ];
  function drawChain(t) {
    const c0 = T_SEC;
    if (t < c0 || t > M3 + 0.35) return;
    const alpha = 1 - easeInExpo(seg(t, M3, M3 + 0.3));
    const cy = 560;
    CHAIN.forEach(([x, label], i) => {
      const k = easeOutBack(seg(t, c0 + 0.1 + i * 0.07, c0 + 0.45 + i * 0.07));
      if (k <= 0) return;
      sx.save();
      sx.globalAlpha = alpha * clamp(k * 2);
      if (i < CHAIN.length - 1) {
        sx.strokeStyle = "rgba(245,245,247,0.3)";
        sx.lineWidth = 1.2;
        sx.setLineDash([4, 6]);
        sx.beginPath();
        sx.moveTo(x + 34, cy);
        sx.lineTo(x + 34 + (CHAIN[i + 1][0] - x - 68) * k, cy);
        sx.stroke();
        sx.setLineDash([]);
      }
      sx.strokeStyle = "rgba(245,245,247,0.5)";
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.arc(x, cy, 30 * k, 0, Math.PI * 2);
      sx.stroke();
      sx.fillStyle = "rgba(245,245,247,0.8)";
      sx.beginPath();
      sx.arc(x, cy, 6 * k, 0, Math.PI * 2);
      sx.fill();
      sx.font = font(600, 13, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "center";
      sx.fillText(label, x, cy + 62);
      sx.restore();
      if (i === 0) questionBadge(x + 24, cy - 24, 28, alpha * clamp(k * 2));
    });
    // the file hops station to station
    const legs = [0, 1, 2].map((i) => easeInOutCubic(seg(t, c0 + 0.35 + i * 0.42, c0 + 0.65 + i * 0.42)));
    const fa = alpha * seg(t, c0 + 0.25, c0 + 0.4);
    if (fa > 0) {
      let li = 0;
      while (li < 2 && legs[li] >= 1) li++;
      const x = lerp(CHAIN[li][0], CHAIN[li + 1][0], legs[li]);
      const y = cy - 96 - Math.sin(Math.PI * legs[li]) * 40;
      const altered = legs[1] >= 1 && seg(t, c0 + 1.2, c0 + 1.3) > 0;
      sx.save();
      sx.globalAlpha = fa;
      sx.beginPath();
      sx.roundRect(x - 44, y - 30, 88, 60, 8);
      sx.fillStyle = "rgba(16,16,20,0.95)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.6)";
      sx.lineWidth = 1.5;
      sx.stroke();
      [52, 38, 60].forEach((bw, r) => {
        sx.fillStyle = altered && r === 1 ? RED : "rgba(245,245,247,0.5)";
        sx.fillRect(x - 30, y - 14 + r * 13, bw, 5);
      });
      sx.restore();
      questionBadge(x + 42, y - 28, 26, fa);
      const hit = seg(t, c0 + 1.2, c0 + 1.5);
      if (hit > 0 && hit < 1) dotGlow(x, y, 60 * (1 - hit), RED, 0.5 * fa);
    }
    maskText(sx, "ACCEPTED ANYWAY", CHAIN[3][0], cy - 160, 14, 600, MUTED, c0 + 1.55, M3, t, { tracking: 3, align: "center", outDur: 0.25 });
  }

  // Digitally signed by its publisher; every node checks before it stores.
  function drawSignVerify(t) {
    const v0 = M3 + 0.3;
    const v1 = T_SEAL;
    if (t < v0 || t > v1 + 0.05) return;
    const alpha = 1 - easeInExpo(seg(t, v1 - 0.3, v1 - 0.05));
    const cx0 = 1110;
    const cy0 = 190;
    const cwid = 480;
    const chei = 200;
    const ck = easeOutExpo(seg(t, v0 + 0.05, v0 + 0.35));
    if (ck > 0) {
      sx.save();
      sx.globalAlpha = alpha * ck;
      sx.translate(cx0 + cwid / 2, cy0 + chei / 2 + (1 - ck) * 30);
      sx.beginPath();
      sx.roundRect(-cwid / 2, -chei / 2, cwid, chei, 16);
      sx.fillStyle = "rgba(245,245,247,0.05)";
      sx.fill();
      sx.strokeStyle = "rgba(245,245,247,0.35)";
      sx.lineWidth = 1.5;
      sx.stroke();
      sx.font = font(600, 15, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = AMBER;
      sx.textAlign = "left";
      sx.fillText("ORBIT UPDATE · ISS", -cwid / 2 + 28, -chei / 2 + 42);
      [
        ["PUBLISHED BY", "Station operator"],
        ["AS OF", "25 Sep 2026 · 12:00 UTC"],
        ["ALTITUDE", "418 km"],
      ].forEach(([k, v], r) => {
        const y = -chei / 2 + 88 + r * 36;
        sx.font = font(600, 13, SANS);
        sx.letterSpacing = "2px";
        sx.fillStyle = MUTED;
        sx.fillText(k, -cwid / 2 + 28, y);
        sx.font = font(500, 19, SANS);
        sx.letterSpacing = "0px";
        sx.fillStyle = r === 0 ? AMBER : INK;
        sx.fillText(v, -cwid / 2 + 190, y + 1);
      });
      sx.restore();
    }
    const st = seg(t, v0 + 0.5, v0 + 0.67);
    if (st > 0) stamp(cx0 + cwid / 2, cy0 + chei + 72, st, alpha);

    // Two copies travel; one arrives intact, one was changed on the way.
    [[1170, 836], [1530, 836]].forEach(([nx, ny], j) => {
      const bad = j === 1;
      const nk = easeOutExpo(seg(t, v0 + 0.7 + j * 0.05, v0 + 1.0 + j * 0.05));
      if (nk > 0) {
        sx.save();
        sx.globalAlpha = alpha * nk;
        sx.strokeStyle = "rgba(245,245,247,0.35)";
        sx.lineWidth = 1.5;
        sx.beginPath();
        sx.arc(nx, ny, 30 * nk, 0, Math.PI * 2);
        sx.stroke();
        sx.restore();
      }
      const t0 = v0 + 0.85 + j * 0.08;
      const p = easeInOutCubic(seg(t, t0, t0 + 0.4));
      const land = seg(t, t0 + 0.4, t0 + 0.5);
      if (p > 0 && land < 1) {
        const px = lerp(cx0 + cwid / 2, nx, p);
        const py = lerp(cy0 + chei / 2, ny, p) - Math.sin(Math.PI * p) * 70;
        const sc = lerp(1, 0.62, p);
        const jitter = bad && p > 0.45 ? (rng(Math.round(FRAME_T * 30) + 5)() - 0.5) * 8 : 0;
        sx.save();
        sx.globalAlpha = alpha * (1 - land);
        sx.translate(px + jitter, py);
        sx.scale(sc, sc);
        sx.beginPath();
        sx.roundRect(-60, -38, 120, 76, 10);
        sx.fillStyle = "rgba(20,20,22,0.9)";
        sx.fill();
        sx.strokeStyle = "rgba(245,245,247,0.6)";
        sx.lineWidth = 1.5;
        sx.stroke();
        [70, 52, 84].forEach((bw, r) => {
          sx.fillStyle = bad && r === 1 && p > 0.45 ? RED : "rgba(245,245,247,0.55)";
          sx.fillRect(-42, -18 + r * 16, bw, 6);
        });
        sx.restore();
        dotGlow(px, py, 40, bad && p > 0.45 ? RED : AMBER, 0.25 * alpha * (1 - land));
      }
      if (land > 0) {
        badge(bad ? "cross" : "check", nx, ny, 46 * easeOutBack(land), bad ? RED : AMBER, alpha);
        const ring = seg(t, t0 + 0.4, t0 + 1.0);
        if (ring < 1) {
          sx.save();
          sx.globalAlpha = alpha * (1 - ring) * 0.8;
          sx.strokeStyle = bad ? RED : AMBER;
          sx.lineWidth = 2;
          sx.beginPath();
          sx.arc(nx, ny, 30 + easeOutExpo(ring) * 60, 0, Math.PI * 2);
          sx.stroke();
          sx.restore();
        }
        dotGlow(nx, ny, 60, bad ? RED : AMBER, 0.2 * alpha * (1 - ring));
      }
      maskText(sx, bad ? "CHANGED · REJECTED" : "VERIFIED · STORED", nx, ny + 70, 15, 600, bad ? RED : AMBER, t0 + 0.46, v1 - 0.3, t, { tracking: 3, align: "center", outDur: 0.28 });
    });
  }

  // Sold data is locked to the buyer's key: only the buyer's node opens it.
  function drawEncrypt(t) {
    const e0 = T_SEAL;
    const e1 = T_CONJ;
    if (t < e0 || t > e1 + 0.05) return;
    const alpha = 1 - easeInExpo(seg(t, e1 - 0.3, e1 - 0.05));
    const bx0 = 1150;
    const by0 = 196;
    const bw = 500;
    const bh = 230;
    const bk = easeOutExpo(seg(t, e0 + 0.08, e0 + 0.38));
    const lockK = easeInExpo(seg(t, e0 + 0.5, e0 + 0.65));
    if (bk > 0) {
      sx.save();
      sx.globalAlpha = alpha * bk;
      sx.translate(0, (1 - bk) * 30);
      sx.beginPath();
      sx.roundRect(bx0, by0, bw, bh, 16);
      sx.fillStyle = "rgba(245,245,247,0.05)";
      sx.fill();
      sx.strokeStyle = lockK > 0 ? `rgba(245,165,36,${0.35 + 0.4 * lockK})` : "rgba(245,245,247,0.35)";
      sx.lineWidth = 1.5;
      sx.stroke();
      sx.font = font(600, 15, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = AMBER;
      sx.textAlign = "left";
      sx.fillText("RADAR TRACKING", bx0 + 28, by0 + 42);
      sx.font = font(600, 13, SANS);
      sx.letterSpacing = "2px";
      sx.fillStyle = MUTED;
      sx.textAlign = "right";
      sx.fillText("FOR SALE", bx0 + bw - 28, by0 + 42);
      // the contents scramble as the lock shuts, left to right
      sx.textAlign = "left";
      sx.font = font(400, 16, MONO);
      sx.letterSpacing = "0.5px";
      const R = rng(700 + Math.round(FRAME_T * 30));
      for (let r = 0; r < 5; r++) {
        const plain = HEX[r + 8].bytes.join(" ").slice(0, 44);
        let s = "";
        for (let c = 0; c < plain.length; c++) {
          const sealed = lockK > 0 && c / plain.length < lockK * 1.2;
          s += sealed && plain[c] !== " " ? GLYPHS[Math.floor(R() * GLYPHS.length)] : plain[c];
        }
        sx.fillStyle = lockK >= 1 ? "rgba(245,245,247,0.4)" : "rgba(89,217,255,0.75)";
        sx.fillText(s, bx0 + 28, by0 + 86 + r * 28);
      }
      sx.restore();
      const lk = easeOutBack(seg(t, e0 + 0.25, e0 + 0.45));
      padlock(bx0 + bw / 2, by0 + bh / 2 + 18, 110 * lk, 1 - lockK, lockK >= 1 ? AMBER : INK, alpha * clamp(lk * 2));
      if (lockK > 0) dotGlow(bx0 + bw / 2, by0 + bh / 2 + 18, 120, AMBER, 0.25 * alpha * (1 - seg(t, e0 + 0.65, e0 + 1.1)));
    }
    maskText(sx, "LOCKED TO THE BUYER’S KEY", bx0, by0 + bh + 40, 14, 600, MUTED, e0 + 0.68, e1 - 0.3, t, { tracking: 2.5, outDur: 0.28 });

    const src = [bx0 + bw / 2, by0 + bh + 66];
    [
      [[1210, 836], true],
      [[1590, 836], false],
    ].forEach(([[nx, ny], buyer], j) => {
      const pts = bezierPts(src, [src[0], src[1] + 90], [nx, ny - 150], [nx, ny - 34]);
      const t0 = e0 + 0.75 + j * 0.06;
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.3)), "rgba(245,245,247,0.5)", 1.4, alpha, 0, [5, 7]);
      const p = easeInOutCubic(seg(t, t0 + 0.15, t0 + 0.5));
      if (p > 0 && p < 1) {
        const [px, py] = pointOn(pts, p);
        padlock(px, py, 30, 0, AMBER, alpha);
        dotGlow(px, py, 30, AMBER, 0.35 * alpha);
      }
      const arrive = seg(t, t0 + 0.5, t0 + 0.6);
      if (arrive <= 0) return;
      const open = buyer ? easeOutBack(seg(t, e0 + 1.45, e0 + 1.62)) : 0;
      if (buyer) {
        const kp = easeInOutCubic(seg(t, e0 + 1.15, e0 + 1.42));
        const ka = alpha * seg(t, e0 + 1.1, e0 + 1.2) * (1 - seg(t, e0 + 1.4, e0 + 1.48));
        keyIcon(lerp(nx - 150, nx - 26, kp), ny + 4, 44, AMBER, ka);
      }
      padlock(nx, ny, 64 * easeOutBack(arrive), open, buyer && open > 0 ? AMBER : "rgba(245,245,247,0.7)", alpha);
      if (buyer && open > 0) {
        const ring = seg(t, e0 + 1.45, e0 + 2.0);
        sx.save();
        sx.globalAlpha = alpha * (1 - ring) * 0.8;
        sx.strokeStyle = AMBER;
        sx.lineWidth = 2;
        sx.beginPath();
        sx.arc(nx, ny, 36 + easeOutExpo(ring) * 60, 0, Math.PI * 2);
        sx.stroke();
        sx.restore();
        dotGlow(nx, ny, 70, AMBER, 0.22 * alpha * (1 - ring));
      }
      maskText(sx, buyer ? "BUYER · OPENED" : "EVERYONE ELSE · LOCKED OUT", nx, ny + 70, 15, 600, buyer ? AMBER : MUTED, buyer ? e0 + 1.5 : t0 + 0.6, e1 - 0.3, t, {
        tracking: 3,
        align: "center",
        outDur: 0.28,
      });
    });
  }

  function drawConjunction(cam, t, T, alpha) {
    if (alpha <= 0) return;
    const { A, B } = CONJ;
    const qa = drawOrbitPath(cam, A, T, AMBER, alpha * 0.9, 2.2, 0.35);
    const qb = drawOrbitPath(cam, B, T, CYAN, alpha * 0.9, 2.2, 0.35);
    // faint full orbits
    drawOrbitPath(cam, A, T, AMBER, alpha * 0.18, 1, 1, false);
    drawOrbitPath(cam, B, T, CYAN, alpha * 0.18, 1, 1, false);
    if (!qa || !qb) return;
    // Where each track comes from, riding beside it until the alert takes over.
    const lab = alpha * seg(t, T_CONJ + 1.1, T_CONJ + 1.5) * (1 - seg(t, T_TCA - 0.9, T_TCA - 0.6));
    if (lab > 0) {
      [
        [qa, AMBER, "SATELLITE A", "OPERATOR DATA · DIGITALLY SIGNED", 1, 64],
        [qb, CYAN, "SATELLITE B", "RADAR DATA · DIGITALLY SIGNED", -1, 84],
      ].forEach(([q, col, name, src, side, dy]) => {
        const lx = q.x + side * 60;
        const ly = q.y + dy;
        sx.save();
        sx.globalAlpha = lab;
        sx.strokeStyle = col;
        sx.lineWidth = 1.2;
        sx.beginPath();
        sx.moveTo(q.x + side * 8, q.y + 10);
        sx.lineTo(lx, ly);
        sx.lineTo(lx + side * 30, ly);
        sx.stroke();
        sx.textAlign = side < 0 ? "right" : "left";
        const tx = lx + side * 40;
        sx.font = font(600, 28, SANS);
        sx.letterSpacing = "3px";
        sx.fillStyle = col;
        sx.fillText(name, tx, ly - 6);
        sx.font = font(600, 21, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillStyle = "rgba(245,245,247,0.8)";
        sx.fillText(src, tx, ly + 28);
        sx.restore();
      });
    }
    const near = seg(t, T_TCA - 1.05, T_TCA - 0.375) * (1 - seg(t, T_END - 0.375, T_END + 0.05));
    // uncertainty ellipses, aligned with each track's screen velocity
    [[A, qa, AMBER], [B, qb, CYAN]].forEach(([o, q, col]) => {
      const q2 = project(cam, orbitPos(o, o.u0 + o.w * (T + 0.02)));
      if (!q2) return;
      const ang = Math.atan2(q2.y - q.y, q2.x - q.x);
      const pulse = 1 + 0.06 * Math.sin(t * 12);
      sx.save();
      sx.translate(q.x, q.y);
      sx.rotate(ang);
      sx.globalAlpha = alpha * near * 0.9;
      sx.strokeStyle = col;
      sx.lineWidth = 1.6;
      sx.setLineDash([6, 5]);
      sx.beginPath();
      sx.ellipse(0, 0, 86 * pulse, 26 * pulse, 0, 0, Math.PI * 2);
      sx.stroke();
      sx.setLineDash([]);
      sx.globalAlpha = alpha * near * 0.12;
      sx.fillStyle = col;
      sx.fill();
      sx.restore();
    });
    const tca = seg(t, T_TCA - 0.525, T_TCA - 0.15) * (1 - seg(t, T_END - 0.225, T_END + 0.1));
    if (tca > 0) {
      const mx = (qa.x + qb.x) / 2;
      const my = (qa.y + qb.y) / 2;
      // shockwave ring at closest approach
      const sw = seg(t, T_TCA - 0.225, T_TCA + 1.4);
      sx.save();
      sx.strokeStyle = RED;
      sx.globalAlpha = alpha * (1 - sw) * 0.9;
      sx.lineWidth = 2;
      sx.beginPath();
      sx.arc(mx, my, 20 + easeOutExpo(sw) * 260, 0, Math.PI * 2);
      sx.stroke();
      // miss-distance line
      sx.globalAlpha = alpha * tca;
      sx.setLineDash([4, 4]);
      sx.beginPath();
      sx.moveTo(qa.x, qa.y);
      sx.lineTo(qb.x, qb.y);
      sx.stroke();
      sx.setLineDash([]);
      sx.restore();
      dotGlow(mx, my, 50 * tca, RED, 0.22 * alpha * tca * (1 - seg(t, T_TCA + 0.45, T_TCA + 1.05)));
      // alert block with a leader line
      const lx = mx + 150;
      const ly = my - 260;
      const lk = easeOutExpo(seg(t, T_TCA - 0.375, T_TCA + 0.225));
      sx.save();
      sx.globalAlpha = alpha * tca;
      sx.strokeStyle = "rgba(255,59,48,0.8)";
      sx.lineWidth = 1.5;
      sx.beginPath();
      sx.moveTo(mx, my);
      sx.lineTo(lerp(mx, lx, lk), lerp(my, ly + 40, lk));
      sx.lineTo(lerp(mx, lx + 470, lk), lerp(my, ly + 40, lk));
      sx.stroke();
      // a dark card behind the readout, so orbit lines and the limb never cross the type
      sx.save();
      sx.globalAlpha = alpha * tca * lk;
      sx.fillStyle = "rgba(0,0,0,0.66)";
      sx.beginPath();
      sx.roundRect(lx - 18, ly - 38, 512, 216, 14);
      sx.fill();
      sx.restore();
      // triangle with an exclamation mark: red means danger, never color alone
      sx.translate(lx + 22, ly);
      sx.fillStyle = RED;
      sx.beginPath();
      sx.moveTo(0, -22);
      sx.lineTo(22, 16);
      sx.lineTo(-22, 16);
      sx.closePath();
      sx.fill();
      sx.fillStyle = "#1a0503";
      sx.font = font(800, 24, SANS);
      sx.textAlign = "center";
      sx.fillText("!", 0, 11);
      sx.restore();
      maskText(sx, "CLOSE APPROACH", lx + 58, ly + 12, 30, 700, RED, T_TCA - 0.375, T_END - 0.225, t, { tracking: 3 });
      sx.save();
      sx.globalAlpha = alpha * tca;
      sx.font = font(500, 26, MONO);
      sx.letterSpacing = "1px";
      sx.fillStyle = INK;
      decodeText(sx, "WHEN  25 SEP 2026 · 14:02 UTC", lx, ly + 82, T_TCA - 0.225, 0.5, t, 7);
      decodeText(sx, "MISS  214 m", lx, ly + 118, T_TCA - 0.075, 0.45, t, 8);
      decodeText(sx, "RISK  1 IN 8,300", lx, ly + 154, T_TCA + 0.075, 0.45, t, 9);
      sx.restore();
    }
  }

  // ---------------------------------------------------------- one subframe
  function drawSubframe(t, frameIndex) {
    const T = tau(t);
    const cam = cameraAt(t);
    beginSubframe();

    // Earth: in during the match cut, dimmed and blurred under the first
    // shift, the slow half and the third shift, gone into the lockup.
    const earthIn = easeInOutCubic(seg(t, 1.55, 2.3));
    const under =
      seg(t, T_ORG - 0.1, T_ORG + 0.15) * (1 - seg(t, T_NET - 0.05, T_NET + 0.3)) +
      seg(t, T_SEC - 0.1, T_SEC + 0.15) * (1 - seg(t, T_CONJ - 0.05, T_CONJ + 0.3));
    const earthOut = 1 - easeInCubic(seg(t, T_END + 1.0, T_END + 1.5));
    const earthFade = earthIn * (1 - 0.8 * under) * earthOut;
    const rim = 0.9 * seg(t, 1.6, 2.1) * (1 - seg(t, 2.3, 3.0)) + 0.9 * seg(t, T_END + 0.9, T_END + 1.35);
    const sunAz = t < 5 ? lerp(35, 60, seg(t, 2, 5)) : t < T_CONJ ? lerp(60, 118, seg(t, T_NET, T_NET + 4)) : lerp(150, 60, seg(t, T_END - 0.05, T_END + 1.15));
    const sunEl = t < T_CONJ ? 18 : lerp(4, 18, seg(t, T_END - 0.05, T_END + 1.15));
    if (earthFade > 0.001) {
      renderEarth(cam, t, earthFade, rim, sunAz, sunEl);
      if (under > 0) sx.filter = `blur(${14 * under}px)`;
      sx.drawImage(glc, 0, 0);
      sx.filter = "none";
    }

    // Satellites: the swarm fills the sky, leaves for the type-led beats,
    // dims under the network and the close approach, returns for the finale.
    const satAlpha =
      seg(t, 2.1, 2.4) *
      (1 - seg(t, T_ORG - 0.2, T_ORG + 0.05) * (1 - seg(t, T_NET, T_NET + 0.3))) *
      (1 - seg(t, T_SEC - 0.2, T_SEC + 0.05) * (1 - seg(t, T_CONJ, T_CONJ + 0.3))) *
      (1 - 0.72 * seg(t, T_NET, T_NET + 0.6) * (1 - seg(t, T_END + 0.05, T_END + 0.75))) *
      (1 - 0.55 * seg(t, T_CONJ, T_CONJ + 0.3) * (1 - seg(t, T_END, T_END + 0.35))) *
      earthOut;
    if (satAlpha > 0.01) drawSatellites(cam, t, T, satAlpha, true);

    // The amber satellite from the mark, in orbit with its arc.
    const heroA =
      seg(t, 2.0, 2.4) * (1 - seg(t, T_ORG - 0.25, T_ORG)) +
      seg(t, T_NET + 0.3, T_NET + 0.8) * (1 - seg(t, T_SEC - 0.3, T_SEC));
    if (heroA > 0) drawOrbitPath(cam, HERO, T, AMBER, heroA, 2.4, 0.42);

    if (t > T_NET && t < T_SEC) drawNetwork(cam, t, seg(t, T_NET, T_NET + 0.3) * (1 - seg(t, T_SEC - 0.3, T_SEC)));
    // The type over the lit planet sits low and left: wash that corner back.
    scrim(seg(t, T_NET + 0.1, T_NET + 0.5) * (1 - seg(t, T_SEC - 0.3, T_SEC)) + seg(t, T_CONJ, T_CONJ + 0.4) * (1 - seg(t, T_END, T_END + 0.9)));
    if (t > T_CONJ - 0.05 && t < T_END + 0.45) drawConjunction(cam, t, T, seg(t, T_CONJ, T_CONJ + 0.45) * (1 - seg(t, T_END, T_END + 0.45)));

    // The mark: opening build, then the closing lockup.
    if (t < 2.4) {
      const R = ringRadius(t);
      const fade = 1 - easeInCubic(seg(t, 1.65, 2.15));
      drawMark(W / 2, H / 2, R, t, {
        head: easeInOutExpo(seg(t, 0.2, 1.05)),
        ring: easeInOutCubic(seg(t, 0.35, 1.15)),
        nodes: seg(t, 0.95, 1.4),
        links: easeOutCubic(seg(t, 1.1, 1.5)),
        fade,
      });
    }
    if (t > T_END + 0.85) {
      // Match cut back to the mark: the ring rides the shrinking planet's
      // limb, then settles into the lockup.
      const c = project(cam, [0, 0, 0]) ?? { x: W / 2, y: H / 2 };
      const globePx = (1 / Math.sqrt(Math.max(1e-6, cam.dist * cam.dist - 1)) / TAN) * (H / 2);
      const settle = easeInOutCubic(seg(t, T_END + 1.45, T_END + 1.85));
      const R = lerp(Math.max(150, globePx), 118, settle);
      const mx = lerp(c.x, W / 2 - 380, settle);
      const my = lerp(c.y, H / 2, settle);
      drawMark(mx, my, R, t, {
        head: easeInOutExpo(seg(t, T_END + 1.05, T_END + 1.55)),
        ring: seg(t, T_END + 0.95, T_END + 1.25),
        nodes: seg(t, T_END + 1.57, T_END + 1.81),
        links: easeOutCubic(seg(t, T_END + 1.65, T_END + 1.87)),
        fade: 1,
      });
      maskText(sx, "Space Data Network", W / 2 - 220, H / 2 + 8, 78, 650, INK, T_END + 1.77, null, t, { dur: 0.45, tracking: -1.5 });
      maskText(sx, "An open network for space traffic management", W / 2 - 218, H / 2 + 64, 28, 400, MUTED, T_END + 1.85, null, t, { dur: 0.4 });
      maskText(sx, "SPACEDATANETWORK.ORG", W / 2 - 218, H / 2 - 92, 18, 600, AMBER, T_END + 1.89, null, t, { dur: 0.4, tracking: 4 });
    }

    // The crowded sky: the count and the orbit regimes.
    if (t > 2.3 && t < 5.2) {
      // The count never settles: it keeps climbing until the text leaves,
      // because the catalog only grows.
      const k = easeOutExpo(seg(FRAME_T, 2.45, 4.1));
      const n = Math.round(46600 * k + 900 * Math.max(0, FRAME_T - 3.1) + 260 * Math.pow(Math.max(0, FRAME_T - 3.1), 2));
      maskText(sx, "OBJECTS TRACKED IN ORBIT", 150, 790, 18, 600, AMBER, 2.4, 4.75, t, { tracking: 4 });
      sx.save();
      const outK = easeInExpo(seg(t, 4.75, 5.2));
      sx.beginPath();
      sx.rect(140, 780, 900, 150);
      sx.clip();
      sx.font = font(700, 128, SANS);
      sx.letterSpacing = "-4px";
      sx.fillStyle = INK;
      sx.fillText(n.toLocaleString("en-US"), 146, 905 + (1 - easeOutExpo(seg(t, 2.4, 2.9))) * 140 - outK * 150);
      sx.restore();
      // orbit regime labels with leader lines
      const labels = [
        ["LOW EARTH ORBIT", 1.1, -30, 4.0],
        ["NAVIGATION SATELLITES", 2.15, 150, 4.12],
        ["GEOSTATIONARY RING", 3.0, 200, 4.24],
      ];
      labels.forEach(([name, r, ang, t0]) => {
        const k2 = easeOutExpo(seg(t, t0, t0 + 0.5)) * (1 - seg(t, 4.85, 5.1));
        if (k2 <= 0) return;
        const p = [r * Math.cos(ang * D), 0.0, r * Math.sin(ang * D)];
        const q = project(cam, p);
        if (!q) return;
        const lx = q.x + 90;
        const ly = q.y - 70;
        sx.save();
        sx.globalAlpha = k2;
        sx.strokeStyle = "rgba(245,245,247,0.7)";
        sx.lineWidth = 1.2;
        sx.beginPath();
        sx.moveTo(q.x, q.y);
        sx.lineTo(lerp(q.x, lx, k2), lerp(q.y, ly, k2));
        sx.lineTo(lerp(q.x, lx + 150, k2), lerp(q.y, ly, k2));
        sx.stroke();
        sx.fillStyle = INK;
        sx.beginPath();
        sx.arc(q.x, q.y, 3, 0, Math.PI * 2);
        sx.fill();
        sx.font = font(600, 15, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillText(name, lx + 4, ly - 10);
        sx.restore();
      });
    }
    if (t > 5.2 && t < T_ORG + 0.05) {
      block(
        [
          ["More satellites.", INK],
          ["More operators.", INK],
          ["More close calls.", AMBER],
        ],
        "EVERY OPERATOR NEEDS TIMELY ACCESS TO VITAL DATA",
        5.3,
        T_ORG - 0.4,
        t,
        84,
      );
    }
    SHIFTS.forEach((sh) => {
      if (t > sh.tIn - 0.05 && t < sh.tOut + 0.5) shiftBlock(sh, t);
    });
    drawSources(t);
    drawHub(t);
    drawChain(t);
    drawSignVerify(t);
    drawEncrypt(t);
    if (t > T_SEAL - 0.1 && t < T_CONJ) caption("SOLD DATA OPENS ONLY FOR THE BUYER", "check", T_SEAL, T_CONJ - 0.4, t);
    if (t > T_STORE - 0.1 && t < T_SEC) {
      block(
        [
          ["A storefront", INK],
          ["with no middlemen.", AMBER],
        ],
        "SHARE DATA FREE, OR SELL IT FROM YOUR OWN NODE",
        T_STORE,
        T_SEC - 0.4,
        t,
      );
    }
    if (t > T_CONJ + 0.3 && t < T_END + 1.2) {
      block(
        [
          ["Infrastructure that", INK],
          ["prevents close calls.", AMBER],
        ],
        "SIGNED DATA FROM EVERY SOURCE, SCREENED TOGETHER",
        T_CONJ + 0.6,
        T_TCA - 0.75,
        t,
        80,
      );
      block(
        [
          ["Provided at the speed", INK],
          ["of operations.", INK],
          ["Anyone can check the math.", AMBER],
        ],
        "OPEN SOFTWARE AND ALGORITHMS, EVEN WHEN THE DATA IS ENCRYPTED",
        T_TCA + 0.4,
        T_END - 1.0,
        t,
        80,
      );
      // The three shifts, together, on the way out.
      maskText(sx, "ON THE NETWORK", LX + 2, 758, EYE, 600, AMBER, T_END - 0.65, T_END + 0.8, t, { tracking: 3.5, outDur: 0.25 });
      sx.save();
      sx.font = font(700, BIG);
      sx.letterSpacing = "-3px";
      let x = LX;
      ["Organized.", "Fast.", "Secure."].forEach((w, i) => {
        maskText(sx, w, x, 870, BIG, 700, AMBER, T_END - 0.6 + i * 0.35, T_END + 0.8 + i * 0.03, t, { tracking: -3, outDur: 0.25 });
        x += sx.measureText(`${w} `).width;
      });
      sx.restore();
    }

    bloom();
    hud(t, frameIndex, T_END + 1.75);
  }

  // Chromatic aberration on each cut and on each word that turns, a flash on
  // the hard cuts. The piece plays once and rests on the lockup, so it fades
  // in but never out.
  const ca = studio.cutPulses([
    [2.2, 1],
    [T_ORG, 0.5],
    [M1 + 0.3, 0.6],
    [T_SLOW, 1],
    [M2 + 0.3, 0.6],
    [T_NET, 1],
    [T_SEC, 1],
    [M3 + 0.3, 0.6],
    [T_SEAL, 0.5],
    [T_CONJ, 1],
    [T_END + 1.05, 1],
  ]);
  const pulse = (t, c, w) => Math.exp(-Math.pow((t - c) / w, 2));
  const flash = (t) => 0.1 * pulse(t, T_SLOW + 0.01, 0.05) + 0.25 * pulse(t, T_NET + 0.01, 0.05) + 0.1 * pulse(t, T_SEC + 0.01, 0.05) + 0.12 * pulse(t, T_TCA - 0.205, 0.06);
  const fade = (t) => seg(t, 0, 0.12);
  const renderFrame = studio.makeRenderFrame({ drawSubframe, ca, flash, fade });

  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: studio.canvas };
}
