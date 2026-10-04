// Reel kit: everything the Space Data Network family of showreels share.
// A reel is a deterministic motion-graphics piece; every frame is a pure
// function of its time, so render.mjs can step it frame by frame and encode
// the result. Layers, back to front:
//   1. a WebGL2 ray-traced Earth (the site's earth.js shader, extended)
//   2. Canvas2D vector layers: satellites, orbits, network arcs, typography
//   3. a glow layer, blurred twice and added for bloom
//   4. subframe accumulation for real motion blur
//   5. a WebGL2 finishing pass: chromatic aberration on cuts, vignette, grain
// Colors and type follow the style guide (docs/STYLE-GUIDE.md). Each reel in
// reels/ builds a studio with createStudio() and draws its own scenes.

export const W = 1920;
export const H = 1080;
export const FPS = 30;
export const SUBFRAMES = 10;
export const SHUTTER = 0.5; // fraction of a frame the virtual shutter stays open

export const AMBER = "#f5a524";
export const CYAN = "#59d9ff";
export const SAT = "#b3e0ff";
export const RED = "#ff3b30";
export const INK = "#f5f5f7";
export const MUTED = "rgba(245,245,247,0.55)";
export const SANS = '-apple-system, "SF Pro Display", system-ui, "Helvetica Neue", Arial, sans-serif';
export const MONO = 'ui-monospace, "SF Mono", Menlo, Consolas, monospace';
export const D = Math.PI / 180;
// Text content (counters, scrambled glyphs) follows the frame, not the
// subframe, so motion blur never averages two different strings together.
export let FRAME_T = 0;

// ---------------------------------------------------------------- utilities
export const clamp = (x, a = 0, b = 1) => Math.min(b, Math.max(a, x));
export const lerp = (a, b, t) => a + (b - a) * t;
export const seg = (t, a, b) => clamp((t - a) / (b - a));
export const easeOutExpo = (x) => (x >= 1 ? 1 : 1 - Math.pow(2, -10 * x));
export const easeInExpo = (x) => (x <= 0 ? 0 : Math.pow(2, 10 * x - 10));
export const easeInOutExpo = (x) =>
  x <= 0 ? 0 : x >= 1 ? 1 : x < 0.5 ? Math.pow(2, 20 * x - 10) / 2 : (2 - Math.pow(2, -20 * x + 10)) / 2;
export const easeInOutCubic = (x) => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2);
export const easeOutCubic = (x) => 1 - Math.pow(1 - x, 3);
export const easeInCubic = (x) => x * x * x;
export const easeOutBack = (x) => {
  const c1 = 1.9;
  const c3 = c1 + 1;
  return 1 + c3 * Math.pow(x - 1, 3) + c1 * Math.pow(x - 1, 2);
};
export const v3 = (x, y, z) => [x, y, z];
export const add = (a, b) => [a[0] + b[0], a[1] + b[1], a[2] + b[2]];
export const sub = (a, b) => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
export const mul = (a, s) => [a[0] * s, a[1] * s, a[2] * s];
export const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
export const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
export const len = (a) => Math.hypot(a[0], a[1], a[2]);
export const norm = (a) => mul(a, 1 / (len(a) || 1));
export const mix3 = (a, b, t) => [lerp(a[0], b[0], t), lerp(a[1], b[1], t), lerp(a[2], b[2], t)];

export function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function canvas(w, h) {
  const c = document.createElement("canvas");
  c.width = w;
  c.height = h;
  return c;
}

// --------------------------------------------------------------- time ramp
// Scene time τ runs at a variable rate(t) (slow motion, freezes, snaps);
// τ(t) is integrated once.
export function makeTau(rate, duration) {
  const STEP = 0.001;
  const n = Math.ceil((duration + 1) / STEP);
  const a = new Float64Array(n + 1);
  for (let i = 1; i <= n; i++) a[i] = a[i - 1] + rate((i - 0.5) * STEP) * STEP;
  return (t) => {
    const x = clamp(t, 0, duration + 0.999) / STEP;
    const i = Math.floor(x);
    return lerp(a[i], a[i + 1], x - i);
  };
}

// ------------------------------------------------------------------ orbits
export const OMEGA_K = 0.34;
export function orbitBasis(inc, raan) {
  const ci = Math.cos(inc);
  const si = Math.sin(inc);
  const cO = Math.cos(raan);
  const sO = Math.sin(raan);
  const rot = (p) => {
    const x = p[0];
    const y = p[1] * ci - p[2] * si;
    const z = p[1] * si + p[2] * ci;
    return [x * cO + z * sO, y, -x * sO + z * cO];
  };
  // In-plane axes: e1 at u = 0, e2 at u = 90°. The base plane is x-z.
  return { e1: rot([1, 0, 0]), e2: rot([0, 0, 1]) };
}
export function orbitPos(o, u) {
  return add(mul(o.e1, o.r * Math.cos(u)), mul(o.e2, o.r * Math.sin(u)));
}

// The sky: a crowded low orbit, navigation shells and the geostationary
// ring, each satellite born at its own moment for the opening fill.
export function makeSky() {
  const R = rng(7);
  const list = [];
  const shell = (n, r0, r1, incDeg, spread, t0, t1, kind) => {
    for (let i = 0; i < n; i++) {
      const inc = (incDeg + (R() - 0.5) * spread) * D;
      const raan = R() * Math.PI * 2;
      const b = orbitBasis(inc, raan);
      const r = lerp(r0, r1, R());
      list.push({ ...b, r, u0: R() * Math.PI * 2, w: OMEGA_K * Math.pow(r, -1.5), born: lerp(t0, t1, R()), kind, tw: R() });
    }
  };
  shell(2600, 1.075, 1.095, 53, 1.5, 2.25, 3.0, 0);
  shell(900, 1.07, 1.09, 43, 1.5, 2.3, 3.05, 0);
  shell(900, 1.08, 1.13, 97.6, 1.2, 2.35, 3.1, 0);
  shell(700, 1.09, 1.12, 70, 2, 2.4, 3.15, 0);
  shell(1100, 1.06, 1.35, 60, 90, 2.45, 3.3, 0);
  shell(260, 2.05, 2.25, 55, 3, 2.95, 3.5, 1);
  shell(160, 1.6, 2.5, 60, 60, 3.0, 3.55, 1);
  // Geostationary belt: a thin ring, near-zero inclination.
  for (let i = 0; i < 420; i++) {
    const b = orbitBasis((R() - 0.5) * 2.5 * D, R() * Math.PI * 2);
    const r = 3.0 + (R() - 0.5) * 0.03;
    list.push({ ...b, r, u0: R() * Math.PI * 2, w: 0.012, born: lerp(3.2, 3.8, R()), kind: 2, tw: R() });
  }
  return list;
}

