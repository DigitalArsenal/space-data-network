/**
 * SDN Peer Map — self-contained canvas globe / 2D map (loop U0.2).
 *
 * TypeScript port of the design handoff's sdn_console/globe.js
 * (design/SpaceAware.io.zip). Behavior ground truth is that file: plots
 * connected clients with great-circle links to the local node, 3D globe and
 * 2D equirectangular modes, pointer drag to rotate (auto-spin resumes 2.6 s
 * after release), hover picking with tooltip, travelling packet dots along
 * the arcs, ResizeObserver-aware, devicePixelRatio capped at 2.
 *
 * ONE deliberate change vs the prototype (packaging hard rule): the land
 * silhouette is no longer fetched from CDN GeoJSON at runtime — it ships
 * embedded as a precomputed 2° dot grid (./land-dots.ts), with the same
 * localStorage['sdn_land_dots_v3_2deg'] cache semantics and the same
 * graticule-only fallback.
 */

import { loadLandDots, type LandDot } from './land-dots';

const DEG = Math.PI / 180;

export type SdnGlobeMode = '3d' | '2d';

export interface SdnGlobePoint {
  lat: number;
  lon: number;
  /** e.g. 'provider' | 'peer' | 'client' — passed to colorFor, sizes markers. */
  kind?: string;
  /** Always-on caption above the marker. */
  label?: string;
  /** Tooltip line 1 (tooltip only shows when city is set — prototype rule). */
  city?: string;
  /** Tooltip line 2 suffix. */
  ip?: string;
}

export interface SdnGlobeHome extends SdnGlobePoint {
  lat: number;
  lon: number;
}

export interface SdnGlobeOptions {
  home: SdnGlobeHome;
  points?: SdnGlobePoint[];
  colorFor?: (kind: string | undefined) => string;
  /**
   * Declarative mode (preferred with the Svelte action: reading it during
   * options construction makes reactivity track it, so the action's update
   * applies mode switches). Wins over getMode when both are set.
   */
  mode?: SdnGlobeMode;
  /** Prototype-style mode callback (read once at construction / on update). */
  getMode?: () => SdnGlobeMode;
}

interface Vec3 {
  x: number;
  y: number;
  z: number;
}

interface Projected {
  x: number;
  y: number;
  z: number;
}

function clamp(v: number, a: number, b: number): number {
  return v < a ? a : v > b ? b : v;
}

export class SdnGlobe {
  readonly canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  home: SdnGlobeHome;
  points: SdnGlobePoint[];
  private colorFor: (kind: string | undefined) => string;
  private getMode: () => SdnGlobeMode;
  mode: SdnGlobeMode;

  /** longitude rotation (deg) — starts on the Atlantic */
  rot = -60;
  /** view tilt (deg), north up */
  tilt = 16;
  spin = true;
  land: LandDot[] | null = null;

  private dpr = 1;
  private W = 10;
  private H = 10;
  private cx = 0;
  private cy = 0;
  private R = 0;
  private mx0 = 0;
  private my0 = 0;
  private mw = 0;
  private mh = 0;

  private _last: number;
  private _resumeAt = 0;
  private _hover = -1;
  private _alive = true;
  private _mx: number | null = null;
  private _my: number | null = null;
  private _drag: { x: number; y: number; rot: number; tilt: number } | null = null;
  private _ro: ResizeObserver | null = null;
  private _loop: FrameRequestCallback;

  private _pd!: (e: PointerEvent) => void;
  private _pm!: (e: PointerEvent) => void;
  private _pu!: () => void;
  private _hoverMove!: (e: PointerEvent) => void;
  private _hoverLeave!: () => void;

