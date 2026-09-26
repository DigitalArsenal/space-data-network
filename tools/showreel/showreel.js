// Space Data Network showreel: a deterministic 45-second motion-graphics piece.
// Every frame is a pure function of its time, so render.mjs can step it frame
// by frame and encode the result. Layers, back to front:
//   1. a WebGL2 ray-traced Earth (the site's earth.js shader, extended)
//   2. Canvas2D vector layers: satellites, orbits, network arcs, typography
//   3. a glow layer, blurred twice and added for bloom
//   4. subframe accumulation for real motion blur
//   5. a WebGL2 finishing pass: chromatic aberration on cuts, vignette, grain
// Open showreel.html?t=<seconds> to inspect a single frame in a browser.

const W = 1920;
const H = 1080;
const FPS = 30;
const DURATION = 45; // 41 s of motion, then a hold on the lockup
const SUBFRAMES = 10;
const SHUTTER = 0.5; // fraction of a frame the virtual shutter stays open

const AMBER = "#f5a524";
const CYAN = "#59d9ff";
const SAT = "#b3e0ff";
const RED = "#ff3b30";
const INK = "#f5f5f7";
const MUTED = "rgba(245,245,247,0.55)";
const SANS = '-apple-system, "SF Pro Display", system-ui, "Helvetica Neue", Arial, sans-serif';
const MONO = 'ui-monospace, "SF Mono", Menlo, Consolas, monospace';
const D = Math.PI / 180;
// Chapter starts, in seconds. Ignition and the catalog run from 0.
const T_FORMAT = 7.2; // the old two-line format
const T_STD = 10.0; // Space Data Standards
const T_KEYS = 15.2; // keys, digital signatures, encryption
const T_SIGN = T_KEYS + 2.6;
const T_SEAL = T_KEYS + 5.2;
const T_NET = 23.0; // the network and the storefront
const T_CONJ = 30.6; // conjunction
const T_TCA = T_CONJ + 4.6;
const T_END = 39.0; // pull back to the lockup
// Text content (counters, scrambled glyphs) follows the frame, not the
// subframe, so motion blur never averages two different strings together.
let FRAME_T = 0;

// ---------------------------------------------------------------- utilities
const clamp = (x, a = 0, b = 1) => Math.min(b, Math.max(a, x));
const lerp = (a, b, t) => a + (b - a) * t;
const seg = (t, a, b) => clamp((t - a) / (b - a));
const easeOutExpo = (x) => (x >= 1 ? 1 : 1 - Math.pow(2, -10 * x));
const easeInExpo = (x) => (x <= 0 ? 0 : Math.pow(2, 10 * x - 10));
const easeInOutExpo = (x) =>
  x <= 0 ? 0 : x >= 1 ? 1 : x < 0.5 ? Math.pow(2, 20 * x - 10) / 2 : (2 - Math.pow(2, -20 * x + 10)) / 2;
const easeInOutCubic = (x) => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2);
const easeOutCubic = (x) => 1 - Math.pow(1 - x, 3);
const easeInCubic = (x) => x * x * x;
const easeOutBack = (x) => {
  const c1 = 1.9;
  const c3 = c1 + 1;
  return 1 + c3 * Math.pow(x - 1, 3) + c1 * Math.pow(x - 1, 2);
};
const v3 = (x, y, z) => [x, y, z];
const add = (a, b) => [a[0] + b[0], a[1] + b[1], a[2] + b[2]];
const sub = (a, b) => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
const mul = (a, s) => [a[0] * s, a[1] * s, a[2] * s];
const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const len = (a) => Math.hypot(a[0], a[1], a[2]);
const norm = (a) => mul(a, 1 / (len(a) || 1));
const mix3 = (a, b, t) => [lerp(a[0], b[0], t), lerp(a[1], b[1], t), lerp(a[2], b[2], t)];

function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function canvas(w, h) {
  const c = document.createElement("canvas");
  c.width = w;
  c.height = h;
  return c;
}

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
const TAU_STEP = 0.001;
const TAU = (() => {
  const n = Math.ceil((DURATION + 1) / TAU_STEP);
  const a = new Float64Array(n + 1);
  for (let i = 1; i <= n; i++) a[i] = a[i - 1] + rate((i - 0.5) * TAU_STEP) * TAU_STEP;
  return a;
})();
function tau(t) {
  const x = clamp(t, 0, DURATION + 0.999) / TAU_STEP;
  const i = Math.floor(x);
  return lerp(TAU[i], TAU[i + 1], x - i);
}

// ------------------------------------------------------------------ orbits
const OMEGA_K = 0.34;
function orbitBasis(inc, raan) {
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
function orbitPos(o, u) {
  return add(mul(o.e1, o.r * Math.cos(u)), mul(o.e2, o.r * Math.sin(u)));
}

const SATS = (() => {
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
})();

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
  [17, "OD MODULE", "FOR SALE", 34, -62],
  [5, "OCM CATALOG", "FREE", -40, -76],
  [16, "RADAR TRACKS", "FOR SALE", 34, 52],
  [3, "SCREENING SERVICE", "FOR SALE", -40, 44],
  [15, "SPACE WEATHER", "FREE", 34, -56],
];

// ------------------------------------------------------------------ camera
function lookAt(pos, target, upHint) {
  const f = norm(sub(target, pos));
  const r = norm(cross(f, upHint));
  const u = cross(r, f);
  return { pos, f, r, u };
}
function geoDir(lat, lon, spin) {
  const a = lon * D + spin;
  return [Math.cos(lat * D) * Math.sin(a), Math.sin(lat * D), Math.cos(lat * D) * Math.cos(a)];
}
const TAN = Math.tan(22 * D);
// Camera distance at which the globe's limb has radius px on screen.
function distForRadius(px) {
  const t = (px / (H / 2)) * TAN;
  return Math.sqrt(1 + 1 / (t * t));
}
function ringRadius(t) {
  // The mark's ring: 150 px at rest, swelling into the planet's limb.
  const grow = easeInExpo(seg(t, 1.5, 2.25));
  return lerp(150, 430, grow);
}