// ------------------------------------------------------------------ camera
export function lookAt(pos, target, upHint) {
  const f = norm(sub(target, pos));
  const r = norm(cross(f, upHint));
  const u = cross(r, f);
  return { pos, f, r, u };
}
export function geoDir(lat, lon, spin) {
  const a = lon * D + spin;
  return [Math.cos(lat * D) * Math.sin(a), Math.sin(lat * D), Math.cos(lat * D) * Math.cos(a)];
}
export const TAN = Math.tan(22 * D);
// Camera distance at which the globe's limb has radius px on screen.
export function distForRadius(px) {
  const t = (px / (H / 2)) * TAN;
  return Math.sqrt(1 + 1 / (t * t));
}
// A camera on a line from the Earth's center through (lat, lon), dist Earth
// radii out, looking at the center; off shifts the frame, zoom narrows it.
export function orbitCamera(lat, lon, dist, { off = [0, 0], zoom = 1, spin = 0 } = {}) {
  const pos = mul(geoDir(lat, lon, 0), dist);
  return { ...lookAt(pos, [0, 0, 0], [0, 1, 0]), spin, off, zoom, dist };
}
// World → screen, matching the ray tracer's projection exactly.
export function project(cam, p) {
  const d = sub(p, cam.pos);
  const z = dot(d, cam.f);
  if (z <= 0.01) return null;
  const tan = TAN / cam.zoom;
  const vx = dot(d, cam.r) / z / (tan * (W / H)) + cam.off[0];
  const vy = dot(d, cam.u) / z / tan + cam.off[1];
  return { x: (vx * 0.5 + 0.5) * W, y: (0.5 - vy * 0.5) * H, z };
}
// Is p hidden behind the planet as seen from the camera?
export function occluded(cam, p) {
  const d = sub(p, cam.pos);
  const L = len(d);
  const rd = mul(d, 1 / L);
  const b = dot(cam.pos, rd);
  const c = dot(cam.pos, cam.pos) - 1;
  const h = b * b - c;
  if (h <= 0) return false;
  const t0 = -b - Math.sqrt(h);
  return t0 > 0 && t0 < L;
}

// ------------------------------------------------------------ WebGL: Earth
export const EARTH_FRAG = `#version 300 es
precision highp float;
in vec2 v;out vec4 o;
uniform sampler2D uDay,uNight,uClouds;
uniform vec2 uRes,uOff;uniform vec3 uCam,uSun;uniform mat3 uBasis;
uniform float uTan,uSpin,uCloudSpin,uFade,uRim,uStars;
const float PI=3.14159265;
float hash(vec3 p){p=fract(p*.3183099+.1);p*=17.;return fract(p.x*p.y*p.z*(p.x+p.y+p.z));}
vec3 lin(vec3 c){return c*c;}
vec2 geo(vec3 n,float spin){float lon=atan(n.x,n.z)-spin;return vec2(lon,asin(clamp(n.y,-1.,1.)));}
vec4 tex(sampler2D t,vec2 g){
  vec2 uv=vec2(fract((g.x+PI)/(2.*PI)),.5-g.y/PI);
  vec2 uv2=vec2(fract((g.x)/(2.*PI)),uv.y);
  vec2 dx=dFdx(uv),dy=dFdy(uv),dx2=dFdx(uv2),dy2=dFdy(uv2);
  if(dot(dx2,dx2)+dot(dy2,dy2)<dot(dx,dx)+dot(dy,dy)){dx=dx2;dy=dy2;}
  return textureGrad(t,uv,dx,dy);}
void main(){
  float asp=uRes.x/uRes.y;
  vec2 q=(v-uOff)*vec2(asp,1.)*uTan;
  vec3 rd=normalize(uBasis*vec3(q,-1.));
  vec3 ro=uCam;
  float b=dot(ro,rd),c=dot(ro,ro)-1.,h=b*b-c;
  vec3 col=vec3(0.);
  float closest=length(ro-rd*b);
  vec3 cp=normalize(ro-rd*b);
  if(h>0.&&-b-sqrt(h)>0.){
    vec3 n=normalize(ro+rd*(-b-sqrt(h)));
    float d=dot(n,uSun);
    vec2 g=geo(n,uSpin);
    vec3 day=lin(tex(uDay,g).rgb);
    vec3 night=lin(tex(uNight,g).rgb);
    float cl=tex(uClouds,geo(n,uSpin+uCloudSpin)).r;cl=smoothstep(.15,.95,cl);
    float lit=smoothstep(-.08,.22,d);
    float ocean=smoothstep(.02,.1,day.b-day.r);
    vec3 hv=normalize(uSun-rd);
    float spec=pow(max(dot(n,hv),0.),60.)*ocean*(1.-cl)*.9;
    vec3 dcol=mix(day,vec3(.95),cl*.9)*(.06+1.15*max(d,0.))+vec3(1.,.9,.75)*spec*lit;
    dcol*=mix(vec3(1.),vec3(1.25,.8,.55),smoothstep(.35,0.,d)*lit);
    vec3 ncol=night*vec3(1.6,1.25,.85)*(1.-cl*.85)*smoothstep(.12,-.12,d);
    col=dcol*lit+ncol*1.35;
    float fr=pow(1.-max(dot(n,-rd),0.),3.);
    col+=vec3(.25,.5,1.)*fr*smoothstep(-.3,.5,d)*.9;
  }else{
    vec3 s=rd*420.;vec3 cell=floor(s);float hs=hash(cell);
    if(hs>.994){vec3 off=vec3(hash(cell+1.7),hash(cell+3.1),hash(cell+5.3))-.5;
      float st=smoothstep(.22,0.,length(fract(s)-.5-off*.5));col+=vec3(.8,.85,1.)*st*(hs-.994)*140.*uStars;}
    col+=vec3(1.,.92,.8)*(pow(max(dot(rd,uSun),0.),3000.)*40.+pow(max(dot(rd,uSun),0.),90.)*.14);
  }
  float alt=closest-1.;
  if(alt>-.02){
    float glow=exp(-max(alt,0.)*55.)*smoothstep(-.02,.004,alt);
    float day=smoothstep(-.35,.4,dot(cp,uSun));
    float fwd=pow(max(dot(rd,uSun),0.),6.);
    col+=glow*(vec3(.3,.55,1.)*day*.9+vec3(1.,.55,.25)*fwd*2.2);
    col+=glow*uRim*vec3(1.,.66,.18)*1.4;
  }
  col=sqrt(1.-exp(-col*1.1));
  o=vec4(col*uFade,1.);
}`;
export const QUAD_VERT = "#version 300 es\nin vec2 p;out vec2 v;void main(){v=p;gl_Position=vec4(p,0.,1.);}";