  constructor(canvas: HTMLCanvasElement, opts: SdnGlobeOptions) {
    this.canvas = canvas;
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('SdnGlobe: 2d canvas context unavailable');
    this.ctx = ctx;
    this.home = opts.home;
    this.points = opts.points ?? [];
    this.colorFor = opts.colorFor ?? (() => '#9fd4f5');
    this.getMode = opts.getMode ?? (() => '3d');
    this.mode = opts.mode ?? this.getMode();
    this._last = performance.now();
    this._bind();
    this._resize();
    this._loadLand();
    // paint one frame immediately (before RAF / in throttled/backgrounded contexts)
    try {
      this._frame(performance.now());
    } catch {
      /* first paint is best-effort, matching the prototype */
    }
    this._loop = (t) => {
      if (this._alive) {
        this._frame(t);
        requestAnimationFrame(this._loop);
      }
    };
    requestAnimationFrame(this._loop);
  }

  setMode(m: SdnGlobeMode): void {
    if (m !== this.mode) {
      this.mode = m;
      try {
        this._frame(performance.now());
      } catch {
        /* best-effort immediate repaint */
      }
    }
  }

  setInteractive(on: boolean): void {
    this.canvas.style.pointerEvents = on ? 'auto' : 'none';
  }

  setPoints(points: SdnGlobePoint[]): void {
    this.points = points;
  }

  destroy(): void {
    this._alive = false;
    if (this._ro) this._ro.disconnect();
    const c = this.canvas;
    c.removeEventListener('pointerdown', this._pd);
    window.removeEventListener('pointermove', this._pm);
    window.removeEventListener('pointerup', this._pu);
    c.removeEventListener('pointermove', this._hoverMove);
    c.removeEventListener('pointerleave', this._hoverLeave);
  }

  private _bind(): void {
    const c = this.canvas;
    this._drag = null;
    this._pd = (e) => {
      this._drag = { x: e.clientX, y: e.clientY, rot: this.rot, tilt: this.tilt };
      this.spin = false;
      try {
        c.setPointerCapture(e.pointerId);
      } catch {
        /* capture is best-effort */
      }
    };
    this._pm = (e) => {
      if (!this._drag) return;
      this.rot = this._drag.rot + (e.clientX - this._drag.x) * 0.4;
      this.tilt = clamp(this._drag.tilt - (e.clientY - this._drag.y) * 0.3, -78, 78);
    };
    this._pu = () => {
      if (this._drag) {
        this._drag = null;
        this._resumeAt = performance.now() + 2600;
      }
    };
    this._hoverMove = (e) => {
      const r = c.getBoundingClientRect();
      this._mx = e.clientX - r.left;
      this._my = e.clientY - r.top;
      c.style.cursor = this._hover >= 0 ? 'pointer' : 'grab';
    };
    this._hoverLeave = () => {
      this._mx = -999;
      this._my = -999;
      this._hover = -1;
    };
    c.addEventListener('pointerdown', this._pd);
    window.addEventListener('pointermove', this._pm);
    window.addEventListener('pointerup', this._pu);
    c.addEventListener('pointermove', this._hoverMove);
    c.addEventListener('pointerleave', this._hoverLeave);
    c.style.cursor = 'grab';
    c.style.touchAction = 'none';
    if (typeof ResizeObserver !== 'undefined') {
      this._ro = new ResizeObserver(() => this._resize());
      this._ro.observe(c);
    }
  }

  private _resize(): void {
    const w = this.canvas.clientWidth || 300;
    const h = this.canvas.clientHeight || 300;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    if (w === this.W && h === this.H && dpr === this.dpr) return;
    this.W = w;
    this.H = h;
    this.dpr = dpr;
    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(h * dpr);
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }

  /**
   * Prototype's _loadLand fetched CDN GeoJSON with a localStorage cache;
   * here the grid is embedded, the cache key + graticule-only fallback are
   * preserved (see ./land-dots.ts).
   */
  private _loadLand(): void {
    this.land = loadLandDots();
  }

  // --- projection ---
  private _toVec(lat: number, lon: number): Vec3 {
    const la = lat * DEG;
    const lo = lon * DEG;
    const c = Math.cos(la);
    return { x: c * Math.sin(lo), y: Math.sin(la), z: c * Math.cos(lo) };
  }