// Where the camera rests from the catalog through the crypto chapter.
function catalogCamera(t) {
  const k = easeInOutCubic(seg(t, 1.6, 5.0));
  const lon = lerp(-62, -30, k) + 12 * seg(t, 5.0, T_NET);
  const lat = lerp(18, 26, k);
  const d0 = distForRadius(ringRadius(Math.min(t, 2.25)));
  const dist = lerp(d0, 7.4, easeInOutCubic(seg(t, 2.35, 4.6))) - 0.7 * easeInOutCubic(seg(t, 4.9, T_FORMAT));
  const s = easeInOutCubic(seg(t, 4.85, 5.55));
  // A zoom kick on each hard cut into a chapter.
  let zoom = 1;
  for (const c of [T_STD, T_KEYS]) if (t >= c) zoom += 0.18 * (1 - easeOutExpo(seg(t, c, c + 0.5)));
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
    // Down from the resting pose to a hemisphere of nodes, then a slow pan east.
    const from = catalogCamera(T_NET);
    const k = easeInOutCubic(seg(t, T_NET, T_NET + 1.4));
    const p = seg(t, T_NET, T_CONJ);
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

// World → screen, matching the ray tracer's projection exactly.
function project(cam, p) {
  const d = sub(p, cam.pos);
  const z = dot(d, cam.f);
  if (z <= 0.01) return null;
  const tan = TAN / cam.zoom;
  const vx = dot(d, cam.r) / z / (tan * (W / H)) + cam.off[0];
  const vy = dot(d, cam.u) / z / tan + cam.off[1];
  return { x: (vx * 0.5 + 0.5) * W, y: (0.5 - vy * 0.5) * H, z };
}
// Is p hidden behind the planet as seen from the camera?
function occluded(cam, p) {
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
const EARTH_FRAG = `#version 300 es
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
const QUAD_VERT = "#version 300 es\nin vec2 p;out vec2 v;void main(){v=p;gl_Position=vec4(p,0.,1.);}";

function makeProgram(gl, vs, fs) {
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
function quad(gl, prog) {
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
function uniforms(gl, prog, names) {
  const u = {};
  for (const n of names) u[n] = gl.getUniformLocation(prog, n);
  return u;
}
function loadImage(url) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error(`image ${url}`));
    img.src = url;
  });
}

// ---------------------------------------------------------- finishing pass
const POST_FRAG = `#version 300 es
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

// ------------------------------------------------------------- the renderer
export async function createShowreel(base = "") {
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
  function hud(t, frame) {
    const a = 0.55 * (1 - seg(t, T_END + 1.75, T_END + 2.15)) * seg(t, 0.2, 0.8);
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
    sx.fillText("SPACE DATA NETWORK", m + 12, m + 30);
    const f = frame % FPS;
    const s = Math.floor(frame / FPS);
    sx.textAlign = "right";
    sx.fillText(`00:00:${String(s).padStart(2, "0")}:${String(f).padStart(2, "0")}`, W - m - 12, m + 30);
    sx.fillText("1920 × 1080 · 30 FPS · DIGITALLY SIGNED", W - m - 12, H - m - 16);
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
  // A beat's headline: two big lines and a caps line under them, left column.
  function headline(a, b, sub, t0, t1, t) {
    maskText(sx, a, 150, 430, 80, 700, INK, t0, t1 - 0.32, t, { tracking: -2, outDur: 0.28 });
    maskText(sx, b, 150, 522, 80, 700, AMBER, t0 + 0.13, t1 - 0.3, t, { tracking: -2, outDur: 0.28 });
    maskText(sx, sub, 154, 590, 17, 600, MUTED, t0 + 0.4, t1 - 0.28, t, { tracking: 3, outDur: 0.28 });
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

  function drawSatellites(cam, t, T, alpha, spawn) {
    const pts = sx;
    pts.save();
    pts.globalCompositeOperation = "lighter";
    for (const s of SATS) {
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

  function drawNetwork(cam, t, alpha) {
    const spin = cam.spin;
    const n0 = T_NET;
    const P = NODES.map(([lat, lon]) => geoDir(lat, lon, spin));
    const screen = (p) => {
      const s = mul(p, 1.004);
      return occluded(cam, s) ? null : project(cam, s);
    };
    // links
    LINKS.forEach(([i, j], k) => {
      const t0 = n0 + 0.45 + k * 0.035;
      const draw = easeInOutCubic(seg(t, t0, t0 + 0.6));
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
      const t0 = n0 + 0.1 + i * 0.04;
      const k = seg(t, t0, t0 + 0.5);
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

    // One record, published once, fans out hop by hop.
    const P0 = n0 + 3.15;
    const HOP = 0.3;
    const wave = alpha * seg(t, P0 - 0.3, P0) * (1 - seg(t, n0 + 4.8, n0 + 5.15));
    if (wave > 0) {
      LINKS.forEach(([i, j]) => {
        if (HOPS[i] === HOPS[j]) return;
        const [a, b] = HOPS[i] < HOPS[j] ? [i, j] : [j, i];
        const t0 = P0 + HOPS[a] * HOP;
        const f = easeInOutCubic(seg(t, t0, t0 + 0.34));
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
        const landed = P0 + HOPS[i] * HOP + (HOPS[i] ? 0.34 : 0);
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
      // the content address rides beside the publishing node
      const q = screen(P[SOURCE]);
      const ta = wave * seg(t, P0 - 0.2, P0 + 0.1);
      if (q && ta > 0) {
        const lx = q.x + 34;
        const ly = q.y - 58;
        sx.save();
        sx.globalAlpha = ta;
        sx.strokeStyle = "rgba(245,245,247,0.6)";
        sx.lineWidth = 1.2;
        sx.beginPath();
        sx.moveTo(q.x, q.y);
        sx.lineTo(lx - 6, ly + 10);
        sx.stroke();
        sx.font = font(600, 13, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillStyle = MUTED;
        sx.textAlign = "left";
        sx.fillText("PUBLISHED ONCE · CONTENT ADDRESS", lx, ly - 12);
        sx.font = font(500, 17, MONO);
        sx.letterSpacing = "0.5px";
        sx.fillStyle = INK;
        decodeText(sx, "bafkreih4v6ogq2c7xzfx3yfwq…", lx, ly + 14, P0 - 0.2, 0.4, t, 61);
        sx.restore();
      }
    }

    // The storefront: listings, free or for sale, straight from each node.
    const S0 = n0 + 5.35;
    STORE.forEach(([i, name, price, dx, dy], k) => {
      const t0 = S0 + k * 0.12;
      const pop = seg(t, t0, t0 + 0.35);
      const a = alpha * clamp(pop * 3) * (1 - seg(t, T_CONJ - 0.45, T_CONJ - 0.2));
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

  // ------------------------------------------------------ the old format
  const TLE = [
    "1 25544U 98067A   26268.50000000  .00016717  00000+0  30306-3 0  9995",
    "2 25544  51.6416 247.4627 0006703 130.5360 325.0288 15.49815361467582",
  ];
  function drawFormat(t) {
    const f0 = T_FORMAT;
    const f1 = T_STD;
    if (t < f0 || t > f1 + 0.05) return;
    const out = easeInExpo(seg(t, f1 - 0.32, f1 - 0.02));
    const alpha = 1 - out;
    const x0 = 150;
    const ys = [490, 544];
    maskText(sx, "THE MOST USED ORBIT FORMAT", x0, 254, 17, 600, AMBER, f0 + 0.05, f1 - 0.42, t, { tracking: 3, outDur: 0.3 });
    maskText(sx, "The data hasn’t kept up.", x0, 350, 80, 700, INK, f0 + 0.1, f1 - 0.4, t, { tracking: -2, outDur: 0.3 });
    // The two-line set types on, then scrambles on the way out.
    sx.save();
    sx.font = font(500, 32, MONO);
    sx.letterSpacing = "0px";
    sx.textAlign = "left";
    const cw = sx.measureText("0").width;
    const R = rng(900 + Math.round(FRAME_T * 30));
    TLE.forEach((line, li) => {
      const t0 = f0 + 0.2 + li * 0.32;
      const n = Math.floor(clamp((FRAME_T - t0) / 0.5) * line.length);
      if (n <= 0) return;
      let str = "";
      for (let c = 0; c < n; c++) str += out > 0 && line[c] !== " " && R() < out * 1.6 ? GLYPHS[Math.floor(R() * GLYPHS.length)] : line[c];
      sx.globalAlpha = alpha;
      sx.fillStyle = INK;
      sx.fillText(str, x0, ys[li]);
      const hk = seg(t, f0 + 1.35, f0 + 1.5);
      if (hk > 0 && n >= 7 && out === 0) {
        sx.globalAlpha = hk;
        sx.fillStyle = AMBER;
        sx.fillText(line.slice(2, 7), x0 + 2 * cw, ys[li]);
      }
      if (n < line.length) {
        sx.globalAlpha = 1;
        sx.fillStyle = AMBER;
        sx.fillRect(x0 + n * cw + 2, ys[li] - 25, cw * 0.62, 31);
      }
    });
    sx.restore();

    sx.save();
    sx.globalAlpha = alpha;
    sx.strokeStyle = "rgba(245,245,247,0.7)";
    sx.lineWidth = 1.5;
    // 69 characters: a bracket over the first line
    const a1 = easeOutExpo(seg(t, f0 + 1.0, f0 + 1.35));
    if (a1 > 0) {
      const y = ys[0] - 42;
      const x1 = x0 + 69 * cw;
      sx.beginPath();
      sx.moveTo(x0, y + 9);
      sx.lineTo(x0, y);
      sx.lineTo(lerp(x0, x1, a1), y);
      if (a1 > 0.98) sx.lineTo(x1, y + 9);
      sx.stroke();
    }
    // the catalog number: a bracket under columns 3–7 of the second line
    const a2 = easeOutExpo(seg(t, f0 + 1.3, f0 + 1.6));
    if (a2 > 0) {
      const bx0 = x0 + 2 * cw;
      const bx1 = x0 + 7 * cw;
      const y = ys[1] + 16;
      sx.strokeStyle = AMBER;
      sx.beginPath();
      sx.moveTo(bx0, y - 9);
      sx.lineTo(bx0, y);
      sx.lineTo(bx1, y);
      sx.lineTo(bx1, y - 9);
      sx.moveTo(bx0 + 20, y);
      sx.lineTo(bx0 + 20, y + 30 * a2);
      sx.stroke();
    }
    sx.restore();
    maskText(sx, "69 CHARACTERS · LAID OUT FOR PUNCH CARDS", x0, ys[0] - 56, 15, 600, INK, f0 + 1.08, f1 - 0.36, t, { tracking: 3, outDur: 0.3 });
    maskText(sx, "FIVE-DIGIT CATALOG NUMBERS · RUNNING OUT", x0 + 2 * cw, ys[1] + 74, 15, 600, AMBER, f0 + 1.42, f1 - 0.35, t, { tracking: 3, outDur: 0.3 });
    badge("cross", x0 + 14, 706, 30, RED, alpha * easeOutExpo(seg(t, f0 + 1.72, f0 + 1.95)));
    maskText(sx, "NO FIELD FOR UNCERTAINTY", x0 + 42, 712, 17, 600, INK, f0 + 1.74, f1 - 0.34, t, { tracking: 3, outDur: 0.3 });
  }

  // --------------------------------------------------- the standards chapter
  const LANGS = ["C++", "C#", "Dart", "Go", "Java", "JavaScript", "Kotlin", "Lobster", "PHP", "Python", "Rust", "Swift", "TypeScript"];
  function drawStandards(t) {
    const s0 = T_STD;
    const recOut = s0 + 2.85;
    if (t < s0 || t > T_KEYS + 0.05) return;
    const inK = seg(t, s0, s0 + 0.175);
    const outK = easeInExpo(seg(t, recOut, recOut + 0.2));
    if (inK > 0 && outK < 1) {
      const x0 = 180 - outK * 260;
      const y0 = 250;
      sx.save();
      sx.globalAlpha = 1 - outK;
      // grid hairlines that draw on
      const grid = easeOutExpo(seg(t, s0, s0 + 0.35));
      sx.strokeStyle = "rgba(245,245,247,0.14)";
      sx.lineWidth = 1;
      for (let i = 0; i < 12; i++) {
        const y = y0 + 34 + i * 46;
        sx.beginPath();
        sx.moveTo(x0, y);
        sx.lineTo(x0 + 860 * grid, y);
        sx.stroke();
      }
      sx.beginPath();
      sx.moveTo(x0 + 350, y0 + 34);
      sx.lineTo(x0 + 350, y0 + 34 + 506 * grid);
      sx.stroke();
      // the covariance row lights up: uncertainty travels with the orbit
      const cov = easeOutExpo(seg(t, s0 + 1.25, s0 + 1.55));
      if (cov > 0) {
        const y = y0 + 68 + 8 * 46;
        sx.fillStyle = "rgba(245,165,36,0.1)";
        sx.fillRect(x0 - 14, y - 31, 874 * cov, 44);
        sx.fillStyle = AMBER;
        sx.fillRect(x0 - 14, y - 31, 3, 44);
      }
      sx.restore();

      maskText(sx, "SPACE DATA STANDARDS · ORBIT COMPREHENSIVE MESSAGE", x0, y0 - 84, 17, 600, AMBER, s0 + 0.03, recOut, t, { tracking: 2.5, dur: 0.35, outDur: 0.2 });
      maskText(sx, "ISS (ZARYA)", x0, y0 - 6, 64, 700, INK, s0 + 0.06, recOut - 0.015, t, { dur: 0.4, outDur: 0.2 });
      // An Orbit Comprehensive Message: state vector, covariance and physical
      // properties, not just mean elements.
      const FIELDS = [
        ["OBJECT_DESIGNATOR", "25544"],
        ["INTERNATIONAL_DESIGNATOR", "1998-067A"],
        ["EPOCH_TZERO", "2026-09-25T12:00:00.000Z"],
        ["TIME_SYSTEM", "UTC"],
        ["CENTER · FRAME", "EARTH · EME2000"],
        ["TRAJ_TYPE", "CARTPV"],
        ["X  Y  Z  (km)", "4523.712  -3811.204  3294.587"],
        ["VX VY VZ (km/s)", "3.02411  6.11783  3.55820"],
        ["COV_TYPE", "CARTPV · 6 × 6"],
        ["MASS", "420000 kg"],
        ["MANEUVERABLE", "YES"],
      ];
      sx.save();
      FIELDS.forEach(([k, v], i) => {
        const y = y0 + 68 + i * 46;
        const t0 = s0 + 0.15 + i * 0.06;
        const a = seg(t, t0, t0 + 0.15);
        if (a <= 0) return;
        sx.globalAlpha = (1 - outK) * a;
        sx.font = font(500, 17, MONO);
        sx.letterSpacing = "0.5px";
        sx.fillStyle = MUTED;
        sx.textAlign = "left";
        sx.fillText(k, x0, y);
        sx.fillStyle = INK;
        sx.font = font(500, 22, MONO);
        decodeText(sx, v, x0 + 372, y, t0 + 0.025, 0.35, t, 100 + i);
      });
      sx.restore();
      const tagK = easeOutExpo(seg(t, s0 + 1.4, s0 + 1.7));
      if (tagK > 0) {
        const y = y0 + 68 + 8 * 46;
        sx.save();
        sx.globalAlpha = (1 - outK) * tagK;
        sx.font = font(600, 14, SANS);
        sx.letterSpacing = "2.5px";
        const label = "UNCERTAINTY INCLUDED";
        const w = sx.measureText(label).width + 40;
        const lx = x0 + 846 - w + (1 - tagK) * 30;
        pill(lx, y - 26, w, 34, AMBER, "rgba(0,0,0,0.6)");
        sx.fillStyle = AMBER;
        sx.textAlign = "left";
        sx.fillText(label, lx + 20, y - 3.5);
        sx.restore();
      }

      // FlatBuffer bytes streaming up the right side
      const hx = 1180;
      sx.save();
      sx.beginPath();
      sx.rect(hx - 10, 170, 620, 720);
      sx.clip();
      const scroll = (t - s0) * 120;
      const hk = seg(t, s0 + 0.03, s0 + 0.23) * (1 - outK);
      const scanY = 900 - (((t - s0) * 320) % 760);
      sx.font = font(400, 17, MONO);
      sx.letterSpacing = "0.5px";
      HEX.forEach((row, i) => {
        const y = 200 + i * 28 - scroll;
        if (y < 160 || y > 900) return;
        const scan = Math.abs(y - scanY) < 30;
        sx.globalAlpha = hk * (scan ? 1 : 0.4) * clamp(1 - Math.abs(y - 530) / 380);
        sx.fillStyle = MUTED;
        sx.fillText(row.off, hx, y);
        sx.fillStyle = scan ? CYAN : "rgba(89,217,255,0.75)";
        sx.fillText(row.bytes.join(" "), hx + 90, y);
      });
      sx.restore();
      maskText(sx, "FLATBUFFERS · 1,184 BYTES", hx, 168, 15, 600, CYAN, s0 + 0.075, recOut - 0.015, t, { tracking: 2.5, dur: 0.35, outDur: 0.2 });
      const rk = easeOutExpo(seg(t, s0 + 1.7, s0 + 2.0));
      if (rk > 0) {
        sx.save();
        sx.globalAlpha = (1 - outK) * rk;
        sx.font = font(600, 14, SANS);
        sx.letterSpacing = "2.5px";
        const label = "READ IN PLACE · NO PARSING";
        const w = sx.measureText(label).width + 40;
        pill(hx, 902 + (1 - rk) * 20, w, 34, CYAN, "rgba(0,0,0,0.6)");
        sx.fillStyle = CYAN;
        sx.textAlign = "left";
        sx.fillText(label, hx + 20, 925.5 + (1 - rk) * 20);
        sx.restore();
      }
    }

    // Typed schemas, generated code for every major language.
    const L0 = recOut + 0.15;
    const L1 = T_KEYS - 0.3;
    if (t < L0) return;
    maskText(sx, "Typed binary schemas.", 150, 400, 84, 700, INK, L0, L1, t, { tracking: -2, outDur: 0.28 });
    maskText(sx, "Code for 13 languages.", 150, 495, 84, 700, AMBER, L0 + 0.13, L1 + 0.03, t, { tracking: -2, outDur: 0.28 });
    maskText(sx, "ONE SET OF SCHEMAS FOR EVERY KIND OF SPACE DATA", 154, 566, 17, 600, MUTED, L0 + 0.35, L1 + 0.05, t, { tracking: 3, outDur: 0.28 });
    const out = easeInExpo(seg(t, L1, L1 + 0.25));
    sx.save();
    sx.font = font(600, 21, SANS);
    sx.letterSpacing = "0.5px";
    sx.textAlign = "center";
    let x = 150;
    let y = 640;
    const h = 48;
    LANGS.forEach((name, i) => {
      const w = sx.measureText(name).width + 44;
      if (x + w > 1770) {
        x = 150;
        y += h + 12;
      }
      const t0 = L0 + 0.45 + i * 0.045;
      const k = seg(t, t0, t0 + 0.32);
      if (k > 0 && out < 1) {
        const sc = easeOutBack(k);
        sx.save();
        sx.globalAlpha = (1 - out) * clamp(k * 3);
        sx.translate(x + w / 2, y + h / 2 - out * 30);
        sx.scale(sc, sc);
        pill(-w / 2, -h / 2, w, h, "rgba(245,245,247,0.35)", "rgba(245,245,247,0.05)");
        sx.fillStyle = INK;
        sx.fillText(name, 0, 7.5);
        sx.restore();
      }
      x += w + 12;
    });
    sx.restore();
  }

  // ------------------------------------------------------ the crypto chapter
  // Keys: one recovery phrase (never shown) derives every key a node uses.
  const WORDLEN = (() => {
    const R = rng(24);
    return Array.from({ length: 24 }, () => 3 + Math.floor(R() * 6));
  })();
  function drawKeys(t) {
    const k0 = T_KEYS;
    const k1 = T_SIGN;
    if (t < k0 || t > k1 + 0.05) return;
    const out = easeInExpo(seg(t, k1 - 0.3, k1 - 0.05));
    const alpha = 1 - out;
    const lift = out * 40;
    headline("One recovery phrase.", "Every key.", "ENCRYPTED AT REST · BOUND TO YOUR MACHINE", k0 + 0.05, k1, t);
    const gx0 = 1120;
    const gy0 = 262;
    const cw = 150;
    const ch = 44;
    maskText(sx, "RECOVERY PHRASE · 24 WORDS", gx0, 230, 15, 600, AMBER, k0 + 0.1, k1 - 0.3, t, { tracking: 3, outDur: 0.28 });
    const scan = (t - k0 - 0.55) * 1500;
    sx.save();
    sx.textAlign = "left";
    for (let i = 0; i < 24; i++) {
      const x = gx0 + (i % 4) * (cw + 14);
      const y0 = gy0 + Math.floor(i / 4) * (ch + 12);
      const t0 = k0 + 0.15 + i * 0.018;
      const e = easeOutExpo(seg(t, t0, t0 + 0.25));
      if (e <= 0) continue;
      const y = y0 + (1 - e) * 14 - lift;
      sx.globalAlpha = e * alpha;
      sx.strokeStyle = "rgba(245,245,247,0.2)";
      sx.lineWidth = 1.2;
      sx.beginPath();
      sx.roundRect(x, y, cw, ch, 10);
      sx.stroke();
      sx.font = font(500, 13, MONO);
      sx.letterSpacing = "0px";
      sx.fillStyle = MUTED;
      sx.fillText(String(i + 1).padStart(2, "0"), x + 14, y + 27);
      const hot = clamp(1 - Math.abs(x + (y0 - gy0) * 0.6 - scan) / 110);
      for (let d = 0; d < WORDLEN[i]; d++) {
        sx.fillStyle = INK;
        sx.globalAlpha = e * alpha * 0.75;
        sx.beginPath();
        sx.arc(x + 48 + d * 12, y + ch / 2, 3.2, 0, Math.PI * 2);
        sx.fill();
        if (hot > 0) {
          sx.fillStyle = AMBER;
          sx.globalAlpha = e * alpha * hot;
          sx.fill();
        }
      }
    }
    sx.restore();
    // Three keys branch off the phrase.
    const bx = gx0 + (4 * cw + 3 * 14) / 2;
    const by = gy0 + 6 * ch + 5 * 12 + 6 - lift;
    const chipY = 712 - lift;
    const chipW = 250;
    const chipH = 86;
    const CHIPS = [
      ["NODE IDENTITY", "12D3KooWRk9x…Qm7t"],
      ["SIGNING KEY", "Ed25519 · 7f3a9c21e8…"],
      ["ENCRYPTION KEY", "X25519 · c84e10b7a2…"],
    ];
    CHIPS.forEach(([title, val], j) => {
      const cx = bx + (j - 1) * 280;
      const t0 = k0 + 0.75 + j * 0.07;
      const pts = bezierPts([bx, by], [bx, by + 70], [cx, chipY - 70], [cx, chipY]);
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.4)), AMBER, 1.6, 0.8 * alpha, 0.5 * alpha);
      const c = seg(t, t0 + 0.3, t0 + 0.6);
      if (c <= 0) return;
      sx.save();
      sx.globalAlpha = alpha * clamp(c * 3);
      sx.translate(cx, chipY + chipH / 2);
      const sc = easeOutBack(c);
      sx.scale(sc, sc);
      sx.beginPath();
      sx.roundRect(-chipW / 2, -chipH / 2, chipW, chipH, 14);
      sx.fillStyle = "rgba(245,245,247,0.05)";
      sx.fill();
      sx.strokeStyle = "rgba(245,165,36,0.7)";
      sx.lineWidth = 1.5;
      sx.stroke();
      sx.textAlign = "left";
      sx.font = font(600, 14, SANS);
      sx.letterSpacing = "2.5px";
      sx.fillStyle = INK;
      sx.fillText(title, -chipW / 2 + 20, -8);
      sx.font = font(500, 15, MONO);
      sx.letterSpacing = "0.5px";
      sx.fillStyle = AMBER;
      decodeText(sx, val, -chipW / 2 + 20, 22, t0 + 0.45, 0.4, t, 40 + j);
      sx.restore();
    });
  }

  // Digitally signed at the source; every node checks before it stores.
  function drawSignVerify(t) {
    const v0 = T_SIGN;
    const v1 = T_SEAL;
    if (t < v0 || t > v1 + 0.05) return;
    const out = easeInExpo(seg(t, v1 - 0.3, v1 - 0.05));
    const alpha = 1 - out;
    headline("Digitally signed", "at the source.", "VERIFIED BEFORE ANY NODE STORES IT", v0 + 0.05, v1, t);
    const cx0 = 1110;
    const cy0 = 200;
    const cwid = 480;
    const chei = 200;
    const ck = easeOutExpo(seg(t, v0 + 0.08, v0 + 0.4));
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
      sx.fillText("OCM · ISS (ZARYA)", -cwid / 2 + 28, -chei / 2 + 42);
      sx.font = font(500, 13, MONO);
      sx.letterSpacing = "0.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "right";
      sx.fillText("1,184 BYTES", cwid / 2 - 28, -chei / 2 + 42);
      [220, 180, 260, 150].forEach((vw, r) => {
        const y = -chei / 2 + 76 + r * 28;
        sx.fillStyle = "rgba(245,245,247,0.28)";
        sx.fillRect(-cwid / 2 + 28, y, 92, 7);
        sx.fillStyle = "rgba(245,245,247,0.6)";
        sx.fillRect(-cwid / 2 + 140, y, vw, 7);
      });
      sx.restore();
    }
    maskText(sx, "Ed25519 DIGITAL SIGNATURE", cx0, cy0 + chei + 44, 14, 600, MUTED, v0 + 0.4, v1 - 0.3, t, { tracking: 2.5, outDur: 0.28 });
    sx.save();
    sx.globalAlpha = alpha;
    sx.font = font(500, 18, MONO);
    sx.letterSpacing = "1px";
    sx.fillStyle = INK;
    sx.textAlign = "left";
    decodeText(sx, "3f9a c21e 8b04 77d1 e6a2 09fc 5b3d 11e8", cx0, cy0 + chei + 76, v0 + 0.45, 0.4, t, 31);
    decodeText(sx, "c7a0 42be 9d15 f6c3 20a8 7e4b d902 5c61 …", cx0, cy0 + chei + 104, v0 + 0.55, 0.4, t, 32);
    sx.restore();
    const st = seg(t, v0 + 0.85, v0 + 1.02);
    if (st > 0) stamp(cx0 + cwid / 2, 590, st, alpha);

    // Two copies travel; one arrives intact, one was altered on the way.
    const NODE_XY = [[1170, 856], [1530, 856]];
    NODE_XY.forEach(([nx, ny], j) => {
      const bad = j === 1;
      const nk = easeOutExpo(seg(t, v0 + 1.05 + j * 0.05, v0 + 1.35 + j * 0.05));
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
      const t0 = v0 + 1.25 + j * 0.08;
      const p = easeInOutCubic(seg(t, t0, t0 + 0.42));
      const land = seg(t, t0 + 0.42, t0 + 0.52);
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
        const ring = seg(t, t0 + 0.42, t0 + 1.0);
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
      maskText(sx, bad ? "ALTERED · REJECTED" : "VERIFIED · STORED", nx, ny + 70, 15, 600, bad ? RED : AMBER, t0 + 0.48, v1 - 0.3, t, { tracking: 3, align: "center", outDur: 0.28 });
    });
  }

  // Encrypted to the buyer's key: only the buyer's node can open it.
  function drawEncrypt(t) {
    const e0 = T_SEAL;
    const e1 = T_NET;
    if (t < e0 || t > e1 + 0.05) return;
    const out = easeInExpo(seg(t, e1 - 0.3, e1 - 0.05));
    const alpha = 1 - out;
    headline("Encrypted to", "the buyer’s key.", "ONLY THE BUYER’S NODE CAN OPEN IT", e0 + 0.05, e1, t);
    const bx0 = 1150;
    const by0 = 196;
    const bw = 500;
    const bh = 230;
    const bk = easeOutExpo(seg(t, e0 + 0.08, e0 + 0.4));
    const lockK = easeInExpo(seg(t, e0 + 0.55, e0 + 0.72));
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
      sx.fillText("OD MODULE · WEBASSEMBLY", bx0 + 28, by0 + 42);
      sx.font = font(500, 13, MONO);
      sx.letterSpacing = "0.5px";
      sx.fillStyle = MUTED;
      sx.textAlign = "right";
      sx.fillText("FOR SALE", bx0 + bw - 28, by0 + 42);
      // bytes turn to ciphertext as the lock shuts, left to right
      sx.textAlign = "left";
      sx.font = font(400, 16, MONO);
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
      const lk = easeOutBack(seg(t, e0 + 0.3, e0 + 0.5));
      padlock(bx0 + bw / 2, by0 + bh / 2 + 18, 110 * lk, 1 - lockK, lockK >= 1 ? AMBER : INK, alpha * clamp(lk * 2));
      if (lockK > 0) dotGlow(bx0 + bw / 2, by0 + bh / 2 + 18, 120, AMBER, 0.25 * alpha * (1 - seg(t, e0 + 0.72, e0 + 1.2)));
    }
    maskText(sx, "ENCRYPTED · STORED BY CONTENT ID", bx0, by0 + bh + 40, 14, 600, MUTED, e0 + 0.75, e1 - 0.3, t, { tracking: 2.5, outDur: 0.28 });

    // Ciphertext to two nodes; the buyer's key opens one of them.
    const src = [bx0 + bw / 2, by0 + bh + 66];
    const TARGETS = [
      [[1210, 856], true],
      [[1590, 856], false],
    ];
    TARGETS.forEach(([[nx, ny], buyer], j) => {
      const pts = bezierPts(src, [src[0], src[1] + 90], [nx, ny - 150], [nx, ny - 34]);
      const t0 = e0 + 0.85 + j * 0.06;
      strokeOn(pts, easeInOutCubic(seg(t, t0, t0 + 0.35)), "rgba(245,245,247,0.5)", 1.4, alpha, 0, [5, 7]);
      const p = easeInOutCubic(seg(t, t0 + 0.2, t0 + 0.6));
      if (p > 0 && p < 1) {
        const [px, py] = pointOn(pts, p);
        padlock(px, py, 30, 0, AMBER, alpha);
        dotGlow(px, py, 30, AMBER, 0.35 * alpha);
      }
      const arrive = seg(t, t0 + 0.6, t0 + 0.7);
      if (arrive <= 0) return;
      const open = buyer ? easeOutBack(seg(t, e0 + 1.62, e0 + 1.82)) : 0;
      if (buyer) {
        const kp = easeInOutCubic(seg(t, e0 + 1.3, e0 + 1.6));
        const ka = alpha * seg(t, e0 + 1.25, e0 + 1.35) * (1 - seg(t, e0 + 1.58, e0 + 1.66));
        keyIcon(lerp(nx - 150, nx - 26, kp), ny + 4, 44, AMBER, ka);
        maskText(sx, "X25519", lerp(nx - 150, nx - 26, kp), ny - 30, 13, 600, AMBER, e0 + 1.25, e0 + 1.5, t, { tracking: 2.5, align: "center", outDur: 0.15 });
      }
      padlock(nx, ny, 64 * easeOutBack(arrive), open, buyer && open > 0 ? AMBER : "rgba(245,245,247,0.7)", alpha);
      if (buyer && open > 0) {
        const ring = seg(t, e0 + 1.62, e0 + 2.2);
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
      maskText(
        sx,
        buyer ? "BUYER’S NODE · OPENED" : "ANY OTHER NODE · SEALED",
        nx,
        ny + 70,
        15,
        600,
        buyer ? AMBER : MUTED,
        buyer ? e0 + 1.7 : t0 + 0.7,
        e1 - 0.3,
        t,
        { tracking: 3, align: "center", outDur: 0.28 },
      );
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
    // Who produced each track, riding beside it until the alert takes over.
    const lab = alpha * seg(t, T_CONJ + 1.1, T_CONJ + 1.5) * (1 - seg(t, T_TCA - 0.9, T_TCA - 0.6));
    if (lab > 0) {
      [
        [qa, AMBER, "OPERATOR EPHEMERIS", "3f9a…c21e", 1, 64],
        [qb, CYAN, "RADAR TRACK", "8b04…77d1", -1, 84],
      ].forEach(([q, col, name, key, side, dy]) => {
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
        sx.font = font(600, 15, SANS);
        sx.letterSpacing = "2.5px";
        sx.fillStyle = col;
        sx.fillText(name, tx, ly - 4);
        sx.font = font(500, 14, MONO);
        sx.letterSpacing = "0.5px";
        sx.fillStyle = "rgba(245,245,247,0.8)";
        sx.fillText(`DIGITALLY SIGNED · Ed25519 ${key}`, tx, ly + 20);
        sx.restore();
      });
    }
    const near = seg(t, T_TCA - 1.05, T_TCA - 0.375) * (1 - seg(t, T_END - 0.375, T_END + 0.05));
    // covariance ellipses, aligned with each track's screen velocity
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
      sx.lineTo(lerp(mx, lx + 360, lk), lerp(my, ly + 40, lk));
      sx.stroke();
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
      maskText(sx, "CONJUNCTION", lx + 58, ly + 12, 30, 700, RED, T_TCA - 0.375, T_END - 0.225, t, { tracking: 3 });
      sx.save();
      sx.globalAlpha = alpha * tca;
      sx.font = font(500, 19, MONO);
      sx.letterSpacing = "1px";
      sx.fillStyle = INK;
      decodeText(sx, "TCA   2026-09-25 14:02:31Z", lx, ly + 76, T_TCA - 0.225, 0.5, t, 7);
      decodeText(sx, "MISS  214 m", lx, ly + 104, T_TCA - 0.075, 0.45, t, 8);
      decodeText(sx, "Pc    1.2 × 10⁻⁴", lx, ly + 132, T_TCA + 0.075, 0.45, t, 9);
      sx.restore();
    }
  }

  // ---------------------------------------------------------- one subframe
  function drawSubframe(t, frameIndex) {
    const T = tau(t);
    const cam = cameraAt(t);
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

    // Earth: in during the match cut, dimmed and blurred under the type-led
    // chapters, back for the network, gone into the lockup.
    const earthIn = easeInOutCubic(seg(t, 1.55, 2.3));
    const under = seg(t, T_FORMAT - 0.1, T_FORMAT + 0.15) * (1 - seg(t, T_NET - 0.05, T_NET + 0.3));
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

    // Satellites: the swarm arrives with the catalog, leaves for the type-led
    // chapters, dims under the network and the conjunction, returns.
    const satAlpha =
      seg(t, 2.1, 2.4) *
      (1 - seg(t, T_FORMAT - 0.2, T_FORMAT + 0.05) * (1 - seg(t, T_NET, T_NET + 0.3))) *
      (1 - 0.72 * seg(t, T_NET, T_NET + 0.6) * (1 - seg(t, T_END + 0.05, T_END + 0.75))) *
      (1 - 0.55 * seg(t, T_CONJ, T_CONJ + 0.3) * (1 - seg(t, T_END, T_END + 0.35))) *
      earthOut;
    if (satAlpha > 0.01) drawSatellites(cam, t, T, satAlpha, true);

    // The amber satellite from the mark, in orbit with its arc.
    const heroA =
      seg(t, 2.0, 2.4) * (1 - seg(t, T_FORMAT - 0.25, T_FORMAT)) +
      seg(t, T_NET + 0.3, T_NET + 0.8) * (1 - seg(t, T_CONJ - 0.3, T_CONJ));
    if (heroA > 0) drawOrbitPath(cam, HERO, T, AMBER, heroA, 2.4, 0.42);

    if (t > T_NET && t < T_CONJ) drawNetwork(cam, t, seg(t, T_NET, T_NET + 0.3) * (1 - seg(t, T_CONJ - 0.3, T_CONJ)));
    // The network's type sits low and left, over the planet: wash it back.
    scrim(seg(t, T_NET + 0.2, T_NET + 0.7) * (1 - seg(t, T_CONJ - 0.3, T_CONJ)));
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

    // The catalog: the count and the orbit regimes.
    if (t > 2.3 && t < 5.2) {
      // The count never settles: it keeps climbing until the text leaves,
      // because the catalog only grows.
      const k = easeOutExpo(seg(FRAME_T, 2.45, 4.1));
      const n = Math.round(46600 * k + 900 * Math.max(0, FRAME_T - 3.1) + 260 * Math.pow(Math.max(0, FRAME_T - 3.1), 2));
      maskText(sx, "TRACKED OBJECTS", 150, 790, 18, 600, AMBER, 2.4, 4.75, t, { tracking: 4 });
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
        ["LEO", 1.1, -30, 4.0],
        ["MEO · GNSS", 2.15, 150, 4.12],
        ["GEO BELT", 3.0, 200, 4.24],
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
        sx.font = font(600, 16, MONO);
        sx.letterSpacing = "2px";
        sx.fillText(name, lx + 4, ly - 10);
        sx.restore();
      });
    }
    if (t > 5.2 && t < T_FORMAT + 0.05) {
      maskText(sx, "More satellites.", 150, 400, 80, 700, INK, 5.35, 6.85, t, { tracking: -2, outDur: 0.25 });
      maskText(sx, "More operators.", 150, 490, 80, 700, INK, 5.5, 6.88, t, { tracking: -2, outDur: 0.25 });
      maskText(sx, "More ephemeris,", 150, 580, 80, 700, INK, 5.65, 6.91, t, { tracking: -2, outDur: 0.25 });
      maskText(sx, "several times a day.", 150, 670, 80, 700, AMBER, 5.8, 6.94, t, { tracking: -2, outDur: 0.25 });
    }
    drawFormat(t);
    drawStandards(t);
    drawKeys(t);
    drawSignVerify(t);
    drawEncrypt(t);
    if (t > T_NET + 0.3 && t < T_CONJ) {
      const n0 = T_NET;
      maskText(sx, "Peer to peer.", 150, 800, 80, 700, INK, n0 + 0.5, n0 + 2.55, t, { tracking: -2, outDur: 0.28 });
      maskText(sx, "One open protocol.", 150, 892, 80, 700, AMBER, n0 + 0.63, n0 + 2.58, t, { tracking: -2, outDur: 0.28 });
      maskText(sx, "ANY NODE FINDS, FETCHES, VERIFIES AND STREAMS FROM ANY OTHER", 154, 956, 17, 600, MUTED, n0 + 0.9, n0 + 2.6, t, { tracking: 3, outDur: 0.28 });
      maskText(sx, "Fetched once.", 150, 800, 80, 700, INK, n0 + 2.95, n0 + 4.95, t, { tracking: -2, outDur: 0.28 });
      maskText(sx, "Shared by every node.", 150, 892, 80, 700, AMBER, n0 + 3.08, n0 + 4.98, t, { tracking: -2, outDur: 0.28 });
      maskText(sx, "COMPACT BINARY RECORDS, FETCHED BY CONTENT ADDRESS", 154, 956, 17, 600, MUTED, n0 + 3.35, n0 + 5.0, t, { tracking: 3, outDur: 0.28 });
      maskText(sx, "A storefront", 150, 800, 80, 700, INK, n0 + 5.25, T_CONJ - 0.35, t, { tracking: -2, outDur: 0.28 });
      maskText(sx, "with no middlemen.", 150, 892, 80, 700, AMBER, n0 + 5.38, T_CONJ - 0.32, t, { tracking: -2, outDur: 0.28 });
      maskText(sx, "PUBLISH FREE OR SELL STRAIGHT FROM YOUR NODE", 154, 956, 17, 600, MUTED, n0 + 5.65, T_CONJ - 0.3, t, { tracking: 3, outDur: 0.28 });
    }
    if (t > T_CONJ + 0.3 && t < T_END + 0.1) {
      maskText(sx, "Two sources.", 150, 772, 72, 700, INK, T_CONJ + 0.6, T_TCA - 0.75, t, { tracking: -1.5, outDur: 0.3 });
      maskText(sx, "Both digitally signed.", 150, 854, 72, 700, AMBER, T_CONJ + 0.75, T_TCA - 0.72, t, { tracking: -1.5, outDur: 0.3 });
      maskText(sx, "SCREENED BY OPEN WEBASSEMBLY MODULES ANYONE CAN RERUN", 154, 916, 17, 600, MUTED, T_CONJ + 1.1, T_TCA - 0.7, t, { tracking: 3, outDur: 0.3 });
      maskText(sx, "Open standards.", 150, 690, 72, 700, INK, T_TCA + 0.4, T_END - 0.3, t, { tracking: -1.5 });
      maskText(sx, "Open-source software.", 150, 772, 72, 700, INK, T_TCA + 0.55, T_END - 0.225, t, { tracking: -1.5 });
      maskText(sx, "Open algorithms.", 150, 854, 72, 700, INK, T_TCA + 0.7, T_END - 0.15, t, { tracking: -1.5 });
      maskText(sx, "All free.", 150, 936, 72, 700, AMBER, T_TCA + 0.95, T_END - 0.075, t, { tracking: -1.5 });
    }

    // Bloom: the glow layer blurred at two radii, added over the scene.
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

    hud(t, frameIndex);
  }

  // Frame i: SUBFRAMES samples across the open shutter, averaged.
  function renderFrame(i) {
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
    // Finishing pass: chromatic aberration on each cut, a flash on the hard ones.
    const t = t0;
    const cuts = [[2.2, 1], [T_FORMAT, 0.5], [T_STD, 1], [T_KEYS, 1], [T_SIGN, 0.5], [T_SEAL, 0.5], [T_NET, 1], [T_CONJ, 1], [T_END + 1.05, 1]];
    let ca = 0;
    for (const [c, w] of cuts) ca = Math.max(ca, w * Math.exp(-Math.pow((t - c) / 0.09, 2)));
    const pulse = (c, w) => Math.exp(-Math.pow((t - c) / w, 2));
    const flash = 0.3 * pulse(T_STD + 0.01, 0.05) + 0.1 * pulse(T_KEYS + 0.01, 0.05) + 0.1 * pulse(T_NET + 0.01, 0.05) + 0.12 * pulse(T_TCA - 0.205, 0.06);
    // The piece plays once and rests on the lockup, so it fades in but never out.
    const fade = seg(t, 0, 0.12);
    pgl.viewport(0, 0, W, H);
    pgl.useProgram(post);
    pgl.bindVertexArray(postVao);
    pgl.activeTexture(pgl.TEXTURE0);
    pgl.bindTexture(pgl.TEXTURE_2D, postTex);
    pgl.texImage2D(pgl.TEXTURE_2D, 0, pgl.RGBA, pgl.RGBA, pgl.UNSIGNED_BYTE, accum);
    pgl.uniform1i(PU.uImg, 0);
    pgl.uniform2f(PU.uRes, W, H);
    pgl.uniform1f(PU.uCA, ca * 0.012);
    pgl.uniform1f(PU.uGrain, 0.035);
    pgl.uniform1f(PU.uSeed, (i % 97) * 1.37);
    pgl.uniform1f(PU.uFlash, flash);
    pgl.uniform1f(PU.uFade, fade);
    pgl.drawArrays(pgl.TRIANGLES, 0, 3);
    return out;
  }

  return { W, H, FPS, DURATION, frames: Math.round(FPS * DURATION), renderFrame, canvas: out };
}