export function makeProgram(gl, vs, fs) {
  const sh = (type, src) => {
    const s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s));
    return s;
  };
  const p = gl.createProgram();
  gl.attachShader(p, sh(gl.VERTEX_SHADER, vs));
  gl.attachShader(p, sh(gl.FRAGMENT_SHADER, fs));
  gl.linkProgram(p);
  if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p));
  return p;
}
export function quad(gl, prog) {
  const vao = gl.createVertexArray();
  gl.bindVertexArray(vao);
  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  gl.bindVertexArray(null);
  return vao;
}
export function uniforms(gl, prog, names) {
  const u = {};
  for (const n of names) u[n] = gl.getUniformLocation(prog, n);
  return u;
}
export function loadImage(url) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error(`image ${url}`));
    img.src = url;
  });
}

// ---------------------------------------------------------- finishing pass
export const POST_FRAG = `#version 300 es
precision highp float;
in vec2 v;out vec4 o;
uniform sampler2D uImg;uniform vec2 uRes;uniform float uCA,uGrain,uSeed,uFlash,uFade;
float h(vec2 p){return fract(sin(dot(p,vec2(12.9898,78.233))+uSeed)*43758.5453);}
void main(){
  vec2 uv=v*.5+.5;uv.y=1.-uv.y;
  vec2 c=uv-.5;
  float r2=dot(c,c);
  vec2 dir=c*(uCA+.0015)*(1.+r2*2.);
  vec3 col;
  col.r=texture(uImg,uv+dir).r;
  col.g=texture(uImg,uv).g;
  col.b=texture(uImg,uv-dir).b;
  float vig=smoothstep(1.15,.25,length(c*vec2(1.,.82))*1.35);
  col*=mix(.62,1.,vig);
  col+=uFlash;
  float g=(h(gl_FragCoord.xy)-.5)*uGrain;
  col+=g*(1.-col*.6);
  o=vec4(clamp(col,0.,1.)*uFade,1.);
}`;