  private _proj(v: Vec3): Projected {
    const lo = this.rot * DEG;
    const clo = Math.cos(lo);
    const slo = Math.sin(lo);
    const x = v.x * clo + v.z * slo;
    const z = -v.x * slo + v.z * clo;
    const y = v.y;
    const t = this.tilt * DEG;
    const ct = Math.cos(t);
    const st = Math.sin(t);
    const y2 = y * ct - z * st;
    const z2 = y * st + z * ct;
    return { x: this.cx + this.R * x, y: this.cy - this.R * y2, z: z2 };
  }

  private _frame(now: number): void {
    this._resize();
    const dt = Math.min(80, now - this._last);
    this._last = now;
    if (this.spin === false && !this._drag && now > this._resumeAt) this.spin = true;
    if (this.spin && this.mode === '3d') this.rot += dt * 0.0042 * 3.2; // ~ slow drift
    const ctx = this.ctx;
    ctx.clearRect(0, 0, this.W, this.H);
    if (this.mode === '2d') this._draw2d(now);
    else this._draw3d(now);
  }

  // ---------------- 3D GLOBE ----------------
  private _draw3d(now: number): void {
    const ctx = this.ctx;
    const W = this.W;
    const H = this.H;
    this.cx = W / 2;
    this.cy = H / 2;
    this.R = (Math.min(W, H) / 2) * 0.7;
    const R = this.R;
    const cx = this.cx;
    const cy = this.cy;

    // atmosphere glow
    const glow = ctx.createRadialGradient(cx, cy, R * 0.9, cx, cy, R * 1.22);
    glow.addColorStop(0, 'rgba(53,201,216,0.16)');
    glow.addColorStop(0.5, 'rgba(53,201,216,0.05)');
    glow.addColorStop(1, 'rgba(53,201,216,0)');
    ctx.fillStyle = glow;
    ctx.beginPath();
    ctx.arc(cx, cy, R * 1.22, 0, 7);
    ctx.fill();

    // ocean sphere
    const oc = ctx.createRadialGradient(cx - R * 0.3, cy - R * 0.35, R * 0.15, cx, cy, R);
    oc.addColorStop(0, '#0e2734');
    oc.addColorStop(0.7, '#0a1a24');
    oc.addColorStop(1, '#050e15');
    ctx.fillStyle = oc;
    ctx.beginPath();
    ctx.arc(cx, cy, R, 0, 7);
    ctx.fill();

    // graticule
    ctx.lineWidth = 1;
    let g: number;
    for (g = -60; g <= 60; g += 30) {
      this._latLine(g, g === 0 ? 'rgba(120,190,215,0.22)' : 'rgba(96,158,182,0.13)');
    }
    for (g = -180; g < 180; g += 30) this._lonLine(g, 'rgba(96,158,182,0.11)');

    // land dots
    if (this.land && this.land.length) {
      for (let i = 0; i < this.land.length; i++) {
        const p = this._proj(this._toVec(this.land[i][0], this.land[i][1]));
        if (p.z <= 0.02) continue;
        const a = 0.16 + 0.5 * p.z;
        ctx.fillStyle = 'rgba(116,186,206,' + a.toFixed(3) + ')';
        const s = 1.1 + p.z * 0.9;
        ctx.fillRect(p.x - s / 2, p.y - s / 2, s, s);
      }
    } else if (this.land === null) {
      ctx.fillStyle = 'rgba(120,190,215,0.5)';
      ctx.font = "10px 'IBM Plex Mono',monospace";
      ctx.textAlign = 'center';
      ctx.fillText('resolving geoip land data…', cx, cy + R + 16);
      ctx.textAlign = 'left';
    }

    // rim
    ctx.strokeStyle = 'rgba(130,196,224,0.45)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.arc(cx, cy, R, 0, 7);
    ctx.stroke();

    // arcs + markers
    this._drawArcs3d(now);
    this._drawMarkers3d(now);
  }

