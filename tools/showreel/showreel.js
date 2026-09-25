// Space Data Network showreel: a deterministic 15-second motion-graphics piece.
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
const DURATION = 15;
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
// Scene time τ runs at a variable rate: the conjunction slows to a crawl at
// closest approach and the finale snaps forward. τ(t) is integrated once.
function rate(t) {
  let r = 1;
  r += 2.2 * seg(t, 10.1, 10.5) * (1 - seg(t, 11.05, 11.45)); // rush in
  r -= 0.86 * seg(t, 11.05, 11.45) * (1 - seg(t, 11.95, 12.25)); // slow-mo at TCA
  r += 3.0 * seg(t, 12.25, 12.6) * (1 - seg(t, 13.2, 13.6)); // snap out
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
  const tTCA = tau(11.6);
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

function cameraAt(t) {
  const spin = 0.045 * t;
  let dir;
  let dist;
  let target = [0, 0, 0];
  let off = [0, 0];
  let up = [0, 1, 0];
  let zoom = 1;
  if (t < 5.0) {
    const lon = lerp(-62, -30, easeInOutCubic(seg(t, 1.6, 5.0)));
    const lat = lerp(18, 26, easeInOutCubic(seg(t, 1.6, 5.0)));
    dir = geoDir(lat, lon, 0);
    const d0 = distForRadius(ringRadius(Math.min(t, 2.25)));
    dist = lerp(d0, 7.4, easeInOutCubic(seg(t, 2.35, 4.6)));
  } else if (t < 7.75) {
    const k = easeInOutCubic(seg(t, 5.0, 6.4));
    dir = geoDir(lerp(26, 34, k), lerp(-30, -38, k) + 6 * seg(t, 6.4, 7.75), 0);
    dist = lerp(7.4, 2.75, k);
    off = [lerp(0, 0.34, k), lerp(0, -0.12, k)];
    // Whip-zoom into a packet before the cut to the record.
    zoom = 1 + 7 * easeInExpo(seg(t, 7.3, 7.75));
  } else if (t < 10.1) {
    dir = geoDir(28, -44 + 6 * seg(t, 7.75, 10.1), 0);
    dist = 3.1;
    off = [0.52, -0.08];
    zoom = lerp(1.25, 1, easeOutExpo(seg(t, 7.75, 8.6)));
  } else {
    // Conjunction close-up, then the pull back to the whole network.
    const X = CONJ.X;
    const side = norm(cross(X, [0, 1, 0]));
    const camNear = add(add(mul(X, 1.5), mul(side, 0.34)), mul(norm(cross(side, X)), 0.12));
    const targetNear = mul(X, 1.08);
    const drift = seg(t, 10.1, 12.6);
    const near = add(camNear, mul(side, -0.12 * drift));
    const pull = easeInOutExpo(seg(t, 12.45, 13.55));
    const farDir = norm(add(mul(X, 1), [0, 0.35, 0]));
    const shrink = easeInExpo(seg(t, 13.3, 13.95));
    const farDist = lerp(6.2, distForRadius(150), shrink);
    const pos = mix3(near, mul(farDir, farDist), pull);
    target = mix3(targetNear, [0, 0, 0], pull);
    const cam = lookAt(pos, target, [0, 1, 0]);
    return { ...cam, spin, off: [0, 0], zoom: 1, dist: len(pos) };
  }
  const pos = add(target, mul(dir, dist));
  const cam = lookAt(pos, target, up);
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
    const outK = tOut == null ? 0 : easeInExpo(seg(t, tOut, tOut + 0.45));
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
    const a = 0.55 * (1 - seg(t, 14.2, 14.6)) * seg(t, 0.2, 0.8);
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
    const chapters = [
      [0, "01  IGNITION"], [2.0, "02  CATALOG"], [5.0, "03  NETWORK"],
      [7.75, "04  RECORD"], [10.1, "05  CONJUNCTION"], [12.6, "06  RESOLVE"],
    ];
    let ch = chapters[0][1];
    for (const [t0, name] of chapters) if (t >= t0) ch = name;
    sx.textAlign = "left";
    sx.fillStyle = AMBER;
    sx.fillText(ch, m + 12, H - m - 16);
    sx.fillStyle = INK;
    sx.textAlign = "right";
    sx.fillText("1920 × 1080 · 30 FPS · DIGITALLY SIGNED", W - m - 12, H - m - 16);
    sx.restore();
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

  function drawNetwork(cam, t, alpha) {
    const spin = cam.spin;
    const P = NODES.map(([lat, lon]) => geoDir(lat, lon, spin));
    // links
    LINKS.forEach(([i, j], k) => {
      const t0 = 5.45 + k * 0.035;
      const draw = easeInOutCubic(seg(t, t0, t0 + 0.6));
      if (draw <= 0) return;
      const N = 60;
      let prev = null;
      sx.save();
      sx.lineCap = "round";
      for (let s = 0; s <= N; s++) {
        const f = (s / N) * draw;
        const p = arcPoint(P[i], P[j], f);
        const q = project(cam, p);
        const hid = occluded(cam, p);
        if (q && prev && !hid && !prev.hid) {
          sx.strokeStyle = AMBER;
          sx.globalAlpha = alpha * 0.55;
          sx.lineWidth = 1.4;
          sx.beginPath();
          sx.moveTo(prev.x, prev.y);
          sx.lineTo(q.x, q.y);
          sx.stroke();
          gx.strokeStyle = AMBER;
          gx.globalAlpha = alpha * 0.35;
          gx.lineWidth = 2;
          gx.beginPath();
          gx.moveTo(prev.x / 2, prev.y / 2);
          gx.lineTo(q.x / 2, q.y / 2);
          gx.stroke();
        }
        prev = q ? { ...q, hid } : null;
      }
      sx.restore();
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
      const t0 = 5.1 + i * 0.04;
      const k = seg(t, t0, t0 + 0.5);
      if (k <= 0) return;
      const q = project(cam, mul(p, 1.004));
      if (!q || occluded(cam, mul(p, 1.004))) return;
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
  }

  function drawRecord(t) {
    const inK = seg(t, 7.75, 8.1);
    const outK = easeInExpo(seg(t, 9.75, 10.1));
    if (inK <= 0 || outK >= 1) return;
    const x0 = 180 - outK * 260;
    const y0 = 250;
    sx.save();
    sx.globalAlpha = 1 - outK;
    // grid hairlines that draw on
    const grid = easeOutExpo(seg(t, 7.75, 8.35));
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
    sx.moveTo(x0 + 330, y0 + 34);
    sx.lineTo(x0 + 330, y0 + 34 + 506 * grid);
    sx.stroke();
    sx.restore();

    maskText(sx, "OMM · ORBIT MEAN-ELEMENTS MESSAGE", x0, y0 - 84, 17, 600, AMBER, 7.8, 9.75, t, { tracking: 2.5, family: SANS });
    maskText(sx, "ISS (ZARYA)", x0, y0 - 6, 64, 700, INK, 7.85, 9.72, t, { dur: 0.8 });
    const FIELDS = [
      ["NORAD_CAT_ID", "25544"],
      ["EPOCH", "2026-09-25T12:00:00.000Z"],
      ["MEAN_MOTION", "15.49872741"],
      ["ECCENTRICITY", "0.0004183"],
      ["INCLINATION", "51.6412"],
      ["RA_OF_ASC_NODE", "211.2071"],
      ["ARG_OF_PERICENTER", "84.1036"],
      ["MEAN_ANOMALY", "276.0512"],
      ["BSTAR", "0.00013452"],
      ["CENTER · FRAME", "EARTH · TEME"],
      ["TIME_SYSTEM", "UTC"],
    ];
    sx.save();
    sx.globalAlpha = 1 - outK;
    FIELDS.forEach(([k, v], i) => {
      const y = y0 + 68 + i * 46;
      const t0 = 8.0 + i * 0.07;
      const a = seg(t, t0, t0 + 0.25);
      if (a <= 0) return;
      sx.globalAlpha = (1 - outK) * a;
      sx.font = font(500, 18, MONO);
      sx.letterSpacing = "1px";
      sx.fillStyle = MUTED;
      sx.textAlign = "left";
      sx.fillText(k, x0, y);
      sx.fillStyle = INK;
      sx.font = font(500, 22, MONO);
      decodeText(sx, v, x0 + 350, y, t0 + 0.05, 0.55, t, 100 + i);
    });
    sx.restore();

    // FlatBuffer bytes streaming up the right side
    const hx = 1180;
    sx.save();
    sx.beginPath();
    sx.rect(hx - 10, 170, 620, 720);
    sx.clip();
    const scroll = (t - 7.75) * 90;
    const hk = seg(t, 7.8, 8.2) * (1 - outK);
    sx.font = font(400, 17, MONO);
    sx.letterSpacing = "0.5px";
    HEX.forEach((row, i) => {
      const y = 200 + i * 28 - scroll;
      if (y < 160 || y > 900) return;
      const scan = Math.abs(y - (760 - (t - 8.0) * 190)) < 30;
      sx.globalAlpha = hk * (scan ? 1 : 0.4) * clamp(1 - Math.abs(y - 530) / 380);
      sx.fillStyle = MUTED;
      sx.fillText(row.off, hx, y);
      sx.fillStyle = scan ? CYAN : "rgba(89,217,255,0.75)";
      sx.fillText(row.bytes.join(" "), hx + 90, y);
    });
    sx.restore();
    maskText(sx, "FLATBUFFERS · 312 BYTES", hx, 168, 15, 600, CYAN, 7.9, 9.72, t, { tracking: 2.5 });

    // signature and the stamp
    maskText(sx, "SIGNATURE  3045 0221 00c7 9a1f 5e83 b2d4 …", x0, y0 + 620, 18, 500, MUTED, 9.0, 9.7, t, { family: MONO });
    maskText(sx, "CID  bafkreih4v6ogq2c7xzfx3yfwq7o5lj2s3pd…", x0, y0 + 652, 18, 500, MUTED, 9.12, 9.7, t, { family: MONO });
    const st = seg(t, 9.1, 9.45);
    if (st > 0 && outK < 1) {
      const sc = easeOutBack(st);
      const cx = hx + 290;
      const cy = 800;
      sx.save();
      sx.globalAlpha = 1 - outK;
      sx.translate(cx, cy);
      sx.scale(sc * (1 + 0.6 * (1 - st)), sc * (1 + 0.6 * (1 - st)));
      sx.rotate(-0.04);
      const w = 420;
      const h = 76;
      sx.fillStyle = "rgba(245,165,36,0.14)";
      sx.strokeStyle = AMBER;
      sx.lineWidth = 2.5;
      sx.beginPath();
      sx.roundRect(-w / 2, -h / 2, w, h, 38);
      sx.fill();
      sx.stroke();
      sx.strokeStyle = AMBER;
      sx.lineWidth = 4;
      sx.lineCap = "round";
      sx.lineJoin = "round";
      sx.beginPath();
      sx.moveTo(-w / 2 + 38, 2);
      sx.lineTo(-w / 2 + 52, 16);
      sx.lineTo(-w / 2 + 78, -14);
      sx.stroke();
      sx.fillStyle = AMBER;
      sx.font = font(700, 26, SANS);
      sx.letterSpacing = "3px";
      sx.textAlign = "left";
      sx.fillText("DIGITALLY SIGNED", -w / 2 + 100, 10);
      sx.restore();
      dotGlow(cx, cy, 150 * sc, AMBER, 0.1 * (1 - outK) * (1 - st * 0.7));
    }
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
    const near = seg(t, 10.9, 11.35) * (1 - seg(t, 12.2, 12.5));
    const close = Math.hypot(qa.x - qb.x, qa.y - qb.y);
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
    const tca = seg(t, 11.25, 11.5) * (1 - seg(t, 12.3, 12.55));
    if (tca > 0) {
      const mx = (qa.x + qb.x) / 2;
      const my = (qa.y + qb.y) / 2;
      // shockwave ring at closest approach
      const sw = seg(t, 11.45, 12.3);
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
      dotGlow(mx, my, 50 * tca, RED, 0.22 * alpha * tca * (1 - seg(t, 11.9, 12.3)));
      // alert block with a leader line
      const lx = mx + 150;
      const ly = my - 260;
      const lk = easeOutExpo(seg(t, 11.35, 11.75));
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
      maskText(sx, "CONJUNCTION", lx + 58, ly + 12, 30, 700, RED, 11.35, 12.3, t, { tracking: 3 });
      sx.save();
      sx.globalAlpha = alpha * tca;
      sx.font = font(500, 19, MONO);
      sx.letterSpacing = "1px";
      sx.fillStyle = INK;
      decodeText(sx, "TCA   2026-09-25 14:02:31Z", lx, ly + 76, 11.45, 0.4, t, 7);
      decodeText(sx, "MISS  214 m", lx, ly + 104, 11.55, 0.35, t, 8);
      decodeText(sx, "Pc    1.2 × 10⁻⁴", lx, ly + 132, 11.65, 0.35, t, 9);
      sx.restore();
    }
    if (close < 0) return; // keep linters quiet about an unused binding
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

    // Earth: in during the match cut, out for the record, back for the finale.
    const earthIn = easeInOutCubic(seg(t, 1.55, 2.3));
    const earthRecord = 1 - 0.8 * seg(t, 7.65, 7.9) * (1 - seg(t, 9.95, 10.15));
    const earthOut = 1 - easeInCubic(seg(t, 13.45, 13.95));
    const earthFade = earthIn * earthRecord * earthOut;
    const rim = 0.9 * seg(t, 1.6, 2.1) * (1 - seg(t, 2.3, 3.0)) + 0.9 * seg(t, 13.35, 13.8);
    const sunAz = t < 5 ? lerp(35, 60, seg(t, 2, 5)) : t < 7.75 ? lerp(60, 118, seg(t, 5, 7.2)) : t < 10.1 ? 90 : lerp(150, 60, seg(t, 12.4, 13.6));
    const sunEl = t < 10.1 ? 18 : lerp(4, 18, seg(t, 12.4, 13.6));
    if (earthFade > 0.001) {
      renderEarth(cam, t, earthFade, rim, sunAz, sunEl);
      if (t > 7.7 && t < 10.15) {
        sx.filter = `blur(${lerp(0, 14, seg(t, 7.7, 7.95) * (1 - seg(t, 9.9, 10.15)))}px)`;
      }
      sx.drawImage(glc, 0, 0);
      sx.filter = "none";
    }

    // Satellites: the swarm arrives in S2, dims under the network, returns.
    const satAlpha =
      seg(t, 2.1, 2.4) *
      (1 - 0.72 * seg(t, 5.0, 5.6) * (1 - seg(t, 12.5, 13.2))) *
      (1 - seg(t, 7.6, 7.8) * (1 - seg(t, 10.0, 10.2))) *
      (1 - 0.55 * seg(t, 10.1, 10.3) * (1 - seg(t, 12.45, 12.8))) *
      earthOut;
    if (satAlpha > 0.01) drawSatellites(cam, t, T, satAlpha, true);

    // The amber satellite from the mark, now in orbit with its arc.
    const heroA = seg(t, 2.0, 2.4) * (1 - seg(t, 7.55, 7.75)) * (1 - seg(t, 13.4, 13.8));
    const heroB = t > 10.1 ? 0 : 1;
    if (heroA * heroB > 0) drawOrbitPath(cam, HERO, T, AMBER, heroA, 2.4, 0.42);

    if (t > 5.0 && t < 7.8) drawNetwork(cam, t, (1 - seg(t, 7.45, 7.75)) * seg(t, 5.0, 5.3));
    if (t > 10.05 && t < 12.9) drawConjunction(cam, t, T, seg(t, 10.1, 10.4) * (1 - seg(t, 12.45, 12.9)));

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
    if (t > 13.3) {
      // Match cut back to the mark: the ring rides the shrinking planet's
      // limb, then settles into the lockup.
      const c = project(cam, [0, 0, 0]) ?? { x: W / 2, y: H / 2 };
      const globePx = (1 / Math.sqrt(Math.max(1e-6, cam.dist * cam.dist - 1)) / TAN) * (H / 2);
      const settle = easeInOutCubic(seg(t, 13.9, 14.3));
      const R = lerp(Math.max(150, globePx), 118, settle);
      const mx = lerp(c.x, W / 2 - 380, settle);
      const my = lerp(c.y, H / 2, settle);
      drawMark(mx, my, R, t, {
        head: easeInOutExpo(seg(t, 13.5, 14.0)),
        ring: seg(t, 13.4, 13.7),
        nodes: seg(t, 14.02, 14.26),
        links: easeOutCubic(seg(t, 14.1, 14.32)),
        fade: 1,
      });
      maskText(sx, "Space Data Network", W / 2 - 220, H / 2 + 8, 78, 650, INK, 14.22, null, t, { dur: 0.45, tracking: -1.5 });
      maskText(sx, "An open baseline for space traffic management", W / 2 - 218, H / 2 + 64, 28, 400, MUTED, 14.3, null, t, { dur: 0.4 });
      maskText(sx, "SPACEDATANETWORK.ORG", W / 2 - 218, H / 2 - 92, 18, 600, AMBER, 14.34, null, t, { dur: 0.4, tracking: 4 });
    }

    // Typography per chapter.
    if (t > 2.3 && t < 5.2) {
      const k = easeOutExpo(seg(FRAME_T, 2.45, 4.1));
      const n = Math.round(46600 * k);
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
    if (t > 5.3 && t < 7.7) {
      maskText(sx, "Peer to peer.", 150, 400, 92, 700, INK, 5.55, 7.3, t, { tracking: -2 });
      maskText(sx, "Every record", 150, 500, 92, 700, INK, 5.8, 7.35, t, { tracking: -2 });
      maskText(sx, "digitally signed.", 150, 600, 92, 700, AMBER, 5.95, 7.4, t, { tracking: -2 });
      maskText(sx, "NODES ON EVERY CONTINENT · ONE OPEN CATALOG", 154, 680, 17, 600, MUTED, 6.3, 7.4, t, { tracking: 3 });
    }
    drawRecord(t);
    if (t > 10.2 && t < 12.7) {
      maskText(sx, "Screened in", 150, 830, 84, 700, INK, 11.9, 12.45, t, { tracking: -2 });
      maskText(sx, "your browser.", 150, 922, 84, 700, INK, 12.0, 12.5, t, { tracking: -2 });
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
    // Finishing pass.
    const t = t0;
    const cuts = [2.2, 7.75, 10.1, 13.5];
    let ca = 0;
    for (const c of cuts) ca = Math.max(ca, Math.exp(-Math.pow((t - c) / 0.09, 2)));
    const flash = 0.35 * Math.exp(-Math.pow((t - 7.76) / 0.05, 2)) + 0.12 * Math.exp(-Math.pow((t - 11.47) / 0.06, 2));
    const fade = seg(t, 0, 0.12) * (1 - easeInCubic(seg(t, DURATION - 0.22, DURATION - 0.02)));
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