// One studio per reel: the Earth renderer, the 2D scene and glow layers, the
// drawing kit, and the frame loop (motion blur, bloom, finishing pass).
export async function createStudio({ base = "", hudTitle = "SPACE DATA NETWORK", hudUrl = "SPACEDATANETWORK.ORG" } = {}) {
  const glc = canvas(W, H);
  const gl = glc.getContext("webgl2", { antialias: false, alpha: false, preserveDrawingBuffer: true });
  if (!gl) throw new Error("WebGL2 unavailable");
  const earth = makeProgram(gl, QUAD_VERT, EARTH_FRAG);
  const earthVao = quad(gl, earth);
  const EU = uniforms(gl, earth, ["uDay", "uNight", "uClouds", "uRes", "uOff", "uCam", "uSun", "uBasis", "uTan", "uSpin", "uCloudSpin", "uFade", "uRim", "uStars"]);
  const [day, night, clouds] = await Promise.all(
    ["day", "night", "clouds"].map((n) => loadImage(`${base}../../docs/img/earth/${n}.webp`)),
  );
  gl.useProgram(earth);
  [day, night, clouds].forEach((img, i) => {
    const t = gl.createTexture();
    gl.activeTexture(gl.TEXTURE0 + i);
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
    gl.generateMipmap(gl.TEXTURE_2D);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.REPEAT);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    const ext = gl.getExtension("EXT_texture_filter_anisotropic");
    if (ext) gl.texParameterf(gl.TEXTURE_2D, ext.TEXTURE_MAX_ANISOTROPY_EXT, 8);
  });
  gl.uniform1i(EU.uDay, 0);
  gl.uniform1i(EU.uNight, 1);
  gl.uniform1i(EU.uClouds, 2);

  const out = canvas(W, H);
  const pgl = out.getContext("webgl2", { antialias: false, alpha: false, preserveDrawingBuffer: true });
  const post = makeProgram(pgl, QUAD_VERT, POST_FRAG);
  const postVao = quad(pgl, post);
  const PU = uniforms(pgl, post, ["uImg", "uRes", "uCA", "uGrain", "uSeed", "uFlash", "uFade"]);
  const postTex = pgl.createTexture();
  pgl.bindTexture(pgl.TEXTURE_2D, postTex);
  pgl.texParameteri(pgl.TEXTURE_2D, pgl.TEXTURE_MIN_FILTER, pgl.LINEAR);
  pgl.texParameteri(pgl.TEXTURE_2D, pgl.TEXTURE_MAG_FILTER, pgl.LINEAR);
  pgl.texParameteri(pgl.TEXTURE_2D, pgl.TEXTURE_WRAP_S, pgl.CLAMP_TO_EDGE);
  pgl.texParameteri(pgl.TEXTURE_2D, pgl.TEXTURE_WRAP_T, pgl.CLAMP_TO_EDGE);

  const scene = canvas(W, H);
  const sx = scene.getContext("2d");
  const glow = canvas(W / 2, H / 2);
  const gx = glow.getContext("2d");
  const blurA = canvas(W / 2, H / 2);
  const bax = blurA.getContext("2d");
  const blurB = canvas(W / 4, H / 4);
  const bbx = blurB.getContext("2d");
  const accum = canvas(W, H);
  const ax = accum.getContext("2d");

  // ---------------------------------------------------------- drawing kit
  function font(weight, px, family = SANS) {
    return `${weight} ${px}px ${family}`;
  }
  // Text revealed upward out of a mask, the staple of kinetic typography.
  function maskText(ctx, text, x, y, px, weight, color, tIn, tOut, t, opts = {}) {
    const inK = easeOutExpo(seg(t, tIn, tIn + (opts.dur ?? 0.7)));
    const outK = tOut == null ? 0 : easeInExpo(seg(t, tOut, tOut + (opts.outDur ?? 0.45)));
    if (inK <= 0 || outK >= 1) return;
    ctx.save();
    ctx.font = font(weight, px, opts.family ?? SANS);
    if (opts.tracking) ctx.letterSpacing = `${opts.tracking}px`;
    ctx.textAlign = opts.align ?? "left";
    const m = ctx.measureText(text);
    const w = m.width;
    const left = ctx.textAlign === "center" ? x - w / 2 : ctx.textAlign === "right" ? x - w : x;
    ctx.beginPath();
    ctx.rect(left - 4, y - px * 1.05, w + 8, px * 1.4);
    ctx.clip();
    const dy = (1 - inK) * px * 1.25 - outK * px * 1.25;
    ctx.fillStyle = color;
    ctx.globalAlpha = opts.alpha ?? 1;
    ctx.fillText(text, x, y + dy);
    ctx.restore();
  }
  // Characters scramble before settling: a decoder look for data values.
  const GLYPHS = "0123456789ABCDEF#%&*+-=/<>";
  function decodeText(ctx, text, x, y, t0, dur, t, seed) {
    const k = seg(t, t0, t0 + dur);
    if (k <= 0) return;
    const R = rng(seed + Math.round(FRAME_T * 30));
    const k2 = seg(FRAME_T, t0, t0 + dur);
    let s = "";
    for (let i = 0; i < text.length; i++) {
      const settle = i / text.length;
      if (k2 > settle * 0.7 + 0.3) s += text[i];
      else if (k2 > settle * 0.7) s += text[i] === " " ? " " : GLYPHS[Math.floor(R() * GLYPHS.length)];
      else break;
    }
    ctx.fillText(s, x, y);
  }
  // Radius in full-resolution pixels; the glow layer is half resolution.
  function dotGlow(x, y, r, color, a) {
    if (a <= 0 || r <= 0) return;
    gx.globalAlpha = Math.min(1, a);
    gx.fillStyle = color;
    gx.beginPath();
    gx.arc(x / 2, y / 2, r / 2, 0, Math.PI * 2);
    gx.fill();
  }

  // --------------------------------------------------------------- the mark
  // Faint ring, amber satellite at top center, amber tail fading up the left
  // from 6 o'clock, three linked nodes (style guide: docs/STYLE-GUIDE.md §1).
  function drawMark(cx, cy, R, t, k) {
    // k: {head, ring, nodes, links, fade}
    const s = R / 13; // the brand mark is drawn on a 32-unit box with r = 13
    sx.save();
    sx.globalAlpha = k.fade;
    // ring
    if (k.ring > 0) {
      sx.strokeStyle = "rgba(245,245,247,0.3)";
      sx.lineWidth = 1.6 * s;
      sx.beginPath();
      sx.arc(cx, cy, R, Math.PI / 2, Math.PI / 2 + Math.PI * 2 * k.ring);
      sx.stroke();
    }
    // tail: from 6 o'clock up the left side to the head
    const a0 = Math.PI / 2; // 6 o'clock in canvas angles
    const aH = a0 + Math.PI * k.head; // sweeps through 9 o'clock to 12
    if (k.head > 0) {
      const steps = 48;
      for (let i = 0; i < steps; i++) {
        const u0 = i / steps;
        const u1 = (i + 1) / steps;
        const alpha = Math.pow(u1, 1.6);
        sx.strokeStyle = `rgba(245,165,36,${alpha})`;
        sx.lineWidth = 2.2 * s;
        sx.lineCap = "round";
        sx.beginPath();
        sx.arc(cx, cy, R, lerp(a0, aH, u0), lerp(a0, aH, u1) + 0.002);
        sx.stroke();
      }
      const hx = cx + R * Math.cos(aH);
      const hy = cy + R * Math.sin(aH);
      sx.fillStyle = AMBER;
      sx.beginPath();
      sx.arc(hx, hy, 2.5 * s, 0, Math.PI * 2);
      sx.fill();
      dotGlow(hx, hy, 2.6 * s, AMBER, 0.55 * k.fade);
      dotGlow(hx, hy, 1.1 * s, "#fff3d6", 0.6 * k.fade);
    }
    // triangle of linked nodes
    const P = [
      [cx, cy + (10.2 - 16) * s],
      [cx + (10.4 - 16) * s, cy + (20.1 - 16) * s],
      [cx + (21.6 - 16) * s, cy + (20.1 - 16) * s],
    ];
    if (k.links > 0) {
      sx.strokeStyle = INK;
      sx.lineWidth = 1.6 * s;
      sx.lineJoin = "round";
      for (let i = 0; i < 3; i++) {
        const a = P[i];
        const b = P[(i + 1) % 3];
        const e = clamp(k.links * 3 - i);
        if (e <= 0) continue;
        sx.beginPath();
        sx.moveTo(a[0], a[1]);
        sx.lineTo(lerp(a[0], b[0], e), lerp(a[1], b[1], e));
        sx.stroke();
      }
    }
    P.forEach((p, i) => {
      const e = clamp(k.nodes * 3 - i * 0.9);
      if (e <= 0) return;
      const sc = easeOutBack(e);
      sx.fillStyle = INK;
      sx.beginPath();
      sx.arc(p[0], p[1], 2.6 * s * sc, 0, Math.PI * 2);
      sx.fill();
      dotGlow(p[0], p[1], 2 * s * sc, "#ffffff", 0.12 * k.fade);
    });
    sx.restore();
  }

  // ---------------------------------------------------------- HUD chrome
  // Corner marks, the title and the running timecode; gone from fadeAt.
  function hud(t, frame, fadeAt) {
    const a = 0.55 * (1 - seg(t, fadeAt, fadeAt + 0.4)) * seg(t, 0.2, 0.8);
    if (a <= 0) return;
    sx.save();
    sx.globalAlpha = a;
    sx.strokeStyle = "rgba(245,245,247,0.5)";
    sx.lineWidth = 1.5;
    const m = 44;
    const L = 26;
    [[m, m, 1, 1], [W - m, m, -1, 1], [m, H - m, 1, -1], [W - m, H - m, -1, -1]].forEach(([x, y, dx, dy]) => {
      sx.beginPath();
      sx.moveTo(x, y + dy * L);
      sx.lineTo(x, y);
      sx.lineTo(x + dx * L, y);
      sx.stroke();
    });
    sx.font = font(500, 15, MONO);
    sx.fillStyle = INK;
    sx.letterSpacing = "1.5px";
    sx.textAlign = "left";
    sx.fillText(hudTitle, m + 12, m + 30);
    const f = frame % FPS;
    const s = Math.floor(frame / FPS);
    sx.textAlign = "right";
    sx.fillText(`00:00:${String(s).padStart(2, "0")}:${String(f).padStart(2, "0")}`, W - m - 12, m + 30);
    sx.fillText(hudUrl, W - m - 12, H - m - 16);
    sx.restore();
  }

  // ------------------------------------------------------------ more kit
  // A dark wash in the lower-left corner, behind type set over the planet.
  function scrim(a) {
    if (a <= 0) return;
    const g = sx.createRadialGradient(260, 900, 0, 260, 900, 980);
    g.addColorStop(0, `rgba(0,0,0,${0.6 * a})`);
    g.addColorStop(0.55, `rgba(0,0,0,${0.35 * a})`);
    g.addColorStop(1, "rgba(0,0,0,0)");
    sx.fillStyle = g;
    sx.fillRect(0, 0, W, H);
  }
  function pill(x, y, w, h, stroke, fill, lw = 1.5) {
    sx.beginPath();
    sx.roundRect(x, y, w, h, h / 2);
    if (fill) {
      sx.fillStyle = fill;
      sx.fill();
    }
    if (stroke) {
      sx.strokeStyle = stroke;
      sx.lineWidth = lw;
      sx.stroke();
    }
  }
  // Style-guide line icons on a 24-unit box.
  function icon(size, cx, cy, color, alpha, draw) {
    if (alpha <= 0 || size <= 0) return;
    const s = size / 24;
    sx.save();
    sx.globalAlpha = alpha;
    sx.translate(cx - 12 * s, cy - 12 * s);
    sx.scale(s, s);
    sx.strokeStyle = color;
    sx.lineWidth = 1.7;
    sx.lineCap = "round";
    sx.lineJoin = "round";
    draw();
    sx.restore();
  }
  function badge(kind, cx, cy, size, color, alpha) {
    icon(size, cx, cy, color, alpha, () => {
      sx.beginPath();
      sx.arc(12, 12, 9, 0, Math.PI * 2);
      if (kind === "check") {
        sx.moveTo(8.5, 12.3);
        sx.lineTo(11, 14.8);
        sx.lineTo(15.8, 9.6);
      } else {
        sx.moveTo(9, 9);
        sx.lineTo(15, 15);
        sx.moveTo(15, 9);
        sx.lineTo(9, 15);
      }
      sx.stroke();
    });
  }
  // open: 0 shut … 1 shackle lifted clear of the body.
  function padlock(cx, cy, size, open, color, alpha) {
    icon(size, cx, cy, color, alpha, () => {
      const lift = 3.4 * open;
      sx.beginPath();
      sx.roundRect(4, 10, 16, 11, 2);
      sx.fillStyle = "rgba(0,0,0,0.7)";
      sx.fill();
      sx.stroke();
      sx.beginPath();
      sx.moveTo(8, 10);
      sx.lineTo(8, 7 - lift);
      sx.arc(12, 7 - lift, 4, Math.PI, 0);
      sx.lineTo(16, 10 - lift);
      sx.stroke();
      sx.beginPath();
      sx.moveTo(12, 14.6);
      sx.lineTo(12, 16.8);
      sx.stroke();
    });
  }
  function keyIcon(cx, cy, size, color, alpha) {
    icon(size, cx, cy, color, alpha, () => {
      sx.beginPath();
      sx.arc(7.5, 12, 3.8, 0, Math.PI * 2);
      sx.moveTo(11.3, 12);
      sx.lineTo(21, 12);
      sx.moveTo(17, 12);
      sx.lineTo(17, 15);
      sx.moveTo(20, 12);
      sx.lineTo(20, 14.5);
      sx.stroke();
    });
  }
  function bezierPts(a, c1, c2, b, n = 48) {
    const pts = [];
    for (let i = 0; i <= n; i++) {
      const u = i / n;
      const v = 1 - u;
      pts.push([
        v * v * v * a[0] + 3 * v * v * u * c1[0] + 3 * v * u * u * c2[0] + u * u * u * b[0],
        v * v * v * a[1] + 3 * v * v * u * c1[1] + 3 * v * u * u * c2[1] + u * u * u * b[1],
      ]);
    }
    return pts;
  }
  function pointOn(pts, k) {
    const n = (pts.length - 1) * clamp(k);
    const i = Math.min(pts.length - 2, Math.floor(n));
    const f = n - i;
    return [lerp(pts[i][0], pts[i + 1][0], f), lerp(pts[i][1], pts[i + 1][1], f)];
  }
  // Strokes the first k (0…1) of a polyline, with a matching trace on the glow layer.
  function strokeOn(pts, k, color, width, alpha, glowA = 0, dash = null) {
    if (k <= 0 || alpha <= 0) return;
    const n = (pts.length - 1) * clamp(k);
    const whole = Math.floor(n);
    const path = pts.slice(0, whole + 1);
    if (whole < pts.length - 1) path.push(pointOn(pts, k));
    sx.save();
    sx.strokeStyle = color;
    sx.lineWidth = width;
    sx.globalAlpha = alpha;
    sx.lineCap = "round";
    sx.lineJoin = "round";
    if (dash) sx.setLineDash(dash);
    sx.beginPath();
    path.forEach(([x, y], i) => (i ? sx.lineTo(x, y) : sx.moveTo(x, y)));
    sx.stroke();
    sx.restore();
    if (glowA > 0) {
      gx.strokeStyle = color;
      gx.globalAlpha = glowA;
      gx.lineWidth = width * 1.5;
      gx.beginPath();
      path.forEach(([x, y], i) => (i ? gx.lineTo(x / 2, y / 2) : gx.moveTo(x / 2, y / 2)));
      gx.stroke();
    }
  }
  function stamp(cx, cy, st, alpha) {
    const sc = easeOutBack(st);
    sx.save();
    sx.globalAlpha = alpha;
    sx.translate(cx, cy);
    sx.scale(sc * (1 + 0.6 * (1 - st)), sc * (1 + 0.6 * (1 - st)));
    sx.rotate(-0.04);
    const w = 400;
    const h = 72;
    sx.fillStyle = "rgba(245,165,36,0.14)";
    sx.strokeStyle = AMBER;
    sx.lineWidth = 2.5;
    sx.beginPath();
    sx.roundRect(-w / 2, -h / 2, w, h, h / 2);
    sx.fill();
    sx.stroke();
    sx.lineWidth = 4;
    sx.lineCap = "round";
    sx.lineJoin = "round";
    sx.beginPath();
    sx.moveTo(-w / 2 + 38, 2);
    sx.lineTo(-w / 2 + 52, 16);
    sx.lineTo(-w / 2 + 78, -14);
    sx.stroke();
    sx.fillStyle = AMBER;
    sx.font = font(700, 25, SANS);
    sx.letterSpacing = "3px";
    sx.textAlign = "left";
    sx.fillText("DIGITALLY SIGNED", -w / 2 + 98, 9);
    sx.restore();
    dotGlow(cx, cy, 150 * sc, AMBER, 0.1 * alpha * (1 - st * 0.7));
  }

  // --------------------------------------------------------------- scenes
  function renderEarth(cam, t, fade, rim, sunAz, sunEl) {
    gl.viewport(0, 0, W, H);
    gl.useProgram(earth);
    gl.bindVertexArray(earthVao);
    const sv = [Math.sin(sunAz * D) * Math.cos(sunEl * D), Math.sin(sunEl * D), Math.cos(sunAz * D) * Math.cos(sunEl * D)];
    const back = mul(cam.f, -1);
    const sun = norm(add(add(mul(cam.r, sv[0]), mul(cam.u, sv[1])), mul(back, sv[2])));
    gl.uniformMatrix3fv(EU.uBasis, false, [...cam.r, ...cam.u, ...back]);
    gl.uniform3fv(EU.uCam, cam.pos);
    gl.uniform3fv(EU.uSun, sun);
    gl.uniform2f(EU.uRes, W, H);
    gl.uniform2f(EU.uOff, cam.off[0], cam.off[1]);
    gl.uniform1f(EU.uTan, TAN / cam.zoom);
    gl.uniform1f(EU.uSpin, cam.spin);
    gl.uniform1f(EU.uCloudSpin, cam.spin * 0.35);
    gl.uniform1f(EU.uFade, fade);
    gl.uniform1f(EU.uRim, rim);
    gl.uniform1f(EU.uStars, 1);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }

  function drawSatellites(cam, t, T, alpha, spawn, sats) {
    const pts = sx;
    pts.save();
    pts.globalCompositeOperation = "lighter";
    for (const s of sats) {
      if (spawn && t < s.born) continue;
      const age = spawn ? t - s.born : 5;
      const p = orbitPos(s, s.u0 + s.w * T);
      const q = project(cam, p);
      if (!q || q.x < -10 || q.x > W + 10 || q.y < -10 || q.y > H + 10) continue;
      if (occluded(cam, p)) continue;
      const flash = Math.exp(-age * 9) * 1.4;
      const a = alpha * (0.55 + 0.45 * s.tw) * Math.min(1, age * 6);
      const size = (s.kind === 2 ? 2.2 : 1.8) * Math.min(2.2, 2.6 / Math.sqrt(q.z));
      pts.globalAlpha = clamp(a + flash * 0.4);
      pts.fillStyle = s.kind === 2 ? "#ffe0a8" : SAT;
      pts.fillRect(q.x - size / 2, q.y - size / 2, size, size);
      if (flash > 0.05 && s.tw > 0.85) dotGlow(q.x, q.y, 6 + flash * 4, SAT, clamp(flash * 0.12 * alpha));
    }
    pts.restore();
  }

  function drawOrbitPath(cam, o, T, color, alpha, width, arcFrac = 1, headGlow = true) {
    const N = 220;
    const u1 = o.u0 + o.w * T;
    let prev = null;
    sx.save();
    sx.lineCap = "round";
    for (let i = 0; i <= N; i++) {
      const f = i / N;
      const u = u1 - f * Math.PI * 2 * arcFrac;
      const p = orbitPos(o, u);
      const q = project(cam, p);
      const hidden = occluded(cam, p);
      if (q && prev && !hidden && !prev.hidden) {
        const a = alpha * Math.pow(1 - f, arcFrac < 1 ? 1.3 : 0.001);
        sx.strokeStyle = color;
        sx.globalAlpha = a;
        sx.lineWidth = width;
        sx.beginPath();
        sx.moveTo(prev.x, prev.y);
        sx.lineTo(q.x, q.y);
        sx.stroke();
        gx.strokeStyle = color;
        gx.globalAlpha = a * 0.6;
        gx.lineWidth = width * 1.5;
        gx.beginPath();
        gx.moveTo(prev.x / 2, prev.y / 2);
        gx.lineTo(q.x / 2, q.y / 2);
        gx.stroke();
      }
      prev = q ? { ...q, hidden } : null;
    }
    sx.restore();
    const hp = orbitPos(o, u1);
    const hq = project(cam, hp);
    if (hq && !occluded(cam, hp)) {
      sx.save();
      sx.globalAlpha = alpha;
      sx.fillStyle = color;
      sx.beginPath();
      sx.arc(hq.x, hq.y, width * 2.2, 0, Math.PI * 2);
      sx.fill();
      sx.restore();
      if (headGlow) {
        dotGlow(hq.x, hq.y, width * 7, color, alpha * 0.7);
        dotGlow(hq.x, hq.y, width * 2.5, "#ffffff", alpha * 0.6);
      }
      return hq;
    }
    return null;
  }

  // Great-circle arc lifted off the surface between two nodes.
  function arcPoint(a, b, f) {
    const om = Math.acos(clamp(dot(a, b), -1, 1));
    const so = Math.sin(om) || 1;
    const p = add(mul(a, Math.sin((1 - f) * om) / so), mul(b, Math.sin(f * om) / so));
    const lift = 1 + 0.06 + om * 0.16 * Math.sin(Math.PI * f);
    return mul(norm(p), lift);
  }
  // Projects an arc from a toward b up to fraction f and strokes it in pieces
  // that skip the planet's far side.
  function strokeArc(cam, a, b, f, color, width, alpha, glowColor, glowA, steps = 60) {
    let prev = null;
    sx.save();
    sx.lineCap = "round";
    for (let s = 0; s <= steps; s++) {
      const p = arcPoint(a, b, (s / steps) * f);
      const q = project(cam, p);
      const hid = occluded(cam, p);
      if (q && prev && !hid && !prev.hid) {
        sx.strokeStyle = color;
        sx.globalAlpha = alpha;
        sx.lineWidth = width;
        sx.beginPath();
        sx.moveTo(prev.x, prev.y);
        sx.lineTo(q.x, q.y);
        sx.stroke();
        gx.strokeStyle = glowColor;
        gx.globalAlpha = glowA;
        gx.lineWidth = width * 1.4;
        gx.beginPath();
        gx.moveTo(prev.x / 2, prev.y / 2);
        gx.lineTo(q.x / 2, q.y / 2);
        gx.stroke();
      }
      prev = q ? { ...q, hid } : null;
    }
    sx.restore();
  }

  // ------------------------------------------------------ the through line
  // Type always sits lower left: an eyebrow, one or two big lines, a caption.
  const LX = 150;
  const CAP_Y = 956;
  const BIG = 96;
  // Eyebrow and caption sizes are set for the site, where the reel plays
  // 740-1100 px wide: 40 px and 30 px here read at roughly 15-23 px and 12-17 px.
  const EYE = 40;
  const CAP = 30;
  const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#%&*+=/<>";
  const lineYs = (n) => Array.from({ length: n }, (_, i) => 870 - (n - 1 - i) * 100);
  function caption(text, kind, tIn, tOut, t) {
    const a = easeOutExpo(seg(t, tIn, tIn + 0.4)) * (tOut == null ? 1 : 1 - easeInExpo(seg(t, tOut, tOut + 0.28)));
    if (kind) badge(kind, LX + 17, CAP_Y - 10, 34, kind === "cross" ? RED : AMBER, a);
    maskText(sx, text, LX + (kind ? 52 : 2), CAP_Y, CAP, 600, kind === "check" ? "rgba(245,245,247,0.85)" : MUTED, tIn, tOut, t, { tracking: 3, outDur: 0.28 });
  }
  function block(lines, cap, tIn, tOut, t, px = 88) {
    const ys = lineYs(lines.length);
    lines.forEach(([text, color], i) => maskText(sx, text, LX, ys[i], px, 700, color, tIn + i * 0.13, tOut + i * 0.03, t, { tracking: -2.5, outDur: 0.28 }));
    if (cap) caption(cap, null, tIn + 0.4, tOut + 0.06, t);
  }
  // One shift: "TODAY" and the problem, struck through, scrambled and
  // rewritten as the network's answer.
  function shiftBlock(sh, t) {
    const ys = lineYs(sh.before.length);
    const eyeY = ys[0] - 112;
    maskText(sx, "TODAY", LX + 2, eyeY, EYE, 600, MUTED, sh.tIn, sh.tMorph + 0.1, t, { tracking: 3.5, outDur: 0.2 });
    maskText(sx, "ON THE NETWORK", LX + 2, eyeY, EYE, 600, AMBER, sh.tMorph + 0.3, sh.tOut, t, { tracking: 3.5, outDur: 0.28 });
    sh.before.forEach((b, i) => {
      const a = sh.after[i];
      const y = ys[i];
      const s0 = sh.tMorph + 0.2 + i * 0.08;
      const s1 = s0 + 0.4;
      if (t < s0) {
        maskText(sx, b, LX, y, BIG, 700, "rgba(245,245,247,0.5)", sh.tIn + 0.08 + i * 0.1, null, t, { tracking: -3 });
        const k = easeOutExpo(seg(t, sh.tMorph + i * 0.06, sh.tMorph + i * 0.06 + 0.18));
        if (k > 0) {
          sx.save();
          sx.font = font(700, BIG);
          sx.letterSpacing = "-3px";
          const w = sx.measureText(b).width;
          sx.strokeStyle = RED;
          sx.lineWidth = 6;
          sx.lineCap = "round";
          sx.beginPath();
          sx.moveTo(LX - 6, y - BIG * 0.3);
          sx.lineTo(LX - 6 + (w + 12) * k, y - BIG * 0.3);
          sx.stroke();
          sx.restore();
          dotGlow(LX + w * k, y - BIG * 0.3, 30, RED, 0.3 * (1 - k * 0.5));
        }
      } else if (t < s1) {
        const k = seg(FRAME_T, s0, s1);
        const R = rng(300 + i * 17 + Math.round(FRAME_T * 30));
        const n = Math.max(b.length, a.length);
        let str = "";
        for (let c = 0; c < n; c++) {
          const th = c / n;
          if (k > th * 0.6 + 0.4) str += a[c] ?? "";
          else if (k > th * 0.6) str += (a[c] ?? b[c]) === " " ? " " : LETTERS[Math.floor(R() * LETTERS.length)];
          else str += b[c] ?? "";
        }
        sx.save();
        sx.font = font(700, BIG);
        sx.letterSpacing = "-3px";
        sx.fillStyle = INK;
        sx.textAlign = "left";
        sx.fillText(str, LX, y);
        sx.restore();
      } else {
        maskText(sx, a, LX, y, BIG, 700, AMBER, s1 - 5, sh.tOut + i * 0.03, t, { tracking: -3, outDur: 0.3 });
      }
    });
    caption(sh.why, "cross", sh.tIn + 0.35, sh.tMorph + 0.1, t);
    caption(sh.fix, "check", sh.tMorph + 0.45, sh.fixOut ?? sh.tOut, t);
  }
  // A corner badge with a question mark: nobody knows where this came from.
  function questionBadge(cx, cy, size, alpha) {
    if (alpha <= 0) return;
    sx.save();
    sx.globalAlpha = alpha;
    sx.fillStyle = "rgba(0,0,0,0.85)";
    sx.strokeStyle = RED;
    sx.lineWidth = 1.7 * (size / 24);
    sx.beginPath();
    sx.arc(cx, cy, size * 0.4, 0, Math.PI * 2);
    sx.fill();
    sx.stroke();
    sx.fillStyle = RED;
    sx.font = font(700, size * 0.55, SANS);
    sx.letterSpacing = "0px";
    sx.textAlign = "center";
    sx.fillText("?", cx, cy + size * 0.2);
    sx.restore();
  }

  // An orbit arc from u0 to u1 (radians along the orbit), hidden where the
  // planet is in front of it.
  function strokeOrbitArc(cam, o, u0, u1, color, alpha, width, glowA = 0.6, dash = null) {
    if (alpha <= 0 || u1 === u0) return;
    const N = Math.max(8, Math.ceil(Math.abs(u1 - u0) * 40));
    let prev = null;
    sx.save();
    sx.lineCap = "round";
    sx.strokeStyle = color;
    sx.lineWidth = width;
    sx.globalAlpha = alpha;
    if (dash) sx.setLineDash(dash);
    gx.strokeStyle = color;
    gx.globalAlpha = alpha * glowA;
    gx.lineWidth = width * 1.5;
    sx.beginPath();
    gx.beginPath();
    for (let i = 0; i <= N; i++) {
      const p = orbitPos(o, lerp(u0, u1, i / N));
      const q = project(cam, p);
      const hidden = occluded(cam, p);
      if (q && prev && !hidden && !prev.hidden) {
        sx.moveTo(prev.x, prev.y);
        sx.lineTo(q.x, q.y);
        gx.moveTo(prev.x / 2, prev.y / 2);
        gx.lineTo(q.x / 2, q.y / 2);
      }
      prev = q ? { ...q, hidden } : null;
    }
    sx.stroke();
    if (glowA > 0) gx.stroke();
    sx.restore();
  }

  // An eyebrow over the lower-left type block (block() lines sit at 770-870).
  function eyebrow(text, lines, tIn, tOut, t, color = AMBER) {
    const y = lineYs(lines)[0] - 112;
    maskText(sx, text, LX + 2, y, EYE, 600, color, tIn, tOut, t, { tracking: 3.5, outDur: 0.28 });
  }

  // Closing card: the mark (optional), a title, a subtitle and the address,
  // revealed from t0 and held. The group is centered on the frame.
  function lockup({ title, subtitle, url, t0, mark = true }, t) {
    if (t < t0) return;
    const TP = 78;
    sx.save();
    sx.font = font(650, TP);
    sx.letterSpacing = "-1.5px";
    const tw = sx.measureText(title).width;
    sx.font = font(400, 28);
    sx.letterSpacing = "0px";
    const sw = sx.measureText(subtitle).width;
    sx.restore();
    const R = 118;
    const gap = 102;
    const textW = Math.max(tw, sw);
    const total = mark ? 2 * R + gap + textW : textW;
    const x0 = (W - total) / 2;
    const tx = mark ? x0 + 2 * R + gap : x0;
    if (mark) {
      drawMark(x0 + R, H / 2, R, t, {
        head: easeInOutExpo(seg(t, t0, t0 + 0.5)),
        ring: seg(t, t0 - 0.1, t0 + 0.2),
        nodes: seg(t, t0 + 0.52, t0 + 0.76),
        links: easeOutCubic(seg(t, t0 + 0.6, t0 + 0.82)),
        fade: seg(t, t0 - 0.1, t0 + 0.1),
      });
    }
    maskText(sx, title, tx, H / 2 + 8, TP, 650, INK, t0 + 0.72, null, t, { dur: 0.45, tracking: -1.5 });
    maskText(sx, subtitle, tx + 2, H / 2 + 64, 28, 400, MUTED, t0 + 0.8, null, t, { dur: 0.4 });
    maskText(sx, url, tx + 2, H / 2 - 92, 18, 600, AMBER, t0 + 0.84, null, t, { dur: 0.4, tracking: 4 });
  }

  // ---------------------------------------------------------- frame loop
  // Clears the scene and glow layers before a subframe is drawn.
  function beginSubframe() {
  sx.setTransform(1, 0, 0, 1, 0, 0);
  sx.globalCompositeOperation = "source-over";
  sx.globalAlpha = 1;
  sx.fillStyle = "#000";
  sx.fillRect(0, 0, W, H);
  gx.setTransform(1, 0, 0, 1, 0, 0);
  gx.globalAlpha = 1;
  gx.globalCompositeOperation = "source-over";
  gx.clearRect(0, 0, W / 2, H / 2);
  gx.globalCompositeOperation = "lighter";
  }
  // Bloom: the glow layer blurred at two radii, added over the scene.
  function bloom() {
  bax.globalCompositeOperation = "source-over";
  bax.clearRect(0, 0, W / 2, H / 2);
  bax.filter = "blur(4px)";
  bax.drawImage(glow, 0, 0);
  bax.filter = "none";
  bbx.clearRect(0, 0, W / 4, H / 4);
  bbx.filter = "blur(10px)";
  bbx.drawImage(glow, 0, 0, W / 4, H / 4);
  bbx.filter = "none";
  sx.save();
  sx.globalCompositeOperation = "lighter";
  sx.globalAlpha = 0.9;
  sx.drawImage(blurA, 0, 0, W, H);
  sx.globalAlpha = 0.8;
  sx.drawImage(blurB, 0, 0, W, H);
  sx.restore();
  }
  // Frame i: SUBFRAMES samples across the open shutter, averaged, then the
  // finishing pass: chromatic aberration ca(t), a flash(t) on hard cuts, and
  // fade(t) from black.
  function makeRenderFrame({ drawSubframe, ca, flash, fade }) {
    return function renderFrame(i) {
      const t0 = i / FPS;
      FRAME_T = t0;
      ax.globalCompositeOperation = "source-over";
      for (let s = 0; s < SUBFRAMES; s++) {
        const t = t0 + ((s + 0.5) / SUBFRAMES - 0.5) * (SHUTTER / FPS);
        drawSubframe(Math.max(0, t), i);
        ax.globalAlpha = 1 / (s + 1);
        ax.drawImage(scene, 0, 0);
      }
      ax.globalAlpha = 1;
      pgl.viewport(0, 0, W, H);
      pgl.useProgram(post);
      pgl.bindVertexArray(postVao);
      pgl.activeTexture(pgl.TEXTURE0);
      pgl.bindTexture(pgl.TEXTURE_2D, postTex);
      pgl.texImage2D(pgl.TEXTURE_2D, 0, pgl.RGBA, pgl.RGBA, pgl.UNSIGNED_BYTE, accum);
      pgl.uniform1i(PU.uImg, 0);
      pgl.uniform2f(PU.uRes, W, H);
      pgl.uniform1f(PU.uCA, ca(t0) * 0.012);
      pgl.uniform1f(PU.uGrain, 0.035);
      pgl.uniform1f(PU.uSeed, (i % 97) * 1.37);
      pgl.uniform1f(PU.uFlash, flash(t0));
      pgl.uniform1f(PU.uFade, fade(t0));
      pgl.drawArrays(pgl.TRIANGLES, 0, 3);
      return out;
    };
  }
  // Gaussian chromatic-aberration pulses at [time, weight] cuts.
  const cutPulses = (cuts) => (t) => {
    let ca = 0;
    for (const [c, w] of cuts) ca = Math.max(ca, w * Math.exp(-Math.pow((t - c) / 0.09, 2)));
    return ca;
  };

  return {
    glc, sx, gx, font, maskText, GLYPHS, decodeText, dotGlow, drawMark, hud, scrim, pill, icon, badge, padlock, keyIcon,
    bezierPts, pointOn, strokeOn, stamp, renderEarth, drawSatellites, drawOrbitPath, arcPoint, strokeArc,
    LX, CAP_Y, BIG, EYE, CAP, LETTERS, lineYs, caption, block, shiftBlock, questionBadge,
    strokeOrbitArc, eyebrow, lockup, beginSubframe, bloom, makeRenderFrame, cutPulses, canvas: out,
  };
}