  private _latLine(lat: number, color: string): void {
    const ctx = this.ctx;
    ctx.strokeStyle = color;
    ctx.beginPath();
    let started = false;
    for (let lon = -180; lon <= 180; lon += 4) {
      const p = this._proj(this._toVec(lat, lon));
      if (p.z > 0) {
        if (!started) {
          ctx.moveTo(p.x, p.y);
          started = true;
        } else ctx.lineTo(p.x, p.y);
      } else started = false;
    }
    ctx.stroke();
  }

  private _lonLine(lon: number, color: string): void {
    const ctx = this.ctx;
    ctx.strokeStyle = color;
    ctx.beginPath();
    let started = false;
    for (let lat = -88; lat <= 88; lat += 4) {
      const p = this._proj(this._toVec(lat, lon));
      if (p.z > 0) {
        if (!started) {
          ctx.moveTo(p.x, p.y);
          started = true;
        } else ctx.lineTo(p.x, p.y);
      } else started = false;
    }
    ctx.stroke();
  }

  private _arcPts(a: SdnGlobePoint, b: SdnGlobePoint, steps: number): Projected[] {
    const A = this._toVec(a.lat, a.lon);
    const B = this._toVec(b.lat, b.lon);
    const d = clamp(A.x * B.x + A.y * B.y + A.z * B.z, -1, 1);
    const ang = Math.acos(d);
    const sin = Math.sin(ang);
    const pts: Projected[] = [];
    const arcH = 0.09 + 0.17 * (ang / Math.PI);
    for (let k = 0; k <= steps; k++) {
      const t = k / steps;
      let s0: number;
      let s1: number;
      if (sin < 1e-4) {
        s0 = 1 - t;
        s1 = t;
      } else {
        s0 = Math.sin((1 - t) * ang) / sin;
        s1 = Math.sin(t * ang) / sin;
      }
      const x = A.x * s0 + B.x * s1;
      const y = A.y * s0 + B.y * s1;
      const z = A.z * s0 + B.z * s1;
      const e = 1 + arcH * Math.sin(Math.PI * t);
      pts.push(this._proj({ x: x * e, y: y * e, z: z * e }));
    }
    return pts;
  }

  private _drawArcs3d(now: number): void {
    const ctx = this.ctx;
    const home = this.home;
    const FADE = 0.24; // depth window over which an arc dissolves into the limb
    ctx.lineCap = 'round';
    for (let i = 0; i < this.points.length; i++) {
      const pt = this.points[i];
      const col = this.colorFor(pt.kind);
      const pts = this._arcPts(home, pt, 64);
      const lw = pt.kind === 'client' ? 0.9 : 1.3;
      ctx.strokeStyle = col;
      ctx.lineWidth = lw;
      // segment-by-segment so alpha can taper with depth — arcs melt into the horizon instead of a hard cut
      for (let k = 1; k < pts.length; k++) {
        const a = pts[k - 1];
        const b = pts[k];
        const zmid = (a.z + b.z) / 2;
        if (zmid <= 0) continue; // segment is behind the globe
        ctx.globalAlpha = 0.46 * (zmid < FADE ? zmid / FADE : 1);
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.stroke();
      }
      ctx.globalAlpha = 1;
      // travelling packet (fades out as it rounds the limb)
      const per = 3200 + (i % 5) * 260;
      const tt = ((now + i * 620) % per) / per;
      const idx = Math.floor(tt * (pts.length - 1));
      const pk = pts[idx];
      if (pk && pk.z > 0.01) {
        ctx.globalAlpha = pk.z < FADE ? pk.z / FADE : 1;
        ctx.fillStyle = col;
        ctx.shadowColor = col;
        ctx.shadowBlur = 8;
        ctx.beginPath();
        ctx.arc(pk.x, pk.y, pt.kind === 'client' ? 1.5 : 2, 0, 7);
        ctx.fill();
        ctx.shadowBlur = 0;
        ctx.globalAlpha = 1;
      }
    }
    ctx.lineCap = 'butt';
  }

  private _drawMarkers3d(now: number): void {
    const ctx = this.ctx;
    const pulse = 0.5 + 0.5 * Math.sin(now * 0.004);
    this._hover = -1;
    // connection markers
    for (let i = 0; i < this.points.length; i++) {
      const pt = this.points[i];
      const p = this._proj(this._toVec(pt.lat, pt.lon));
      if (p.z <= 0.02) continue;
      const col = this.colorFor(pt.kind);
      const near =
        this._mx != null &&
        this._my != null &&
        Math.abs(p.x - this._mx) < 8 &&
        Math.abs(p.y - this._my) < 8;
      if (near) this._hover = i;
      const rad = pt.kind === 'client' ? 2 : 3;
      // pulse ring
      ctx.strokeStyle = col;
      ctx.globalAlpha = 0.5 * (1 - pulse);
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.arc(p.x, p.y, rad + 1 + pulse * (pt.kind === 'client' ? 4 : 7), 0, 7);
      ctx.stroke();
      ctx.globalAlpha = 1;
      ctx.fillStyle = col;
      ctx.shadowColor = col;
      ctx.shadowBlur = 7;
      ctx.beginPath();
      ctx.arc(p.x, p.y, rad, 0, 7);
      ctx.fill();
      ctx.shadowBlur = 0;
      if (pt.label) this._label(p.x, p.y - rad - 4, pt.label, col);
    }
    // home node
    const hp = this._proj(this._toVec(this.home.lat, this.home.lon));
    if (hp.z > 0.02) {
      ctx.strokeStyle = '#ffd089';
      ctx.globalAlpha = 0.55 * (1 - pulse);
      ctx.lineWidth = 1.2;
      ctx.beginPath();
      ctx.arc(hp.x, hp.y, 5 + pulse * 9, 0, 7);
      ctx.stroke();
      ctx.globalAlpha = 1;
      ctx.fillStyle = 'rgba(255,208,137,0.9)';
      ctx.beginPath();
      ctx.arc(hp.x, hp.y, 4.5, 0, 7);
      ctx.fill();
      ctx.fillStyle = '#fff';
      ctx.shadowColor = '#ffd089';
      ctx.shadowBlur = 9;
      ctx.beginPath();
      ctx.arc(hp.x, hp.y, 2.1, 0, 7);
      ctx.fill();
      ctx.shadowBlur = 0;
      this._label(hp.x, hp.y - 12, this.home.label || 'THIS NODE', '#ffd089');
    }
    this._drawHover();
  }

  private _label(x: number, y: number, text: string, color: string): void {
    const ctx = this.ctx;
    ctx.font = "600 9px 'Chakra Petch','IBM Plex Mono',monospace";
    ctx.textAlign = 'center';
    ctx.fillStyle = 'rgba(4,8,12,0.72)';
    const w = ctx.measureText(text).width;
    ctx.fillRect(x - w / 2 - 3, y - 9, w + 6, 11);
    ctx.fillStyle = color;
    ctx.fillText(text, x, y);
    ctx.textAlign = 'left';
  }

  private _drawHover(): void {
    if (this._hover < 0) return;
    const pt = this.points[this._hover];
    if (!pt || !pt.city) return;
    const ctx = this.ctx;
    const x = this._mx ?? 0;
    const y = this._my ?? 0;
    const line1 = pt.city;
    const line2 = (pt.kind || '').toUpperCase() + (pt.ip ? '  ·  ' + pt.ip : '');
    ctx.font = "600 10px 'Chakra Petch',monospace";
    const w1 = ctx.measureText(line1).width;
    ctx.font = "9px 'IBM Plex Mono',monospace";
    const w2 = ctx.measureText(line2).width;
    const w = Math.max(w1, w2) + 16;
    const h = 30;
    const bx = clamp(x + 12, 4, this.W - w - 4);
    const by = clamp(y - h - 6, 4, this.H - h - 4);
    ctx.fillStyle = 'rgba(6,13,19,0.94)';
    ctx.strokeStyle = this.colorFor(pt.kind);
    ctx.lineWidth = 1;
    ctx.fillRect(bx, by, w, h);
    ctx.strokeRect(bx, by, w, h);
    ctx.fillStyle = '#eaf6f8';
    ctx.font = "600 10px 'Chakra Petch',monospace";
    ctx.fillText(line1, bx + 8, by + 13);
    ctx.fillStyle = '#8fa6b0';
    ctx.font = "9px 'IBM Plex Mono',monospace";
    ctx.fillText(line2, bx + 8, by + 24);
  }

  // ---------------- 2D MAP ----------------
  private _mapXY(lat: number, lon: number): { x: number; y: number } {
    return {
      x: this.mx0 + ((lon + 180) / 360) * this.mw,
      y: this.my0 + ((90 - lat) / 180) * this.mh,
    };
  }

  private _draw2d(now: number): void {
    const ctx = this.ctx;
    const W = this.W;
    const H = this.H;
    const pad = 12;
    this.mx0 = pad;
    this.my0 = pad;
    this.mw = W - pad * 2;
    this.mh = H - pad * 2;
    // frame
    ctx.fillStyle = '#081722';
    ctx.fillRect(this.mx0, this.my0, this.mw, this.mh);
    ctx.strokeStyle = 'rgba(96,158,182,0.25)';
    ctx.lineWidth = 1;
    ctx.strokeRect(this.mx0 + 0.5, this.my0 + 0.5, this.mw - 1, this.mh - 1);
    // grid
    ctx.strokeStyle = 'rgba(96,158,182,0.1)';
    for (let lo = -150; lo < 180; lo += 30) {
      const gx = this._mapXY(0, lo).x;
      ctx.beginPath();
      ctx.moveTo(gx, this.my0);
      ctx.lineTo(gx, this.my0 + this.mh);
      ctx.stroke();
    }
    for (let la = -60; la <= 60; la += 30) {
      const gy = this._mapXY(la, 0).y;
      ctx.beginPath();
      ctx.moveTo(this.mx0, gy);
      ctx.lineTo(this.mx0 + this.mw, gy);
      ctx.stroke();
    }
    // land
    if (this.land && this.land.length) {
      ctx.fillStyle = 'rgba(116,186,206,0.5)';
      for (let i = 0; i < this.land.length; i++) {
        const lp = this._mapXY(this.land[i][0], this.land[i][1]);
        ctx.fillRect(lp.x - 0.7, lp.y - 0.7, 1.4, 1.4);
      }
    } else if (this.land === null) {
      ctx.fillStyle = 'rgba(120,190,215,0.5)';
      ctx.font = "10px 'IBM Plex Mono',monospace";
      ctx.textAlign = 'center';
      ctx.fillText('resolving geoip land data…', W / 2, H / 2);
      ctx.textAlign = 'left';
    }
    // arcs
    const home = this.home;
    const hp = this._mapXY(home.lat, home.lon);
    for (let a = 0; a < this.points.length; a++) {
      const pt = this.points[a];
      const tp = this._mapXY(pt.lat, pt.lon);
      const col = this.colorFor(pt.kind);
      let tx = tp.x;
      if (Math.abs(pt.lon - home.lon) > 180) {
        tx += (pt.lon > home.lon ? -1 : 1) * this.mw; // short way across dateline
      }
      const mxp = (hp.x + tx) / 2;
      const myp = (hp.y + tp.y) / 2 - Math.min(64, Math.abs(tx - hp.x) * 0.24 + 20);
      ctx.strokeStyle = col;
      ctx.globalAlpha = 0.4;
      ctx.lineWidth = pt.kind === 'client' ? 0.8 : 1.2;
      ctx.beginPath();
      ctx.moveTo(hp.x, hp.y);
      ctx.quadraticCurveTo(mxp, myp, tx, tp.y);
      ctx.stroke();
      ctx.globalAlpha = 1;
      const per = 3200 + (a % 5) * 260;
      const tt = ((now + a * 620) % per) / per;
      const u = 1 - tt;
      const px = u * u * hp.x + 2 * u * tt * mxp + tt * tt * tx;
      const py = u * u * hp.y + 2 * u * tt * myp + tt * tt * tp.y;
      ctx.fillStyle = col;
      ctx.shadowColor = col;
      ctx.shadowBlur = 8;
      ctx.beginPath();
      ctx.arc(px, py, pt.kind === 'client' ? 1.5 : 2, 0, 7);
      ctx.fill();
      ctx.shadowBlur = 0;
    }
    // markers
    const pulse = 0.5 + 0.5 * Math.sin(now * 0.004);
    this._hover = -1;
    for (let m = 0; m < this.points.length; m++) {
      const p2 = this.points[m];
      const mp = this._mapXY(p2.lat, p2.lon);
      const c2 = this.colorFor(p2.kind);
      const near =
        this._mx != null &&
        this._my != null &&
        Math.abs(mp.x - this._mx) < 8 &&
        Math.abs(mp.y - this._my) < 8;
      if (near) this._hover = m;
      const rad = p2.kind === 'client' ? 2 : 3;
      ctx.strokeStyle = c2;
      ctx.globalAlpha = 0.5 * (1 - pulse);
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.arc(mp.x, mp.y, rad + 1 + pulse * (p2.kind === 'client' ? 4 : 7), 0, 7);
      ctx.stroke();
      ctx.globalAlpha = 1;
      ctx.fillStyle = c2;
      ctx.shadowColor = c2;
      ctx.shadowBlur = 7;
      ctx.beginPath();
      ctx.arc(mp.x, mp.y, rad, 0, 7);
      ctx.fill();
      ctx.shadowBlur = 0;
      if (p2.label) this._label(mp.x, mp.y - rad - 4, p2.label, c2);
    }
    ctx.strokeStyle = '#ffd089';
    ctx.globalAlpha = 0.55 * (1 - pulse);
    ctx.lineWidth = 1.2;
    ctx.beginPath();
    ctx.arc(hp.x, hp.y, 5 + pulse * 9, 0, 7);
    ctx.stroke();
    ctx.globalAlpha = 1;
    ctx.fillStyle = 'rgba(255,208,137,0.9)';
    ctx.beginPath();
    ctx.arc(hp.x, hp.y, 4.5, 0, 7);
    ctx.fill();
    ctx.fillStyle = '#fff';
    ctx.shadowColor = '#ffd089';
    ctx.shadowBlur = 9;
    ctx.beginPath();
    ctx.arc(hp.x, hp.y, 2.1, 0, 7);
    ctx.fill();
    ctx.shadowBlur = 0;
    this._label(hp.x, hp.y - 12, this.home.label || 'THIS NODE', '#ffd089');
    this._drawHover();
  }
}

/** Canvas element carrying its live SdnGlobe instance (set by the action). */
export interface SdnGlobeCanvas extends HTMLCanvasElement {
  __sdnGlobe?: SdnGlobe;
}

/**
 * Svelte action: `<canvas use:sdnGlobe={options} />`.
 * Mode changes are applied via update (pass a fresh options object with the
 * new getMode/points); the underlying instance is reused. The instance is
 * exposed on the element as `__sdnGlobe` for tests and debugging.
 */
export function sdnGlobe(
  canvas: HTMLCanvasElement,
  options: SdnGlobeOptions,
): {
  update: (next: SdnGlobeOptions) => void;
  destroy: () => void;
} {
  const globe = new SdnGlobe(canvas, options);
  (canvas as SdnGlobeCanvas).__sdnGlobe = globe;
  return {
    update(next: SdnGlobeOptions) {
      globe.home = next.home;
      globe.setPoints(next.points ?? []);
      const mode = next.mode ?? next.getMode?.();
      if (mode) globe.setMode(mode);
    },
    destroy() {
      globe.destroy();
      delete (canvas as SdnGlobeCanvas).__sdnGlobe;
    },
  };
}
